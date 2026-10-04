package extensionconformance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	"github.com/MichaelKinsy/PiG/coding/extension/host/letgo"
)

// The let-go rows run the supported subset through the real loader and the in-process runner and compare each observation with the
// native reference, never with a constant the interpreter could produce by accident. Unsupported rows are inventoried in
// letGoUnsupportedRows; no row of another SDK is weakened or skipped here. Full SDK parity is a non-goal.
//
// Supported-row map (each row names the production path it drives):
//
//	tool success, thrown error, structured error  -> registered ToolDefinition.Execute through letgo.Load
//	streamed partial results                      -> :contextual? on-update to the runner's AgentToolUpdateCallback
//	command, immediate error, awaited dialog      -> RegisteredCommand.Handler through inproc.Runner
//	select, input, confirm, notify                -> the runner's bound UIContext
//	context reads                                 -> the callback context map over extension.Context
//	session_start and session_shutdown            -> Runner.Emit
//	dynamic tool registration                     -> shared tool registry plus the bound RefreshTools action
//	tool_call, tool_result, before_agent_start    -> Runner.EmitToolCall, EmitToolResult, EmitBeforeAgentStart
//	reload                                        -> cmd/pig TestLetGoReload* (production reload paths)
const letGoConformanceSource = `(ns conformance.letgo (:require [pig.extension :as ext]))
(defn init [api]
  (ext/register-tool! api {:name "echo" :description "Echo back the input"
    :parameters {:type "object" :required ["text"] :properties {:text {:type "string"}}}
    :execute (fn [args] {:content (str "echo: " (:text args))})})
  (ext/register-tool! api {:name "tool_error" :description "Return a thrown tool error" :parameters {:type "object"}
    :execute (fn [args] (throw (ex-info "tool exploded" {})))})
  (ext/register-tool! api {:name "tool_is_error" :description "Return a structured tool error result" :parameters {:type "object"}
    :execute (fn [args] {:content "soft tool error" :is-error true})})
  (ext/register-tool! api {:name "update_tool" :description "Stream two partial results" :parameters {:type "object"} :contextual? true
    :execute (fn [args on-update signal c]
               (on-update {:content "step 1"})
               (on-update {:content "step 2"})
               {:content "done"})})
  (ext/register-command! api {:name "ping" :description "Respond with pong"
    :handler (fn [c args] ((:notify c) "pong" :info))})
  (ext/register-command! api {:name "command_error" :description "Return a command error"
    :handler (fn [c args] (throw (ex-info "command exploded" {})))})
  (ext/register-command! api {:name "dialog-probe" :description "Exercise interactive dialog responses"
    :handler (fn [c args]
               (let [selected ((:select c) "Pick" ["first" "second"])
                     input ((:input c) "Input" "placeholder")
                     confirmed ((:confirm c) "Confirm" "message")]
                 ((:notify c) (str "select=" selected " input=" input " confirm=" confirmed) :info)))})
  (ext/register-command! api {:name "context-probe" :description "Read the context"
    :handler (fn [c args]
               ((:notify c) (str "mode=" (:mode c) " has-ui=" (:has-ui c) " cwd=" (:cwd c) " idle=" ((:is-idle c))) :info))})
  (ext/register-command! api {:name "add-late" :description "Register a tool after load"
    :handler (fn [c args]
               (ext/register-tool! api {:name "late_tool" :description "Registered late" :parameters {:type "object"}
                 :execute (fn [a] {:content "late"})}))})
  (ext/on-event api :session-start (fn [event c] ((:notify c) (str "start=" (:reason event)) :info)))
  (ext/on-event api :session-shutdown (fn [event c] ((:notify c) (str "shutdown=" (:reason event)) :info)))
  (ext/on-tool-call api (fn [event] (when (= (:tool-name event) "blocked_tool") {:block true :reason "policy"})))
  (ext/on-tool-result api (fn [event] (when (= (:tool-name event) "custom") {:content [{:type "text" :text "redacted"}] :is-error true})))
  (ext/on-before-agent-start api (fn [event] {:system-prompt (str (:system-prompt event) "+conformance")})))
`

