package letgo

import (
	"bytes"
	"context"
	"errors"
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
   ((:register-tool! api) {:name "direct" :parameters {:type "object"} :execute (fn [p] {:content []})})
   (pig/register-command! api {:name "wrapped" :handler (fn [c args] nil)}))`)
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
   (pig/register-tool! api {:name "partial" :parameters {:type "object"} :execute (fn [p] {:content []})})
   (throw (ex-info "init failed" {})))`, "init failed"},
		{"top-level registration has no api", `(ns pig.lifecycle.toplevel (:require [pig.extension :as pig]))
 (pig/register-tool! {:name "early" :parameters {:type "object"} :execute (fn [p] {:content []})})
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

func loadWithOutput(t *testing.T, source string) (*Loaded, *bytes.Buffer, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "extension.lg")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	loaded, err := Load(t.Context(), LoadOptions{Entrypoint: path, Identity: extension.Extension{Name: "test", Path: path}, Streams: Streams{Stdout: &out}})
	return loaded, &out, err
}

const shutdownProbe = `(ns pig.lifecycle.probe (:require [pig.extension :as pig]))
 (def saved (atom nil))
 (defn init [api] (reset! saved api) (pig/register-command! api {:name "noop" :handler (fn [c args] nil)}))
 (defn shutdown [api]
   (println "shutdown" (identical? api @saved))
   (println (try (pig/register-tool! api {:name "late" :parameters {:type "object"} :execute (fn [p] {:content []})}) "registered" (catch e "rejected"))))`

func TestShutdownRunsOnceWithInitAPIAndRejectsRegistration(t *testing.T) {
	loaded, out, err := loadWithOutput(t, shutdownProbe)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "shutdown true\nrejected\n" {
		t.Fatalf("shutdown output %q", got)
	}
}

func TestShutdownRetriesAfterCancelledClose(t *testing.T) {
	loaded, out, err := loadWithOutput(t, shutdownProbe)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := loaded.Close(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled close: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("shutdown ran without VM entry: %q", out.String())
	}
	for range 2 {
		if err := loaded.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(out.String(), "shutdown "); got != 1 {
		t.Fatalf("shutdown ran %d times: %q", got, out.String())
	}
}

func TestShutdownFailureStillClosesGeneration(t *testing.T) {
	loaded, _, err := loadWithOutput(t, `(ns pig.lifecycle.failing (:require [pig.extension :as pig]))
 (defn init [api] (pig/register-command! api {:name "noop" :handler (fn [c args] nil)}))
 (defn shutdown [api] (throw (ex-info "shutdown failed" {})))`)
	if err != nil {
		t.Fatal(err)
	}
	command := loaded.Extension.Commands["noop"]
	if err := loaded.Close(t.Context()); err == nil || !strings.Contains(err.Error(), "shutdown failed") || !strings.Contains(err.Error(), ": shutdown:") {
		t.Fatalf("shutdown failure not reported: %v", err)
	}
	if err := command.Handler(t.Context(), ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("generation still callable after failed shutdown: %v", err)
	}
	if err := loaded.Close(t.Context()); err != nil {
		t.Fatalf("retry after failed shutdown re-ran it: %v", err)
	}
}

func TestShutdownSkippedAfterFailedInit(t *testing.T) {
	_, out, err := loadWithOutput(t, `(ns pig.lifecycle.noinit)
 (defn init [api] (throw (ex-info "init failed" {})))
 (defn shutdown [api] (println "shutdown"))`)
	if err == nil || !strings.Contains(err.Error(), "init failed") {
		t.Fatalf("init failure: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("shutdown ran after failed init: %q", out.String())
	}
}
