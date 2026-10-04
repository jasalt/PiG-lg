package letgo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

func loadError(t *testing.T, source string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "extension.lg")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(t.Context(), LoadOptions{Entrypoint: path})
	if err == nil {
		_ = loaded.Close(context.Background())
	}
	return err
}

func TestToolOneArgumentExecuteThroughRunnerCoercesStringContent(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.test.fixture (:require [pig.extension :as ext]))
 (defn init [api]
   (ext/register-tool! api {:name "echo" :description "Echo" :parameters {:type "object"}
     :execute (fn [& received] {:content (str "echo: " (:text (first received)) " arity " (count received))})}))`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	result, err := runner.Tools()[0].Definition.Execute(runner.DispatchContext(t.Context()), "call", json.RawMessage(`{"text":"hi"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	native := result.(agent.AgentToolResult)
	if len(native.Content) != 1 || native.Text() != "echo: hi arity 1" {
		t.Fatalf("one-argument result %#v", native)
	}
}

func TestToolContextualReceivesUpdatesSignalAndContext(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.test.fixture (:require [pig.extension :as ext] [pig.context :as ctx]))
 (def observed (atom nil))
 (def retained (atom nil))
 (defn init [api]
   (ext/register-tool! api {:name "work" :parameters {:type "object"} :contextual? true
     :execute (fn [args on-update signal c]
                (let [before @signal]
                  (on-update {:content "halfway"})
                  (reset! retained on-update)
                  (reset! observed [before @signal (ctx/signal-cancelled? signal) (ctx/request-cancelled? c) (:n args)])
                  {:content [{:type "text" :text "done"}]}))}))`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	ctx, cancel := context.WithCancel(runner.DispatchContext(t.Context()))
	defer cancel()
	var updates []string
	update := agent.ToolUpdateCallback(func(partial agent.AgentToolResult) {
		updates = append(updates, partial.Text())
		cancel()
	})
	result, err := runner.Tools()[0].Definition.Execute(ctx, "call", json.RawMessage(`{"n":3}`), update)
	if err != nil {
		t.Fatal(err)
	}
	if result.(agent.AgentToolResult).Text() != "done" || len(updates) != 1 || updates[0] != "halfway" {
		t.Fatalf("result %#v updates %q", result, updates)
	}
	value, err := loaded.generation.Run(t.Context(), `(deref pig.test.fixture/observed)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	if want := []any{false, true, true, true, int64(3)}; !equalPlain(got, want) {
		t.Fatalf("observed %#v, want %#v", got, want)
	}
	if _, err := loaded.generation.Run(t.Context(), `((deref pig.test.fixture/retained) {:content "late"})`); err == nil || !strings.Contains(err.Error(), "outside its tool call") {
		t.Fatalf("retained on-update after return: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("late update delivered: %q", updates)
	}
}

func equalPlain(got any, want []any) bool {
	items, ok := got.([]any)
	if !ok || len(items) != len(want) {
		return false
	}
	for i := range items {
		if items[i] != want[i] {
			return false
		}
	}
	return true
}

func TestToolAndCommandRegistrationRejectsMissingNamesAndUnsupportedKeys(t *testing.T) {
	const tool = `(ns pig.test.fixture (:require [pig.extension :as ext])) (defn init [api] (ext/register-tool! api {%s :parameters {:type "object"} :execute (fn [args] {:content ""})}))`
	const command = `(ns pig.test.fixture (:require [pig.extension :as ext])) (defn init [api] (ext/register-command! api {%s :handler (fn [c args] nil)}))`
	for _, test := range []struct{ source, want string }{
		{strings.Replace(tool, "%s", "", 1), "$.name: tool requires a non-empty string name"},
		{strings.Replace(tool, "%s", `:name ""`, 1), "$.name: tool requires a non-empty string name"},
		{strings.Replace(tool, "%s", `:name "t" :params {:path {:type :string}}`, 1), "$.params: Kmet :params shorthand is not supported"},
		{strings.Replace(tool, "%s", `:name "t" :prepare-arguments (fn [a] a)`, 1), `$["prepare-arguments"]: Kmet :prepare-arguments is not supported`},
		{strings.Replace(tool, "%s", `:name "t" :title (fn [a] "x")`, 1), "$.title: Kmet :title is not supported"},
		{strings.Replace(tool, "%s", `:name "t" :streams? true`, 1), `$["streams?"]: Kmet :streams? is not supported`},
		{strings.Replace(tool, "%s", `:name "t" :execution-mode "parallel" :executionMode "parallel"`, 1), `keys "execution-mode" and "executionMode" both name field "executionMode"`},
		{strings.Replace(tool, "%s", `:name "t" :execution-order "x"`, 1), `$["execution-order"]: unsupported registration key`},
		{strings.Replace(tool, "%s", `:name "t" :contextual? "yes"`, 1), `$["contextual?"]: expected a boolean`},
		{strings.Replace(command, "%s", ``, 1), "$.name: command requires a non-empty string name"},
		{strings.Replace(command, "%s", `:name 12`, 1), "register command"},
		{strings.Replace(command, "%s", `:name "c" :argument-hint "<arg>"`, 1), `$["argument-hint"]: Kmet :argument-hint has no native command field`},
		{strings.Replace(command, "%s", `:name "c" :get-argument-completions "nope"`, 1), `$["get-argument-completions"]: expected a function`},
		{strings.Replace(command, "%s", `:name "c" :aliases ["d"]`, 1), "$.aliases: unsupported registration key"},
	} {
		if err := loadError(t, test.source); err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), ": init: ") {
			t.Errorf("source %s\nwant %q, got %v", test.source, test.want, err)
		}
	}
}

