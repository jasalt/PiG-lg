package letgo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

func loadToolSource(t *testing.T, source string) *Loaded {
	t.Helper()
	path := filepath.Join(t.TempDir(), "extension.lg")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(t.Context(), LoadOptions{Entrypoint: path, LoadPaths: []string{filepath.Dir(path)}, Identity: extension.Extension{Name: "test", Path: path, ResolvedPath: path, SourceInfo: "test-source"}})
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

func TestLoadToolFixtureUsesNativeRunnerAfterLoad(t *testing.T) {
	path, err := filepath.Abs("../../../../test/fixtures/extensions/letgo/full/extension.lg")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(t.Context(), LoadOptions{Entrypoint: path, LoadPaths: []string{filepath.Dir(path)}, Identity: extension.Extension{Path: path, ResolvedPath: path, SourceInfo: "fixture-source"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := loaded.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	if !runner.ExecuteCommand(t.Context(), "hello", "fixture args") {
		t.Fatal("fixture command not handled")
	}
	value, err := loaded.generation.Run(t.Context(), `(deref pig.fixture.tool/command-arguments)`)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fromValue(value); err != nil || got != "fixture args" {
		t.Fatalf("fixture command args=%v, err=%v", got, err)
	}
	tools := runner.Tools()
	if len(tools) != 2 || tools[0].Definition.Name != "hello" || tools[1].Definition.Name != "other" || tools[0].SourceInfo != "fixture-source" {
		t.Fatalf("tools %#v", tools)
	}
	result, err := tools[0].Definition.Execute(runner.DispatchContext(t.Context()), "call", json.RawMessage(`{"name":"Ada"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	native, ok := result.(agent.AgentToolResult)
	if !ok || native.Text() != "Welcome Ada" || native.Details.(map[string]any)["received"] != "Ada" {
		t.Fatalf("native result %#v", result)
	}
	for _, arguments := range []string{`[]`, `null`, `{} {}`, `{"name":9007199254740993}`} {
		if _, err := tools[0].Definition.Execute(t.Context(), "bad-arguments", json.RawMessage(arguments), nil); err == nil {
			t.Fatalf("invalid arguments accepted: %s", arguments)
		}
	}
	if err := loaded.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := tools[0].Definition.Execute(t.Context(), "closed", json.RawMessage(`{}`), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed tool: %v", err)
	}
}

func TestLoadToolRegistrationAndSourceErrors(t *testing.T) {
	for _, source := range []string{
		`(pig.extension/register-tool! {:name "bad" :parameters [] :execute (fn [c p] nil)})`,
		`(pig.extension/register-tool! {:name "bad" :parameters {} :execute "not a fn"})`,
		`(pig.extension/register-tool! {:name "bad" :parameters {} :execute (fn [c p] nil) "execute" (fn [c p] nil)})`,
		`(throw (ex-info "load failed" {}))`,
		`(def broken\n (`,
	} {
		path := filepath.Join(t.TempDir(), "broken.lg")
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(t.Context(), LoadOptions{Entrypoint: path})
		if err == nil || loaded != nil || !strings.Contains(err.Error(), path+": load:") {
			t.Fatalf("source %q: %v", source, err)
		}
	}
}

func TestLoadToolThrowMalformedResultAndCancellation(t *testing.T) {
	for _, test := range []struct{ body, want string }{
		{`(throw (ex-info "callback failed" {}))`, "callback failed"},
		{`{:content [{:type "thinking" :thinking "bad"}]}`, "not tool result content"},
		{`{:content "bad"}`, "$.content"},
		{`{:content [] :details {:unsupported :keyword}}`, "$.details.unsupported"},
	} {
		loaded := loadToolSource(t, `(pig.extension/register-tool! {:name "bad" :parameters {} :execute (fn [c p] `+test.body+`)})`)
		runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
		tool := runner.Tools()[0].Definition
		if _, err := tool.Execute(t.Context(), "bad", json.RawMessage(`{}`), nil); err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), loaded.path) {
			t.Fatalf("wanted %q: %v", test.want, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := tool.Execute(ctx, "canceled", json.RawMessage(`{}`), nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
		runner.Invalidate("test complete")
	}
}

func BenchmarkLoadToolLifecycle(b *testing.B) {
	path := filepath.Join(b.TempDir(), "extension.lg")
	if err := os.WriteFile(path, []byte(`(pig.extension/register-tool! {:name "bench" :parameters {} :execute (fn [c p] {:content [{:type "text" :text (:text p)}]})})`), 0o600); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		loaded, err := Load(b.Context(), LoadOptions{Entrypoint: path})
		if err != nil {
			b.Fatal(err)
		}
		runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, filepath.Dir(path))
		_, err = runner.Tools()[0].Definition.Execute(b.Context(), "bench", json.RawMessage(`{"text":"representative"}`), nil)
		runner.Invalidate("benchmark complete")
		closeErr := loaded.Close(b.Context())
		if err != nil || closeErr != nil {
			b.Fatalf("invoke=%v close=%v", err, closeErr)
		}
	}
}

func TestLoadToolHostCallbackCarriesReentryMarker(t *testing.T) {
	loaded := loadToolSource(t, `(pig.extension/register-tool! {:name "probe" :parameters {} :execute (fn [c p] (pig.extension/register-tool! {:name "nested" :parameters {} :execute (fn [c p] {:content []})}) {:content []})})`)
	var retained context.Context
	var nested error
	runtime := extension.CreateExtensionRuntime()
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir(), runtime)
	defer runner.Invalidate("test complete")
	runner.BindCore(extension.ExtensionActions{}, extension.ContextActions{RefreshTools: func() error {
		ctx, err := loaded.callbackContext()
		if err != nil {
			return err
		}
		retained = ctx
		_, nested = loaded.generation.Run(ctx, "nil")
		return nil
	}}, nil)
	live, err := runtime.CreateContext()
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Bind(live); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Tools()[0].Definition.Execute(t.Context(), "probe", json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(nested, ErrReentrant) {
		t.Fatalf("nested call: %v", nested)
	}
	if _, err := loaded.generation.Run(retained, "nil"); err != nil {
		t.Fatalf("retained marker after callback: %v", err)
	}
}
