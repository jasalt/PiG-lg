package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nooga/let-go/pkg/rt"
	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	"github.com/MichaelKinsy/PiG/coding/extension/host/letgo"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
	"github.com/MichaelKinsy/PiG/internal/testbudget"
)

// letGoStartupEnv is a hermetic home for one binary run. The binary needs no lg executable: let-go is interpreted in process.
type letGoStartupEnv struct {
	binary, home, agentDir, cwd, sessions string
}

func newLetGoStartupEnv(t *testing.T) letGoStartupEnv {
	t.Helper()
	home := t.TempDir()
	env := letGoStartupEnv{binary: buildPigBinaryForSignalTest(t), home: home, agentDir: filepath.Join(home, "agent"), cwd: filepath.Join(home, "project"), sessions: filepath.Join(home, "sessions")}
	if err := os.MkdirAll(env.cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	return env
}

func (e letGoStartupEnv) write(t *testing.T, path, body string) string {
	t.Helper()
	writeStartupFixtureFile(t, path, body)
	return path
}

// runJSON runs one prompt in JSON mode and returns its run and the parsed stdout records.
func (e letGoStartupEnv) runJSON(t *testing.T, prompt string, args ...string) (startupRun, []map[string]any) {
	t.Helper()
	all := append([]string{"--mode", "json", "--session-dir", e.sessions, "--model", "test-faux/faux-1"}, args...)
	run := runPigStartup(t, e.binary, e.home, e.agentDir, e.cwd, "", append(all, prompt)...)
	var records []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(run.stdout))
	scanner.Buffer(nil, 1<<24)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("stdout line is not JSON (interpreted output leaked?): %q\nstderr: %s", scanner.Text(), run.stderr)
		}
		records = append(records, record)
	}
	return run, records
}

