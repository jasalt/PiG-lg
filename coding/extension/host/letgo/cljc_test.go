package letgo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

const portableFixture = "../../../../test/fixtures/extensions/letgo/cljc"

func loadAt(t *testing.T, entrypoint string) *Loaded {
	t.Helper()
	path, err := filepath.Abs(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(t.Context(), LoadOptions{Entrypoint: path, LoadPaths: []string{filepath.Dir(path)}, Identity: extension.Extension{Name: filepath.Base(path), Path: path, ResolvedPath: path}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := loaded.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return loaded
}

func executeOnly(t *testing.T, loaded *Loaded, arguments string) agent.AgentToolResult {
	t.Helper()
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	t.Cleanup(func() { runner.Invalidate("test complete") })
	tools := runner.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools %#v", tools)
	}
	result, err := tools[0].Definition.Execute(runner.DispatchContext(t.Context()), "call", json.RawMessage(arguments), nil)
	if err != nil {
		t.Fatal(err)
	}
	return result.(agent.AgentToolResult)
}

// The same pure helper namespace must report identically under Babashka; see check-bb.sh beside the fixture.
func TestCljcPortableEntryRequiresHelperAndSelectsLetGoBranch(t *testing.T) {
	loaded := loadAt(t, filepath.Join(portableFixture, "extension.cljc"))
	if loaded.entry.namespace != "portable.entry" {
		t.Fatalf("entry namespace %q", loaded.entry.namespace)
	}
	golden, err := os.ReadFile(filepath.Join(portableFixture, "report.golden"))
	if err != nil {
		t.Fatal(err)
	}
	result := executeOnly(t, loaded, `{"text":"  the quick brown   fox "}`)
	if got := result.Text(); got != strings.TrimSpace(string(golden)) {
		t.Fatalf("let-go report %q\nwant golden %q", got, golden)
	}
	if host := result.Details.(map[string]any)["host"]; host != "let-go" {
		t.Fatalf("reader conditional selected %v, want the :lg branch", host)
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const reportingEntry = `(ns probe.entry (:require [pig.extension :as ext] [probe.helper :as helper]))
 (defn init [api]
   (ext/register-tool! api {:name "probe" :parameters {:type "object"}
     :execute (fn [p] {:content [{:type "text" :text (pr-str helper/value)}]})}))`

func TestCljcHelperResolutionOrderPrefersLgOverCljcOverClj(t *testing.T) {
	for _, test := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"lg shadows cljc", map[string]string{
			"probe/helper.lg":   `(ns probe.helper) (def value :lg)`,
			"probe/helper.cljc": `(ns probe.helper) (def value :cljc)`,
		}, ":lg"},
		{"cljc shadows clj", map[string]string{
			"probe/helper.cljc": `(ns probe.helper) (def value :cljc)`,
			"probe/helper.clj":  `(ns probe.helper) (def value :clj)`,
		}, ":cljc"},
		{"clj helper is reachable by require", map[string]string{
			"probe/helper.clj": `(ns probe.helper) (def value :clj)`,
		}, ":clj"},
		{"default branch when :lg is absent", map[string]string{
			"probe/helper.cljc": `(ns probe.helper) (def value #?(:clj :jvm :default :fallback))`,
		}, ":fallback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.files["extension.cljc"] = reportingEntry
			root := writeTree(t, test.files)
			if got := executeOnly(t, loadAt(t, filepath.Join(root, "extension.cljc")), `{}`).Text(); got != test.want {
				t.Fatalf("helper value %s, want %s", got, test.want)
			}
		})
	}
}

func TestCljcSameHelperNamespaceStaysIsolatedPerGeneration(t *testing.T) {
	first := writeTree(t, map[string]string{"extension.cljc": reportingEntry, "probe/helper.cljc": `(ns probe.helper) (def value :first)`})
	second := writeTree(t, map[string]string{"extension.cljc": reportingEntry, "probe/helper.cljc": `(ns probe.helper) (def value :second)`})
	firstLoaded := loadAt(t, filepath.Join(first, "extension.cljc"))
	secondLoaded := loadAt(t, filepath.Join(second, "extension.cljc"))
	if got := executeOnly(t, firstLoaded, `{}`).Text(); got != ":first" {
		t.Fatalf("first generation sees %s", got)
	}
	if got := executeOnly(t, secondLoaded, `{}`).Text(); got != ":second" {
		t.Fatalf("second generation sees %s", got)
	}
}

func TestCljcHostInteropFailsWithSourcePath(t *testing.T) {
	root := writeTree(t, map[string]string{"extension.cljc": `(ns probe.interop)
 (def pause (Thread/sleep 1))
 (defn init [api] nil)`})
	path := filepath.Join(root, "extension.cljc")
	if _, err := Load(t.Context(), LoadOptions{Entrypoint: path}); err == nil || !strings.Contains(err.Error(), path+": load:") || !strings.Contains(err.Error(), "Can't resolve Thread/sleep") {
		t.Fatalf("JVM interop accepted or undiagnosed: %v", err)
	}
}
