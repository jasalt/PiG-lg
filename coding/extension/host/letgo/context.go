package letgo

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/nooga/let-go/pkg/rt"
	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	codingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
)

// pig additive (D89): only explicit native context reads are exposed; the native receiver never reaches the interpreter.
func (l *Loaded) nativeContext(value vm.Value) (*invocationToken, *extension.Context, error) {
	token, ok := value.(*invocationToken)
	if !ok || token == nil || token.owner != l {
		return nil, nil, errors.New("invalid let-go context handle")
	}
	if err := l.registrations.activeError(); err != nil {
		return nil, nil, err
	}
	native := extension.FromContext(token.ctx)
	if native == nil {
		return nil, nil, errors.New("let-go context has no native runtime binding")
	}
	if _, err := native.CWD(); err != nil {
		return nil, nil, err
	}
	return token, native, nil
}

type signalToken struct {
	owner   *Loaded
	context *invocationToken
	signal  context.Context
}

func (*signalToken) String() string     { return "#<pig.signal>" }
func (*signalToken) Type() vm.ValueType { return vm.AnyType }
func (*signalToken) Unbox() any         { return nil }

// Deref makes @signal report cancellation, like Kmet's abort atom; it reads only the native context state.
func (t *signalToken) Deref() vm.Value { return vm.Boolean(t.signal.Err() != nil) }

func (l *Loaded) signalCancelled(value vm.Value) (vm.Value, error) {
	token, ok := value.(*signalToken)
	if !ok || token == nil || token.owner != l {
		return vm.NIL, errors.New("invalid let-go signal handle")
	}
	if _, _, err := l.nativeContext(token.context); err != nil {
		return vm.NIL, err
	}
	return vm.Boolean(token.signal.Err() != nil), nil
}

type sessionReader interface {
	Entries() []codingagent.SessionEntry
	GetBranch() []codingagent.SessionEntry
}

func (l *Loaded) contextRead(operation string, value vm.Value) (vm.Value, error) {
	token, native, err := l.nativeContext(value)
	if err != nil {
		return vm.NIL, l.phaseError("context "+operation, err)
	}
	var data any
	switch operation {
	case "cwd":
		data, err = native.CWD()
	case "mode":
		data, err = native.Mode()
	case "has-ui?":
		data, err = native.HasUI()
	case "is-idle?":
		data, err = native.IsIdle()
	case "request-cancelled?":
		data = token.ctx.Err() != nil
	case "signal":
		signal, signalErr := native.Signal()
		if signalErr != nil {
			err = signalErr
			break
		}
		if signal == nil {
			l.latestSignal = nil
			return vm.NIL, nil
		}
		if l.latestSignal == nil || l.latestSignal.signal != signal {
			l.latestSignal = &signalToken{owner: l, context: token, signal: signal}
		}
		return l.latestSignal, nil
	case "get-active-tools":
		data = native.GetActiveTools()
	case "get-all-tools":
		data = native.GetAllTools()
	case "model":
		var model extension.Model
		model, err = native.Model()
		if err != nil {
			break
		}
		switch model := model.(type) {
		case nil:
			data = nil
		case ai.Model:
			data = extension.ModelInfo(&model)
		case *ai.Model:
			data = extension.ModelInfo(model)
		case ai.AnyModel:
			data = extension.AnyModelInfo(model)
		default:
			err = fmt.Errorf("unsupported native model %T", model)
		}
	case "get-entries", "get-branch":
		var manager extension.SessionManager
		manager, err = native.SessionManager()
		if err != nil {
			break
		}
		reader, ok := manager.(sessionReader)
		if !ok {
			err = fmt.Errorf("native session manager %T does not provide entries and branch reads", manager)
			break
		}
		if operation == "get-entries" {
			data = reader.Entries()
		} else {
			data = reader.GetBranch()
		}
	default:
		err = fmt.Errorf("unsupported context operation %q", operation)
	}
	if err != nil {
		return vm.NIL, l.phaseError("context "+operation, err)
	}
	return publicValue(data)
}

// contextSnapshots are the callback context's scalar keys, read once when the callback enters the VM.
var contextSnapshots = map[string]string{"cwd": "cwd", "mode": "mode", "has-ui": "has-ui?", "model": "model"}

// contextFunctions are the callback context's live keys. Kmet's spelling drops the ? and ! of the pig.context wrappers.
var contextFunctions = map[string]string{
	"is-idle": "is-idle?", "request-cancelled": "request-cancelled?", "signal": "signal",
	"get-active-tools": "get-active-tools", "get-all-tools": "get-all-tools", "get-entries": "get-entries", "get-branch": "get-branch",
	"notify": "notify!", "select": "select!", "confirm": "confirm!", "input": "input!",
}

// pig additive (D89): callbacks receive a persistent map of scalar snapshots and host-backed closures, the shape of Kmet's ctx.
// Only the closures reach PiG. Each validates the captured handle through nativeContext on every call, so a retained closure keeps
// working while the runner is live and fails with the native stale error after replacement, as a captured Pi ctx does.
// An unbound callback, such as one run through LoadForTest, gets nil snapshots and closures that fail as unbound.
func (l *Loaded) contextValue(token *invocationToken) (vm.Value, error) {
	ctx := vm.EmptyPersistentMap
	for key, operation := range contextFunctions {
		ctx = ctx.Assoc(vm.Keyword(key), &contextFunction{token: token, operation: operation}).(*vm.PersistentMap)
	}
	bound := extension.FromContext(token.ctx) != nil
	for key, operation := range contextSnapshots {
		var value vm.Value = vm.NIL
		if bound {
			var err error
			if value, err = l.contextRead(operation, token); err != nil {
				return vm.NIL, err
			}
		}
		ctx = ctx.Assoc(vm.Keyword(key), value).(*vm.PersistentMap)
	}
	return ctx, nil
}

// contextFunction is one host-backed closure in a callback context map.
type contextFunction struct {
	token     *invocationToken
	operation string
}

func (f *contextFunction) String() string   { return "#<pig.context " + f.operation + ">" }
func (*contextFunction) Type() vm.ValueType { return vm.AnyType }
func (*contextFunction) Unbox() any         { return nil }
func (*contextFunction) Arity() int         { return -1 }

func (f *contextFunction) Invoke(args []vm.Value) (vm.Value, error) {
	owner := f.token.owner
	if strings.HasSuffix(f.operation, "!") {
		return owner.uiCall(f.operation, append([]vm.Value{f.token}, args...)...)
	}
	if len(args) != 0 {
		return vm.NIL, owner.phaseError("context "+f.operation, fmt.Errorf("expected no arguments, got %d", len(args)))
	}
	return owner.contextRead(f.operation, f.token)
}

//go:embed clj/pig/context.cljc
var contextFacade string

// installContext runs only inside the generation coordinator during initial installation.
// The pig.context wrappers over the map are Clojure source; signal-cancelled? stays native because it takes a signal, not a context.
func (l *Loaded) installContext() error {
	function, err := vm.NativeFnType.Box(l.signalCancelled)
	if err != nil {
		return err
	}
	rt.NS("pig.context").Def("signal-cancelled?", function)
	return nil
}
