package letgo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

func TestLifecycleInitReceivesCapabilityMap(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.lifecycle.api (:require [pig.extension :as pig]))
 (def seen (atom nil))
 (defn init [api]
   (reset! seen [(:extension-name api) (:extension-path api) (:extension-dir api)])
   ((:register-tool! api) {:name "direct" :parameters {:type "object"} :execute (fn [c p] {:content []})})
   (pig/register-command! api "wrapped" {:handler (fn [c args] nil)}))`)
	if loaded.entry.namespace != "pig.lifecycle.api" || loaded.entry.shutdown != nil {
		t.Fatalf("entry %#v", loaded.entry)
	}
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	if tools := runner.Tools(); len(tools) != 1 || tools[0].Definition.Name != "direct" {
		t.Fatalf("direct capability call did not register: %#v", tools)
	}
	if _, ok := loaded.Extension.Commands["wrapped"]; !ok {
		t.Fatalf("wrapper did not register: %#v", loaded.Extension.Commands)
	}
	value, err := loaded.generation.Run(t.Context(), `(deref pig.lifecycle.api/seen)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	identity := got.([]any)
	if identity[0] != "test" || identity[1] != loaded.path || identity[2] != filepath.Dir(loaded.path) {
		t.Fatalf("identity %#v", identity)
	}
}

func TestLifecycleResolvesOptionalShutdown(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.lifecycle.shutdown) (defn init [api] nil) (defn shutdown [api] nil)`)
	if loaded.entry.shutdown == nil {
		t.Fatal("shutdown was not resolved")
	}
}

func TestLifecycleActivationErrorsPublishNothing(t *testing.T) {
	for _, test := range []struct{ name, source, want string }{
		{"missing init", `(ns pig.lifecycle.missing)`, "does not define init"},
		{"non-callable init", `(ns pig.lifecycle.value) (def init 7)`, "init must be a function"},
		{"non-callable shutdown", `(ns pig.lifecycle.badshutdown) (defn init [api] nil) (def shutdown "no")`, "shutdown must be a function"},
		{"undeclared namespace", `(defn init [api] nil)`, "declare its own namespace"},
		{"init throws after registering", `(ns pig.lifecycle.throws (:require [pig.extension :as pig]))
 (defn init [api]
   (pig/register-tool! api {:name "partial" :parameters {:type "object"} :execute (fn [c p] {:content []})})
   (throw (ex-info "init failed" {})))`, "init failed"},
		{"top-level registration has no api", `(ns pig.lifecycle.toplevel (:require [pig.extension :as pig]))
 (pig/register-tool! {:name "early" :parameters {:type "object"} :execute (fn [c p] {:content []})})
 (defn init [api] nil)`, ": load:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "extension.lg")
			if err := os.WriteFile(path, []byte(test.source), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(t.Context(), LoadOptions{Entrypoint: path, Identity: extension.Extension{Name: "test", Path: path}})
			if err == nil || loaded != nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("loaded=%v err=%v, want %q", loaded, err, test.want)
			}
		})
	}
}
