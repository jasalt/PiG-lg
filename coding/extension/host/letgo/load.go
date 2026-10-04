package letgo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/agent"
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
	api            vm.Value
	entry          entryFunctions
	activated      bool         // init returned; shutdown is owed
	shutdownRan    bool         // accessed only under the generation's VM gate
	latestSignal   *signalToken // accessed only under the generation's VM gate
}

// pig additive (D89): an invocationToken is the internal callback handle captured by context-map closures; it never reaches the interpreter itself.
type invocationToken struct {
	owner  *Loaded
	ctx    context.Context
	active bool
}

func (*invocationToken) String() string     { return "#<pig.context>" }
func (*invocationToken) Type() vm.ValueType { return vm.AnyType }
func (*invocationToken) Unbox() any         { return nil }

// scopedFunction builds the callback's context map inside VM entry, so its snapshot reads run under the generation gate.
type scopedFunction struct {
	vm.Fn
	owner     *Loaded
	token     *invocationToken
	arguments func(token *invocationToken, ctx vm.Value) []vm.Value
}

func (f scopedFunction) Invoke([]vm.Value) (vm.Value, error) {
	f.owner.current = f.token
	f.token.active = true
	defer func() { f.token.active = false; f.owner.current = nil }()
	ctx, err := f.owner.contextValue(f.token)
	if err != nil {
		return vm.NIL, err
	}
	return f.Fn.Invoke(f.arguments(f.token, ctx))
}

// Load evaluates one selected source, calls its init with the api capability map,
// and publishes the typed native registrations init made.
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
	if _, err = generation.withVM(ctx, func() (vm.Value, error) { return vm.NIL, loaded.installContext() }); err != nil {
		return nil, loaded.phaseError("install context", err)
	}
	// The pig.context wrappers run first so the generation is back in pig.extension before the source loads.
	if _, err = guardedValue(func() (vm.Value, error) { return generation.RunSource(ctx, contextFacade) }); err != nil {
		return nil, loaded.phaseError("install", err)
	}
	if _, err = guardedValue(func() (vm.Value, error) { return generation.RunSource(ctx, extensionFacade) }); err != nil {
		return nil, loaded.phaseError("install", err)
	}
	if _, err = guardedValue(func() (vm.Value, error) { return generation.RunSource(ctx, string(source)) }); err != nil {
		return nil, loaded.phaseError("load", err)
	}
	if _, err = generation.withVM(ctx, func() (vm.Value, error) {
		var resolveErr error
		loaded.entry, resolveErr = resolveEntry()
		return vm.NIL, resolveErr
	}); err != nil {
		return nil, loaded.phaseError("load", err)
	}
	if loaded.api, err = loaded.apiValue(options); err != nil {
		return nil, loaded.phaseError("install", err)
	}
	// Registrations made by init stay in the unpublished builder; a failure closes the generation before publication.
	if _, err = guardedValue(func() (vm.Value, error) {
		return generation.Invoke(ctx, loaded.entry.init, []vm.Value{loaded.api})
	}); err != nil {
		return nil, loaded.phaseError("init", err)
	}
	loaded.activated = true
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

