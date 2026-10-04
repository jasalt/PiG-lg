package letgo

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadForTestCapturesInitRegistrationsWithoutSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extension.lg")
	if err := os.WriteFile(path, []byte(`(ns pig.testapi.fixture (:require [pig.extension :as pig] [pig.context :as ctx]))
 (defn init [api]
   (pig/register-tool! api {:name "first" :parameters {:type "object"} :execute (fn [p] {:content []})})
   (pig/register-tool! api {:name "second" :parameters {:type "object"} :execute (fn [p] {:content []})})
   (pig/register-command! api {:name "where" :handler (fn [c args] (when (nil? (ctx/cwd c)) (ctx/is-idle? c)))})
   (pig/on-event api :session-start (fn [e c] nil))
   (pig/on-event api :session-start (fn [e c] nil))
   (pig/on-event api :agent-end (fn [e c] nil)))`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, registrations, err := LoadForTest(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := loaded.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	want := Registrations{Tools: []string{"first", "second"}, Commands: []string{"where"}, Events: map[string]int{"session-start": 2, "agent-end": 1}, Handlers: []string{"agent_end", "session_start"}}
	if !reflect.DeepEqual(registrations, want) {
		t.Fatalf("registrations %#v, want %#v", registrations, want)
	}
	if err := loaded.Extension.Commands["where"].Handler(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "no native runtime binding") {
		t.Fatalf("unbound context read: %v", err)
	}
}