func toolResultTexts(records []map[string]any) []string {
	var texts []string
	for _, record := range records {
		if record["type"] != "tool_execution_end" {
			continue
		}
		result, _ := record["result"].(map[string]any)
		content, _ := result["content"].([]any)
		for _, block := range content {
			if text, ok := block.(map[string]any)["text"].(string); ok {
				texts = append(texts, text)
			}
		}
	}
	return texts
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func letGoToolSource(namespace, marker, shutdownMarker string) string {
	return `(ns ` + namespace + ` (:require [pig.extension :as ext]))
(spit ` + strconv.Quote(marker) + ` "evaluated")
(defn init [api]
  (println "interpreted stdout must not reach the mode's stream")
  (ext/register-tool!
   api
   {:name "echo_bridge" :description "let-go probe"
    :parameters {:type "object" :properties {:text {:type "string"}}}
    :execute (fn [args] {:content (str "let-go says " (:text args))})}))
(defn shutdown [api] (spit ` + strconv.Quote(shutdownMarker) + ` "shutdown"))
`
}

func TestLetGoStartupRunsExplicitToolAndShutsDownOnNormalExit(t *testing.T) {
	env := newLetGoStartupEnv(t)
	marker, shutdown := filepath.Join(env.home, "loaded"), filepath.Join(env.home, "shutdown")
	entry := env.write(t, filepath.Join(env.home, "probe.lg"), letGoToolSource("letgo.startup.probe", marker, shutdown))
	run, records := env.runJSON(t, "Run: extension echo hello", "-e", entry)
	if run.err != nil {
		t.Fatalf("%v\nstdout: %s\nstderr: %s", run.err, run.stdout, run.stderr)
	}
	if texts := toolResultTexts(records); len(texts) != 1 || texts[0] != "let-go says hello" {
		t.Fatalf("tool results %q\nstderr: %s", texts, run.stderr)
	}
	if !fileExists(marker) {
		t.Fatal("the source was never evaluated")
	}
	if !fileExists(shutdown) {
		t.Fatal("the optional shutdown did not run on normal exit")
	}
}

func TestLetGoStartupLoadsCljcDirectoryEntryWithHelperNamespace(t *testing.T) {
	env := newLetGoStartupEnv(t)
	dir := filepath.Join(env.home, "portable")
	env.write(t, filepath.Join(dir, "extension.cljc"), `(ns portable.entry
  (:require #?(:lg [pig.extension :as ext]
               :default [kmet.extension :as ext])
            [portable.helper :as helper]))
(defn init [api]
  (ext/register-tool!
   api
   {:name "echo_bridge" :description "portable probe"
    :parameters {:type "object" :properties {:text {:type "string"}}}
    :execute (fn [args] {:content (helper/shout (:text args))})}))
`)
	env.write(t, filepath.Join(dir, "portable", "helper.cljc"), "(ns portable.helper (:require [clojure.string :as str]))\n(defn shout [text] (str/upper-case text))\n")
	run, records := env.runJSON(t, "Run: extension echo hello", "-e", dir)
	if run.err != nil {
		t.Fatalf("%v\nstdout: %s\nstderr: %s", run.err, run.stdout, run.stderr)
	}
	if texts := toolResultTexts(records); len(texts) != 1 || texts[0] != "HELLO" {
		t.Fatalf("tool results %q\nstderr: %s", texts, run.stderr)
	}
}

func TestLetGoStartupFailedInitExitsWithTheNativeLoadFailure(t *testing.T) {
	env := newLetGoStartupEnv(t)
	shutdown := filepath.Join(env.home, "shutdown")
	bad := env.write(t, filepath.Join(env.home, "bad.lg"), `(ns bad)
(defn init [api] (throw (ex-info "init failed" {})))
(defn shutdown [api] (spit `+strconv.Quote(shutdown)+` "ran"))`)
	run, _ := env.runJSON(t, "hello", "-e", bad)
	requireUpstreamExtensionFailure(t, run, `Error: Failed to load extension "`+bad+`"`)
	if !strings.Contains(run.stderr, "init failed") {
		t.Fatalf("stderr lacks the cause:\n%s", run.stderr)
	}
	if fileExists(shutdown) {
		t.Fatal("shutdown ran for a source whose init failed")
	}
}

func TestLetGoStartupReportsToolConflictsInLoadOrder(t *testing.T) {
	env := newLetGoStartupEnv(t)
	first := env.write(t, filepath.Join(env.home, "a.lg"), letGoToolSource("a", filepath.Join(env.home, "ma"), filepath.Join(env.home, "sa")))
	second := env.write(t, filepath.Join(env.home, "b.lg"), letGoToolSource("b", filepath.Join(env.home, "mb"), filepath.Join(env.home, "sb")))
	run, _ := env.runJSON(t, "hello", "-e", first, "-e", second)
	requireUpstreamExtensionFailure(t, run, `Error: Failed to load extension "`+second+`": Tool "echo_bridge" conflicts with `+first)
	// Both generations were loaded and retired: a conflict is a diagnostic, and every extension stays loaded as in Pi.
	for _, name := range []string{"sa", "sb"} {
		if !fileExists(filepath.Join(env.home, name)) {
			t.Errorf("generation %s was not retired through shutdown", name)
		}
	}
}

func TestLetGoStartupNeverEvaluatesAnUntrustedProjectSource(t *testing.T) {
	env := newLetGoStartupEnv(t)
	projectMarker, userMarker := filepath.Join(env.home, "project-evaluated"), filepath.Join(env.home, "user-evaluated")
	env.write(t, filepath.Join(env.cwd, ".pig", "extensions", "project.lg"), letGoToolSource("project.ext", projectMarker, filepath.Join(env.home, "project-shutdown")))
	env.write(t, filepath.Join(env.agentDir, "extensions", "user.lg"), `(ns user.ext) (spit `+strconv.Quote(userMarker)+` "evaluated") (defn init [api] nil)`)

	if run, _ := env.runJSON(t, "hello", "--no-approve"); run.err != nil {
		t.Fatalf("%v\nstderr: %s", run.err, run.stderr)
	}
	if fileExists(projectMarker) {
		t.Fatal("an untrusted project source was evaluated")
	}
	if !fileExists(userMarker) {
		t.Fatal("a trusted user source was not evaluated, so the negative check proves nothing")
	}

	// The same project source evaluates once trust is granted, so the marker is a real detector.
	if err := os.Remove(userMarker); err != nil {
		t.Fatal(err)
	}
	if run, _ := env.runJSON(t, "hello", "--approve"); run.err != nil {
		t.Fatalf("%v\nstderr: %s", run.err, run.stderr)
	}
	if !fileExists(projectMarker) || !fileExists(userMarker) {
		t.Fatalf("with trust granted project evaluated=%v user evaluated=%v", fileExists(projectMarker), fileExists(userMarker))
	}
}

func TestLetGoStartupNoExtensionsSkipsAmbientButKeepsExplicitSources(t *testing.T) {
	env := newLetGoStartupEnv(t)
	ambient, explicit := filepath.Join(env.home, "ambient"), filepath.Join(env.home, "explicit")
	env.write(t, filepath.Join(env.agentDir, "extensions", "ambient.lg"), `(ns ambient.ext) (spit `+strconv.Quote(ambient)+` "evaluated") (defn init [api] nil)`)
	entry := env.write(t, filepath.Join(env.home, "explicit.lg"), `(ns explicit.ext) (spit `+strconv.Quote(explicit)+` "evaluated") (defn init [api] nil)`)

	if run, _ := env.runJSON(t, "hello", "--no-extensions"); run.err != nil || fileExists(ambient) {
		t.Fatalf("--no-extensions err=%v ambient evaluated=%v\nstderr: %s", run.err, fileExists(ambient), run.stderr)
	}
	if run, _ := env.runJSON(t, "hello", "--no-extensions", "-e", entry); run.err != nil || fileExists(ambient) || !fileExists(explicit) {
		t.Fatalf("--no-extensions -e err=%v ambient=%v explicit=%v\nstderr: %s", run.err, fileExists(ambient), fileExists(explicit), run.stderr)
	}
}

func TestLetGoStartupRPCListsAndDispatchesTheCommand(t *testing.T) {
	env := newLetGoStartupEnv(t)
	marker := filepath.Join(env.home, "greeted")
	entry := env.write(t, filepath.Join(env.home, "greet.lg"), `(ns greet.ext (:require [pig.extension :as ext]))
(defn init [api]
  (ext/register-command! api {:name "greet" :description "Greet someone"
    :handler (fn [ctx args] (spit `+strconv.Quote(marker)+` (str args "@" (:cwd ctx))))}))`)
	p := startRPCProcessAt(t, env.cwd, []string{"HOME=" + env.home, "PIG_HOME=" + env.home, "PIG_CODING_AGENT_DIR=" + env.agentDir, "PIG_TEST_FAUX=1", "PIG_OFFLINE=1"}, "--no-session", "-e", entry)
	p.send(`{"id":"commands","type":"get_commands"}`)
	p.await("the command catalog lists greet", func(record rpcRecord) bool {
		if record["type"] != "response" || record["id"] != "commands" {
			return false
		}
		data, _ := record["data"].(map[string]any)
		commands, _ := data["commands"].([]any)
		for _, value := range commands {
			if command, _ := value.(map[string]any); command["name"] == "greet" && command["description"] == "Greet someone" {
				return true
			}
		}
		t.Fatalf("greet is not listed: %v", commands)
		return false
	})
	p.send(`{"id":"run","type":"prompt","message":"/greet Ada"}`)
	p.await("the command runs", func(record rpcRecord) bool { return isSuccessResponse(record, "run") })
	deadline := time.Now().Add(testbudget.Wait(t))
	for !fileExists(marker) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the command handler did not run: %v", err)
	}
	if want := "Ada@" + env.cwd; string(got) != want {
		t.Fatalf("handler saw %q, want %q", got, want)
	}
	p.closeAndWait("the command ran")
}