// pig additive (D89): tools take Kmet's (fn [args]); :contextual? true selects (fn [args on-update signal ctx]) without arity inspection.
func (l *Loaded) registerTool(value vm.Value) (vm.Value, error) {
	if _, err := l.callbackContext(); err != nil {
		return vm.NIL, err
	}
	callback, data, err := callbackField(value, "execute")
	if err != nil {
		return vm.NIL, l.phaseError("register tool", err)
	}
	contextual, data, err := flagField(data, "contextual?")
	if err != nil {
		return vm.NIL, l.phaseError("register tool", err)
	}
	if err := registrationKeys(data, reflect.TypeFor[extension.ToolDefinition](), unsupportedToolKeys); err != nil {
		return vm.NIL, l.phaseError("register tool", err)
	}
	var definition extension.ToolDefinition
	if err := decodeValue(data, &definition); err != nil {
		return vm.NIL, l.phaseError("register tool", err)
	}
	if definition.Name == "" {
		return vm.NIL, l.phaseError("register tool", errors.New("$.name: tool requires a non-empty string name"))
	}
	definition.Execute = func(ctx context.Context, _ string, params json.RawMessage, onUpdate extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
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
		result, err := l.invokeScoped(ctx, callback, func(token *invocationToken, callbackContext vm.Value) []vm.Value {
			if !contextual {
				return []vm.Value{arguments}
			}
			return []vm.Value{arguments, &toolUpdate{owner: l, token: token, update: updateCallback(onUpdate)}, &signalToken{owner: l, context: token, signal: ctx}, callbackContext}
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

// unsupportedToolKeys are Kmet tool keys with no let-go realization; they fail rather than being silently ignored.
var unsupportedToolKeys = map[string]string{
	"params":            "Kmet :params shorthand is not supported; use :parameters with a JSON schema",
	"prepare-arguments": "Kmet :prepare-arguments is not supported by let-go tools",
	"title":             "Kmet :title is not supported by let-go tools",
	"streams?":          "Kmet :streams? is not supported; use :contextual? true for on-update",
	"render-call":       "Kmet :render-call is not supported by let-go tools",
	"render-result":     "Kmet :render-result is not supported by let-go tools",
}

// unsupportedCommandKeys name Kmet command metadata with no native CommandOptions field.
var unsupportedCommandKeys = map[string]string{
	"argument-hint": "Kmet :argument-hint has no native command field",
}

// updateCallback accepts the agent's update callback shapes; any other value drops updates, as the subprocess host does.
func updateCallback(onUpdate extension.AgentToolUpdateCallback) func(agent.AgentToolResult) {
	switch update := onUpdate.(type) {
	case agent.ToolUpdateCallback:
		return update
	case func(agent.AgentToolResult):
		return update
	}
	return nil
}

// toolUpdate is the contextual on-update function; it is valid only while its tool call runs.
type toolUpdate struct {
	owner  *Loaded
	token  *invocationToken
	update func(agent.AgentToolResult)
}

func (*toolUpdate) String() string     { return "#<pig.on-update>" }
func (*toolUpdate) Type() vm.ValueType { return vm.AnyType }
func (*toolUpdate) Unbox() any         { return nil }
func (*toolUpdate) Arity() int         { return 1 }

func (u *toolUpdate) Invoke(args []vm.Value) (vm.Value, error) {
	if len(args) != 1 {
		return vm.NIL, fmt.Errorf("on-update expects one partial result, got %d arguments", len(args))
	}
	if !u.token.active || u.owner.current != u.token {
		return vm.NIL, errors.New("on-update called outside its tool call")
	}
	if err := u.owner.registrations.activeError(); err != nil {
		return vm.NIL, err
	}
	partial, err := toolResultValue(args[0])
	if err != nil {
		return vm.NIL, fmt.Errorf("on-update: %w", err)
	}
	if u.update != nil {
		u.update(partial)
	}
	return vm.NIL, nil
}

// invoke calls a command callback as (ctx args).
func (l *Loaded) invoke(ctx context.Context, callback vm.Fn, payload vm.Value) (vm.Value, error) {
	return l.invokeScoped(ctx, callback, func(_ *invocationToken, callbackContext vm.Value) []vm.Value {
		return []vm.Value{callbackContext, payload}
	})
}

// invokeEvent calls an event handler as (event ctx), the Kmet-compatible order.
func (l *Loaded) invokeEvent(ctx context.Context, callback vm.Fn, event vm.Value) (vm.Value, error) {
	return l.invokeScoped(ctx, callback, func(_ *invocationToken, callbackContext vm.Value) []vm.Value {
		return []vm.Value{event, callbackContext}
	})
}

func (l *Loaded) invokeScoped(ctx context.Context, callback vm.Fn, arguments func(token *invocationToken, callbackContext vm.Value) []vm.Value) (vm.Value, error) {
	token := &invocationToken{owner: l, ctx: ctx}
	return guardedValue(func() (vm.Value, error) {
		return l.generation.Invoke(ctx, scopedFunction{Fn: callback, owner: l, token: token, arguments: arguments}, nil)
	})
}

// pig additive (D89): commands register Kmet's {:name :handler} map through the typed native subset and reuse generation-owned invocation.
func (l *Loaded) registerCommand(value vm.Value) (vm.Value, error) {
	if _, err := l.callbackContext(); err != nil {
		return vm.NIL, err
	}
	callback, data, err := callbackField(value, "handler")
	if err != nil {
		return vm.NIL, l.phaseError("register command", err)
	}
	completions, data, err := optionalCallbackField(data, "get-argument-completions")
	if err != nil {
		return vm.NIL, l.phaseError("register command", err)
	}
	var fields struct {
		Name string `json:"name"`
		extension.CommandOptions
	}
	if err := registrationKeys(data, reflect.TypeOf(fields), unsupportedCommandKeys); err != nil {
		return vm.NIL, l.phaseError("register command", err)
	}
	if err := decodeValue(data, &fields); err != nil {
		return vm.NIL, l.phaseError("register command", err)
	}
	commandName, options := fields.Name, fields.CommandOptions
	if commandName == "" {
		return vm.NIL, l.phaseError("register command", errors.New("$.name: command requires a non-empty string name"))
	}
	options.Handler = func(ctx context.Context, args string) error {
		_, err := l.invoke(ctx, callback, vm.String(args))
		if err != nil {
			return l.phaseError("execute command "+commandName, err)
		}
		return nil
	}
	if completions != nil {
		// Native completion has no context, so it is entered like the subprocess host's request: uncancelled.
		options.GetArgumentCompletions = func(prefix string) ([]extension.AutocompleteItem, error) {
			result, err := guardedValue(func() (vm.Value, error) {
				return l.generation.Invoke(context.Background(), completions, []vm.Value{vm.String(prefix)})
			})
			if err != nil {
				return nil, l.phaseError("argument completions "+commandName, err)
			}
			var items []extension.AutocompleteItem
			if err := decodeValue(result, &items); err != nil {
				return nil, l.phaseError("argument completions "+commandName, err)
			}
			return items, nil
		}
	}
	if err := l.registrations.RegisterCommand(commandName, options); err != nil {
		return vm.NIL, l.phaseError("register command", err)
	}
	return vm.NIL, nil
}

func callbackField(value vm.Value, field string) (vm.Fn, vm.Value, error) {
	function, data, err := takeField(value, field)
	if err != nil {
		return nil, nil, err
	}
	callback, ok := function.(vm.Fn)
	if !ok {
		return nil, nil, fmt.Errorf("$.%s: expected a function, got %T", field, function)
	}
	return callback, data, nil
}

// optionalCallbackField returns a nil callback when the field is absent or nil.
func optionalCallbackField(value vm.Value, field string) (vm.Fn, vm.Value, error) {
	function, data, err := takeField(value, field)
	if err != nil || function == vm.NIL {
		return nil, data, err
	}
	callback, ok := function.(vm.Fn)
	if !ok {
		return nil, nil, fmt.Errorf("%s: expected a function, got %T", fieldPath("$", field), function)
	}
	return callback, data, nil
}

// flagField reads an optional boolean registration flag; nil and absence are false.
func flagField(value vm.Value, field string) (bool, vm.Value, error) {
	flag, data, err := takeField(value, field)
	if err != nil {
		return false, nil, err
	}
	switch flag := flag.(type) {
	case vm.Boolean:
		return bool(flag), data, nil
	case *vm.Nil:
		return false, data, nil
	}
	return false, nil, fmt.Errorf("%s: expected a boolean, got %T", fieldPath("$", field), flag)
}

// takeField removes one keyword- or string-keyed field and returns its value, or nil when absent.
func takeField(value vm.Value, field string) (vm.Value, vm.Value, error) {
	switch value.(type) {
	case vm.Map, *vm.PersistentMap:
	default:
		return nil, nil, fmt.Errorf("$: registration must be an object, got %T", value)
	}
	keyed := value.(vm.Keyed)
	keyword, text := vm.Keyword(field), vm.String(field)
	if keyed.Contains(keyword) == vm.TRUE && keyed.Contains(text) == vm.TRUE {
		return nil, nil, fmt.Errorf("%s: duplicate object key", fieldPath("$", field))
	}
	lookup := value.(vm.Lookup)
	found := lookup.ValueAtOr(keyword, lookup.ValueAtOr(text, vm.NIL))
	return found, value.(vm.Associative).Dissoc(keyword).Dissoc(text), nil
}

// registrationKeys rejects every key the native decoder would otherwise ignore, naming Kmet-only keys explicitly.
// It inspects keys before converting values, so a Kmet callback value reports its key rather than its type.
func registrationKeys(data vm.Value, native reflect.Type, unsupported map[string]string) error {
	allowed := jsonFieldNames(native)
	var keys []string
	sequence := data.(vm.Sequable)
	for seq, i := sequence.Seq(), 0; i < data.(vm.Counted).RawCount() && seq != nil; seq, i = seq.Next(), i+1 {
		switch key := seq.First().(vm.Seq).First().(type) {
		case vm.String:
			keys = append(keys, string(key))
		case vm.Keyword:
			keys = append(keys, string(key))
		default:
			return fmt.Errorf("$: unsupported object key %T", key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		if reason, ok := unsupported[key]; ok {
			return fmt.Errorf("%s: %s", fieldPath("$", key), reason)
		}
		if !allowed[key] {
			return fmt.Errorf("%s: unsupported registration key", fieldPath("$", key))
		}
	}
	return nil
}

// jsonFieldNames lists the JSON object names the native decoder accepts, including embedded struct fields.
func jsonFieldNames(native reflect.Type) map[string]bool {
	names := make(map[string]bool)
	for i := range native.NumField() {
		field := native.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		switch {
		case field.Anonymous && name == "":
			maps.Copy(names, jsonFieldNames(field.Type))
		case name == "-" || !field.IsExported():
		case name == "":
			names[field.Name] = true
		default:
			names[name] = true
		}
	}
	return names
}

// Bind connects the published builder to the native runner's live context.
func (l *Loaded) Bind(ctx *extension.Context) error {
	if ctx == nil {
		return errors.New("let-go binding requires a native context")
	}
	return l.registrations.bind(func() error { _, err := ctx.CWD(); return err }, ctx.RefreshTools)
}

// Close rejects registrations, runs the optional shutdown at most once, and drains the owned generation.
// It is safe to retry after cancellation; a shutdown failure is reported but never prevents teardown.
func (l *Loaded) Close(ctx context.Context) error {
	l.registrations.close()
	var shutdownErr error
	if l.activated && l.entry.shutdown != nil {
		if _, err := guardedValue(func() (vm.Value, error) {
			return l.generation.Invoke(ctx, onceFunction{Fn: l.entry.shutdown, ran: &l.shutdownRan}, []vm.Value{l.api})
		}); err != nil && !errors.Is(err, ErrClosed) {
			shutdownErr = l.phaseError("shutdown", err)
		}
	}
	return errors.Join(shutdownErr, l.generation.Close(ctx))
}

// onceFunction marks itself run only after VM entry, so a Close cancelled while waiting can still run shutdown on retry.
type onceFunction struct {
	vm.Fn
	ran *bool
}

func (f onceFunction) Invoke(args []vm.Value) (vm.Value, error) {
	if *f.ran {
		return vm.NIL, nil
	}
	*f.ran = true
	return f.Fn.Invoke(args)
}
