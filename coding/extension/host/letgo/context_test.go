package letgo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	codingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
)

// contextClosure returns one host-backed closure from a callback context map, as a retained Clojure reference would hold it.
func contextClosure(t *testing.T, ctx vm.Value, key string) *contextFunction {
	t.Helper()
	closure, ok := ctx.(vm.Lookup).ValueAt(vm.Keyword(key)).(*contextFunction)
	if !ok {
		t.Fatalf("context %v has no %s closure", ctx, key)
	}
	return closure
}

// contextToken returns the internal callback handle a context map's closures capture.
func contextToken(t *testing.T, ctx vm.Value) *invocationToken {
	t.Helper()
	return contextClosure(t, ctx, "is-idle").token
}

func TestContextReadsNativeValuesAndRetainsHandle(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.context.fixture (:require [pig.extension :as pig] [pig.context :as ctx]))
 (def saved (atom nil))
 (def observed (atom nil))
 (defn init [api]
 (pig/register-command! api {:name "capture" :handler (fn [c args] (reset! saved c))})
 (pig/register-command! api {:name "read" :handler (fn [c args] (reset! observed {:cwd (ctx/cwd @saved) :mode (ctx/mode @saved) :hasUI (ctx/has-ui? @saved) :idle (ctx/is-idle? @saved) :model (ctx/model @saved) :tools (ctx/get-active-tools @saved) :allTools (ctx/get-all-tools @saved)}))}))`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	idle := true
	model := &ai.Model{ID: "custom", ProviderMeta: ai.ProviderMetadata{ProviderID: "custom-provider", API: ai.APIOpenAICompletions, BaseURL: "http://localhost"}}
	active := []string{"hello"}
	runner.BindCore(extension.ExtensionActions{}, extension.ContextActions{IsIdle: func() bool { return idle }, GetModel: func() extension.Model { return model }, GetActiveTools: func() []string { return active }, GetAllTools: func() []extension.ToolInfo { return []extension.ToolInfo{{Name: "hello"}} }}, nil)
	if !runner.ExecuteCommand(t.Context(), "capture", "") {
		t.Fatal("capture not handled")
	}
	idle = false
	active = []string{"changed"}
	if !runner.ExecuteCommand(t.Context(), "read", "") {
		t.Fatal("read not handled")
	}
	value, err := loaded.generation.Run(t.Context(), `(deref pig.context.fixture/observed)`)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	got := plain.(map[string]any)
	native := extension.FromContext(runner.DispatchContext(t.Context()))
	cwd, _ := native.CWD()
	mode, _ := native.Mode()
	if got["cwd"] != cwd || got["mode"] != string(mode) || got["hasUI"] != false || got["idle"] != false {
		t.Fatalf("live reads %#v", got)
	}
	if !reflect.DeepEqual(got["tools"], []any{"changed"}) {
		t.Fatal(got)
	}
	if got["model"].(map[string]any)["id"] != model.ID {
		t.Fatal(got)
	}
	saved, err := loaded.generation.Run(t.Context(), `(deref pig.context.fixture/saved)`)
	if err != nil {
		t.Fatal(err)
	}
	runner.Invalidate("replacement")
	if _, err := contextClosure(t, saved, "is-idle").Invoke(nil); !errors.Is(err, extension.ErrStaleContext) {
		t.Fatalf("stale: %v", err)
	}
}

func TestContextRequestAndRunCancellationAreDistinct(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.cancel.fixture (:require [pig.extension :as pig] [pig.context :as ctx]))
 (def saved (atom nil)) (def signal (atom nil))
 (defn init [api]
 (pig/register-command! api {:name "capture" :handler (fn [c args] (reset! saved c) (reset! signal (ctx/signal c)))}))`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	var run context.Context
	runner.BindCore(extension.ExtensionActions{}, extension.ContextActions{GetSignal: func() context.Context { return run }}, nil)
	runner.ExecuteCommand(t.Context(), "capture", "")
	absent, err := loaded.generation.Run(t.Context(), `(deref pig.cancel.fixture/signal)`)
	if err != nil || absent != vm.NIL {
		t.Fatalf("idle signal %v, %v", absent, err)
	}
	var abort context.CancelFunc
	run, abort = context.WithCancel(t.Context())
	defer abort()
	request, cancelRequest := context.WithCancel(t.Context())
	runner.ExecuteCommand(request, "capture", "")
	savedContext, err := loaded.generation.Run(t.Context(), `(deref pig.cancel.fixture/saved)`)
	if err != nil {
		t.Fatal(err)
	}
	saved := contextToken(t, savedContext)
	signal, err := loaded.generation.Run(t.Context(), `(deref pig.cancel.fixture/signal)`)
	if err != nil {
		t.Fatal(err)
	}
	again, err := loaded.contextRead("signal", saved)
	if err != nil || again != signal {
		t.Fatalf("signal identity changed: %v", err)
	}
	cancelRequest()
	cancelled, err := loaded.contextRead("request-cancelled?", saved)
	if err != nil || cancelled != vm.TRUE {
		t.Fatalf("request cancelled=%v, %v", cancelled, err)
	}
	runCancelled, err := loaded.signalCancelled(signal)
	if err != nil || runCancelled != vm.FALSE {
		t.Fatalf("run cancelled=%v, %v", runCancelled, err)
	}
	abort()
	runCancelled, err = loaded.signalCancelled(signal)
	if err != nil || runCancelled != vm.TRUE {
		t.Fatalf("aborted=%v, %v", runCancelled, err)
	}
	if _, err := loaded.contextRead("cwd", vm.Map{}); err == nil {
		t.Fatal("forged context accepted")
	}
	other := loadToolSource(t, `(ns pig.test.fixture (:require [pig.extension])) (defn init [api] (pig.extension/register-command! api {:name "noop" :handler (fn [c args] nil)}))`)
	if _, err := other.contextRead("cwd", saved); err == nil {
		t.Fatal("foreign generation handle accepted")
	}
	if err := loaded.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.contextRead("cwd", saved); err == nil {
		t.Fatal("retired context accepted")
	}
}