func TestLetGoInterleavePlacesInterpretedExtensionsAtTheirDiscoveryPosition(t *testing.T) {
	named := func(name string) extension.Extension { return extension.Extension{Name: name} }
	interpreted := func(name string) *letgo.Loaded { return &letgo.Loaded{Extension: named(name)} }
	names := func(extensions []extension.Extension) []string {
		var out []string
		for _, ext := range extensions {
			out = append(out, ext.Name)
		}
		return out
	}
	subprocessLoaded := []extension.Extension{named("s1"), named("s2"), named("s3")}
	for _, test := range []struct {
		name string
		set  *letGoSet
		want []string
	}{
		{name: "none", set: &letGoSet{}, want: []string{"s1", "s2", "s3"}},
		{name: "first and last", set: &letGoSet{loaded: []*letgo.Loaded{interpreted("l1"), interpreted("l2")}, positions: []int{0, 3}}, want: []string{"l1", "s1", "s2", "s3", "l2"}},
		{name: "between", set: &letGoSet{loaded: []*letgo.Loaded{interpreted("l1"), interpreted("l2")}, positions: []int{1, 1}}, want: []string{"s1", "l1", "l2", "s2", "s3"}},
		{name: "clamped when a subprocess extension failed to load", set: &letGoSet{loaded: []*letgo.Loaded{interpreted("l1")}, positions: []int{5}}, want: []string{"s1", "s2", "s3", "l1"}},
	} {
		if got := names(interleaveLetGo(subprocessLoaded, test.set)); !slices.Equal(got, test.want) {
			t.Errorf("%s: %v, want %v", test.name, got, test.want)
		}
	}
	// Disabled configs do not count toward a position, and interpreted configs are split out of the subprocess list in order.
	configs := []subprocess.ExtConfig{
		{Name: "a", Enabled: true}, {Name: "l1", Enabled: true, RuntimeKind: "let-go"}, {Name: "off", Enabled: false},
		{Name: "b", Enabled: true}, {Name: "l2", Enabled: false, RuntimeKind: "let-go"},
	}
	interpretedConfigs, others := splitLetGoConfigs(configs)
	if len(interpretedConfigs) != 2 || len(others) != 3 || others[0].Name != "a" || others[2].Name != "b" {
		t.Fatalf("split %v / %v", interpretedConfigs, others)
	}
}

