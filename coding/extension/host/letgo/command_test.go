package letgo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

func TestCommandLoadNativeDispatchExactArgumentsAndReplacement(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.command.fixture (:require [pig.extension :as pig]))
 (def received (atom nil))
 (pig/register-command! "echo" {:description "old" :handler (fn [c args] (reset! received "old"))})
 (pig/register-command! "other" {:handler (fn [c args] nil)})
 (pig/register-command! "echo" {:description "new" :handler (fn [c args] (reset! received args))})`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	commands := runner.Commands()
	if len(commands) != 2 || commands[0].Name != "echo" || commands[0].Description != "new" || commands[1].Name != "other" || commands[0].SourceInfo != "test-source" {
		t.Fatalf("commands %#v", commands)
	}
	arguments := "  Ada  🐷\nlast line  "
	if !runner.ExecuteCommand(t.Context(), "echo", arguments) {
		t.Fatal("native command was not handled")
	}
	value, err := loaded.generation.Run(t.Context(), `(deref pig.command.fixture/received)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromValue(value)
	if err != nil || got != arguments {
		t.Fatalf("arguments %#v, %v", got, err)
	}
}

func TestCommandNativeConflictAndErrorReporting(t *testing.T) {
	first := loadToolSource(t, `(pig.extension/register-command! "same" {:handler (fn [c args] nil)})`)
	second := loadToolSource(t, `(pig.extension/register-command! "same" {:handler (fn [c args] (throw (ex-info "command failed" {})))})`)
	runner := inproc.NewRunner([]extension.Extension{first.Extension, second.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	commands := runner.Commands()
	if len(commands) != 2 || commands[0].InvocationName != "same:1" || commands[1].InvocationName != "same:2" {
		t.Fatalf("commands %#v", commands)
	}
	var reported []*extension.ExtensionError
	unsubscribe := runner.AddErrorListener(func(err *extension.ExtensionError) { reported = append(reported, err) })
	defer unsubscribe()
	if !runner.ExecuteCommand(t.Context(), "same:2", "exact") {
		t.Fatal("throwing command must remain handled as in native runner")
	}
	if len(reported) != 1 || !strings.Contains(reported[0].Error, "command failed") || !strings.Contains(reported[0].Error, second.path) {
		t.Fatalf("reported %#v", reported)
	}
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := commands[0].Handler(t.Context(), "closed"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed command: %v", err)
	}
}

func TestCommandMalformedRegistrationAndCancellation(t *testing.T) {
	loaded := loadToolSource(t, `(pig.extension/register-command! "cancel" {:handler (fn [c args] nil)})`)
	command := loaded.Extension.Commands["cancel"]
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := command.Handler(ctx, "args"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	for _, source := range []string{
		`(pig.extension/register-command! "" {:handler (fn [c args] nil)})`,
		`(pig.extension/register-command! 12 {:handler (fn [c args] nil)})`,
		`(pig.extension/register-command! "bad" {:handler "not a fn"})`,
	} {
		path := filepath.Join(t.TempDir(), "malformed.lg")
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(t.Context(), LoadOptions{Entrypoint: path})
		if err == nil || !strings.Contains(err.Error(), "register command") || !strings.Contains(err.Error(), path) {
			t.Fatalf("malformed command accepted: %s", source)
		}
	}
}
