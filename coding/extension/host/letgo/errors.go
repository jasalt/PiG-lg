package letgo

import (
	"fmt"
	"strings"
)

// Failure categories a diagnostic reports. Resolve and reload failures happen outside this package: the resolver reports the first
// before a source reaches Load, and the reload path reports the second around Load.
const (
	PhaseEval     = "eval"     // reading, compiling and evaluating the source's forms
	PhaseInit     = "init"     // resolving, calling and finishing init
	PhaseRegister = "register" // publishing a registration the extension made
	PhaseCallback = "callback" // a tool, command, event, hook or context call after loading
	PhaseShutdown = "shutdown" // the optional shutdown
)

// PhaseError is a failure of one interpreted source in one phase. Error reads "<source path>: <phase>: <cause>".
// pig additive (D89): the typed form lets validation and the CLI report the extension, source, phase and cause separately.
type PhaseError struct {
	// Path is the selected entrypoint.
	Path string
	// Phase is the detailed step, for example "init" or "register tool" or "execute command greet".
	Phase string
	Err   error
	// category overrides the one Phase implies, when the interpreter wrapped a more specific host failure.
	category string
}

func (e *PhaseError) Error() string { return fmt.Sprintf("%s: %s: %v", e.Path, e.Phase, e.Err) }
func (e *PhaseError) Unwrap() error { return e.Err }

// Category groups the detailed phase into one of the Phase constants.
func (e *PhaseError) Category() string {
	if e.category != "" {
		return e.category
	}
	switch {
	case e.Phase == "install context" || e.Phase == "install" || e.Phase == "load":
		return PhaseEval
	case e.Phase == "init":
		return PhaseInit
	case e.Phase == "publish" || strings.HasPrefix(e.Phase, "register"):
		return PhaseRegister
	case e.Phase == "shutdown":
		return PhaseShutdown
	default:
		return PhaseCallback
	}
}
