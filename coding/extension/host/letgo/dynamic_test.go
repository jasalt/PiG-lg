package letgo

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

func TestDynamicToolsPublishReplaceRefreshAndRetainOnRefreshError(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.dynamic.fixture (:require [pig.extension :as pig]))
 (pig/register-tool! {:name "early" :description "initial" :parameters {:type "object"} :execute (fn [c p] {:content [{:type "text" :text "initial"}]})})
 (pig/register-command! "replace" {:handler (fn [c a] (pig/register-tool! {:name "early" :description a :parameters {:type "object"} :execute (fn [c p] {:content [{:type "text" :text "replacement"}]})}))})
 (pig/register-command! "add" {:handler (fn [c a] (pig/register-tool! {:name "late" :description "late" :parameters {:type "object"} :execute (fn [c p] {:content [{:type "text" :text "late result"}]})}))})`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	var refreshFailure error
	var refreshed []string
	runner.BindCore(extension.ExtensionActions{}, extension.ContextActions{RefreshTools: func() error {
		refreshed = nil
		for _, tool := range runner.Tools() {
			refreshed = append(refreshed, tool.Definition.Description)
		}
		return refreshFailure
	}}, nil)
	native := extension.FromContext(runner.DispatchContext(t.Context()))
	if err := loaded.Bind(native); err != nil {
		t.Fatal(err)
	}
	var reports []*extension.ExtensionError
	runner.AddErrorListener(func(err *extension.ExtensionError) { reports = append(reports, err) })
	runner.ExecuteCommand(t.Context(), "replace", "replaced")
	if !reflect.DeepEqual(refreshed, []string{"replaced"}) {
		t.Fatal(refreshed)
	}
	refreshFailure = errors.New("refresh failed")
	runner.ExecuteCommand(t.Context(), "add", "")
	if !reflect.DeepEqual(refreshed, []string{"replaced", "late"}) || len(reports) != 1 {
		t.Fatalf("refresh %#v, reports %#v", refreshed, reports)
	}
	for _, tool := range runner.Tools() {
		result, err := tool.Definition.Execute(runner.DispatchContext(t.Context()), "id", json.RawMessage(`{}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"early": "replacement", "late": "late result"}[tool.Definition.Name]
		if want == "" || !strings.Contains(string(data), `"text":"`+want+`"`) {
			t.Fatalf("%s result %s; want text %q", tool.Definition.Name, data, want)
		}
	}
	runner.Invalidate("replacement")
	// The bound builder rejects a callback-time registration from a retained native definition even if its request context itself is live.
	command := loaded.Extension.Commands["add"]
	if err := command.Handler(t.Context(), ""); err == nil {
		t.Fatal("stale registration accepted")
	}
	if len(runner.Tools()) != 2 {
		t.Fatal("stale registration changed native registry")
	}
}
