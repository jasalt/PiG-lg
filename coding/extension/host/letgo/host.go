// Package letgo owns all entry into the process-global let-go interpreter.
// A generation installs its namespace and loader only while holding the host
// gate. Callback cancellation is cooperative once interpreted code begins.
package letgo

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/nooga/let-go/pkg/api"
	"github.com/nooga/let-go/pkg/rt"
	"github.com/nooga/let-go/pkg/vm"
)

var (
	ErrClosed    = errors.New("let-go extension generation is closed")
	ErrReentrant = errors.New("let-go callback cannot synchronously enter the interpreter again")
)

type vmScopeKey struct{}

type vmScope struct {
	host   *RuntimeHost
	active atomic.Bool
}

var processHost struct {
	sync.Once
	host *RuntimeHost
}

// RuntimeHost serializes VM entry across all let-go extension generations.
// All generations in a process must share one host.
type RuntimeHost struct {
	gate       chan struct{}
	baseline   map[string]*vm.Namespace
	baseLoader rt.NSLoader
	active     map[string]*vm.Namespace
}

// NewRuntimeHost returns the one coordinator for this process. A separate
// coordinator cannot safely serialize let-go's global namespace registry.
// pig additive (D89): selected let-go sources share process-global VM ownership.
func NewRuntimeHost() *RuntimeHost {
	processHost.Do(func() {
		gate := make(chan struct{}, 1)
		gate <- struct{}{}
		processHost.host = &RuntimeHost{gate: gate, baseline: rt.AllNSes(), baseLoader: rt.GetNSLoader()}
	})
	return processHost.host
}

type Streams struct {
	Stdout io.Writer
	Stderr io.Writer
}

// Generation retains the interpreter and namespace state of one source load.
// Its callbacks must enter via Invoke rather than calling vm.Fn directly.
type Generation struct {
	host         *RuntimeHost
	run          *api.LetGo
	loader       rt.NSLoader
	names        map[string]*vm.Namespace
	stdoutHandle vm.Value
	stderrHandle vm.Value
	scope        atomic.Pointer[vmScope]
	closed       bool // protected by host.gate
}

func (h *RuntimeHost) enter(ctx context.Context) error {
	if scope, ok := ctx.Value(vmScopeKey{}).(*vmScope); ok && scope.host == h && scope.active.Load() {
		return ErrReentrant
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.gate:
		if err := ctx.Err(); err != nil {
			h.gate <- struct{}{}
			return err
		}
		return nil
	}
}

func (h *RuntimeHost) leave() { h.gate <- struct{}{} }

func (h *RuntimeHost) swap(names map[string]*vm.Namespace, loader rt.NSLoader) {
	for name := range h.active {
		rt.RemoveNS(name)
	}
	for _, ns := range names {
		_ = rt.RegisterNS(ns)
	}
	h.active = names
	rt.SetNSLoader(loader)
}

func (h *RuntimeHost) capture() map[string]*vm.Namespace {
	names := make(map[string]*vm.Namespace)
	for name, ns := range rt.AllNSes() {
		if _, exists := h.baseline[name]; !exists {
			names[name] = ns
		}
	}
	return names
}

// NewGeneration creates an isolated interpreter namespace while holding the
// shared gate. Load paths are selected before any user code executes.
func (h *RuntimeHost) NewGeneration(ctx context.Context, namespace string, paths []string, streams ...Streams) (*Generation, error) {
	if len(streams) > 1 {
		return nil, errors.New("one streams configuration is supported per generation")
	}
	var options Streams
	if len(streams) != 0 {
		options = streams[0]
	}
	apiOptions := make([]api.Option, 0, 2)
	if options.Stdout != nil {
		apiOptions = append(apiOptions, api.WithStdout(options.Stdout))
	}
	if options.Stderr != nil {
		apiOptions = append(apiOptions, api.WithStderr(options.Stderr))
	}
	if err := h.enter(ctx); err != nil {
		return nil, err
	}
	defer h.leave()
	h.swap(nil, h.baseLoader)
	defer func() {
		h.active = h.capture()
		h.swap(nil, h.baseLoader)
	}()
	run, err := api.NewLetGo(namespace, apiOptions...)
	if err != nil {
		return nil, err
	}
	run.SetLoadPath(paths)
	g := &Generation{host: h, run: run, loader: rt.GetNSLoader(), names: h.capture()}
	if options.Stdout != nil {
		g.stdoutHandle = vm.NewBoxed(rt.NewWriterHandle("letgo.Stdout", options.Stdout))
	}
	if options.Stderr != nil {
		g.stderrHandle = vm.NewBoxed(rt.NewWriterHandle("letgo.Stderr", options.Stderr))
	}
	return g, nil
}

func (g *Generation) withVM(ctx context.Context, call func() (vm.Value, error)) (vm.Value, error) {
	if err := g.host.enter(ctx); err != nil {
		return vm.NIL, err
	}
	defer g.host.leave()
	if g.closed {
		return vm.NIL, ErrClosed
	}
	g.host.swap(g.names, g.loader)
	scope := &vmScope{host: g.host}
	scope.active.Store(true)
	g.scope.Store(scope)
	defer func() {
		scope.active.Store(false)
		g.scope.Store(nil)
		g.names = g.host.capture()
		g.host.active = g.names
		g.host.swap(nil, g.host.baseLoader)
	}()
	return call()
}

// CallbackContext marks the context passed to synchronous Go host functions
// invoked by this generation. Those functions must propagate it to any nested
// let-go call so the coordinator rejects reentry instead of deadlocking.
func (g *Generation) CallbackContext(ctx context.Context) context.Context {
	if scope := g.scope.Load(); scope != nil {
		return context.WithValue(ctx, vmScopeKey{}, scope)
	}
	return ctx
}

// Def injects one Go function or value into the generation's current namespace.
func (g *Generation) Def(ctx context.Context, name string, value any) error {
	_, err := g.withVM(ctx, func() (vm.Value, error) { return vm.NIL, g.run.Def(name, value) })
	return err
}

// Run compiles and evaluates source in this generation.
func (g *Generation) Run(ctx context.Context, source string) (vm.Value, error) {
	return g.withVM(ctx, func() (vm.Value, error) { return g.run.Run(source) })
}

// Invoke calls a retained interpreted function in its originating generation.
// It restores the generation's output bindings because vm.Fn.Invoke bypasses
// api.Run's per-evaluation binding scope.
func (g *Generation) Invoke(ctx context.Context, fn vm.Fn, args []vm.Value) (vm.Value, error) {
	return g.withVM(ctx, func() (vm.Value, error) {
		if g.stdoutHandle != nil {
			if out := rt.LookupCoreVar("*out*"); out != nil {
				out.PushBinding(g.stdoutHandle)
				defer out.PopBinding()
			}
		}
		if g.stderrHandle != nil {
			if stderr := rt.LookupCoreVar("*err*"); stderr != nil {
				stderr.PushBinding(g.stderrHandle)
				defer stderr.PopBinding()
			}
		}
		return fn.Invoke(args)
	})
}

// Close prevents new callbacks after earlier calls have left the VM. It does
// not interrupt pure CPU-bound code already executing in this generation.
func (g *Generation) Close(ctx context.Context) error {
	if err := g.host.enter(ctx); err != nil {
		return err
	}
	defer g.host.leave()
	g.closed = true
	g.names = nil
	g.run = nil
	g.loader = nil
	return nil
}
