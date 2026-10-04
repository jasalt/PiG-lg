package letgo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// LoadOptions supplies an already-resolved, trusted source. Discovery and trust remain the caller's responsibility.
type LoadOptions struct {
	Identity   extension.Extension
	Entrypoint string
	LoadPaths  []string
	Streams    Streams
}

// Loaded owns one interpreted extension generation and its native registrations.
// Invalidate its runner before closing it; Close drains interpreter entry cooperatively.
type Loaded struct {
	Extension      extension.Extension
	generation     *Generation
	registrations  *registrationBuilder
	path           string
	loadingContext context.Context
	current        *invocationToken // accessed only under the generation's VM gate
}

// pig additive (D89): callback contexts are opaque interpreter values, not reflective Go contexts.
type invocationToken struct {
	ctx    context.Context
	active bool
}

func (*invocationToken) String() string     { return "#<pig.context>" }
func (*invocationToken) Type() vm.ValueType { return vm.AnyType }
func (*invocationToken) Unbox() any         { return nil }

type scopedFunction struct {
	vm.Fn
	owner *Loaded
	token *invocationToken
}

func (f scopedFunction) Invoke(args []vm.Value) (vm.Value, error) {
	f.owner.current = f.token
	f.token.active = true
	defer func() { f.token.active = false; f.owner.current = nil }()
	return f.Fn.Invoke(args)
}

// Load evaluates one selected source and publishes typed native registrations.
// It does not construct a runner or bind Session actions.
// pig additive (D89): only selected trusted let-go sources enter the process-owned interpreter.
func Load(ctx context.Context, options LoadOptions) (_ *Loaded, err error) {
	if !filepath.IsAbs(options.Entrypoint) {
		return nil, errors.New("let-go entrypoint must be an absolute resolved path")
	}
	source, err := os.ReadFile(options.Entrypoint)
	if err != nil {
		return nil, fmt.Errorf("%s: read: %w", options.Entrypoint, err)
	}
	paths := options.LoadPaths
	if len(paths) == 0 {
		paths = []string{filepath.Dir(options.Entrypoint)}
	}
	generation, err := NewRuntimeHost().NewGeneration(ctx, "pig.extension", paths, options.Streams)
	if err != nil {
		return nil, fmt.Errorf("%s: construct: %w", options.Entrypoint, err)
	}
	loaded := &Loaded{generation: generation, registrations: newRegistrationBuilder(options.Identity), path: options.Entrypoint, loadingContext: ctx}
	defer func() {
		loaded.loadingContext = nil
		if err != nil {
			err = errors.Join(err, loaded.Close(context.WithoutCancel(ctx)))
		}
	}()
	if err = generation.Def(ctx, "register-tool!", loaded.registerTool); err != nil {
		return nil, loaded.phaseError("install", err)
	}
	if _, err = guardedValue(func() (vm.Value, error) { return generation.Run(ctx, "(do\n"+string(source)+"\n)") }); err != nil {
		return nil, loaded.phaseError("load", err)
	}
	loaded.Extension, err = loaded.registrations.snapshot()
	if err != nil {
		return nil, loaded.phaseError("publish", err)
	}
	return loaded, nil
}

func (l *Loaded) phaseError(phase string, err error) error {
	return fmt.Errorf("%s: %s: %w", l.path, phase, err)
}

func guardedValue(call func() (vm.Value, error)) (value vm.Value, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("interpreter panic: %v", recovered)
		}
	}()
	return call()
}

// callbackContext is called by Go functions only while this generation is in the VM.
// Propagating the marker makes synchronous nested interpreter entry fail rather than deadlock.
func (l *Loaded) callbackContext() (context.Context, error) {
	ctx := l.loadingContext
	if l.current != nil && l.current.active {
		ctx = l.current.ctx
	}
	if ctx == nil {
		return nil, errors.New("let-go host call has no active invocation")
	}
	return l.generation.CallbackContext(ctx), nil
}

func (l *Loaded) registerTool(value vm.Value) (vm.Value, error) {
	if _, err := l.callbackContext(); err != nil {
		return vm.NIL, err
	}
	callback, data, err := callbackField(value, "execute")
	if err != nil {
		return vm.NIL, l.phaseError("register tool", err)
	}
	var definition extension.ToolDefinition
	if err := decodeValue(data, &definition); err != nil {
		return vm.NIL, l.phaseError("register tool", err)
	}
	definition.Execute = func(ctx context.Context, _ string, params json.RawMessage, _ extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
		var plain any
		decoder := json.NewDecoder(bytes.NewReader(params))
		decoder.UseNumber()
		if err := decoder.Decode(&plain); err != nil {
			return nil, l.phaseError("tool arguments", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, l.phaseError("tool arguments", errors.New("arguments must contain one JSON object"))
		}
		if _, ok := plain.(map[string]any); !ok {
			return nil, l.phaseError("tool arguments", errors.New("arguments must be a JSON object"))
		}
		arguments, err := toValue(plain)
		if err != nil {
			return nil, l.phaseError("tool arguments", err)
		}
		token := &invocationToken{ctx: ctx}
		result, err := guardedValue(func() (vm.Value, error) {
			return l.generation.Invoke(ctx, scopedFunction{Fn: callback, owner: l, token: token}, []vm.Value{token, arguments})
		})
		if err != nil {
			return nil, l.phaseError("execute tool "+definition.Name, err)
		}
		decoded, err := toolResultValue(result)
		if err != nil {
			return nil, l.phaseError("tool result "+definition.Name, err)
		}
		return decoded, nil
	}
	if err := l.registrations.RegisterTool(definition); err != nil {
		return vm.NIL, l.phaseError("register tool", err)
	}
	return vm.NIL, nil
}

func callbackField(value vm.Value, field string) (vm.Fn, vm.Value, error) {
	switch value.(type) {
	case vm.Map, *vm.PersistentMap:
	default:
		return nil, nil, fmt.Errorf("$: registration must be an object, got %T", value)
	}
	keyed := value.(vm.Keyed)
	keyword, text := vm.Keyword(field), vm.String(field)
	if keyed.Contains(keyword) == vm.TRUE && keyed.Contains(text) == vm.TRUE {
		return nil, nil, fmt.Errorf("$.%s: duplicate object key", field)
	}
	lookup := value.(vm.Lookup)
	function := lookup.ValueAtOr(keyword, lookup.ValueAt(text))
	callback, ok := function.(vm.Fn)
	if !ok {
		return nil, nil, fmt.Errorf("$.%s: expected a function, got %T", field, function)
	}
	data := value.(vm.Associative).Dissoc(keyword).Dissoc(text)
	return callback, data, nil
}

// Bind connects the published builder to the native runner's live context.
func (l *Loaded) Bind(ctx *extension.Context) error {
	if ctx == nil {
		return errors.New("let-go binding requires a native context")
	}
	return l.registrations.bind(func() error { _, err := ctx.CWD(); return err }, ctx.RefreshTools)
}

// Close rejects registrations and drains the owned generation. It is safe to retry after cancellation.
func (l *Loaded) Close(ctx context.Context) error {
	l.registrations.close()
	return l.generation.Close(ctx)
}
