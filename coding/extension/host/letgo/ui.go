package letgo

import (
	"fmt"

	"github.com/nooga/let-go/pkg/rt"
	"github.com/nooga/let-go/pkg/vm"
)

// pig additive (D89): selected sources call the bound native UI; dialogs are awaited under the invocation's cancellation and reentry marker.
func (l *Loaded) uiCall(operation string, args ...vm.Value) (vm.Value, error) {
	if len(args) < 2 || len(args) > 3 || (operation == "confirm!" && len(args) != 3) {
		return vm.NIL, l.phaseError("UI "+operation, fmt.Errorf("expected context, title/message and optional argument"))
	}
	token, native, err := l.nativeContext(args[0])
	if err != nil {
		return vm.NIL, l.phaseError("UI "+operation, err)
	}
	ui, err := native.UI()
	if err != nil {
		return vm.NIL, l.phaseError("UI "+operation, err)
	}
	var title string
	if err := decodeValue(args[1], &title); err != nil {
		return vm.NIL, l.phaseError("UI "+operation, err)
	}
	ctx := l.generation.CallbackContext(token.ctx)
	var result any
	if operation == "select!" {
		if len(args) != 3 {
			return vm.NIL, l.phaseError("UI "+operation, fmt.Errorf("select requires an options vector"))
		}
		var options []string
		if err := decodeValue(args[2], &options); err != nil {
			return vm.NIL, l.phaseError("UI "+operation, err)
		}
		result, err = ui.Select(ctx, title, options, nil)
	} else {
		argument := ""
		if operation == "notify!" {
			argument = "info"
		}
		if len(args) == 3 {
			if err := decodeValue(args[2], &argument); err != nil {
				return vm.NIL, l.phaseError("UI "+operation, err)
			}
		}
		switch operation {
		case "notify!":
			ui.Notify(title, argument)
		case "confirm!":
			result, err = ui.Confirm(ctx, title, argument, nil)
		case "input!":
			result, err = ui.Input(ctx, title, argument, nil)
		default:
			err = fmt.Errorf("unsupported UI operation %q", operation)
		}
	}
	if err != nil {
		return vm.NIL, l.phaseError("UI "+operation, err)
	}
	return publicValue(result)
}

func (l *Loaded) installUI() error {
	namespace := rt.NS("pig.context")
	for _, operation := range []string{"notify!", "select!", "confirm!", "input!"} {
		function, err := vm.NativeFnType.Box(func(args ...vm.Value) (vm.Value, error) { return l.uiCall(operation, args...) })
		if err != nil {
			return err
		}
		namespace.Def(operation, function)
	}
	return nil
}
