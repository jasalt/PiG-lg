package letgo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/nooga/let-go/pkg/rt"
	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
)

// lifetimeSource is a representative extension: helper functions, a tool, a command, an event handler and a shutdown.
const lifetimeSource = `(ns lifetime.fixture (:require [pig.extension :as ext]))
(def counter (atom 0))
(defn helper [x] (let [a (inc x) b (* a 2)] (str a b)))
(defn init [api]
  (ext/register-tool! api {:name "t" :parameters {:type "object"} :execute (fn [args] (swap! counter inc) {:content (helper @counter)})})
  (ext/register-command! api {:name "c" :handler (fn [ctx args] (swap! counter inc))})
  (ext/on-event api :session-start (fn [event ctx] (swap! counter inc))))
(defn shutdown [api] (println "shutdown"))
`

func lifetimeSnapshot() (forms, namespaces int) {
	return vm.FormSource.Len(), len(rt.AllNSes())
}

// ownedGoroutines counts goroutines started by the interpreter or by this package's host, which a retired generation must not leave.
func ownedGoroutines() int {
	buffer := make([]byte, 1<<22)
	buffer = buffer[:runtime.Stack(buffer, true)]
	owned := 0
	for _, block := range strings.Split(string(buffer), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "created by github.com/nooga/let-go") || strings.HasPrefix(line, "created by github.com/MichaelKinsy/PiG/coding/extension/host/letgo.") && !strings.Contains(line, "Test") {
				owned++
				break
			}
		}
	}
	return owned
}

func openDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	return len(entries)
}

func loadLifetime(t *testing.T, path string, stdout *bytes.Buffer) *Loaded {
	t.Helper()
	options := LoadOptions{Entrypoint: path, Identity: extension.Extension{Name: "lifetime", Path: path}}
	if stdout != nil {
		options.Streams = Streams{Stdout: stdout}
	}
	loaded, err := Load(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestLifetimeRepeatedGenerationsLeaveNoOwnedState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "extension.lg")
	if err := os.WriteFile(path, []byte(lifetimeSource), 0o600); err != nil {
		t.Fatal(err)
	}
	// Warm up once so lazily created process state is part of the baseline.
	warm := loadLifetime(t, path, &bytes.Buffer{})
	if err := warm.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	baseForms, baseNamespaces := lifetimeSnapshot()
	baseGoroutines, baseDescriptors := ownedGoroutines(), openDescriptors(t)
	for cycle := range 40 {
		var stdout bytes.Buffer
		loaded := loadLifetime(t, path, &stdout)
		runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, dir)
		if !runner.ExecuteCommand(t.Context(), "c", "") {
			t.Fatalf("cycle %d: command not handled", cycle)
		}
		if _, err := runner.Emit(t.Context(), extension.SessionStartEvent{Type: "session_start", Reason: "startup"}); err != nil {
			t.Fatal(err)
		}
		runner.Invalidate("cycle complete")
		for range 2 {
			if err := loaded.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		if got := strings.Count(stdout.String(), "shutdown"); got != 1 {
			t.Fatalf("cycle %d: shutdown ran %d times", cycle, got)
		}
		if forms, namespaces := lifetimeSnapshot(); forms != baseForms || namespaces != baseNamespaces {
			t.Fatalf("cycle %d: form sources %d (base %d), namespaces %d (base %d)", cycle, forms, baseForms, namespaces, baseNamespaces)
		}
	}
	if goroutines := ownedGoroutines(); goroutines != baseGoroutines {
		t.Errorf("owned goroutines %d after 40 generations, base %d", goroutines, baseGoroutines)
	}
	if descriptors := openDescriptors(t); descriptors != baseDescriptors {
		t.Errorf("open descriptors %d after 40 generations, base %d", descriptors, baseDescriptors)
	}
}

