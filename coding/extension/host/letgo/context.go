package letgo

import (
	"context"
	"errors"
	"fmt"

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

// installContext runs only inside the generation coordinator during initial installation.
func (l *Loaded) installContext() error {
	namespace := rt.NS("pig.context")
	for _, operation := range []string{"cwd", "mode", "model", "has-ui?", "is-idle?", "request-cancelled?", "signal", "get-active-tools", "get-all-tools", "get-entries", "get-branch"} {
		function, err := vm.NativeFnType.Box(func(value vm.Value) (vm.Value, error) { return l.contextRead(operation, value) })
		if err != nil {
			return err
		}
		namespace.Def(operation, function)
	}
	function, err := vm.NativeFnType.Box(l.signalCancelled)
	if err != nil {
		return err
	}
	namespace.Def("signal-cancelled?", function)
	return nil
}
