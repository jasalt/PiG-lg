package letgo

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// Registrations is the inventory a source's init produced, in native registration order.
type Registrations struct {
	Tools    []string
	Commands []string
	// Events counts handlers by public event keyword, e.g. "session-start". It covers the no-result events and the result hooks.
	Events map[string]int
	// Handlers lists the native names of the events that have at least one handler, sorted, in the form a subprocess report uses.
	Handlers []string
}

// pig additive (D89): a test-only inventory over the real loader, not a fake production API.
// LoadForTest runs a source's init through the real loader without a runner or
// live session, in the spirit of Kmet's nullable api. Callbacks that need a
// live native context fail as they would before binding. The caller owns the
// returned generation and must Close it.
func LoadForTest(ctx context.Context, entrypoint string) (*Loaded, Registrations, error) {
	path, err := filepath.Abs(entrypoint)
	if err != nil {
		return nil, Registrations{}, err
	}
	loaded, err := Load(ctx, LoadOptions{Entrypoint: path, Identity: extension.Extension{Name: filepath.Base(path), Path: path, ResolvedPath: path}})
	if err != nil {
		return nil, Registrations{}, err
	}
	return loaded, loaded.registrationsSummary(), nil
}

func (l *Loaded) registrationsSummary() Registrations {
	summary := Registrations{Commands: append([]string(nil), l.Extension.CommandOrder...), Events: make(map[string]int)}
	for _, tool := range l.Extension.RegisteredTools() {
		summary.Tools = append(summary.Tools, tool.Definition.Name)
	}
	names := make([]string, 0, len(lifecycleEvents)+len(resultEvents))
	for _, adapter := range lifecycleEvents {
		names = append(names, adapter.name)
	}
	for name := range resultEvents {
		names = append(names, name)
	}
	for _, name := range names {
		native := strings.ReplaceAll(name, "-", "_")
		if count := len(l.Extension.EventHandlers(native)); count > 0 {
			summary.Events[name] = count
			summary.Handlers = append(summary.Handlers, native)
		}
	}
	slices.Sort(summary.Handlers)
	return summary
}

// APIKeys lists the capability names init's api map carries, so a conformance row can pin the supported inventory.
func (l *Loaded) APIKeys() []string {
	var keys []string
	sequence := l.api.(vm.Sequable)
	for seq, i := sequence.Seq(), 0; i < l.api.(vm.Counted).RawCount() && seq != nil; seq, i = seq.Next(), i+1 {
		if key, ok := seq.First().(vm.Seq).First().(vm.Keyword); ok {
			keys = append(keys, string(key))
		}
	}
	return keys
}
