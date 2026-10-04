package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	"github.com/MichaelKinsy/PiG/coding/extension/host/letgo"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
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

// letGoSet owns the generations a build loaded. Close retires each at most once, running its optional shutdown.
type letGoSet struct {
	mu     sync.Mutex
	loaded []*letgo.Loaded
	// positions[i] is the number of enabled subprocess configs that precede loaded[i] in discovery order.
	positions []int
	closed    bool
}

func (s *letGoSet) empty() bool { return s == nil || len(s.loaded) == 0 }

// Close retires every generation with a context that outlives a cancelled caller, and reports shutdown failures.
func (s *letGoSet) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	loaded := s.loaded
	s.mu.Unlock()
	var errs []error
	for _, generation := range loaded {
		errs = append(errs, generation.Close(context.WithoutCancel(ctx)))
	}
	return errors.Join(errs...)
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

// closeLetGo is the failure-path retirement of a build's interpreted generations.
func closeLetGo(set *letGoSet, reason string) {
	if err := set.Close(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: let-go shutdown (%s): %v\n", reason, err)
	}
}
