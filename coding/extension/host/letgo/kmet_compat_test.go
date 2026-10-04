package letgo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

const kmetCompatFixtures = "../../../../test/fixtures/extensions/letgo/kmet-compat"

// kmetCompatRequire is the one line a portable fixture may differ in; everything after the ns form is identical on both hosts.
const kmetCompatRequire = "#?(:lg [pig.extension :as ext]\n               :default [kmet.extension :as ext])"

func kmetCompatExpected(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(kmetCompatFixtures, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	return expected
}

func kmetCompatLoad(t *testing.T, file string) (*Loaded, *inproc.Runner) {
	t.Helper()
	loaded := loadAt(t, filepath.Join(kmetCompatFixtures, file))
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	t.Cleanup(func() { runner.Invalidate("test complete") })
	return loaded, runner
}

func kmetCompatLog(t *testing.T, loaded *Loaded) []any {
	t.Helper()
	value, err := loaded.generation.Run(t.Context(), `(deref `+loaded.entry.namespace+`/log)`)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return plain.([]any)
}

// kmetCompatSame compares through JSON so Go and expected.json numbers and nulls agree.
func kmetCompatSame(t *testing.T, name string, got any, want any) {
	t.Helper()
	normalized := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var plain any
		if err := json.Unmarshal(data, &plain); err != nil {
			t.Fatal(err)
		}
		data, _ = json.Marshal(plain)
		return string(data)
	}
	if normalized(got) != normalized(want) {
		t.Errorf("%s:\n got  %s\n want %s", name, normalized(got), normalized(want))
	}
}

func TestKmetCompatFixturesRequireOnlyTheHostConditional(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(kmetCompatFixtures, "*.cljc"))
	if err != nil || len(files) != 6 {
		t.Fatalf("fixtures %q, %v", files, err)
	}
	helper, err := filepath.Glob(filepath.Join(kmetCompatFixtures, "kmet_compat", "*.cljc"))
	if err != nil || len(helper) != 1 {
		t.Fatalf("helper %q, %v", helper, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), kmetCompatRequire) {
			t.Errorf("%s does not use the shared host conditional require", file)
		}
	}
	data, _ := os.ReadFile(helper[0])
	if strings.Contains(string(data), "extension") {
		t.Error("the pure helper namespace must not reference an extension host")
	}
}

func TestKmetCompatToolOnlyCommandAndSessionStartMatchExpected(t *testing.T) {
	expected := kmetCompatExpected(t)

	loaded, runner := kmetCompatLoad(t, "tool_only.cljc")
	result, err := runner.Tools()[0].Definition.Execute(runner.DispatchContext(t.Context()), "call", json.RawMessage(`{"text":"hi"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	kmetCompatSame(t, "tool-only", map[string]any{"result": result.(agent.AgentToolResult).Text(), "log": kmetCompatLog(t, loaded)}, expected["tool-only"])

	loaded, runner = kmetCompatLoad(t, "command.cljc")
	commands := runner.Commands()
	if len(commands) != 1 || !runner.ExecuteCommand(t.Context(), "greet", "Ada") {
		t.Fatalf("commands %#v", commands)
	}
	kmetCompatSame(t, "command", map[string]any{"description": commands[0].Description, "log": kmetCompatLog(t, loaded)}, expected["command"])

	loaded, runner = kmetCompatLoad(t, "session_start.cljc")
	if _, err := runner.Emit(t.Context(), extension.SessionStartEvent{Type: "session_start", Reason: "startup"}); err != nil {
		t.Fatal(err)
	}
	kmetCompatSame(t, "session-start", map[string]any{"log": kmetCompatLog(t, loaded)}, expected["session-start"])
}

func TestKmetCompatHooksAndHelperMatchExpected(t *testing.T) {
	expected := kmetCompatExpected(t)

	loaded, runner := kmetCompatLoad(t, "before_agent_start.cljc")
	combined, err := runner.EmitBeforeAgentStart(t.Context(), "do it", nil, "base", extension.BuildSystemPromptOptions{})
	if err != nil || combined == nil || combined.SystemPrompt == nil {
		t.Fatalf("combined %#v, %v", combined, err)
	}
	kmetCompatSame(t, "before-agent-start", map[string]any{"system-prompt": *combined.SystemPrompt, "log": kmetCompatLog(t, loaded)}, expected["before-agent-start"])

	loaded, runner = kmetCompatLoad(t, "tool_hooks.cljc")
	call := func(event extension.ToolCallEvent) any {
		result, err := runner.EmitToolCall(t.Context(), event)
		if err != nil {
			t.Fatal(err)
		}
		if result == nil {
			return nil
		}
		return map[string]any{"block": result.Block, "reason": result.Reason}
	}
	isError := func(event extension.ToolResultEvent) any {
		result, err := runner.EmitToolResult(t.Context(), event)
		if err != nil {
			t.Fatal(err)
		}
		if result == nil || result.IsError == nil {
			return nil
		}
		return *result.IsError
	}
	base := extension.ToolResultEventBase{Type: "tool_result", ToolCallID: "call-1", Content: []any{}}
	kmetCompatSame(t, "tool-hooks", map[string]any{
		"bash-call":            call(extension.BashToolCallEvent{ToolCallEventBase: extension.ToolCallEventBase{Type: "tool_call", ToolCallID: "call-1"}, ToolName: "bash"}),
		"read-call":            call(extension.ReadToolCallEvent{ToolCallEventBase: extension.ToolCallEventBase{Type: "tool_call", ToolCallID: "call-2"}, ToolName: "read"}),
		"bash-result-is-error": isError(extension.BashToolResultEvent{ToolResultEventBase: base, ToolName: "bash"}),
		"read-result-is-error": isError(extension.ReadToolResultEvent{ToolResultEventBase: base, ToolName: "read"}),
		"log":                  kmetCompatLog(t, loaded),
	}, expected["tool-hooks"])

	_, runner = kmetCompatLoad(t, "helper_entry.cljc")
	result, err := runner.Tools()[0].Definition.Execute(runner.DispatchContext(t.Context()), "call", json.RawMessage(`{"text":"the quick brown fox jumps"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	kmetCompatSame(t, "helper-entry", map[string]any{"result": result.(agent.AgentToolResult).Text()}, expected["helper-entry"])
}