func TestLifetimeHelperNamespacesRetireWithTheirGeneration(t *testing.T) {
	root := writeTree(t, map[string]string{
		"extension.cljc": `(ns lifetime.entry (:require [pig.extension :as ext] [lifetime.helper :as helper]))
(defn init [api] (ext/register-tool! api {:name "t" :parameters {:type "object"} :execute (fn [args] {:content (helper/value)})}))`,
		"lifetime/helper.cljc": `(ns lifetime.helper) (defn value [] "helper")`,
	})
	path := filepath.Join(root, "extension.cljc")
	warm := loadAt(t, path)
	if err := warm.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, baseNamespaces := lifetimeSnapshot()
	live := loadAt(t, path)
	if _, namespaces := lifetimeSnapshot(); namespaces != baseNamespaces {
		// Namespaces are installed into the process registry only while a generation is inside the VM, so a loaded generation adds none.
		t.Logf("a loaded generation holds %d namespaces outside its entry", namespaces-baseNamespaces)
	}
	if err := live.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name := range rt.AllNSes() {
		if strings.HasPrefix(name, "lifetime.") {
			t.Errorf("namespace %s outlived its generation", name)
		}
	}
	if _, namespaces := lifetimeSnapshot(); namespaces != baseNamespaces {
		t.Fatalf("namespaces %d after close, base %d", namespaces, baseNamespaces)
	}
}

func TestLifetimeCancelledQueueEntriesNeverEnterOrLeakTheGate(t *testing.T) {
	loaded := loadToolSource(t, `(ns lifetime.queue (:require [pig.extension :as ext]))
(def entered (atom 0))
(defn init [api]
  (ext/register-command! api {:name "block" :handler (fn [ctx args] ((:select ctx) "hold" ["a"]))})
  (ext/register-command! api {:name "count" :handler (fn [ctx args] (swap! entered inc))}))`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	ui := &blockingTestUI{UIContext: extension.NoopUIContext, entered: make(chan struct{}), release: make(chan struct{})}
	runner.SetUIContext(ui, extension.ModeTUI)
	held := make(chan struct{})
	go func() { defer close(held); runner.ExecuteCommand(t.Context(), "block", "") }()
	<-ui.entered

	// The gate is held by the blocked callback. Queued entries with a context that is cancelled while they wait return without entering.
	var waiters sync.WaitGroup
	cancels := make([]context.CancelFunc, 8)
	results := make([]error, len(cancels))
	for i := range cancels {
		ctx, cancel := context.WithCancel(t.Context())
		cancels[i] = cancel
		waiters.Add(1)
		go func() {
			defer waiters.Done()
			results[i] = loaded.Extension.Commands["count"].Handler(ctx, "")
		}()
	}
	for _, cancel := range cancels {
		cancel()
	}
	waiters.Wait()
	for i, err := range results {
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Errorf("queued entry %d: %v", i, err)
		}
	}
	close(ui.release)
	<-held
	if err := loaded.Extension.Commands["count"].Handler(t.Context(), ""); err != nil {
		t.Fatalf("the gate was lost: %v", err)
	}
	value, err := loaded.generation.Run(t.Context(), `(deref lifetime.queue/entered)`)
	if err != nil || value != vm.Int(1) {
		t.Fatalf("cancelled entries ran: entered=%v, %v", value, err)
	}
}

func TestLifetimeNativeWorkDoesNotWaitForTheVM(t *testing.T) {
	loaded := loadToolSource(t, `(ns lifetime.native (:require [pig.extension :as ext]))
(defn init [api] (ext/register-command! api {:name "block" :handler (fn [ctx args] ((:select ctx) "hold" ["a"]))}))`)
	interpreted := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer interpreted.Invalidate("test complete")
	ui := &blockingTestUI{UIContext: extension.NoopUIContext, entered: make(chan struct{}), release: make(chan struct{})}
	interpreted.SetUIContext(ui, extension.ModeTUI)
	held := make(chan struct{})
	go func() { defer close(held); interpreted.ExecuteCommand(t.Context(), "block", "") }()
	<-ui.entered

	// Another runner with only native extensions shares the process but takes no interpreter entry, so the held VM cannot delay it.
	native := extension.Extension{Name: "native", Path: "native"}
	native.InitializeEventHandlers()
	handled := make(chan struct{})
	native.AddEventHandler("session_start", 1, func(args ...any) (any, error) { close(handled); return nil, nil })
	runner := inproc.NewRunner([]extension.Extension{native}, t.TempDir())
	defer runner.Invalidate("test complete")
	emitted := make(chan error, 1)
	go func() {
		_, err := runner.Emit(t.Context(), extension.SessionStartEvent{Type: "session_start", Reason: "startup"})
		emitted <- err
	}()
	<-handled
	if err := <-emitted; err != nil {
		t.Fatal(err)
	}
	close(ui.release)
	<-held
}

