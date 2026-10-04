package letgo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

func BenchmarkUICommandDispatch(b *testing.B) {
	path := filepath.Join(b.TempDir(), "extension.lg")
	if err := os.WriteFile(path, []byte(`(pig.extension/register-command! "prompt" {:handler (fn [c args] (pig.context/select! c "pick" ["first" "second"]))})`), 0o600); err != nil {
		b.Fatal(err)
	}
	loaded, err := Load(b.Context(), LoadOptions{Entrypoint: path})
	if err != nil {
		b.Fatal(err)
	}
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, filepath.Dir(path))
	b.Cleanup(func() {
		runner.Invalidate("benchmark complete")
		if err := loaded.Close(context.WithoutCancel(b.Context())); err != nil {
			b.Error(err)
		}
	})
	runner.SetUIContext(&dialogUI{UIContext: extension.NoopUIContext, selectCall: func(context.Context, string, []string) (string, error) { return "second", nil }}, extension.ModeTUI)
	for b.Loop() {
		if !runner.ExecuteCommand(b.Context(), "prompt", "") {
			b.Fatal("not handled")
		}
	}
}

type dialogUI struct {
	extension.UIContext
	selectCall    func(context.Context, string, []string) (string, error)
	notifications []string
}

func (u *dialogUI) Select(ctx context.Context, title string, choices []string, _ extension.ExtensionUIDialogOptions) (string, error) {
	return u.selectCall(ctx, title, choices)
}
func (*dialogUI) Confirm(context.Context, string, string, extension.ExtensionUIDialogOptions) (bool, error) {
	return true, nil
}
func (*dialogUI) Input(context.Context, string, string, extension.ExtensionUIDialogOptions) (string, error) {
	return "typed λ", nil
}
func (u *dialogUI) Notify(message, kind string) {
	u.notifications = append(u.notifications, message+":"+kind)
}

func TestUIAwaitsNativePromptAndPropagatesReentryMarker(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.ui.fixture (:require [pig.extension :as pig] [pig.context :as ctx]))
 (def seen (atom nil))
 (pig/register-command! "prompt" {:handler (fn [c args] (ctx/notify! c "hello") (reset! seen [(ctx/select! c "pick" ["α" "β"]) (ctx/confirm! c "sure" "question") (ctx/input! c "type" "placeholder")]))})`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan bool, 1)
	ui := &dialogUI{UIContext: extension.NoopUIContext, selectCall: func(ctx context.Context, title string, choices []string) (string, error) {
		if title != "pick" || !reflect.DeepEqual(choices, []string{"α", "β"}) {
			return "", errors.New("bad prompt arguments")
		}
		if _, err := loaded.generation.Run(ctx, "nil"); !errors.Is(err, ErrReentrant) {
			return "", errors.New("reentry marker missing")
		}
		close(entered)
		select {
		case <-release:
			return "β", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	runner.SetUIContext(ui, extension.ModeTUI)
	go func() { finished <- runner.ExecuteCommand(t.Context(), "prompt", "") }()
	<-entered
	select {
	case <-finished:
		t.Fatal("prompt returned before host response")
	default:
	}
	close(release)
	if !<-finished {
		t.Fatal("not handled")
	}
	value, err := loaded.generation.Run(t.Context(), `(deref pig.ui.fixture/seen)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromValue(value)
	if err != nil || !reflect.DeepEqual(got, []any{"β", true, "typed λ"}) {
		t.Fatalf("results %#v, %v", got, err)
	}
	if !reflect.DeepEqual(ui.notifications, []string{"hello:info"}) {
		t.Fatal(ui.notifications)
	}
}

func TestUIAvailabilityCancellationAndStaleContext(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.ui.modes (:require [pig.extension :as pig] [pig.context :as ctx]))
 (def saved (atom nil)) (def seen (atom nil))
 (pig/register-command! "prompt" {:handler (fn [c args] (reset! saved c) (reset! seen [(ctx/has-ui? c) (ctx/select! c "title" []) (ctx/confirm! c "title" "message") (ctx/input! c "title")]))})`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	for _, mode := range []extension.ExtensionMode{extension.ModePrint, extension.ModeRPC} {
		runner.SetUIContext(nil, mode)
		if !runner.ExecuteCommand(t.Context(), "prompt", "") {
			t.Fatal("not handled")
		}
		value, err := loaded.generation.Run(t.Context(), `(deref pig.ui.modes/seen)`)
		if err != nil {
			t.Fatal(err)
		}
		got, err := fromValue(value)
		if err != nil || !reflect.DeepEqual(got, []any{false, "", false, ""}) {
			t.Fatalf("native no-op results %#v, %v", got, err)
		}
	}
	entered := make(chan struct{})
	done := make(chan bool, 1)
	runner.SetUIContext(&dialogUI{UIContext: extension.NoopUIContext, selectCall: func(ctx context.Context, _ string, _ []string) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}}, extension.ModeTUI)
	var reports []*extension.ExtensionError
	runner.AddErrorListener(func(err *extension.ExtensionError) { reports = append(reports, err) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { done <- runner.ExecuteCommand(ctx, "prompt", "") }()
	<-entered
	cancel()
	<-done
	if len(reports) != 1 || !strings.Contains(reports[0].Error, context.Canceled.Error()) {
		t.Fatalf("cancellation reports %#v", reports)
	}
	saved, err := loaded.generation.Run(t.Context(), `(deref pig.ui.modes/saved)`)
	if err != nil {
		t.Fatal(err)
	}
	runner.Invalidate("replacement")
	title, _ := toValue("stale")
	if _, err := loaded.uiCall("input!", saved, title); !errors.Is(err, extension.ErrStaleContext) {
		t.Fatalf("stale %v", err)
	}
}
