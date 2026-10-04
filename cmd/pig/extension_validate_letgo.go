package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/MichaelKinsy/PiG/coding/extension/host/letgo"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
	extsource "github.com/MichaelKinsy/PiG/coding/extension/source"
)

// validateLetGoSource validates an interpreted source the way a subprocess extension is validated: it loads it for real. A fresh
// generation evaluates the source and calls init against the real registration builder, which is code execution under the same trust as
// loading a subprocess extension. Validation dispatches no event, tool or command, and it disposes the generation before it returns.
// A generation whose init completed then runs its optional shutdown once, as any retired generation does, because init may have taken
// resources; a failing shutdown is a warning. A generation that failed to load was already disposed by the loader, and its shutdown never runs.
//
// pig additive (D89): interpreted sources validate through the real loader; the report lists what init registered.
func validateLetGoSource(ctx context.Context, abs string, config subprocess.ExtConfig, definitionReport *extensionValidationSourceReport, definition *extsource.Definition) (extensionValidationReport, error) {
	hash, err := extensionContentHash(abs, definition)
	if err != nil {
		return extensionValidationReport{}, err
	}
	loaded, registrations, err := letgo.LoadForTest(ctx, config.Entrypoint)
	if err != nil {
		return extensionValidationReport{}, err
	}
	ext := loaded.Extension
	report := extensionValidationReport{
		Valid: true, Name: config.Name, Source: config.Source, Path: config.Path, Hash: hash,
		Tools: sortedToolNames(&ext), Commands: mapKeys(ext.Commands), Handlers: registrations.Handlers,
		ToolDetails: toolDetailsFromExtension(&ext), CommandDetails: commandDetailsFromExtension(&ext),
		Registered: true, Definition: definitionReport,
	}
	if closeErr := loaded.Close(ctx); closeErr != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("shutdown of %s: %v", filepath.Base(config.Entrypoint), closeErr))
	}
	return report, nil
}