func BenchmarkContextLargeSessionRead(b *testing.B) {
	path := filepath.Join(b.TempDir(), "extension.lg")
	if err := os.WriteFile(path, []byte(`(ns pig.test.fixture (:require [pig.extension])) (defn init [api] (pig.extension/register-command! api {:name "history" :handler (fn [c args] (pig.context/get-entries c) nil)}))`), 0o600); err != nil {
		b.Fatal(err)
	}
	loaded, err := Load(b.Context(), LoadOptions{Entrypoint: path})
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		if err := loaded.Close(context.Background()); err != nil {
			b.Error(err)
		}
	}()
	manager := codingagent.NewSession("benchmark", b.TempDir())
	for i := range 10000 {
		if _, err := manager.AppendCustomEntry("item", map[string]any{"index": i}); err != nil {
			b.Fatal(err)
		}
	}
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, b.TempDir())
	defer runner.Invalidate("benchmark complete")
	runner.BindSessionManager(manager)
	runner.AddErrorListener(func(err *extension.ExtensionError) { b.Error(err.Error) })
	for b.Loop() {
		if !runner.ExecuteCommand(b.Context(), "history", "") {
			b.Fatal("command missing")
		}
	}
}

func TestContextSessionEntriesAndBranchUseNativeSnapshots(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.history.fixture (:require [pig.extension :as pig] [pig.context :as ctx]))
 (def observed (atom nil))
 (defn init [api]
 (pig/register-command! api {:name "history" :handler (fn [c args] (reset! observed [(ctx/get-entries c) (ctx/get-branch c)]))}))`)
	manager := codingagent.NewSession("context-history", t.TempDir())
	for _, text := range []string{"first", "second", "third"} {
		if _, err := manager.AppendCustomEntry("ordered", map[string]any{"text": text}); err != nil {
			t.Fatal(err)
		}
	}
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	runner.BindSessionManager(manager)
	if !runner.ExecuteCommand(t.Context(), "history", "") {
		t.Fatal("history command not handled")
	}
	value, err := loaded.generation.Run(t.Context(), `(deref pig.history.fixture/observed)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	expectedValue, err := publicValue([]any{manager.Entries(), manager.GetBranch()})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := fromValue(expectedValue)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("history %#v; want %#v", got, expected)
	}
	entries := got.([]any)[0].([]any)
	if len(entries) != 3 || entries[0].(map[string]any)["data"].(map[string]any)["text"] != "first" {
		t.Fatalf("ordered entries %#v", entries)
	}
	entries[0].(map[string]any)["customType"] = "mutated snapshot"
	raw, err := json.Marshal(manager.Entries()[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "mutated snapshot") {
		t.Fatal("interpreter snapshot aliased session")
	}
}
