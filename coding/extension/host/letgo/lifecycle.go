package letgo

import (
	_ "embed"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/nooga/let-go/pkg/rt"
	"github.com/nooga/let-go/pkg/vm"
)

// facadeNamespace hosts the public wrappers. The generation starts in it so the
// facade source defines its vars before any extension code runs.
const facadeNamespace = "pig.extension"

//go:embed clj/pig/extension.cljc
var extensionFacade string

// entryFunctions are the lifecycle vars resolved after the source is evaluated.
type entryFunctions struct {
	namespace string
	init      vm.Fn
	shutdown  vm.Fn // nil when the extension defines no shutdown
}

// resolveEntry finds init and optional shutdown in the namespace that is current
// after evaluation, normally the one the source declared with ns. It runs only
// inside the generation coordinator.
func resolveEntry() (entryFunctions, error) {
	namespace, ok := rt.CurrentNS.Deref().(*vm.Namespace)
	if !ok || namespace == nil {
		return entryFunctions{}, errors.New("source left no current namespace")
	}
	entry := entryFunctions{namespace: namespace.Name()}
	if entry.namespace == facadeNamespace {
		return entryFunctions{}, errors.New("source must declare its own namespace with (ns ...)")
	}
	initVar := namespace.LookupLocal(vm.Symbol("init"))
	if initVar == nil {
		return entryFunctions{}, fmt.Errorf("namespace %s does not define init", entry.namespace)
	}
	initFn, ok := initVar.Deref().(vm.Fn)
	if !ok {
		return entryFunctions{}, fmt.Errorf("%s/init must be a function, got %s", entry.namespace, initVar.Deref().Type().Name())
	}
	entry.init = initFn
	if shutdownVar := namespace.LookupLocal(vm.Symbol("shutdown")); shutdownVar != nil {
		shutdownFn, ok := shutdownVar.Deref().(vm.Fn)
		if !ok {
			return entryFunctions{}, fmt.Errorf("%s/shutdown must be a function, got %s", entry.namespace, shutdownVar.Deref().Type().Name())
		}
		entry.shutdown = shutdownFn
	}
	return entry, nil
}

// pig additive (D89): init receives an explicit capability map; there is no pig.internal.* namespace.
func (l *Loaded) apiValue(options LoadOptions) (vm.Value, error) {
	name := options.Identity.Name
	if name == "" {
		name = filepath.Base(options.Entrypoint)
	}
	api := vm.EmptyPersistentMap
	for key, value := range map[string]vm.Value{
		"extension-name": vm.String(name),
		"extension-path": vm.String(options.Entrypoint),
		"extension-dir":  vm.String(filepath.Dir(options.Entrypoint)),
	} {
		api = api.Assoc(vm.Keyword(key), value).(*vm.PersistentMap)
	}
	for key, function := range map[string]any{
		"register-tool!":    l.registerTool,
		"register-command!": l.registerCommand,
		"on-event":          l.onEvent,
	} {
		boxed, err := vm.NativeFnType.Box(function)
		if err != nil {
			return vm.NIL, err
		}
		api = api.Assoc(vm.Keyword(key), boxed).(*vm.PersistentMap)
	}
	return api, nil
}