// letGoUnsupportedRows are the rows the let-go subset does not implement. They are listed so a new capability must update this inventory.
var letGoUnsupportedRows = []string{
	"flags", "shortcuts", "message renderers", "entry renderers", "tool renderers", "markdown transformer", "providers and models", "login and sprite definitions",
	"status, widgets, header, footer and editor", "editor and custom dialogs", "send message and send user message", "session name and entries", "terminal input",
	"project trust", "agent_before_settle", "context abort, wait-for-idle, reload and compact", "prepareArguments",
}

type letGoRows struct {
	runner  *inproc.Runner
	loaded  *letgo.Loaded
	ui      *recordingUI
	notify  *[]string
	refresh *atomic.Int64
	cwd     string
}

func makeLetGoRows(t *testing.T) *letGoRows {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "extension.lg")
	if err := os.WriteFile(path, []byte(letGoConformanceSource), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := letgo.Load(t.Context(), letgo.LoadOptions{
		Entrypoint: path, Identity: extension.Extension{Name: "letgo-conformance", Path: path, ResolvedPath: path, SourceInfo: "letgo-conformance"},
		Streams: letgo.Streams{Stdout: io.Discard, Stderr: io.Discard},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.Close(context.Background()) })
	notify, status := &[]string{}, &[]string{}
	ui := newRecordingUI(notify, status)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, dir)
	t.Cleanup(func() { runner.Invalidate("test complete") })
	runner.SetUIContext(ui, extension.ExtensionMode(conformanceMode))
	var refresh atomic.Int64
	runner.BindCore(extension.ExtensionActions{}, extension.ContextActions{
		IsProjectTrusted: func() bool { return conformanceProjectTrusted },
		RefreshTools:     func() error { refresh.Add(1); return nil },
	}, nil)
	if err := loaded.Bind(extension.FromContext(runner.DispatchContext(t.Context()))); err != nil {
		t.Fatal(err)
	}
	return &letGoRows{runner: runner, loaded: loaded, ui: ui, notify: notify, refresh: &refresh, cwd: dir}
}

type toolObservation struct {
	Texts   []string
	IsError bool
	Err     string
}

