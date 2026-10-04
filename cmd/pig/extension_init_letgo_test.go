package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	"github.com/MichaelKinsy/PiG/coding/extension/host/letgo"
)

func initLetGo(t *testing.T, name string, args ...string) (dir string, report extensionInitReport) {
	t.Helper()
	t.Setenv("PIG_HOME", t.TempDir())
	dir = filepath.Join(t.TempDir(), name)
	stdout, stderr, code := captureStdoutStderr(t, func() int {
		return runExtensionInit(append([]string{dir, "--lang", "let-go", "--json"}, args...))
	})
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("report is not JSON: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if code != 0 || !report.Created {
		t.Fatalf("init code %d report %+v\nstderr=%s", code, report, stderr)
	}
	return dir, report
}

func TestExtensionInitLetGoScaffoldsPortableCljcWithAPureCore(t *testing.T) {
	dir, report := initLetGo(t, "my-planner")
	if report.Language != "let-go" || report.SDKPath != "" || strings.Join(report.Files, ",") != "extension.cljc,my_planner/core.cljc" {
		t.Fatalf("report %+v", report)
	}
	for _, name := range []string{"go.mod", "extension.go", "extension.lg", "extension.edn", "Cargo.toml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("a let-go scaffold wrote %s: there is no manifest, SDK or toolchain file", name)
		}
	}
	entry, err := os.ReadFile(filepath.Join(dir, "extension.cljc"))
	if err != nil {
		t.Fatal(err)
	}
	core, err := os.ReadFile(filepath.Join(dir, "my_planner", "core.cljc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entry), "#?(:lg [pig.extension :as ext]\n               :default [kmet.extension :as ext])") || !strings.Contains(string(entry), "(defn init [api]") || !strings.Contains(string(entry), "(defn shutdown [_api]") {
		t.Fatalf("entry does not follow the portable shape:\n%s", entry)
	}
	if strings.Contains(string(core), "extension") {
		t.Fatalf("the pure core references a host:\n%s", core)
	}
}

func TestExtensionInitLetGoLgFlagScaffoldsLetGoSpecificSource(t *testing.T) {
	dir, report := initLetGo(t, "lg-only", "--lg")
	if strings.Join(report.Files, ",") != "extension.lg" {
		t.Fatalf("files %v", report.Files)
	}
	source, err := os.ReadFile(filepath.Join(dir, "extension.lg"))
	if err != nil || strings.Contains(string(source), "kmet.extension") || strings.Contains(string(source), "#?") {
		t.Fatalf("let-go-specific source is not host specific: %v\n%s", err, source)
	}
}

func TestExtensionInitLetGoRejectsFlagsThatDoNotApply(t *testing.T) {
	t.Setenv("PIG_HOME", t.TempDir())
	for _, args := range [][]string{{"--lang", "go", "--lg"}, {"--lang", "let-go", "--login"}, {"--lang", "let-go", "--isolated"}} {
		_, stderr, code := captureStdoutStderr(t, func() int {
			return runExtensionInit(append([]string{filepath.Join(t.TempDir(), "x"), "--json"}, args...))
		})
		if code == 0 {
			t.Errorf("%v accepted\n%s", args, stderr)
		}
	}
}

// Both scaffolds load against the pinned let-go with no external executable, and register what they declare.
func TestExtensionInitLetGoScaffoldsLoadAndRegister(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		entry string
	}{{"portable-ext", nil, "extension.cljc"}, {"specific-ext", []string{"--lg"}, "extension.lg"}} {
		t.Run(test.name, func(t *testing.T) {
			dir, _ := initLetGo(t, test.name, test.args...)
			loaded, registrations, err := letgo.LoadForTest(t.Context(), filepath.Join(dir, test.entry))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = loaded.Close(t.Context()) })
			if len(registrations.Tools) != 1 || registrations.Tools[0] != test.name+"_ping" || len(registrations.Commands) != 1 || registrations.Commands[0] != test.name {
				t.Fatalf("registered %+v", registrations)
			}
		})
	}
}

