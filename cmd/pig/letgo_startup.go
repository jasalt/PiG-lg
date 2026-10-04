package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	"github.com/MichaelKinsy/PiG/coding/extension/host/letgo"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
	"github.com/MichaelKinsy/PiG/internal/codingagent"
)

// pig additive (D89): selected, trusted let-go sources are interpreted in this process through the same discovery, trust and
// ordering policy as every other extension source. They never enter the subprocess host. A source evaluates only after project
// trust is resolved, so an untrusted project source never reaches the interpreter, and each generation is closed with the build
// that loaded it.

func isLetGoConfig(config subprocess.ExtConfig) bool { return config.RuntimeKind == "let-go" }

// splitLetGoConfigs separates the interpreted sources from the subprocess ones, keeping each group's order.
func splitLetGoConfigs(configs []subprocess.ExtConfig) (interpreted, others []subprocess.ExtConfig) {
	for _, config := range configs {
		if isLetGoConfig(config) {
			interpreted = append(interpreted, config)
		} else {
			others = append(others, config)
		}
	}
	return interpreted, others
}

// letGoSet is one load of interpreted generations. Close retires each at most once, running its optional shutdown. A second
// Close waits for the first, so a caller that must not exit before shutdown finished can always call it.
type letGoSet struct {
	loaded []*letgo.Loaded
	// positions[i] is the number of enabled subprocess configs that precede loaded[i] in discovery order.
	positions []int
	once      sync.Once
	closeErr  error
}

func (s *letGoSet) empty() bool { return s == nil || len(s.loaded) == 0 }

