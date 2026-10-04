package letgo

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// Registrations is the inventory a source's init produced, in native registration order.
type Registrations struct {
	Tools    []string
	Commands []string
	// Events counts handlers by public event keyword, e.g. "session-start".
	Events map[string]int
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
	for _, adapter := range lifecycleEvents {
		if count := len(l.Extension.EventHandlers(strings.ReplaceAll(adapter.name, "-", "_"))); count > 0 {
			summary.Events[adapter.name] = count
		}
	}
	return summary
}
