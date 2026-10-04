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

func TestLifecycleTypedPayloadsAndShutdownLifetime(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.lifecycle.fixture (:require [pig.extension :as pig]))
 (def seen (atom []))
 (defn init [api]
 (pig/on-event api :session-start (fn [e c] (swap! seen conj e)))
 (pig/on-event api :agent-start (fn [e c] (swap! seen conj e)))
 (pig/on-event api :agent-end (fn [e c] (swap! seen conj e)))
 (pig/on-event api :agent-settled (fn [e c] (swap! seen conj e)))
 (pig/on-event api :session-shutdown (fn [e c] (swap! seen conj e))))`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	events := []any{extension.SessionStartEvent{Type: "session_start"}, extension.AgentStartEvent{Type: "agent_start"}, extension.AgentEndEvent{Type: "agent_end"}, extension.AgentSettledEvent{Type: "agent_settled"}, extension.SessionShutdownEvent{Type: "session_shutdown"}}
	for _, event := range events {
		if _, err := runner.Emit(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	seen, err := loaded.generation.Run(t.Context(), `(deref pig.lifecycle.fixture/seen)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromValue(seen)
	if err != nil {
		t.Fatal(err)
	}
	expectedValue, err := publicValue(events)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := fromValue(expectedValue)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("events %#v; want %#v", got, expected)
	}
}

func TestLifecycleAwaitsHandlersInNativeOrderAndReportsErrors(t *testing.T) {
	first := loadToolSource(t, `(ns pig.lifecycle.first (:require [pig.extension :as pig]))
 (def seen (atom []))
 (defn init [api]
 (pig/on-event api :session-start (fn [e c] (swap! seen conj "first") (throw (ex-info "handler failed" {}))))
 (pig/on-event api :session-start (fn [e c] (swap! seen conj "second"))))`)
	second := loadToolSource(t, `(ns pig.lifecycle.second (:require [pig.extension :as pig]))
 (def seen (atom []))
 (defn init [api]
 (pig/on-event api :session-start (fn [e c] (swap! seen conj "third"))))`)
	runner := inproc.NewRunner([]extension.Extension{first.Extension, second.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	var reports []*extension.ExtensionError
	runner.AddErrorListener(func(err *extension.ExtensionError) { reports = append(reports, err) })
	if _, err := runner.Emit(t.Context(), extension.SessionStartEvent{Type: "session_start"}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		owner      *Loaded
		expression string
		want       []any
	}{
		{first, `(deref pig.lifecycle.first/seen)`, []any{"first", "second"}},
		{second, `(deref pig.lifecycle.second/seen)`, []any{"third"}},
	} {
		value, err := test.owner.generation.Run(t.Context(), test.expression)
		if err != nil {
			t.Fatal(err)
		}
		got, err := fromValue(value)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("order %#v, %v", got, err)
		}
	}
	if len(reports) != 1 || !strings.Contains(reports[0].Error, "handler failed") || !strings.Contains(reports[0].Error, first.path) {
		t.Fatalf("reports %#v", reports)
	}
	// Directly drive the native typed handler to verify its error is returned on the same call, not swallowed or detached.
	handler := first.Extension.EventHandlers("session_start")[0]
	if _, err := handler(extension.SessionStartEvent{Type: "session_start"}, runner.DispatchContext(t.Context())); err == nil || !strings.Contains(err.Error(), "handler failed") {
		t.Fatalf("direct error %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := handler(extension.SessionStartEvent{}, runner.DispatchContext(ctx)); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation %v", err)
	}
}

func TestLifecycleLateSubscriptionUsesNextDispatchSnapshot(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.lifecycle.snapshot (:require [pig.extension :as pig]))
 (def seen (atom [])) (def added (atom false))
 (defn init [api]
 (pig/on-event api :agent-start (fn [e c] (swap! seen conj "first") (if @added nil (do (reset! added true) (pig/on-event api :agent-start (fn [e c] (swap! seen conj "late"))))))))`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	for _, want := range [][]any{{"first"}, {"first", "first", "late"}} {
		if _, err := runner.Emit(t.Context(), extension.AgentStartEvent{Type: "agent_start"}); err != nil {
			t.Fatal(err)
		}
		value, err := loaded.generation.Run(t.Context(), `(deref pig.lifecycle.snapshot/seen)`)
		if err != nil {
			t.Fatal(err)
		}
		got, err := fromValue(value)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("snapshot %#v, %v; want %#v", got, err, want)
		}
	}
}

func TestLifecycleRejectsUnknownUnsupportedAndMalformedRegistrations(t *testing.T) {
	for _, source := range []string{
		`(ns pig.test.fixture (:require [pig.extension])) (defn init [api] (pig.extension/on-event api :unknown (fn [e c] nil)))`,
		`(ns pig.test.fixture (:require [pig.extension])) (defn init [api] (pig.extension/on-event api :tool-result (fn [e c] nil)))`,
		`(ns pig.test.fixture (:require [pig.extension])) (defn init [api] (pig.extension/on-event api :ns/session-start (fn [e c] nil)))`,
		`(ns pig.test.fixture (:require [pig.extension])) (defn init [api] (pig.extension/on-event api "session-start" (fn [e c] nil)))`,
		`(ns pig.test.fixture (:require [pig.extension])) (defn init [api] (pig.extension/on-event api :session-start "not a function"))`,
	} {
		path := filepath.Join(t.TempDir(), "event.lg")
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(t.Context(), LoadOptions{Entrypoint: path}); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("accepted source %s: %v", source, err)
		}
	}
}

func TestLifecycleHandlersReceiveEventThenContext(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.lifecycle.order (:require [pig.extension :as pig] [pig.context :as ctx]))
 (def observed (atom nil))
 (defn init [api]
   (pig/on-event api :agent-start (fn [event c] (reset! observed [(:type event) (ctx/cwd c)]))))`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	if _, err := runner.Emit(t.Context(), extension.AgentStartEvent{Type: "agent_start"}); err != nil {
		t.Fatal(err)
	}
	value, err := loaded.generation.Run(t.Context(), `(deref pig.lifecycle.order/observed)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	cwd, _ := extension.FromContext(runner.DispatchContext(t.Context())).CWD()
	if !reflect.DeepEqual(got, []any{"agent_start", cwd}) {
		t.Fatalf("observed %#v, want event type then context cwd %q", got, cwd)
	}
}
