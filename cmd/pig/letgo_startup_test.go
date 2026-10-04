package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/letgo"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
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
	deadline := time.Now().Add(10 * time.Second)
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
