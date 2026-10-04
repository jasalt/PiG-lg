package letgo_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nooga/let-go/pkg/rt"
	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/coding/extension/host/letgo"
)

func TestGenerationSeparateRootsAndOutput(t *testing.T) {
	ctx := context.Background()
	host := letgo.NewRuntimeHost()
	var generations []*letgo.Generation
	var callbacks []vm.Fn
	var printCallbacks []vm.Fn
	var errorCallbacks []vm.Fn
	var outputs []*bytes.Buffer
	var errors []*bytes.Buffer
	for _, name := range []string{"first", "second"} {
		root := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "helper.lg"), []byte("(ns helper) (def answer \""+name+"\")"), 0o600); err != nil {
			t.Fatal(err)
		}
		var output, stderr bytes.Buffer
		generation, err := host.NewGeneration(ctx, "pig.test."+name, []string{root}, letgo.Streams{Stdout: &output, Stderr: &stderr})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := generation.Run(ctx, "(require 'helper)"); err != nil {
			t.Fatal(err)
		}
		callback, err := generation.Run(ctx, "(fn [] helper/answer)")
		if err != nil {
			t.Fatal(err)
		}
		callbacks = append(callbacks, callback.(vm.Fn))
		printCallback, err := generation.Run(ctx, "(fn [] (println helper/answer))")
		if err != nil {
			t.Fatal(err)
		}
		printCallbacks = append(printCallbacks, printCallback.(vm.Fn))
		errorCallback, err := generation.Run(ctx, "(fn [] (binding [*out* *err*] (println helper/answer)))")
		if err != nil {
			t.Fatal(err)
		}
		errorCallbacks = append(errorCallbacks, errorCallback.(vm.Fn))
		generations = append(generations, generation)
		outputs = append(outputs, &output)
		errors = append(errors, &stderr)
	}
	for i, name := range []string{"first", "second", "first", "second"} {
		index := i % 2
		generation, output := generations[index], outputs[index]
		if result, err := generation.Invoke(ctx, callbacks[index], nil); err != nil || result != vm.String(name) {
			t.Fatalf("%s retained helper closure: %v %v", name, result, err)
		}
		before := output.Len()
		if _, err := generation.Invoke(ctx, printCallbacks[index], nil); err != nil || output.Len() <= before {
			t.Fatalf("%s callback stdout=%q error=%v", name, output.String(), err)
		}
		before = errors[index].Len()
		if _, err := generation.Invoke(ctx, errorCallbacks[index], nil); err != nil || errors[index].Len() <= before {
			t.Fatalf("%s callback stderr=%q error=%v", name, errors[index].String(), err)
		}
		result, err := generation.Run(ctx, "(do (println helper/answer) helper/answer)")
		if err != nil || result != vm.String(name) || !strings.Contains(output.String(), name) {
			t.Fatalf("%s: result=%v output=%q error=%v", name, result, output.String(), err)
		}
		if _, err := generation.Run(ctx, "(binding [*out* *err*] (println helper/answer))"); err != nil || !strings.Contains(errors[index].String(), name) {
			t.Fatalf("%s stderr=%q error=%v", name, errors[index].String(), err)
		}
	}
	for _, generation := range generations {
		if err := generation.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGenerationCloseRejectsRetainedCallback(t *testing.T) {
	ctx := context.Background()
	host := letgo.NewRuntimeHost()
	generation, err := host.NewGeneration(ctx, "pig.test.closed", nil)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := generation.Run(ctx, "(fn [] :old)")
	if err != nil {
		t.Fatal(err)
	}
	if err := generation.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := generation.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = generation.Invoke(ctx, callback.(vm.Fn), nil)
	if !errors.Is(err, letgo.ErrClosed) {
		t.Fatalf("retired callback: %v", err)
	}
}

func TestGenerationSerializesAndCancelsQueuedEntry(t *testing.T) {
	ctx := context.Background()
	host := letgo.NewRuntimeHost()
	first, err := host.NewGeneration(ctx, "pig.test.serial.first", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close(ctx) }()
	second, err := host.NewGeneration(ctx, "pig.test.serial.second", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(ctx) }()
	entered, release := make(chan struct{}), make(chan struct{})
	if err := first.Def(ctx, "hold", func() { close(entered); <-release }); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, callErr := first.Run(ctx, "(hold)")
		finished <- callErr
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first VM entry did not start")
	}
	queued := make(chan struct{})
	queuedResult := make(chan error, 1)
	go func() {
		close(queued)
		result, callErr := second.Run(ctx, "(+ 2 3)")
		if callErr == nil && result != vm.Int(5) {
			callErr = errors.New("queued callback returned the wrong result")
		}
		queuedResult <- callErr
	}()
	<-queued
	select {
	case err := <-queuedResult:
		t.Fatalf("second VM entry completed while first held the gate: %v", err)
	default:
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := second.Run(cancelled, "(+ 2 3)"); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation: %v", err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-queuedResult; err != nil {
		t.Fatal(err)
	}
	if got, err := second.Run(ctx, "(+ 2 3)"); err != nil || got != vm.Int(5) {
		t.Fatalf("second after gate release: %v %v", got, err)
	}
}

