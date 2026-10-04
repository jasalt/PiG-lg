package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
	"github.com/MichaelKinsy/PiG/coding/piglet"
	codingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
)

func writeLetgoInventoryFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLetgoInventorySettingsCLIAndNoExtensions(t *testing.T) {
	t.Setenv("PIG_HOME", t.TempDir())
	cwd, agent := t.TempDir(), t.TempDir()
	global := filepath.Join(agent, "global.lg")
	project := filepath.Join(cwd, ".pig", "project.lg")
	cli := filepath.Join(cwd, "cli.lg")
	for _, entry := range []string{global, project, cli} {
		writeLetgoInventoryFile(t, entry, "(def selected true)")
	}
	sm := codingagent.NewSettingsManager(cwd, agent)
	if err := sm.UpdateGlobal(func(s *codingagent.Settings) { s.Extensions = []string{"global.lg"} }); err != nil {
		t.Fatal(err)
	}
	if err := sm.UpdateProject(func(s *codingagent.Settings) { s.Extensions = []string{"project.lg"} }); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		disabled bool
		want     []string
	}{{false, []string{cli, project, global}}, {true, []string{cli}}} {
		configs := collectExtensionConfigs(cwd, agent, sm, CLIFlags{NoExtensions: test.disabled, Extensions: []string{cli}}, nil)
		var got []string
		for _, cfg := range configs {
			if cfg.RuntimeKind != "let-go" || cfg.SDKName != "" || cfg.ResolveError() != nil {
				t.Fatalf("configuration %#v", cfg)
			}
			got = append(got, cfg.Entrypoint)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("selected %v; want %v", got, test.want)
		}
	}
}

func TestLetgoInventoryAmbientDiscoveryAndScopes(t *testing.T) {
	t.Setenv("PIG_HOME", t.TempDir())
	cwd, agent := t.TempDir(), t.TempDir()
	user := filepath.Join(agent, "extensions", "direct.lg")
	workspace := filepath.Join(cwd, ".pig", "extensions", "nested", "extension.lg")
	for _, entry := range []string{user, workspace} {
		writeLetgoInventoryFile(t, entry, "(def selected true)")
	}
	sm := codingagent.NewSettingsManager(cwd, agent)
	for _, test := range []struct {
		scopes []string
		want   []string
	}{{[]string{"user"}, []string{user}}, {[]string{"workspace"}, []string{workspace}}, {[]string{"user", "workspace"}, []string{workspace, user}}} {
		configs := collectExtensionConfigs(cwd, agent, sm, CLIFlags{}, &test.scopes)
		var got []string
		for _, cfg := range configs {
			got = append(got, cfg.Entrypoint)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("scopes %v got %v; want %v", test.scopes, got, test.want)
		}
	}
}

func TestLetgoInventoryPackagePigletAndDistinctExactSources(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a.lg"), filepath.Join(root, "b.lg")
	for _, entry := range []string{a, b} {
		writeLetgoInventoryFile(t, entry, "(def selected true)")
	}
	writeLetgoInventoryFile(t, filepath.Join(root, "package.json"), `{"name":"fixture","pi":{"extensions":["./b.lg","./a.lg"]}}`)
	configs := packageExtensionConfigs(root, nil)
	if len(configs) != 2 || configs[0].Entrypoint != b || configs[1].Entrypoint != a {
		t.Fatalf("Package selection %#v", configs)
	}
	// Shared identity does not collapse different source files in one load root.
	for i := range configs {
		configs[i].Name = "same"
	}
	unique := mergeExtConfigs(uniqueExtensionPaths(append(configs, configs[0])))
	if len(unique) != 2 || unique[0].Entrypoint != b || unique[1].Entrypoint != a {
		t.Fatalf("exact identities %#v", unique)
	}
	plan := filepath.Join(root, "fixture.piglet.yaml")
	writeLetgoInventoryFile(t, plan, "name: fixture\nextensions:\n  - name: selected-b\n    origins:\n      - local:./b.lg\n  - name: selected-a\n    origins:\n      - local:./a.lg\n")
	parsed, err := piglet.Parse(plan)
	if err != nil {
		t.Fatal(err)
	}
	configs = resolvePigletExtConfigs(parsed)
	if len(configs) != 2 || configs[0].Name != "selected-b" || configs[0].Entrypoint != b || configs[1].Name != "selected-a" || configs[1].Entrypoint != a {
		t.Fatalf("Piglet selection %#v", configs)
	}
	if cells := subprocess.PlanCells(configs, nil); len(cells) != 0 {
		t.Fatalf("Piglet interpreter sources in process plan %#v", cells)
	}
}

func TestLetgoInventoryFailedExactSourceNeverFallsBackToExecutable(t *testing.T) {
	configs := pathToExtConfigs(filepath.Join(t.TempDir(), "missing.lg"))
	if len(configs) != 1 || configs[0].ResolveError() == nil || configs[0].Path != "" {
		t.Fatalf("missing interpreter fallback %#v", configs)
	}
}

func TestLetgoInventoryPortableCljcSources(t *testing.T) {
	t.Setenv("PIG_HOME", t.TempDir())
	cwd, agent := t.TempDir(), t.TempDir()
	user := filepath.Join(agent, "extensions", "direct.cljc")
	workspace := filepath.Join(cwd, ".pig", "extensions", "nested", "extension.cljc")
	cli := filepath.Join(cwd, "cli.cljc")
	for _, entry := range []string{user, workspace, cli} {
		writeLetgoInventoryFile(t, entry, "(ns selected)")
	}
	// A helper namespace beside the conventional entry is required by it, not selected separately.
	writeLetgoInventoryFile(t, filepath.Join(cwd, ".pig", "extensions", "nested", "nested", "core.cljc"), "(ns nested.core)")
	sm := codingagent.NewSettingsManager(cwd, agent)
	scopes := []string{"user", "workspace"}
	configs := collectExtensionConfigs(cwd, agent, sm, CLIFlags{Extensions: []string{cli}}, &scopes)
	var got []string
	for _, cfg := range configs {
		if cfg.RuntimeKind != "let-go" || cfg.SDKName != "" || cfg.ResolveError() != nil {
			t.Fatalf("configuration %#v", cfg)
		}
		got = append(got, cfg.Entrypoint)
	}
	if want := []string{cli, workspace, user}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected %v; want %v", got, want)
	}
	if cells := subprocess.PlanCells(configs, nil); len(cells) != 0 {
		t.Fatalf("portable interpreter sources in process plan %#v", cells)
	}
	missing := pathToExtConfigs(filepath.Join(t.TempDir(), "missing.cljc"))
	if len(missing) != 1 || missing[0].ResolveError() == nil || missing[0].Path != "" {
		t.Fatalf("missing portable source fell back to executable %#v", missing)
	}
}
