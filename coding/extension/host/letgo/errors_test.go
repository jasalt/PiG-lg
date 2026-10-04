package letgo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func categoryOf(t *testing.T, err error) (*PhaseError, string) {
	t.Helper()
	phase, ok := errors.AsType[*PhaseError](err)
	if !ok {
		t.Fatalf("error is not a PhaseError: %v", err)
	}
	return phase, phase.Category()
}

func TestPhaseErrorCategoriesCoverEveryFailureOfAnInterpretedSource(t *testing.T) {
	for _, test := range []struct{ name, source, want string }{
		{"syntax", `(ns e.syntax (defn init [api] (`, PhaseEval},
		{"top-level call", `(ns e.top (:require [pig.extension :as ext])) (ext/register-tool! {}) (defn init [api] nil)`, PhaseEval},
		{"no init", `(ns e.noinit)`, PhaseInit},
		{"init throws", `(ns e.throws) (defn init [api] (throw (ex-info "x" {})))`, PhaseInit},
		{"rejected registration", `(ns e.reg (:require [pig.extension :as ext])) (defn init [api] (ext/register-tool! api {:name "t" :parameters [] :execute (fn [a] nil)}))`, PhaseRegister},
		{"unsupported Kmet key", `(ns e.kmet (:require [pig.extension :as ext])) (defn init [api] (ext/register-command! api {:name "c" :argument-hint "x" :handler (fn [c a] nil)}))`, PhaseRegister},
	} {
		path := filepath.Join(t.TempDir(), "extension.lg")
		if err := os.WriteFile(path, []byte(test.source), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(t.Context(), LoadOptions{Entrypoint: path})
		phase, got := categoryOf(t, err)
		if got != test.want || phase.Path != path {
			t.Errorf("%s: category %q path %q, want %q %q (%v)", test.name, got, phase.Path, test.want, path, err)
		}
	}
}

func TestPhaseErrorCategoriesOfCallbackAndShutdownFailures(t *testing.T) {
	loaded := loadToolSource(t, `(ns e.late (:require [pig.extension :as ext]))
(defn init [api]
  (ext/register-command! api {:name "boom" :handler (fn [ctx args] (throw (ex-info "command failed" {})))}))
(defn shutdown [api] (throw (ex-info "shutdown failed" {})))`)
	err := loaded.Extension.Commands["boom"].Handler(context.Background(), "")
	if phase, got := categoryOf(t, err); got != PhaseCallback || phase.Path != loaded.path {
		t.Fatalf("callback failure: category %q path %q (%v)", got, phase.Path, err)
	}
	err = loaded.Close(context.Background())
	if _, got := categoryOf(t, err); got != PhaseShutdown {
		t.Fatalf("shutdown failure: category %q (%v)", got, err)
	}
}