// blockingTestUI holds a select dialog open until released.
type blockingTestUI struct {
	extension.UIContext
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (u *blockingTestUI) Select(ctx context.Context, _ string, _ []string, _ extension.ExtensionUIDialogOptions) (string, error) {
	u.once.Do(func() { close(u.entered) })
	select {
	case <-u.release:
		return "done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func benchmarkLifetimeSource(b *testing.B) string {
	path := filepath.Join(b.TempDir(), "extension.lg")
	if err := os.WriteFile(path, []byte(lifetimeSource), 0o600); err != nil {
		b.Fatal(err)
	}
	return path
}

// BenchmarkGenerationReload is the cost of replacing a generation: a fresh load, which evaluates the source and runs init, then its retirement.
func BenchmarkGenerationReload(b *testing.B) {
	path := benchmarkLifetimeSource(b)
	options := LoadOptions{Entrypoint: path, Identity: extension.Extension{Name: "bench", Path: path}, Streams: Streams{Stdout: &bytes.Buffer{}}}
	for b.Loop() {
		loaded, err := Load(b.Context(), options)
		if err != nil {
			b.Fatal(err)
		}
		if err := loaded.Close(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkToolDispatch is one tool call through the native runner into a retained generation.
func BenchmarkToolDispatch(b *testing.B) {
	path := benchmarkLifetimeSource(b)
	loaded, err := Load(b.Context(), LoadOptions{Entrypoint: path, Identity: extension.Extension{Name: "bench", Path: path}, Streams: Streams{Stdout: &bytes.Buffer{}}})
	if err != nil {
		b.Fatal(err)
	}
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, filepath.Dir(path))
	b.Cleanup(func() { runner.Invalidate("benchmark complete"); _ = loaded.Close(context.Background()) })
	tool := runner.Tools()[0].Definition
	ctx := runner.DispatchContext(b.Context())
	for b.Loop() {
		if _, err := tool.Execute(ctx, "bench", []byte(`{"text":"x"}`), nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEventDispatch is one lifecycle event through the native runner into the interpreted handler, including the context map.
func BenchmarkEventDispatch(b *testing.B) {
	path := benchmarkLifetimeSource(b)
	loaded, err := Load(b.Context(), LoadOptions{Entrypoint: path, Identity: extension.Extension{Name: "bench", Path: path}, Streams: Streams{Stdout: &bytes.Buffer{}}})
	if err != nil {
		b.Fatal(err)
	}
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, filepath.Dir(path))
	b.Cleanup(func() { runner.Invalidate("benchmark complete"); _ = loaded.Close(context.Background()) })
	event := extension.SessionStartEvent{Type: "session_start", Reason: "startup"}
	for b.Loop() {
		if _, err := runner.Emit(b.Context(), event); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLifetimeHeapDoesNotGrowWithGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extension.lg")
	if err := os.WriteFile(path, []byte(lifetimeSource), 0o600); err != nil {
		t.Fatal(err)
	}
	cycle := func(count int) {
		for range count {
			var stdout bytes.Buffer
			loaded := loadLifetime(t, path, &stdout)
			if err := loaded.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
	heap := func() uint64 {
		runtime.GC()
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return stats.HeapAlloc
	}
	cycle(50)
	warm := heap()
	cycle(300)
	grown := heap()
	t.Logf("live heap after 50 generations %d KiB, after 350 generations %d KiB", warm/1024, grown/1024)
	if grown > warm+1<<20 {
		t.Fatalf("live heap grew from %d KiB to %d KiB over 300 generations", warm/1024, grown/1024)
	}
}
