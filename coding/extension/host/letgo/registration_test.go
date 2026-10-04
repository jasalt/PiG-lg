package letgo

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

func registrationTool(name, description string) extension.ToolDefinition {
	return extension.ToolDefinition{Name: name, Description: description, Parameters: json.RawMessage(`{"type":"object"}`)}
}

func TestRegistrationOrderReplacementAndSource(t *testing.T) {
	// Pi loader.ts:createExtensionAPI uses Map.set: replacement retains first-registration order and current sourceInfo.
	source := map[string]any{"path": "example.lg", "source": "local"}
	builder := newRegistrationBuilder(extension.Extension{Name: "example", Path: "example.lg", ResolvedPath: "/extensions/example.lg", SourceInfo: source})
	for i, name := range []string{"second", "first", "second"} {
		description := name
		if i == 2 {
			description = "replacement"
		}
		if err := builder.RegisterTool(registrationTool(name, description)); err != nil {
			t.Fatal(err)
		}
		if err := builder.RegisterCommand(name, extension.CommandOptions{Description: description, Handler: func(context.Context, string) error { return nil }}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := builder.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	tools := snapshot.RegisteredTools()
	if !slices.Equal([]string{tools[0].Definition.Name, tools[1].Definition.Name}, []string{"second", "first"}) {
		t.Fatalf("tools %#v", tools)
	}
	if tools[0].Definition.Description != "replacement" || snapshot.Commands["second"].Description != "replacement" {
		t.Fatal("replacement did not update the first registration")
	}
	if !slices.Equal(snapshot.CommandOrder, []string{"second", "first"}) {
		t.Fatal(snapshot.CommandOrder)
	}
	for _, tool := range tools {
		if !reflect.DeepEqual(tool.SourceInfo, source) {
			t.Fatal(tool.SourceInfo)
		}
	}
	runner := inproc.NewRunner([]extension.Extension{snapshot}, t.TempDir())
	defer runner.Invalidate("test finished")
	commands := runner.Commands()
	if len(commands) != 2 || commands[0].Name != "second" || !reflect.DeepEqual(commands[0].SourceInfo, source) {
		t.Fatalf("commands %#v", commands)
	}
	if err := builder.RegisterCommand("late", extension.CommandOptions{}); err == nil {
		t.Fatal("published command mutation accepted")
	}
}

func TestRegistrationNativeRunnerLateToolsAndGuard(t *testing.T) {
	builder := newRegistrationBuilder(extension.Extension{Name: "tools", Path: "tools.lg", SourceInfo: "origin"})
	if err := builder.RegisterTool(registrationTool("early", "initial")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	runtime := extension.CreateExtensionRuntime()
	// Use a fresh shared runtime in the real runner, not a fake native API.
	runner := inproc.NewRunner([]extension.Extension{snapshot}, t.TempDir(), runtime)
	defer runner.Invalidate("test finished")
	var refreshed []string
	refreshFailure := errors.New("refresh failed")
	runner.BindCore(extension.ExtensionActions{}, extension.ContextActions{RefreshTools: func() error {
		for _, tool := range snapshot.RegisteredTools() {
			refreshed = append(refreshed, tool.Definition.Description)
		}
		return refreshFailure
	}}, nil)
	live, err := runtime.CreateContext()
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.bind(func() error { _, err := live.CWD(); return err }, live.RefreshTools); err != nil {
		t.Fatal(err)
	}
	if err := builder.RegisterTool(registrationTool("early", "replacement")); !errors.Is(err, refreshFailure) {
		t.Fatalf("refresh error lost: %v", err)
	}
	if !slices.Equal(refreshed, []string{"replacement"}) {
		t.Fatal(refreshed)
	}
	if tool, ok := snapshot.RegisteredTool("early"); !ok || tool.Definition.Description != "replacement" || tool.SourceInfo != "origin" {
		t.Fatalf("shared tool %#v", tool)
	}
	if _, ok := runner.ToolSourceInfo("early"); !ok {
		t.Fatal("native runner cannot see shared registration")
	}
	runner.Invalidate("replaced")
	if err := builder.RegisterTool(registrationTool("stale", "bad")); err == nil {
		t.Fatal("stale runtime registration accepted")
	}
	if _, exists := snapshot.RegisteredTool("stale"); exists {
		t.Fatal("stale tool leaked")
	}
	builder.close()
	builder.close()
	if err := builder.RegisterTool(registrationTool("closed", "bad")); err == nil {
		t.Fatal("closed registration accepted")
	}
}

func TestRegistrationNativeEventAndCommandDispatch(t *testing.T) {
	builder := newRegistrationBuilder(extension.Extension{Name: "events", Path: "events.lg"})
	var order []string
	if err := builder.OnSessionStart(func(ctx context.Context, event extension.SessionStartEvent) error {
		if extension.FromContext(ctx) == nil || event.Type != "session_start" {
			t.Fatal("native handler context/event absent")
		}
		order = append(order, "first")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	runner := inproc.NewRunner([]extension.Extension{snapshot}, t.TempDir())
	defer runner.Invalidate("test finished")
	// Event subscriptions made after publication share the runner registry.
	if err := builder.OnSessionStart(func(context.Context, extension.SessionStartEvent) error { order = append(order, "second"); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Emit(t.Context(), extension.SessionStartEvent{Type: "session_start"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"first", "second"}) {
		t.Fatal(order)
	}
	called := ""
	commandBuilder := newRegistrationBuilder(extension.Extension{Path: "command.lg"})
	if err := commandBuilder.RegisterCommand("hello", extension.CommandOptions{Handler: func(ctx context.Context, args string) error {
		if extension.FromContext(ctx) == nil {
			t.Fatal("command context missing")
		}
		called = args
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	commands, err := commandBuilder.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	commandRunner := inproc.NewRunner([]extension.Extension{commands}, t.TempDir())
	defer commandRunner.Invalidate("test finished")
	if !commandRunner.ExecuteCommand(t.Context(), "hello", "Ada") || called != "Ada" {
		t.Fatalf("command called=%q", called)
	}
}

func TestRegistrationResultAndFailureSemantics(t *testing.T) {
	builder := newRegistrationBuilder(extension.Extension{Path: "results.lg"})
	empty := ""
	handlerFailure := errors.New("interpreted failure")
	if err := builder.OnBeforeAgentStart(func(context.Context, extension.BeforeAgentStartEvent) (extension.BeforeAgentStartEventResult, error) {
		return extension.BeforeAgentStartEventResult{SystemPrompt: &empty}, handlerFailure
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.OnToolCall(func(context.Context, extension.ToolCallEvent) (extension.ToolCallEventResult, error) {
		return extension.ToolCallEventResult{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.OnToolResult(func(context.Context, extension.ToolResultEvent) (extension.ToolResultEventResult, error) {
		return extension.ToolResultEventResult{IsError: new(false)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := builder.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	call := snapshot.EventHandlers("before_agent_start")[0]
	result, err := call(extension.BeforeAgentStartEvent{}, t.Context())
	if !errors.Is(err, handlerFailure) || result.(*extension.BeforeAgentStartEventResult).SystemPrompt == nil {
		t.Fatalf("result=%#v, err=%v", result, err)
	}
	for _, args := range [][]any{nil, {extension.AgentStartEvent{}, t.Context()}, {extension.BeforeAgentStartEvent{}, "bad context"}} {
		if _, err := call(args...); err == nil {
			t.Fatalf("invalid dispatch accepted: %#v", args)
		}
	}
	result, err = snapshot.EventHandlers("tool_call")[0](extension.CustomToolCallEvent{}, t.Context())
	if err != nil || result != nil {
		t.Fatalf("zero result=%#v, err=%v", result, err)
	}
	result, err = snapshot.EventHandlers("tool_result")[0](extension.CustomToolResultEvent{}, t.Context())
	if err != nil || result.(*extension.ToolResultEventResult).IsError == nil {
		t.Fatalf("explicit false lost: %#v, %v", result, err)
	}
}

func TestRegistrationValidationAndConcurrentTools(t *testing.T) {
	builder := newRegistrationBuilder(extension.Extension{Path: "validation.lg"})
	for _, schema := range []string{"", "null", "[]", "true", "42", "{} trailing"} {
		tool := registrationTool("invalid", "bad")
		tool.Parameters = json.RawMessage(schema)
		if err := builder.RegisterTool(tool); err == nil || !strings.Contains(err.Error(), "object parameter schema") {
			t.Fatalf("schema %q: %v", schema, err)
		}
	}
	if err := builder.RegisterCommand("", extension.CommandOptions{}); err == nil {
		t.Fatal("empty command accepted")
	}
	if err := builder.RegisterCommand("invalid", extension.CommandOptions{}); err == nil {
		t.Fatal("nil handler accepted")
	}
	if err := builder.bind(func() error { return nil }, func() error { return nil }); err == nil {
		t.Fatal("unpublished binding accepted")
	}
	snapshot, err := builder.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.bind(nil, nil); err == nil {
		t.Fatal("missing actions accepted")
	}
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() {
			if err := builder.RegisterTool(registrationTool("same", "concurrent")); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if len(snapshot.RegisteredTools()) != 1 {
		t.Fatal("concurrent replacement duplicated registration")
	}
	if err := builder.OnAgentStart(func(context.Context, extension.AgentStartEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	handler := snapshot.EventHandlers("agent_start")[0]
	builder.close()
	if _, err := handler(extension.AgentStartEvent{}, t.Context()); err == nil {
		t.Fatal("closed handler still dispatched")
	}
}