func TestToolAndCommandRegistrationReturnNil(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.test.fixture (:require [pig.extension :as ext]))
 (def returned (atom nil))
 (defn init [api]
   (reset! returned [(ext/register-tool! api {:name "t" :parameters {:type "object"} :execute (fn [args] {:content ""})})
                     (ext/register-command! api {:name "c" :handler (fn [c args] nil)})]))`)
	value, err := loaded.generation.Run(t.Context(), `(deref pig.test.fixture/returned)`)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fromValue(value); err != nil || !equalPlain(got, []any{nil, nil}) {
		t.Fatalf("registration returns %#v, %v", got, err)
	}
}

func TestCommandMapDispatchesContextArgsAndArgumentCompletions(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.test.fixture (:require [clojure.string :as str] [pig.extension :as ext] [pig.context :as ctx]))
 (def received (atom nil))
 (defn init [api]
   (ext/register-command! api {:name "pick" :description "Pick one"
     :get-argument-completions (fn [prefix] (filterv #(str/starts-with? (:value %) prefix) [{:value "alpha" :label "A"} {:value "beta"}]))
     :handler (fn [c args] (reset! received [(ctx/cwd c) args]))}))`)
	dir := t.TempDir()
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, dir)
	defer runner.Invalidate("test complete")
	commands := runner.Commands()
	if len(commands) != 1 || commands[0].Description != "Pick one" || commands[0].GetArgumentCompletions == nil {
		t.Fatalf("commands %#v", commands)
	}
	items, err := commands[0].GetArgumentCompletions("al")
	if err != nil || len(items) != 1 || items[0] != (extension.AutocompleteItem{Value: "alpha", Label: "A"}) {
		t.Fatalf("completions %#v, %v", items, err)
	}
	if !runner.ExecuteCommand(t.Context(), "pick", "beta") {
		t.Fatal("command not handled")
	}
	value, err := loaded.generation.Run(t.Context(), `(deref pig.test.fixture/received)`)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fromValue(value); err != nil || !equalPlain(got, []any{dir, "beta"}) {
		t.Fatalf("received %#v, %v", got, err)
	}
}

// kmetCommandsAndTools is Kmet's extension.md "Commands and tools" example with the namespace swapped and its
// unregister/get calls, which PiG does not provide, omitted.
const kmetCommandsAndTools = `(ns kmet.example (:require [pig.extension :as ext]))
(defn init [api]
(ext/register-command! api
  {:name "cmd" :description "..." :argument-hint "<arg>"
   :get-argument-completions (fn [prefix] [{:value "a" :label "A"}])
   :handler (fn [ctx args] nil)})
(ext/register-tool! api
  {:name "my-tool" :description "..."
   :params {:path {:type :string :description "..."}}
   :execute (fn [args] {:content "..." :is-error false})}))`

func TestToolKmetDocumentationExampleLoadsWhereTheDecisionAllows(t *testing.T) {
	if err := loadError(t, kmetCommandsAndTools); err == nil || !strings.Contains(err.Error(), `$["argument-hint"]`) {
		t.Fatalf("verbatim Kmet example: %v", err)
	}
	withoutHint := strings.Replace(kmetCommandsAndTools, ` :argument-hint "<arg>"`, "", 1)
	if err := loadError(t, withoutHint); err == nil || !strings.Contains(err.Error(), "$.params") {
		t.Fatalf("Kmet :params example: %v", err)
	}
	allowed := strings.Replace(withoutHint, `:params {:path {:type :string :description "..."}}`, `:parameters {:type "object" :properties {:path {:type "string" :description "..."}}}`, 1)
	loaded := loadToolSource(t, allowed)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	result, err := runner.Tools()[0].Definition.Execute(runner.DispatchContext(t.Context()), "call", json.RawMessage(`{"path":"x"}`), nil)
	if err != nil || result.(agent.AgentToolResult).Text() != "..." || result.(agent.AgentToolResult).IsError {
		t.Fatalf("Kmet tool result %#v, %v", result, err)
	}
	if !runner.ExecuteCommand(t.Context(), "cmd", "") {
		t.Fatal("Kmet command not handled")
	}
}

func TestToolRegistrationAcceptsKebabAndNativeKeysWithVerbatimSchemas(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.test.fixture (:require [pig.extension :as ext]))
 (defn init [api]
   (ext/register-tool! api {:name "kebab" :prompt-snippet "snippet" :execution-mode "parallel" :default-active false
     :parameters {:type "object" :properties {:filePath {:type "string"} :max_count {:type "integer"}}}
     :execute (fn [args] {:content ""})})
   (ext/register-tool! api {:name "native" :promptSnippet "other" :executionMode "sequential"
     :parameters {:type "object"} :execute (fn [args] {:content ""})}))`)
	tools := loaded.Extension.RegisteredTools()
	kebab, native := tools[0].Definition, tools[1].Definition
	if kebab.PromptSnippet != "snippet" || kebab.ExecutionMode != "parallel" || kebab.DefaultActive == nil || *kebab.DefaultActive {
		t.Fatalf("kebab definition %#v", kebab)
	}
	if native.PromptSnippet != "other" || native.ExecutionMode != "sequential" {
		t.Fatalf("native definition %#v", native)
	}
	if want := `{"properties":{"filePath":{"type":"string"},"max_count":{"type":"integer"}},"type":"object"}`; string(kebab.Parameters) != want {
		t.Fatalf("schema %s, want %s", kebab.Parameters, want)
	}
}
