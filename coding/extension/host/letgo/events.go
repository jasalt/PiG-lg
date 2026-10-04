package letgo

import (
	"context"
	"fmt"

	"github.com/nooga/let-go/pkg/vm"
)

type eventAdapter struct {
	name     string
	register func(*Loaded, vm.Fn) error
}

func lifecycleAdapter[E any](name string, register func(*registrationBuilder, func(context.Context, E) error) error) eventAdapter {
	return eventAdapter{name: name, register: func(owner *Loaded, callback vm.Fn) error {
		return register(owner.registrations, func(ctx context.Context, event E) error {
			value, err := publicValue(event)
			if err != nil {
				return owner.phaseError("event "+name, err)
			}
			_, err = owner.invoke(ctx, callback, value)
			if err != nil {
				return owner.phaseError("event "+name, err)
			}
			return nil
		})
	}}
}

// pig additive (D89): the exact lifecycle subset delegates to typed native registrations and awaited generation-owned callbacks.
var lifecycleEvents = []eventAdapter{
	lifecycleAdapter("session-start", (*registrationBuilder).OnSessionStart),
	lifecycleAdapter("session-shutdown", (*registrationBuilder).OnSessionShutdown),
	lifecycleAdapter("agent-start", (*registrationBuilder).OnAgentStart),
	lifecycleAdapter("agent-end", (*registrationBuilder).OnAgentEnd),
	lifecycleAdapter("agent-settled", (*registrationBuilder).OnAgentSettled),
}

func (l *Loaded) onEvent(event, handler vm.Value) (vm.Value, error) {
	if _, err := l.callbackContext(); err != nil {
		return vm.NIL, err
	}
	name, ok := event.(vm.Keyword)
	if !ok {
		return vm.NIL, l.phaseError("register event", fmt.Errorf("event must be an unqualified supported keyword, got %T", event))
	}
	callback, ok := handler.(vm.Fn)
	if !ok {
		return vm.NIL, l.phaseError("register event", fmt.Errorf("event %s requires a function, got %T", name, handler))
	}
	for _, adapter := range lifecycleEvents {
		if string(name) != adapter.name {
			continue
		}
		if err := adapter.register(l, callback); err != nil {
			return vm.NIL, l.phaseError("register event", err)
		}
		return vm.NIL, nil
	}
	return vm.NIL, l.phaseError("register event", fmt.Errorf("unsupported event %s", name))
}