func TestGenerationCloseWaitsForActiveCallback(t *testing.T) {
	ctx := context.Background()
	host := letgo.NewRuntimeHost()
	generation, err := host.NewGeneration(ctx, "pig.test.close.active", nil)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	if err := generation.Def(ctx, "hold", func() { close(entered); <-release }); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, callErr := generation.Run(ctx, "(hold)")
		finished <- callErr
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("active callback did not start")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := generation.Close(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled close: %v", err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := generation.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationRejectsSynchronousReentryWithoutInvalidatingRetainedContext(t *testing.T) {
	ctx := context.Background()
	host := letgo.NewRuntimeHost()
	generation, err := host.NewGeneration(ctx, "pig.test.reentry", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = generation.Close(ctx) }()
	var retained context.Context
	if err := generation.Def(ctx, "nest", func() (vm.Value, error) {
		retained = generation.CallbackContext(ctx)
		_, callErr := generation.Run(retained, "(+ 2 3)")
		return vm.NIL, callErr
	}); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = generation.Run(bounded, "(nest)")
	if err == nil || !strings.Contains(err.Error(), letgo.ErrReentrant.Error()) {
		t.Fatalf("nested VM entry: %v", err)
	}
	if retained == nil {
		t.Fatal("Go callback did not retain its scope")
	}
	if got, err := generation.Run(retained, "(+ 2 3)"); err != nil || got != vm.Int(5) {
		t.Fatalf("retained context after callback: %v %v", got, err)
	}
}

func TestGenerationRetiresNamespacesAcrossRepeatedLoads(t *testing.T) {
	ctx := context.Background()
	host := letgo.NewRuntimeHost()
	baseline := len(rt.AllNSes())
	for range 50 {
		generation, err := host.NewGeneration(ctx, "pig.test.retirement", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := generation.Run(ctx, "(def state (atom 0))"); err != nil {
			t.Fatal(err)
		}
		if got, err := generation.Run(ctx, "(swap! state inc)"); err != nil || got != vm.Int(1) {
			t.Fatalf("new generation state: %v %v", got, err)
		}
		if err := generation.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if count := len(rt.AllNSes()); count != baseline {
			t.Fatalf("retired generation left %d namespaces, baseline %d", count, baseline)
		}
	}
}

func BenchmarkGenerationInvoke(b *testing.B) {
	ctx := context.Background()
	host := letgo.NewRuntimeHost()
	generation, err := host.NewGeneration(ctx, "pig.test.benchmark", nil)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = generation.Close(ctx) }()
	callback, err := generation.Run(ctx, "(fn [x] (+ x 1))")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if result, err := generation.Invoke(ctx, callback.(vm.Fn), []vm.Value{vm.Int(4)}); err != nil || result != vm.Int(5) {
			b.Fatalf("invocation: %v %v", result, err)
		}
	}
}

// TestGenerationReloadSameNamespace proves a retained callback sees its old
// vars after the same authored namespace is loaded as a new generation.
func TestGenerationReloadSameNamespace(t *testing.T) {
	ctx := context.Background()
	host := letgo.NewRuntimeHost()
	old, err := host.NewGeneration(ctx, "pig.test.generation.reload", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close(ctx) }()
	if _, err := old.Run(ctx, "(def revision 1)"); err != nil {
		t.Fatal(err)
	}
	callback, err := old.Run(ctx, "(fn [] revision)")
	if err != nil {
		t.Fatal(err)
	}
	next, err := host.NewGeneration(ctx, "pig.test.generation.reload", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close(ctx) }()
	if _, err := next.Run(ctx, "(def revision 2)"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if got, err := old.Invoke(ctx, callback.(vm.Fn), nil); err != nil || got != vm.Int(1) {
			t.Fatalf("old generation: %v %v", got, err)
		}
		if got, err := next.Run(ctx, "revision"); err != nil || got != vm.Int(2) {
			t.Fatalf("new generation: %v %v", got, err)
		}
	}
}
