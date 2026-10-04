package letgo

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

const contextMapFixture = `(ns pig.map.fixture (:require [pig.extension :as pig]))
 (def saved (atom nil)) (def plain (atom nil))
 (defn init [api]
   (pig/register-command! api {:name "capture" :handler (fn [c args]
     (reset! saved c)
     (reset! plain {:cwd (:cwd c) :mode (:mode c) :has-ui (:has-ui c) :model-id (:id (:model c)) :keys (sort (map name (keys c)))}))}))`

func TestContextMapHasSnapshotKeysAndClosuresReadableByPlainClojure(t *testing.T) {
	loaded := loadToolSource(t, contextMapFixture)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	model := &ai.Model{ID: "custom", ProviderMeta: ai.ProviderMetadata{ProviderID: "p", API: ai.APIOpenAICompletions, BaseURL: "http://localhost"}}
	runner.BindCore(extension.ExtensionActions{}, extension.ContextActions{GetModel: func() extension.Model { return model }}, nil)
	if !runner.ExecuteCommand(t.Context(), "capture", "") {
		t.Fatal("capture not handled")
	}
	value, err := loaded.generation.Run(t.Context(), `(deref pig.map.fixture/plain)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	native := extension.FromContext(runner.DispatchContext(t.Context()))
	cwd, _ := native.CWD()
	mode, _ := native.Mode()
	want := map[string]any{"cwd": cwd, "mode": string(mode), "has-ui": false, "model-id": "custom", "keys": []any{
		"confirm", "cwd", "get-active-tools", "get-all-tools", "get-branch", "get-entries", "has-ui", "input", "is-idle", "mode", "model", "notify", "request-cancelled", "select", "signal"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("context map %#v\nwant %#v", got, want)
	}
}

func TestContextMapEscapedClosuresStayLiveThenGoStale(t *testing.T) {
	loaded := loadToolSource(t, contextMapFixture)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	idle := true
	runner.BindCore(extension.ExtensionActions{}, extension.ContextActions{IsIdle: func() bool { return idle }}, nil)
	if !runner.ExecuteCommand(t.Context(), "capture", "") {
		t.Fatal("capture not handled")
	}
	idle = false
	// Pi keeps a captured ctx live until session replacement, so a retained closure reads live state after its callback returned.
	live, err := loaded.generation.Run(t.Context(), `((:is-idle @pig.map.fixture/saved))`)
	if err != nil || live.String() != "false" {
		t.Fatalf("retained is-idle %v, %v", live, err)
	}
	runner.Invalidate("replacement")
	_, err = loaded.generation.Run(t.Context(), `((:is-idle @pig.map.fixture/saved))`)
	if err == nil || !strings.Contains(err.Error(), "replacement") {
		t.Fatalf("closure after replacement: %v", err)
	}
	saved, err := loaded.generation.Run(t.Context(), `(deref pig.map.fixture/saved)`)
	if err != nil {
		t.Fatal(err)
	}
	message, _ := toValue("hello")
	for key, args := range map[string][]vm.Value{"is-idle": nil, "signal": nil, "get-entries": nil, "notify": {message}} {
		if _, err := contextClosure(t, saved, key).Invoke(args); !errors.Is(err, extension.ErrStaleContext) {
			t.Errorf("%s after replacement: %v", key, err)
		}
	}
}

func TestContextMapClosureReportsRequestCancellationAndRejectsArguments(t *testing.T) {
	loaded := loadToolSource(t, contextMapFixture)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	request, cancel := context.WithCancel(t.Context())
	runner.ExecuteCommand(request, "capture", "")
	cancelled, err := loaded.generation.Run(t.Context(), `((:request-cancelled @pig.map.fixture/saved))`)
	if err != nil || cancelled.String() != "false" {
		t.Fatalf("before cancel %v, %v", cancelled, err)
	}
	cancel()
	cancelled, err = loaded.generation.Run(t.Context(), `((:request-cancelled @pig.map.fixture/saved))`)
	if err != nil || cancelled.String() != "true" {
		t.Fatalf("after cancel %v, %v", cancelled, err)
	}
	if _, err := loaded.generation.Run(t.Context(), `((:cwd-fn-missing @pig.map.fixture/saved))`); err == nil {
		t.Fatal("absent closure was callable")
	}
	if _, err := loaded.generation.Run(t.Context(), `((:is-idle @pig.map.fixture/saved) 1)`); err == nil || !strings.Contains(err.Error(), "expected no arguments") {
		t.Fatalf("argument to live read: %v", err)
	}
}

func TestContextMapUINotifyAcceptsKmetKeywordLevel(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.map.ui (:require [pig.extension :as pig]))
 (defn init [api]
   (pig/register-command! api {:name "tell" :handler (fn [c args] ((:notify c) "one") ((:notify c) "two" :warning) ((:notify c) "three" "error"))}))`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	ui := &dialogUI{UIContext: extension.NoopUIContext}
	runner.SetUIContext(ui, extension.ModeTUI)
	if !runner.ExecuteCommand(t.Context(), "tell", "") {
		t.Fatal("not handled")
	}
	if want := []string{"one:info", "two:warning", "three:error"}; !reflect.DeepEqual(ui.notifications, want) {
		t.Fatalf("notifications %q, want %q", ui.notifications, want)
	}
}

func TestContextMapAcceptsAProjectedMapModelAndSurvivesAnUnsupportedOne(t *testing.T) {
	for _, test := range []struct {
		name  string
		model extension.Model
		want  any
	}{
		{"projected map as interactive mode supplies", map[string]any{"id": "interactive-model", "contextWindow": 1000}, "interactive-model"},
		{"unsupported shape", 42, nil},
		{"none", nil, nil},
	} {
		loaded := loadToolSource(t, contextMapFixture)
		runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
		runner.BindCore(extension.ExtensionActions{}, extension.ContextActions{GetModel: func() extension.Model { return test.model }}, nil)
		if !runner.ExecuteCommand(t.Context(), "capture", "") {
			t.Fatalf("%s: the callback did not run", test.name)
		}
		value, err := loaded.generation.Run(t.Context(), `(deref pig.map.fixture/plain)`)
		if err != nil {
			t.Fatal(err)
		}
		got, err := fromValue(value)
		if err != nil {
			t.Fatal(err)
		}
		if id := got.(map[string]any)["model-id"]; id != test.want {
			t.Errorf("%s: model id %v, want %v", test.name, id, test.want)
		}
		runner.Invalidate("test complete")
	}
}
