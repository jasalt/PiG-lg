package subprocess

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLetgoRetainsExactEntrypointAndIdentityWithoutSubprocessSDK(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "extension.lg")
	writeResolverFile(t, entry, "(def ready true)")
	for _, selected := range []string{root, entry} {
		cfg, definition, err := ResolveExtConfigWithIdentity(selected, "selected")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Name != "selected" || cfg.RuntimeKind != "let-go" || cfg.RuntimeLanguage != "let-go" || cfg.SDKName != "" || cfg.Entrypoint != entry || cfg.Source != definition.Root || cfg.selectedPath != selected || cfg.Path != "" {
			t.Fatalf("configuration %#v; definition %#v", cfg, definition)
		}
		if cells := PlanCells([]ExtConfig{cfg}, nil); len(cells) != 0 {
			t.Fatalf("interpreter entered process cells %#v", cells)
		}
	}
}

func TestLetgoNeverEntersProcessOrFusedAdmission(t *testing.T) {
	root := t.TempDir()
	writeResolverFile(t, filepath.Join(root, "extension.lg"), "(def ready true)")
	cfg, _, err := ResolveExtConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	native := packableConfig("native", "native-hash")
	configs := []ExtConfig{native, cfg, native}
	cells := PlanCells(configs, nil)
	for _, cell := range cells {
		for _, member := range cell.Extensions {
			if member.RuntimeLanguage == "let-go" {
				t.Fatalf("interpreter admitted %#v", cell)
			}
		}
	}
	host := NewHost(t.TempDir())
	defer host.Shutdown("test complete")
	if _, err := host.Load(context.Background(), cfg); err == nil {
		t.Fatal("process Load accepted interpreter")
	}
	called := false
	_, err = host.LoadInProcess(context.Background(), cfg, func(net.Conn) error { called = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "requires runtime") || called {
		t.Fatalf("fused admission err=%v called=%v", err, called)
	}
	loaded, failures := host.LoadAll(t.Context(), []ExtConfig{cfg})
	if len(loaded) != 0 || len(failures) != 1 || !strings.Contains(failures[0].Error(), "requires runtime") {
		t.Fatalf("process load-all silently admitted or omitted source: loaded=%v failures=%v", loaded, failures)
	}
}
