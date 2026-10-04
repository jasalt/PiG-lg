package letgo

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

func hookSource(body string) string {
	return `(ns pig.hooks.fixture (:require [pig.extension :as ext]))
 (def seen (atom nil)) (def calls (atom 0))
 (defn init [api] ` + body + `)`
}

func hookRunner(t *testing.T, loaded ...*Loaded) (*inproc.Runner, *[]string) {
	t.Helper()
	extensions := make([]extension.Extension, len(loaded))
	for i, l := range loaded {
		extensions[i] = l.Extension
	}
	runner := inproc.NewRunner(extensions, t.TempDir())
	t.Cleanup(func() { runner.Invalidate("test complete") })
	var reports []string
	runner.AddErrorListener(func(err *extension.ExtensionError) { reports = append(reports, err.Error) })
	return runner, &reports
}

func seenPlain(t *testing.T, loaded *Loaded) any {
	t.Helper()
	value, err := loaded.generation.Run(t.Context(), `(deref pig.hooks.fixture/seen)`)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

func hookSections() *ai.OrderedSections {
	zeta, alpha := "zeta text", "alpha text"
	return &ai.OrderedSections{{Name: "zeta", Value: &zeta}, {Name: "alpha", Value: &alpha}}
}

func TestBeforeAgentStartHookKeepsRawSelectedToolsAndSectionOrder(t *testing.T) {
	native := extension.Extension{Path: "native-repair"}
	native.InitializeEventHandlers()
	native.AddEventHandler("before_agent_start", 1, func(args ...any) (any, error) {
		extension.SetBeforeAgentStartSelectedTools(args[1].(context.Context), json.RawMessage(`["read",7]`))
		return nil, nil
	})
	loaded := loadToolSource(t, hookSource(`(ext/on-before-agent-start api (fn [e]
     (reset! seen {:prompt (:prompt e) :tools (:selected-tools (:system-prompt-options e))
                   :sections (mapv (juxt :name :value) (:sections (:system-prompt-options e)))})
     {:system-prompt "changed" :message {:custom-type "note" :content "hi" :display true}}))`))
	runner := inproc.NewRunner([]extension.Extension{native, loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	options := extension.BuildSystemPromptOptions{SelectedTools: []string{"read"}, Sections: hookSections()}
	combined, err := runner.EmitBeforeAgentStart(t.Context(), "do it", nil, "base", options)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"prompt": "do it",
		"tools": []any{"read", int64(7)}, "sections": []any{[]any{"zeta", "zeta text"}, []any{"alpha", "alpha text"}}}
	if got := seenPlain(t, loaded); !reflect.DeepEqual(got, want) {
		t.Fatalf("event view %#v\nwant %#v", got, want)
	}
	if combined == nil || combined.SystemPrompt == nil || *combined.SystemPrompt != "changed" || len(combined.Messages) != 1 || combined.Messages[0].CustomType != "note" || combined.Messages[0].Content != "hi" {
		t.Fatalf("combined %#v", combined)
	}
	if !combined.SelectedToolsEdited {
		t.Fatal("raw selectedTools edit was lost")
	}
}

func TestBeforeAgentStartHookChainsInLoadOrderAndNilKeepsPrompt(t *testing.T) {
	first := loadToolSource(t, hookSource(`(ext/on-before-agent-start api (fn [e] {:system-prompt (str (:system-prompt e) "+one")}))`))
	second := loadToolSource(t, hookSource(`(ext/on-before-agent-start api (fn [e] (reset! seen (:system-prompt e)) nil))`))
	third := loadToolSource(t, hookSource(`(ext/on-before-agent-start api (fn [e] {}))`))
	runner, reports := hookRunner(t, first, second, third)
	combined, err := runner.EmitBeforeAgentStart(t.Context(), "p", nil, "base", extension.BuildSystemPromptOptions{})
	if err != nil || len(*reports) != 0 {
		t.Fatalf("err %v reports %q", err, *reports)
	}
	if got := seenPlain(t, second); got != "base+one" {
		t.Fatalf("second handler saw %v", got)
	}
	if combined == nil || *combined.SystemPrompt != "base+one" {
		t.Fatalf("combined %#v", combined)
	}
}

func TestBeforeAgentStartHookRejectsMalformedResultsWithoutLosingLaterHandlers(t *testing.T) {
	for _, test := range []struct{ result, want string }{
		{`{:system-prompt 1}`, "systemPrompt"},
		{`{:bogus 1}`, "$.bogus: unsupported registration key"},
		{`5`, "result must be a map or nil"},
		{`(throw (ex-info "hook failed" {}))`, "hook failed"},
	} {
		bad := loadToolSource(t, hookSource(`(ext/on-before-agent-start api (fn [e] `+test.result+`))`))
		good := loadToolSource(t, hookSource(`(ext/on-before-agent-start api (fn [e] {:system-prompt "good"}))`))
		runner, reports := hookRunner(t, bad, good)
		combined, err := runner.EmitBeforeAgentStart(t.Context(), "p", nil, "base", extension.BuildSystemPromptOptions{})
		if err != nil || len(*reports) != 1 || !strings.Contains((*reports)[0], test.want) || !strings.Contains((*reports)[0], bad.path) {
			t.Fatalf("%s: err %v reports %q", test.result, err, *reports)
		}
		if combined == nil || *combined.SystemPrompt != "good" {
			t.Fatalf("%s: later handler lost: %#v", test.result, combined)
		}
	}
}

func TestToolCallHookBlocksStopsChainAndRejectsInputRewrite(t *testing.T) {
	event := extension.CustomToolCallEvent{ToolCallEventBase: extension.ToolCallEventBase{Type: "tool_call", ToolCallID: "call-1"}, ToolName: "custom", Input: map[string]any{"filePath": "a.go"}}
	pass := loadToolSource(t, hookSource(`(ext/on-tool-call api (fn [e] (reset! seen [(:tool-call-id e) (:tool-name e) (:filePath (:input e))]) nil))`))
	block := loadToolSource(t, hookSource(`(ext/on-tool-call api (fn [e] {:block true :reason "no" :terminate true}))`))
	after := loadToolSource(t, hookSource(`(ext/on-tool-call api (fn [e] (swap! calls inc) nil))`))
	runner, _ := hookRunner(t, pass, block, after)
	result, err := runner.EmitToolCall(t.Context(), event)
	if err != nil || result == nil || !result.Block || result.Reason != "no" || !result.Terminate {
		t.Fatalf("block result %#v, %v", result, err)
	}
	if got := seenPlain(t, pass); !reflect.DeepEqual(got, []any{"call-1", "custom", "a.go"}) {
		t.Fatalf("event view %#v", got)
	}
	if calls, _ := loaded(t, after, `(deref pig.hooks.fixture/calls)`); calls != int64(0) {
		t.Fatalf("handler after a block ran %v times", calls)
	}
	for _, test := range []struct{ result, want string }{
		{`{:args {:filePath "b.go"}}`, "rewriting tool call input is not supported"},
		{`(throw (ex-info "guard failed" {}))`, "guard failed"},
	} {
		bad := loadToolSource(t, hookSource(`(ext/on-tool-call api (fn [e] `+test.result+`))`))
		runner, _ := hookRunner(t, bad)
		if result, err := runner.EmitToolCall(t.Context(), event); err == nil || result != nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: result %#v err %v", test.result, result, err)
		}
	}
}

func loaded(t *testing.T, l *Loaded, source string) (any, error) {
	t.Helper()
	value, err := l.generation.Run(t.Context(), source)
	if err != nil {
		return nil, err
	}
	return fromValue(value)
}

func TestToolResultHookChainsReplacementsAndKeepsDetailsKeys(t *testing.T) {
	event := extension.CustomToolResultEvent{
		ToolResultEventBase: extension.ToolResultEventBase{Type: "tool_result", ToolCallID: "call-1", Input: map[string]any{"filePath": "a.go"}, Content: []any{ai.TextContent{Text: "original"}}},
		ToolName:            "custom", Details: map[string]any{"camelKey": 1},
	}
	redact := loadToolSource(t, hookSource(`(ext/on-tool-result api (fn [e]
     (reset! seen [(:tool-name e) (:is-error e) (:camelKey (:details e)) (:filePath (:input e))])
     {:content [{:type "text" :text "redacted"}] :is-error true :details {:keptKey {:inner_key 1}}}))`))
	observe := loadToolSource(t, hookSource(`(ext/on-tool-result api (fn [e] (reset! seen [(:text (first (:content e))) (:is-error e) (:keptKey (:details e))]) nil))`))
	broken := loadToolSource(t, hookSource(`(ext/on-tool-result api (fn [e] (throw (ex-info "result hook failed" {}))))`))
	runner, reports := hookRunner(t, redact, observe, broken)
	result, err := runner.EmitToolResult(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	if got := seenPlain(t, redact); !reflect.DeepEqual(got, []any{"custom", false, int64(1), "a.go"}) {
		t.Fatalf("first handler saw %#v", got)
	}
	if got := seenPlain(t, observe); !reflect.DeepEqual(got, []any{"redacted", true, map[string]any{"inner_key": int64(1)}}) {
		t.Fatalf("chained handler saw %#v", got)
	}
	if len(*reports) != 1 || !strings.Contains((*reports)[0], "result hook failed") {
		t.Fatalf("reports %q", *reports)
	}
	details, _ := json.Marshal(result.Details)
	wantContent := []any{map[string]any{"type": "text", "text": "redacted"}}
	if result.IsError == nil || !*result.IsError || string(details) != `{"keptKey":{"inner_key":1}}` || !reflect.DeepEqual(result.Content, wantContent) {
		t.Fatalf("result %#v details %s", result, details)
	}
	unchanged := loadToolSource(t, hookSource(`(ext/on-tool-result api (fn [e] nil))`))
	runner, _ = hookRunner(t, unchanged)
	if result, err := runner.EmitToolResult(t.Context(), event); err != nil || result != nil {
		t.Fatalf("unchanged result %#v, %v", result, err)
	}
}

func TestResultEventsRejectOnEventAndNameTheirWrapper(t *testing.T) {
	for event, wrapper := range map[string]string{"before-agent-start": "on-before-agent-start", "tool-call": "on-tool-call", "tool-result": "on-tool-result"} {
		err := loadError(t, `(ns pig.test.fixture (:require [pig.extension :as ext])) (defn init [api] (ext/on-event api :`+event+` (fn [e c] nil)))`)
		if err == nil || !strings.Contains(err.Error(), "register it with "+wrapper) {
			t.Errorf("%s: %v", event, err)
		}
	}
}