func letGoReloadSource(version, out, shutdown string, commands ...string) string {
	var registered strings.Builder
	for _, name := range commands {
		registered.WriteString(`  (ext/register-command! api {:name "` + name + `" :handler (fn [ctx args] (swap! state inc) (spit ` + strconv.Quote(out+"."+name) + ` (str "` + version + `:" @state)))})` + "\n")
	}
	return `(ns reload.fixture (:require [pig.extension :as ext]))
(def state (atom 0))
(defn init [api]
` + registered.String() + `  nil)
(defn shutdown [api] (spit ` + strconv.Quote(shutdown) + ` "` + version + `"))
`
}

func rpcCommandNames(t *testing.T, p *rpcProcess, id string) []string {
	t.Helper()
	var names []string
	p.send(`{"id":"` + id + `","type":"get_commands"}`)
	p.await("the command catalog", func(record rpcRecord) bool {
		if record["type"] != "response" || record["id"] != id {
			return false
		}
		data, _ := record["data"].(map[string]any)
		commands, _ := data["commands"].([]any)
		for _, value := range commands {
			command, _ := value.(map[string]any)
			if info, _ := command["sourceInfo"].(map[string]any); info["source"] != "builtin" {
				names = append(names, command["name"].(string))
			}
		}
		return true
	})
	return names
}

