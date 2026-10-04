package main

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type letGoValidationReport struct {
	Valid      bool     `json:"valid"`
	Registered bool     `json:"registered"`
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	Hash       string   `json:"hash"`
	Tools      []string `json:"tools"`
	Commands   []string `json:"commands"`
	Handlers   []string `json:"handlers"`
	Phase      string   `json:"phase"`
	Code       string   `json:"code"`
	Error      string   `json:"error"`
	Warnings   []string `json:"warnings"`
	Definition struct {
		Language string `json:"language"`
		Runtime  string `json:"runtime"`
		SDK      string `json:"sdk"`
	} `json:"definition"`
}

func validateLetGo(t *testing.T, source string) (letGoValidationReport, int, string) {
	t.Helper()
	t.Setenv("PIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "extension.lg")
	writeStartupFixtureFile(t, path, source)
	stdout, stderr, code := captureStdoutStderr(t, func() int {
		return runPackageCommand([]string{"install", path, "--validate-only", "--json"})
	})
	var report letGoValidationReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("report is not JSON: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	return report, code, path
}

func TestInstallValidateOnlyLoadsAnInterpretedSourceWithoutRunningItsCallbacks(t *testing.T) {
	dir := t.TempDir()
	marker := func(name string) string { return strconv.Quote(filepath.Join(dir, name)) }
	report, code, _ := validateLetGo(t, `(ns validate.ok (:require [pig.extension :as ext]))
(defn init [api]
  (spit `+marker("init")+` "ran")
  (ext/register-tool! api {:name "echo" :description "Echo text" :parameters {:type "object"}
                           :execute (fn [args] (spit `+marker("tool")+` "ran") {:content "x"})})
  (ext/register-command! api {:name "greet" :description "Greet" :handler (fn [ctx args] (spit `+marker("command")+` "ran"))})
  (ext/on-event api :session-start (fn [event ctx] (spit `+marker("event")+` "ran")))
  (ext/on-tool-call api (fn [event] (spit `+marker("hook")+` "ran") nil)))
(defn shutdown [api] (spit `+marker("shutdown")+` "ran"))
`)
	if code != 0 || !report.Valid || !report.Registered {
		t.Fatalf("code %d report %+v", code, report)
	}
	if len(report.Tools) != 1 || report.Tools[0] != "echo" || len(report.Commands) != 1 || report.Commands[0] != "greet" {
		t.Fatalf("registered tools %v commands %v", report.Tools, report.Commands)
	}
	if want := []string{"session_start", "tool_call"}; len(report.Handlers) != 2 || report.Handlers[0] != want[0] || report.Handlers[1] != want[1] {
		t.Fatalf("handlers %v, want %v", report.Handlers, want)
	}
	if report.Definition.Language != "let-go" || report.Definition.Runtime != "let-go" || report.Definition.SDK != "" || report.Hash == "" {
		t.Fatalf("definition %+v hash %q", report.Definition, report.Hash)
	}
	if !fileExists(filepath.Join(dir, "init")) {
		t.Fatal("init did not run, so the registrations were not produced by the real loader")
	}
	for _, callback := range []string{"tool", "command", "event", "hook"} {
		if fileExists(filepath.Join(dir, callback)) {
			t.Errorf("validation ran the %s callback", callback)
		}
	}
	if !fileExists(filepath.Join(dir, "shutdown")) {
		t.Error("the generation was not disposed through shutdown after a completed init")
	}
}

func TestInstallValidateOnlyReportsTheFailingPhaseOfAnInterpretedSource(t *testing.T) {
	dir := t.TempDir()
	shutdown := strconv.Quote(filepath.Join(dir, "shutdown"))
	for _, test := range []struct {
		name, source, phase, cause string
	}{
		{"eval", `(ns validate.eval (defn init [api] (`, "eval", ""},
		{"top-level host call", `(ns validate.top (:require [pig.extension :as ext]))
(ext/register-tool! {:name "early"})
(defn init [api] nil)`, "eval", ""},
		{"missing init", `(ns validate.noinit)`, "init", "does not define init"},
		{"init is not a function", `(ns validate.value) (def init 7)`, "init", "init must be a function"},
		{"init throws", `(ns validate.throws) (defn init [api] (throw (ex-info "init exploded" {}))) (defn shutdown [api] (spit ` + shutdown + ` "ran"))`, "init", "init exploded"},
		{"registration rejected", `(ns validate.reg (:require [pig.extension :as ext]))
(defn init [api] (ext/register-tool! api {:name "bad" :parameters [] :execute (fn [args] nil)}))`, "register", "must define an object parameter schema"},
		{"unsupported Kmet key", `(ns validate.kmet (:require [pig.extension :as ext]))
(defn init [api] (ext/register-tool! api {:name "bad" :params {:x {:type :string}} :execute (fn [args] nil)}))`, "register", "$.params"},
	} {
		t.Run(test.name, func(t *testing.T) {
			report, code, path := validateLetGo(t, test.source)
			if code != 1 || report.Valid || report.Registered {
				t.Fatalf("code %d report %+v", code, report)
			}
			if report.Phase != test.phase || report.Code != "let-go" || report.Path != path {
				t.Fatalf("phase %q code %q path %q, want %q let-go %s", report.Phase, report.Code, report.Path, test.phase, path)
			}
			if !contains(report.Error, path) || !contains(report.Error, test.cause) {
				t.Fatalf("error %q lacks the source path or %q", report.Error, test.cause)
			}
			if fileExists(filepath.Join(dir, "shutdown")) {
				t.Fatal("shutdown ran for a source whose init did not complete")
			}
		})
	}
}

func contains(text, part string) bool { return strings.Contains(text, part) }