func observeTool(t *testing.T, runner *inproc.Runner, name, arguments string) toolObservation {
	t.Helper()
	tool, ok := findTool(runner, name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	var partials []string
	update := agent.ToolUpdateCallback(func(partial agent.AgentToolResult) { partials = append(partials, partial.Text()) })
	result, err := tool.Definition.Execute(runner.DispatchContext(t.Context()), "tc-"+name, json.RawMessage(arguments), update)
	observation := toolObservation{Texts: partials}
	if err != nil {
		observation.Err = err.Error()
		return observation
	}
	native := result.(agent.AgentToolResult)
	observation.Texts = append(observation.Texts, native.Text())
	observation.IsError = native.IsError
	return observation
}

func observeCommand(t *testing.T, runner *inproc.Runner, notify *[]string, name string) (notifications []string, failure string) {
	t.Helper()
	*notify = nil
	command, ok := findCommand(runner, name)
	if !ok {
		t.Fatalf("command %s not registered", name)
	}
	if err := command.Handler(runner.DispatchContext(t.Context()), ""); err != nil {
		failure = err.Error()
	}
	return append([]string(nil), *notify...), failure
}

func TestLetGoConformance_ToolRowsMatchTheNativeReference(t *testing.T) {
	reference := makeInprocGoHarness(t)
	rows := makeLetGoRows(t)
	for _, test := range []struct {
		name, tool, arguments string
		want                  toolObservation
		errorContains         string
	}{
		{name: "success", tool: "echo", arguments: `{"text":"hello"}`, want: toolObservation{Texts: []string{"echo: hello"}}},
		{name: "thrown error", tool: "tool_error", arguments: `{}`, errorContains: "tool exploded"},
		{name: "structured error", tool: "tool_is_error", arguments: `{}`, want: toolObservation{Texts: []string{"soft tool error"}, IsError: true}},
		{name: "streamed partial results", tool: "update_tool", arguments: `{}`, want: toolObservation{Texts: []string{"step 1", "step 2", "done"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := observeTool(t, reference.runner, test.tool, test.arguments)
			got := observeTool(t, rows.runner, test.tool, test.arguments)
			if test.errorContains != "" {
				if !strings.Contains(native.Err, test.errorContains) || !strings.Contains(got.Err, test.errorContains) {
					t.Fatalf("native error %q, let-go error %q, both want %q", native.Err, got.Err, test.errorContains)
				}
				return
			}
			if !reflect.DeepEqual(native, test.want) || !reflect.DeepEqual(got, native) {
				t.Fatalf("native %+v, let-go %+v, want %+v", native, got, test.want)
			}
		})
	}
}

func TestLetGoConformance_CommandAndDialogRowsMatchTheNativeReference(t *testing.T) {
	reference := makeInprocGoHarness(t)
	rows := makeLetGoRows(t)

	native, nativeFailure := observeCommand(t, reference.runner, reference.notify, "ping")
	got, failure := observeCommand(t, rows.runner, rows.notify, "ping")
	if failure != "" || nativeFailure != "" || !reflect.DeepEqual(got, native) || !reflect.DeepEqual(got, []string{"pong:info"}) {
		t.Fatalf("ping: native %q, let-go %q (%q %q)", native, got, nativeFailure, failure)
	}

	_, nativeFailure = observeCommand(t, reference.runner, reference.notify, "command_error")
	_, failure = observeCommand(t, rows.runner, rows.notify, "command_error")
	if !strings.Contains(nativeFailure, "command exploded") || !strings.Contains(failure, "command exploded") {
		t.Fatalf("command error: native %q, let-go %q", nativeFailure, failure)
	}

	// The recording UI answers select with "second", input with "typed" and confirm with true; the let-go dialogs must carry those answers.
	selected, _ := reference.ui.Select(t.Context(), "Pick", []string{"first", "second"}, nil)
	input, _ := reference.ui.Input(t.Context(), "Input", "placeholder", nil)
	confirmed, _ := reference.ui.Confirm(t.Context(), "Confirm", "message", nil)
	want := "select=" + selected + " input=" + input + " confirm=" + map[bool]string{true: "true", false: "false"}[confirmed] + ":info"
	got, failure = observeCommand(t, rows.runner, rows.notify, "dialog-probe")
	if failure != "" || len(got) != 1 || got[0] != want || want != "select=second input=typed confirm=true:info" {
		t.Fatalf("dialog probe %q (%q), want %q", got, failure, want)
	}
}

func TestLetGoConformance_ContextReadsMatchTheNativeContext(t *testing.T) {
	rows := makeLetGoRows(t)
	native := extension.FromContext(rows.runner.DispatchContext(t.Context()))
	mode, err := native.Mode()
	if err != nil {
		t.Fatal(err)
	}
	cwd, _ := native.CWD()
	hasUI, _ := native.HasUI()
	idle, _ := native.IsIdle()
	got, failure := observeCommand(t, rows.runner, rows.notify, "context-probe")
	if want := []string{"mode=" + string(mode) + " has-ui=" + boolString(hasUI) + " cwd=" + cwd + " idle=" + boolString(idle) + ":info"}; failure != "" || !reflect.DeepEqual(got, want) {
		t.Fatalf("context probe %q (%q), want %q", got, failure, want)
	}
	if string(mode) != conformanceMode || cwd != rows.cwd {
		t.Fatalf("native mode %q cwd %q, want values bound by the harness (%q, %q)", mode, cwd, conformanceMode, rows.cwd)
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestLetGoConformance_SessionEventsCarryTheNativePayload(t *testing.T) {
	rows := makeLetGoRows(t)
	*rows.notify = nil
	if _, err := rows.runner.Emit(t.Context(), extension.SessionStartEvent{Type: "session_start", Reason: "fork"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.runner.Emit(t.Context(), extension.SessionShutdownEvent{Type: "session_shutdown", Reason: "resume"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"start=fork:info", "shutdown=resume:info"}; !reflect.DeepEqual(*rows.notify, want) {
		t.Fatalf("session events %q, want %q", *rows.notify, want)
	}
}

func TestLetGoConformance_DynamicRegistrationReachesTheSharedRegistryAndRefresh(t *testing.T) {
	rows := makeLetGoRows(t)
	if _, exists := findTool(rows.runner, "late_tool"); exists {
		t.Fatal("late_tool registered before its command ran")
	}
	before := rows.refresh.Load()
	if _, failure := observeCommand(t, rows.runner, rows.notify, "add-late"); failure != "" {
		t.Fatal(failure)
	}
	late, exists := findTool(rows.runner, "late_tool")
	if !exists || rows.refresh.Load() != before+1 {
		t.Fatalf("late tool registered=%t, refreshes %d -> %d", exists, before, rows.refresh.Load())
	}
	if got := observeTool(t, rows.runner, "late_tool", `{}`); !reflect.DeepEqual(got, toolObservation{Texts: []string{"late"}}) {
		t.Fatalf("late tool result %+v", got)
	}
	if late.SourceInfo != "letgo-conformance" {
		t.Fatalf("late tool source %v", late.SourceInfo)
	}
}

// nativeHookExtension registers the reference handlers a let-go hook must match.
func nativeHookExtension() extension.Extension {
	native := extension.Extension{Name: "native-hooks", Path: "native-hooks"}
	native.InitializeEventHandlers()
	native.AddEventHandler("tool_call", 1, func(args ...any) (any, error) {
		if call, ok := args[0].(extension.CustomToolCallEvent); ok && call.ToolName == "blocked_tool" {
			return &extension.ToolCallEventResult{Block: true, Reason: "policy"}, nil
		}
		return nil, nil
	})
	native.AddEventHandler("tool_result", 1, func(args ...any) (any, error) {
		if result, ok := args[0].(extension.CustomToolResultEvent); ok && result.ToolName == "custom" {
			return &extension.ToolResultEventResult{Content: []any{map[string]any{"type": "text", "text": "redacted"}}, IsError: new(true)}, nil
		}
		return nil, nil
	})
	native.AddEventHandler("before_agent_start", 1, func(args ...any) (any, error) {
		event := args[0].(extension.BeforeAgentStartEvent)
		prompt := event.SystemPrompt + "+conformance"
		return &extension.BeforeAgentStartEventResult{SystemPrompt: &prompt}, nil
	})
	return native
}

func TestLetGoConformance_HookResultsMatchTheNativeChain(t *testing.T) {
	rows := makeLetGoRows(t)
	reference := inproc.NewRunner([]extension.Extension{nativeHookExtension()}, t.TempDir())
	t.Cleanup(func() { reference.Invalidate("test complete") })

	call := func(runner *inproc.Runner, name string) *extension.ToolCallEventResult {
		result, err := runner.EmitToolCall(t.Context(), extension.CustomToolCallEvent{ToolCallEventBase: extension.ToolCallEventBase{Type: "tool_call", ToolCallID: "c"}, ToolName: name, Input: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got, want := call(rows.runner, "blocked_tool"), call(reference, "blocked_tool"); !reflect.DeepEqual(got, want) || got == nil || !got.Block || got.Reason != "policy" {
		t.Fatalf("blocked call: let-go %+v, native %+v", got, want)
	}
	if got, want := call(rows.runner, "other"), call(reference, "other"); got != nil || want != nil {
		t.Fatalf("unmatched call: let-go %+v, native %+v", got, want)
	}

	result := func(runner *inproc.Runner, name string) *extension.ToolResultEventResult {
		base := extension.ToolResultEventBase{Type: "tool_result", ToolCallID: "c", Input: map[string]any{}, Content: []any{map[string]any{"type": "text", "text": "original"}}}
		out, err := runner.EmitToolResult(t.Context(), extension.CustomToolResultEvent{ToolResultEventBase: base, ToolName: name})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got, want := result(rows.runner, "custom"), result(reference, "custom"); !reflect.DeepEqual(got, want) || got == nil || got.IsError == nil || !*got.IsError {
		t.Fatalf("overridden result: let-go %+v, native %+v", got, want)
	}
	if got, want := result(rows.runner, "other"), result(reference, "other"); got != nil || want != nil {
		t.Fatalf("unmatched result: let-go %+v, native %+v", got, want)
	}

	start := func(runner *inproc.Runner) *extension.BeforeAgentStartCombinedResult {
		out, err := runner.EmitBeforeAgentStart(t.Context(), "prompt", nil, "base", extension.BuildSystemPromptOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	got, want := start(rows.runner), start(reference)
	if got == nil || want == nil || got.SystemPrompt == nil || want.SystemPrompt == nil || *got.SystemPrompt != *want.SystemPrompt || *got.SystemPrompt != "base+conformance" {
		t.Fatalf("before_agent_start: let-go %+v, native %+v", got, want)
	}
}

// A callback that awaits a dialog holds its native caller until the dialog answers, and a cancelled request ends the wait with the context error.
func TestLetGoConformance_AwaitedDialogWaitsAndHonoursCancellation(t *testing.T) {
	rows := makeLetGoRows(t)
	ui := &blockingConformanceUI{recordingUI: rows.ui, entered: make(chan struct{}), release: make(chan struct{})}
	rows.runner.SetUIContext(ui, extension.ExtensionMode(conformanceMode))
	command, ok := findCommand(rows.runner, "dialog-probe")
	if !ok {
		t.Fatal("dialog-probe not registered")
	}
	finished := make(chan error, 1)
	*rows.notify = nil
	go func() { finished <- command.Handler(rows.runner.DispatchContext(t.Context()), "") }()
	<-ui.entered
	select {
	case err := <-finished:
		t.Fatalf("the command returned before its dialog answered: %v", err)
	default:
	}
	close(ui.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if len(*rows.notify) != 1 || !strings.HasPrefix((*rows.notify)[0], "select=released") {
		t.Fatalf("notifications %q", *rows.notify)
	}

	cancelling := &blockingConformanceUI{recordingUI: rows.ui, entered: make(chan struct{}), release: make(chan struct{})}
	rows.runner.SetUIContext(cancelling, extension.ExtensionMode(conformanceMode))
	ctx, cancel := context.WithCancel(t.Context())
	go func() { finished <- command.Handler(rows.runner.DispatchContext(ctx), "") }()
	<-cancelling.entered
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait returned %v, want context.Canceled", err)
	}
}

type blockingConformanceUI struct {
	*recordingUI
	entered chan struct{}
	release chan struct{}
	once    atomic.Bool
}

func (u *blockingConformanceUI) Select(ctx context.Context, _ string, _ []string, _ extension.ExtensionUIDialogOptions) (string, error) {
	if u.once.CompareAndSwap(false, true) {
		close(u.entered)
	}
	select {
	case <-u.release:
		return "released", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// The api map is the capability inventory: a capability that is not a supported row cannot appear without this test and the
// unsupported-row list changing with it.
func TestLetGoConformance_APIMapIsTheSupportedInventory(t *testing.T) {
	rows := makeLetGoRows(t)
	keys := rows.loaded.APIKeys()
	slices.Sort(keys)
	want := []string{"extension-dir", "extension-name", "extension-path", "on-before-agent-start", "on-event", "on-tool-call", "on-tool-result", "register-command!", "register-tool!"}
	if !slices.Equal(keys, want) {
		t.Fatalf("api keys %v, want %v", keys, want)
	}
	if len(letGoUnsupportedRows) == 0 {
		t.Fatal("the unsupported inventory is empty")
	}
}