// The documented workflow with the real CLI: scaffold, validate, load into a session and invoke the tool, edit, then run the edit.
type notifyRecorder struct {
	extension.UIContext
	messages []string
}

func (n *notifyRecorder) Notify(message, kind string) {
	n.messages = append(n.messages, kind+": "+message)
}

// The scaffolded command takes the :lg branch at the host edge and builds its message with the pure core.
func TestExtensionInitLetGoCommandNotifiesThroughTheContextMap(t *testing.T) {
	dir, _ := initLetGo(t, "notify-ext")
	loaded, _, err := letgo.LoadForTest(t.Context(), filepath.Join(dir, "extension.cljc"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.Close(t.Context()) })
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, dir)
	t.Cleanup(func() { runner.Invalidate("test complete") })
	ui := &notifyRecorder{UIContext: extension.NoopUIContext}
	runner.SetUIContext(ui, extension.ModeTUI)
	if !runner.ExecuteCommand(t.Context(), "notify-ext", "  Ada ") {
		t.Fatal("command not handled")
	}
	if len(ui.messages) != 1 || ui.messages[0] != "info: hello from notify-ext, Ada" {
		t.Fatalf("notifications %q", ui.messages)
	}
}

func TestExtensionInitLetGoWorkflowThroughTheCLI(t *testing.T) {
	env := newLetGoStartupEnv(t)
	t.Setenv("PIG_HOME", filepath.Join(env.home, "pig"))
	dir := filepath.Join(env.home, "hello-ext")
	if _, _, code := captureStdoutStderr(t, func() int { return runExtensionInit([]string{dir, "--lang", "let-go"}) }); code != 0 {
		t.Fatalf("scaffold exited %d", code)
	}
	report, code, _ := validateLetGoAt(t, dir)
	if code != 0 || !report.Valid || len(report.Tools) != 1 || report.Tools[0] != "hello-ext_ping" {
		t.Fatalf("validate code %d report %+v", code, report)
	}
	// The faux model calls a tool named echo_bridge; the first edit points the scaffold's tool at it.
	entry := filepath.Join(dir, "extension.cljc")
	source, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.ReplaceAll(string(source), `"hello-ext_ping"`, `"echo_bridge"`)
	if err := os.WriteFile(entry, []byte(renamed), 0o600); err != nil {
		t.Fatal(err)
	}
	run, records := env.runJSON(t, "Run: extension echo hello", "-e", dir)
	if run.err != nil {
		t.Fatalf("%v\nstdout: %s\nstderr: %s", run.err, run.stdout, run.stderr)
	}
	if texts := toolResultTexts(records); len(texts) != 1 || texts[0] != "pong from hello-ext: hello" {
		t.Fatalf("tool results %q\nstderr: %s", texts, run.stderr)
	}
	// The second edit changes the pure core; a fresh load sees it.
	core := filepath.Join(dir, "hello_ext", "core.cljc")
	body, err := os.ReadFile(core)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core, []byte(strings.ReplaceAll(string(body), "pong from hello-ext: ", "edited pong: ")), 0o600); err != nil {
		t.Fatal(err)
	}
	run, records = env.runJSON(t, "Run: extension echo hello", "-e", dir)
	if run.err != nil {
		t.Fatalf("%v\nstderr: %s", run.err, run.stderr)
	}
	if texts := toolResultTexts(records); len(texts) != 1 || texts[0] != "edited pong: hello" {
		t.Fatalf("after the edit: %q\nstderr: %s", texts, run.stderr)
	}
}

func validateLetGoAt(t *testing.T, path string) (letGoValidationReport, int, string) {
	t.Helper()
	stdout, stderr, code := captureStdoutStderr(t, func() int {
		return runPackageCommand([]string{"install", path, "--validate-only", "--json"})
	})
	var report letGoValidationReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("report is not JSON: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	return report, code, path
}
