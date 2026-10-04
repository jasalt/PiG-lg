package letgo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/coding/extension"
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
			_, err = owner.invokeEvent(ctx, callback, value)
			if err != nil {
				return owner.phaseError("event "+name, err)
			}
			return nil
		})
	}}
}

// pig additive (D89): the exact lifecycle subset delegates to typed native registrations and awaited generation-owned callbacks invoked as (event ctx).
var lifecycleEvents = []eventAdapter{
	lifecycleAdapter("session-start", (*registrationBuilder).OnSessionStart),
	lifecycleAdapter("session-shutdown", (*registrationBuilder).OnSessionShutdown),
	lifecycleAdapter("agent-start", (*registrationBuilder).OnAgentStart),
	lifecycleAdapter("agent-end", (*registrationBuilder).OnAgentEnd),
	lifecycleAdapter("agent-settled", (*registrationBuilder).OnAgentSettled),
}

// resultEvents name the events whose handlers return results; they register through dedicated api functions.
var resultEvents = map[string]string{"before-agent-start": "on-before-agent-start", "tool-call": "on-tool-call", "tool-result": "on-tool-result"}

// pig additive (D89): Kmet's one-argument result hooks. The handler receives the read-only public event map (no ctx) and returns
// a replacement map or nil. Results decode through the native typed chain, which keeps native ordering and error semantics.
func (l *Loaded) resultHook(name string, register func(*Loaded, vm.Fn) error) func(vm.Value) (vm.Value, error) {
	return func(handler vm.Value) (vm.Value, error) {
		if _, err := l.callbackContext(); err != nil {
			return vm.NIL, err
		}
		callback, ok := handler.(vm.Fn)
		if !ok {
			return vm.NIL, l.phaseError("register "+name, fmt.Errorf("handler must be a function, got %T", handler))
		}
		if err := register(l, callback); err != nil {
			return vm.NIL, l.phaseError("register "+name, err)
		}
		return vm.NIL, nil
	}
}

// hookResult decodes a returned replacement map into its native result type. Keys that name no native field fail with their path.
func hookResult[R any](owner *Loaded, event string, value vm.Value, unsupported map[string]string) (R, error) {
	var result R
	if value == vm.NIL {
		return result, nil
	}
	switch value.(type) {
	case vm.Map, *vm.PersistentMap:
	default:
		return result, owner.phaseError("event "+event, fmt.Errorf("$: result must be a map or nil, got %T", value))
	}
	if _, err := registrationFields(value, reflect.TypeFor[R](), unsupported); err != nil {
		return result, owner.phaseError("event "+event, err)
	}
	if err := decodePublicValue(value, &result); err != nil {
		return result, owner.phaseError("event "+event, err)
	}
	return result, nil
}

var unsupportedToolCallResultKeys = map[string]string{"args": "rewriting tool call input is not supported; return :block to stop the call"}

func registerResultHook[E, R any](event string, register func(*registrationBuilder, func(context.Context, E) (R, error)) error, unsupported map[string]string, present func(context.Context, E) (any, error)) func(*Loaded, vm.Fn) error {
	return func(owner *Loaded, callback vm.Fn) error {
		return register(owner.registrations, func(ctx context.Context, native E) (R, error) {
			var zero R
			value, err := present(ctx, native)
			if err != nil {
				return zero, owner.phaseError("event "+event, err)
			}
			public, err := publicValue(value)
			if err != nil {
				return zero, owner.phaseError("event "+event, err)
			}
			returned, err := owner.invokeEventOnly(ctx, callback, public)
			if err != nil {
				return zero, owner.phaseError("event "+event, err)
			}
			return hookResult[R](owner, event, returned, unsupported)
		})
	}
}

// beforeAgentStartPublic presents the event without the snapshot's two losses: selectedTools keeps its raw wire value, and
// the authored-order section object becomes an ordered vector of {:name :value} maps because a map has no order.
//
// pig divergence (D90): the handler sees a read-only snapshot; in-place edits of the shared options are not exposed to let-go.
func beforeAgentStartPublic(ctx context.Context, event extension.BeforeAgentStartEvent) (any, error) {
	data, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	var plain map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&plain); err != nil {
		return nil, err
	}
	options, _ := plain["systemPromptOptions"].(map[string]any)
	if options == nil {
		return plain, nil
	}
	if raw := extension.BeforeAgentStartSelectedTools(ctx); raw != nil {
		var selected any
		rawDecoder := json.NewDecoder(bytes.NewReader(raw))
		rawDecoder.UseNumber()
		if err := rawDecoder.Decode(&selected); err != nil {
			return nil, err
		}
		options["selectedTools"] = selected
	}
	sections := []any{}
	if event.SystemPromptOptions.Sections != nil {
		for _, section := range *event.SystemPromptOptions.Sections {
			var value any
			if section.Value != nil {
				value = *section.Value
			}
			sections = append(sections, map[string]any{"name": section.Name, "value": value})
		}
	}
	options["sections"] = sections
	return plain, nil
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
	if wrapper, ok := resultEvents[string(name)]; ok {
		return vm.NIL, l.phaseError("register event", fmt.Errorf("event %s returns a result; register it with %s", name, wrapper))
	}
	return vm.NIL, l.phaseError("register event", fmt.Errorf("unsupported event %s", name))
}

// resultHooks are the api functions that register the result-bearing events.
func (l *Loaded) resultHooks() map[string]func(vm.Value) (vm.Value, error) {
	return map[string]func(vm.Value) (vm.Value, error){
		"on-before-agent-start": l.resultHook("before-agent-start", registerResultHook("before-agent-start", (*registrationBuilder).OnBeforeAgentStart, nil, beforeAgentStartPublic)),
		"on-tool-call": l.resultHook("tool-call", registerResultHook("tool-call", (*registrationBuilder).OnToolCall, unsupportedToolCallResultKeys,
			func(_ context.Context, event extension.ToolCallEvent) (any, error) { return event, nil })),
		"on-tool-result": l.resultHook("tool-result", registerResultHook("tool-result", (*registrationBuilder).OnToolResult, nil,
			func(_ context.Context, event extension.ToolResultEvent) (any, error) { return event, nil })),
	}
}