// Close retires every generation with a context that outlives a cancelled caller, and reports shutdown failures.
func (s *letGoSet) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		var errs []error
		for _, generation := range s.loaded {
			errs = append(errs, generation.Close(context.WithoutCancel(ctx)))
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

// bind connects each generation to the live runner after the Session has bound its actions, so callback-time tool registration
// refreshes the Session's tool set and a replaced runner rejects later registrations.
func (s *letGoSet) bind(ctx context.Context, runner *inproc.Runner) error {
	if s.empty() {
		return nil
	}
	native := extension.FromContext(runner.DispatchContext(ctx))
	for _, generation := range s.loaded {
		if err := generation.Bind(native); err != nil {
			return err
		}
	}
	return nil
}

// loadLetGoSet evaluates each interpreted config in discovery order. A failing source is reported and skipped; the others load.
// Output of the interpreted code is discarded: stdout may carry a mode's protocol and the terminal belongs to the TUI.
// allConfigs is the full discovery order, used to remember where each interpreted extension sits among the subprocess ones.
func loadLetGoSet(ctx context.Context, allConfigs []subprocess.ExtConfig) (*letGoSet, []error) {
	set := &letGoSet{}
	var errs []error
	others := 0
	for _, config := range allConfigs {
		if !isLetGoConfig(config) {
			if config.Enabled {
				others++
			}
			continue
		}
		if !config.Enabled {
			continue
		}
		path, resolved := config.SourcePaths()
		generation, err := letgo.Load(ctx, letgo.LoadOptions{
			Identity:   extension.Extension{Name: config.Name, Path: path, ResolvedPath: resolved, SourceInfo: config.SourceInfo},
			Entrypoint: config.Entrypoint,
			Streams:    letgo.Streams{Stdout: io.Discard, Stderr: io.Discard},
		})
		if err != nil {
			errs = append(errs, &subprocess.ExtensionLoadError{Name: config.Name, Path: config.Origin(), Err: err})
			continue
		}
		set.loaded = append(set.loaded, generation)
		set.positions = append(set.positions, others)
	}
	return set, errs
}

// interleaveLetGo places each interpreted extension where discovery put it relative to the subprocess extensions. The subprocess
// host's own plan order is unchanged.
func interleaveLetGo(subprocessLoaded []extension.Extension, set *letGoSet) []extension.Extension {
	if set.empty() {
		return subprocessLoaded
	}
	merged := make([]extension.Extension, 0, len(subprocessLoaded)+len(set.loaded))
	next := 0
	for i, generation := range set.loaded {
		upto := min(set.positions[i], len(subprocessLoaded))
		for ; next < upto; next++ {
			merged = append(merged, subprocessLoaded[next])
		}
		merged = append(merged, generation.Extension)
	}
	return append(merged, subprocessLoaded[next:]...)
}

// letGoOwner is a build's handle on its interpreted generations across reloads. A reload stages a fresh set, which evaluates the
// sources again from clean interpreter state. When the replacement runner is published, attach makes the staged set current and binds
// it, and the previous set retires in the background: its runner is already stale, so no new call reaches it, and Close drains the
// callbacks still running before it runs shutdown. Nothing patches a live namespace.
type letGoOwner struct {
	mu       sync.Mutex
	current  *letGoSet
	staged   *letGoSet
	retiring sync.WaitGroup
	closed   bool
}

func newLetGoOwner(set *letGoSet) *letGoOwner { return &letGoOwner{current: set} }

// empty reports whether no generation is current.
func (o *letGoOwner) empty() bool {
	if o == nil {
		return true
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.current.empty()
}

// stage evaluates every interpreted config again as a fresh set. Failing sources are reported and left out, as a reload
// leaves out any extension that fails to load; the set that was current keeps serving until attach publishes the staged one.
func (o *letGoOwner) stage(ctx context.Context, configs []subprocess.ExtConfig) []error {
	set, errs := loadLetGoSet(ctx, configs)
	o.mu.Lock()
	previous := o.staged
	o.staged = set
	closed := o.closed
	o.mu.Unlock()
	if previous != nil {
		closeLetGo(previous, "restaged")
	}
	if closed {
		o.abort()
	}
	return errs
}

// merge places the staged set's extensions among the reloaded subprocess extensions. Without a staged set it keeps the current one.
func (o *letGoOwner) merge(subprocessLoaded []extension.Extension) []extension.Extension {
	if o == nil {
		return subprocessLoaded
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.staged != nil {
		return interleaveLetGo(subprocessLoaded, o.staged)
	}
	return interleaveLetGo(subprocessLoaded, o.current)
}

// abort discards a staged set that was not published.
func (o *letGoOwner) abort() {
	if o == nil {
		return
	}
	o.mu.Lock()
	staged := o.staged
	o.staged = nil
	o.mu.Unlock()
	closeLetGo(staged, "reload aborted")
}

// attach publishes the staged set, if any, and binds the current set to the runner that now serves it. It runs after the Session
// bound its actions to the runner and after the runner it replaces was invalidated.
func (o *letGoOwner) attach(ctx context.Context, runner *inproc.Runner) error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	var retired *letGoSet
	if o.staged != nil {
		retired, o.current, o.staged = o.current, o.staged, nil
	}
	current := o.current
	if retired != nil {
		o.retiring.Add(1)
	}
	o.mu.Unlock()
	if retired != nil {
		go func() {
			defer o.retiring.Done()
			closeLetGo(retired, "reload")
		}()
	}
	return current.bind(ctx, runner)
}

// Close retires the current and any staged set, then waits for sets retiring after a reload.
func (o *letGoOwner) Close(ctx context.Context) error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	o.closed = true
	current, staged := o.current, o.staged
	o.staged = nil
	o.mu.Unlock()
	err := errors.Join(current.Close(ctx), staged.Close(ctx))
	o.retiring.Wait()
	return err
}

// closeLetGoOwner retires everything an owner holds and reports a failed shutdown.
func closeLetGoOwner(owner *letGoOwner, reason string) {
	if err := owner.Close(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: let-go shutdown (%s): %v\n", reason, err)
	}
}

// adopt makes set the owner's current generations, as the startup load does.
func (o *letGoOwner) adopt(set *letGoSet) {
	o.mu.Lock()
	o.current = set
	o.mu.Unlock()
}

// closeLetGo is the failure-path retirement of a build's interpreted generations.
func closeLetGo(set *letGoSet, reason string) {
	if err := set.Close(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: let-go shutdown (%s): %v\n", reason, err)
	}
}

// reloadingHost is the interactive reload's view of the subprocess host. Its Reload also stages the interpreted sources, so one
// reload publishes both through the one runner replacement. The host is a named field, not embedded, so the type exposes exactly what
// codingagent.SubprocessHost and the interactive mode's one extra assertion (Extensions) need.
type reloadingHost struct {
	host  *subprocess.Host
	build *cliBuild
}

// Reload stages the interpreted sources, reloads the subprocess extensions on the host's own transaction, and returns the merged load
// order. A failed host reload discards the staged generations, so the ones that were current keep serving.
func (h reloadingHost) Reload(ctx context.Context) ([]extension.Extension, error) {
	for _, stageErr := range h.build.LetGo.stage(ctx, h.build.LetGoConfigs()) {
		fmt.Fprintf(os.Stderr, "extension reload: %v\n", stageErr)
	}
	reloaded, err := h.host.Reload(ctx)
	if err != nil {
		h.build.LetGo.abort()
		return nil, err
	}
	return h.build.LetGo.merge(reloaded), nil
}

func (h reloadingHost) ExtensionCount() int                        { return h.host.ExtensionCount() }
func (h reloadingHost) Extensions() []extension.Extension          { return h.host.Extensions() }
func (h reloadingHost) LastReloadReport() *subprocess.ReloadReport { return h.host.LastReloadReport() }
func (h reloadingHost) LoadErrors() []string                       { return h.host.LoadErrors() }
func (h reloadingHost) SetWidthFunc(fn func() int)                 { h.host.SetWidthFunc(fn) }
func (h reloadingHost) SetHeightFunc(fn func() int)                { h.host.SetHeightFunc(fn) }
func (h reloadingHost) NotifyWidth(width int)                      { h.host.NotifyWidth(width) }
func (h reloadingHost) NotifyHeight(height int)                    { h.host.NotifyHeight(height) }
func (h reloadingHost) IsShuttingDown() bool                       { return h.host.IsShuttingDown() }
func (h reloadingHost) SetCrashHandler(fn func(name string, delay time.Duration, disabled bool, reason string)) {
	h.host.SetCrashHandler(fn)
}

// reloadHost is the host interactive mode reloads through, or nil without one.
func (b *cliBuild) reloadHost() codingagent.SubprocessHost {
	if b.Host == nil {
		return nil
	}
	return reloadingHost{host: b.Host, build: b}
}