func waitForFile(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(testbudget.Wait(t))
	for time.Now().Before(deadline) {
		if got, err := os.ReadFile(path); err == nil && string(got) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := os.ReadFile(path)
	t.Fatalf("%s holds %q, want %q", path, got, want)
}

// A Go probe extension's command calls ctx.reload(), the production path of a headless reload.
func TestLetGoReloadPublishesFreshGenerationsAndRetiresTheOldOnes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the pig binary and a Go extension")
	}
	env := newLetGoStartupEnv(t)
	out, shutdown := filepath.Join(env.home, "out"), filepath.Join(env.home, "shutdown")
	entry := env.write(t, filepath.Join(env.home, "reload.lg"), letGoReloadSource("v1", out, shutdown, "ver", "gone"))
	probe, log := writeReloadProbe(t, "go"), filepath.Join(env.home, "reload.log")
	p := startRPCProcessAt(t, env.cwd, []string{"HOME=" + env.home, "PIG_HOME=" + filepath.Join(env.home, ".pig"), "PIG_CODING_AGENT_DIR=" + env.agentDir, "PIG_TEST_FAUX=1", "PIG_OFFLINE=1", "RELOAD_PROBE_LOG=" + log},
		"--no-session", "--no-extensions", "-e", probe, "-e", entry)
	reload := func(id string) {
		p.send(`{"id":"` + id + `","type":"prompt","message":"/reloadme"}`)
		p.await("the reload command", func(record rpcRecord) bool { return isSuccessResponse(record, id) })
		deadline := time.Now().Add(testbudget.Wait(t))
		for strings.Count(string(mustReadFile(log)), "returned") < map[string]int{"r1": 1, "r2": 2}[id] {
			if time.Now().After(deadline) {
				t.Fatalf("ctx.reload() did not return; log:\n%s", mustReadFile(log))
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	run := func(id, command string) {
		p.send(`{"id":"` + id + `","type":"prompt","message":"/` + command + `"}`)
		p.await("the command", func(record rpcRecord) bool { return isSuccessResponse(record, id) })
	}

	run("a", "ver")
	run("b", "ver")
	waitForFile(t, out+".ver", "v1:2")
	if names := rpcCommandNames(t, p, "c1"); !slices.Contains(names, "gone") || !slices.Contains(names, "ver") {
		t.Fatalf("startup commands %v", names)
	}

	// Edit one command, remove one, add one. The new generation starts from clean interpreter state.
	env.write(t, entry, letGoReloadSource("v2", out, shutdown, "ver", "added"))
	reload("r1")
	run("d", "ver")
	waitForFile(t, out+".ver", "v2:1")
	run("e", "added")
	waitForFile(t, out+".added", "v2:2")
	names := rpcCommandNames(t, p, "c2")
	if slices.Contains(names, "gone") || !slices.Contains(names, "added") || !slices.Contains(names, "ver") {
		t.Fatalf("commands after reload %v", names)
	}
	waitForFile(t, shutdown, "v1")

	// A replacement that does not even parse is not loaded, as for any extension that fails to reload; healthy extensions stay.
	env.write(t, entry, "(ns reload.fixture (defn init [api] (")
	reload("r2")
	if stderr := p.stderr.String(); !strings.Contains(stderr, "extension reload: extension \"reload\": "+entry+": load:") {
		t.Fatalf("a failed reload names no source or phase on stderr:\n%s", stderr)
	}
	names = rpcCommandNames(t, p, "c3")
	if slices.Contains(names, "ver") || slices.Contains(names, "added") || !slices.Contains(names, "reloadme") {
		t.Fatalf("commands after a broken replacement %v", names)
	}
	waitForFile(t, shutdown, "v2")
	p.closeAndWait("the reloads ran")
}

// blockingUI holds a select dialog open until released, so a let-go callback is mid-flight when the runner is replaced.
type blockingUI struct {
	extension.UIContext
	entered chan struct{}
	release chan struct{}
}

func (u *blockingUI) Select(ctx context.Context, _ string, _ []string, _ extension.ExtensionUIDialogOptions) (string, error) {
	close(u.entered)
	select {
	case <-u.release:
		return "done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestLetGoOwnerRetiresTheOldGenerationAfterItsRunningCallbackFinishes(t *testing.T) {
	dir := t.TempDir()
	shutdown := filepath.Join(dir, "shutdown")
	source := func(version string) string {
		return `(ns owner.fixture (:require [pig.extension :as ext]))
(defn init [api]
  (ext/register-command! api {:name "block" :handler (fn [ctx args] (spit ` + strconv.Quote(filepath.Join(dir, "finished-"+version)) + ` ((:select ctx) "hold" ["a"])))}))
(defn shutdown [api] (spit ` + strconv.Quote(shutdown) + ` "` + version + `"))
`
	}
	entry := filepath.Join(dir, "owner.lg")
	writeStartupFixtureFile(t, entry, source("v1"))
	config := subprocess.ExtConfig{Name: "owner", Enabled: true, RuntimeKind: "let-go", Entrypoint: entry}

	first, errs := loadLetGoSet(t.Context(), []subprocess.ExtConfig{config})
	if len(errs) != 0 || first.empty() {
		t.Fatalf("first load %v", errs)
	}
	owner := newLetGoOwner(first)
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	oldRunner := inproc.NewRunner(interleaveLetGo(nil, first), dir)
	ui := &blockingUI{UIContext: extension.NoopUIContext, entered: make(chan struct{}), release: make(chan struct{})}
	oldRunner.SetUIContext(ui, extension.ModeTUI)
	if err := owner.attach(t.Context(), oldRunner); err != nil {
		t.Fatal(err)
	}
	oldHandler := first.loaded[0].Extension.Commands["block"].Handler

	// A reload stages first: evaluating sources enters the interpreter, whose entry is serialized process-wide, so staging cannot wait
	// behind a callback held open in a dialog. Only after staging does the old callback run, and it is still inside its dialog when
	// the runner is replaced.
	writeStartupFixtureFile(t, entry, source("v2"))
	if errs := owner.stage(t.Context(), []subprocess.ExtConfig{config}); len(errs) != 0 {
		t.Fatal(errs)
	}
	done := make(chan struct{})
	go func() { defer close(done); oldRunner.ExecuteCommand(t.Context(), "block", "") }()
	<-ui.entered
	newRunner := inproc.NewRunner(owner.merge(nil), dir)
	oldRunner.Invalidate("replaced")
	if err := owner.attach(t.Context(), newRunner); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if fileExists(shutdown) {
		t.Fatal("the old generation ran shutdown while its callback was still running")
	}

	close(ui.release)
	<-done
	waitForFile(t, shutdown, "v1")
	waitForFile(t, filepath.Join(dir, "finished-v1"), "done")
	if err := oldHandler(t.Context(), ""); !errors.Is(err, letgo.ErrClosed) {
		t.Fatalf("a call to the retired generation: %v", err)
	}

	// The new generation serves and shuts down exactly once, however often Close runs.
	if !newRunner.ExecuteCommand(t.Context(), "block", "") {
		t.Fatal("the new generation's command was not handled")
	}
	for range 3 {
		if err := owner.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	waitForFile(t, shutdown, "v2")
}

func TestLetGoDiagnosticsNameTheSourceAndPhaseAtEveryStage(t *testing.T) {
	env := newLetGoStartupEnv(t)
	t.Run("resolve", func(t *testing.T) {
		missing := filepath.Join(env.home, "missing.lg")
		run, _ := env.runJSON(t, "hello", "-e", missing)
		requireUpstreamExtensionFailure(t, run, `Error: Failed to load extension "`+missing+`"`)
	})
	t.Run("eval", func(t *testing.T) {
		bad := env.write(t, filepath.Join(env.home, "eval.lg"), `(ns diag.eval (defn init [api] (`)
		run, _ := env.runJSON(t, "hello", "-e", bad)
		requireUpstreamExtensionFailure(t, run, `Error: Failed to load extension "`+bad+`"`)
		if !strings.Contains(run.stderr, bad+": load:") {
			t.Fatalf("stderr names no eval phase:\n%s", run.stderr)
		}
	})
	t.Run("init", func(t *testing.T) {
		bad := env.write(t, filepath.Join(env.home, "init.lg"), `(ns diag.init) (defn init [api] (throw (ex-info "init exploded" {})))`)
		run, _ := env.runJSON(t, "hello", "-e", bad)
		requireUpstreamExtensionFailure(t, run, `Error: Failed to load extension "`+bad+`"`)
		if !strings.Contains(run.stderr, bad+": init:") || !strings.Contains(run.stderr, "init exploded") {
			t.Fatalf("stderr names no init phase or cause:\n%s", run.stderr)
		}
	})
	t.Run("register", func(t *testing.T) {
		bad := env.write(t, filepath.Join(env.home, "register.lg"), `(ns diag.register (:require [pig.extension :as ext]))
(defn init [api] (ext/register-tool! api {:name "t" :params {:x {:type :string}} :execute (fn [args] nil)}))`)
		run, _ := env.runJSON(t, "hello", "-e", bad)
		requireUpstreamExtensionFailure(t, run, `Error: Failed to load extension "`+bad+`"`)
		if !strings.Contains(run.stderr, "register tool: $.params") {
			t.Fatalf("stderr names no register phase or field:\n%s", run.stderr)
		}
	})
	t.Run("callback", func(t *testing.T) {
		entry := env.write(t, filepath.Join(env.home, "callback.lg"), `(ns diag.callback (:require [pig.extension :as ext]))
(defn init [api] (ext/register-command! api {:name "boom" :handler (fn [ctx args] (throw (ex-info "command exploded" {})))}))`)
		p := startRPCProcessAt(t, env.cwd, []string{"HOME=" + env.home, "PIG_HOME=" + env.home, "PIG_CODING_AGENT_DIR=" + env.agentDir, "PIG_TEST_FAUX=1", "PIG_OFFLINE=1"}, "--no-session", "-e", entry)
		p.send(`{"id":"run","type":"prompt","message":"/boom"}`)
		p.await("extension_error for the failing command", func(record rpcRecord) bool {
			if record["type"] != "extension_error" {
				return false
			}
			message, _ := record["error"].(string)
			// The runner reports a command failure under the native "command:<name>" path; the message names the source and phase.
			if record["event"] != "command" || record["extensionPath"] != "command:boom" || !strings.Contains(message, entry+": execute command boom:") || !strings.Contains(message, "command exploded") {
				t.Fatalf("extension_error = %v", record)
			}
			return true
		})
		p.closeAndWait("after the command error")
	})
}

// shutdownCounter records each generation's shutdown output, one line per run, so a duplicate or a missing shutdown is visible.
type shutdownCounter struct {
	mu    sync.Mutex
	lines []string
}

func (c *shutdownCounter) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, strings.TrimSpace(string(data)))
	return len(data), nil
}

func (c *shutdownCounter) counts() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	counts := map[string]int{}
	for _, line := range c.lines {
		counts[line]++
	}
	return counts
}

func TestLetGoOwnerRepeatedReloadsRetireEveryGenerationExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	counter := &shutdownCounter{}
	var forms, namespaces int
	generations := 25
	load := func(n int) *letGoSet {
		path := filepath.Join(dir, "gen.cljc")
		writeStartupFixtureFile(t, path, `(ns stress.entry (:require [pig.extension :as ext] [stress.helper :as helper]))
(def state (atom 0))
(defn init [api]
  (ext/register-command! api {:name "tick" :handler (fn [ctx args] (swap! state inc))})
  (ext/register-tool! api {:name "t" :parameters {:type "object"} :execute (fn [args] {:content (helper/label)})}))
(defn shutdown [api] (println "generation-`+strconv.Itoa(n)+`"))
`)
		writeStartupFixtureFile(t, filepath.Join(dir, "stress", "helper.cljc"), `(ns stress.helper) (defn label [] "label")`)
		loaded, err := letgo.Load(t.Context(), letgo.LoadOptions{Entrypoint: path, Identity: extension.Extension{Name: "stress", Path: path}, Streams: letgo.Streams{Stdout: counter}})
		if err != nil {
			t.Fatal(err)
		}
		return &letGoSet{loaded: []*letgo.Loaded{loaded}, positions: []int{0}}
	}
	owner := newLetGoOwner(load(0))
	runner := inproc.NewRunner(interleaveLetGo(nil, owner.current), dir)
	if err := owner.attach(t.Context(), runner); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= generations; n++ {
		// Overlap: a callback runs on the current generation while the next one stages and publishes.
		if !runner.ExecuteCommand(t.Context(), "tick", "") {
			t.Fatalf("generation %d: command not handled", n-1)
		}
		owner.mu.Lock()
		owner.staged = load(n)
		owner.mu.Unlock()
		next := inproc.NewRunner(owner.merge(nil), dir)
		runner.Invalidate("reloaded")
		if err := owner.attach(t.Context(), next); err != nil {
			t.Fatal(err)
		}
		runner = next
		if n == 5 {
			forms, namespaces = lifetimeSnapshotForCLI()
		}
	}
	if err := owner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.Invalidate("done")
	counts := counter.counts()
	for n := 0; n <= generations; n++ {
		if got := counts["generation-"+strconv.Itoa(n)]; got != 1 {
			t.Errorf("generation %d ran shutdown %d times", n, got)
		}
	}
	if len(counts) != generations+1 {
		t.Errorf("shutdown lines %v", counts)
	}
	// Past warm-up, 20 more generations added no form sources and no namespaces to the process.
	if gotForms, gotNamespaces := lifetimeSnapshotForCLI(); gotForms != forms || gotNamespaces != namespaces {
		t.Errorf("form sources %d (after 5 generations %d), namespaces %d (%d)", gotForms, forms, gotNamespaces, namespaces)
	}
}

// lifetimeSnapshotForCLI reads the interpreter's process-wide form-source table and namespace registry.
func lifetimeSnapshotForCLI() (forms, namespaces int) {
	return vm.FormSource.Len(), len(rt.AllNSes())
}
