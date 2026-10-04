package letgo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"sync"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// pig additive (D89): this concrete builder constructs only the approved let-go registrations, not extension.API.
// Native registries own order, replacement and dispatch snapshots. Commands are load-time registrations; tools and handlers may register while active.
type registrationBuilder struct {
	mu           sync.Mutex
	ext          extension.Extension
	nextID       int
	published    bool
	closed       bool
	assertActive func() error
	refreshTools func() error
}

func newRegistrationBuilder(identity extension.Extension) *registrationBuilder {
	identity.Commands = maps.Clone(identity.Commands)
	if identity.Commands == nil {
		identity.Commands = make(map[string]extension.RegisteredCommand)
	}
	identity.InitializeEventHandlers()
	identity.InitializeToolRegistry()
	return &registrationBuilder{ext: identity}
}

func (b *registrationBuilder) checkActive() error {
	if b.closed {
		return errors.New("let-go registrations are closed")
	}
	if b.assertActive != nil {
		return b.assertActive()
	}
	return nil
}

// snapshot freezes command construction and shares the initialized runtime registries with the runner.
func (b *registrationBuilder) snapshot() (extension.Extension, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkActive(); err != nil {
		return extension.Extension{}, err
	}
	b.published = true
	result := b.ext
	result.Commands = maps.Clone(b.ext.Commands)
	result.CommandOrder = append([]string(nil), b.ext.CommandOrder...)
	return result, nil
}

// bind installs the live runner guard and Session refresh action after native runner binding.
func (b *registrationBuilder) bind(assertActive, refreshTools func() error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkActive(); err != nil {
		return err
	}
	if !b.published {
		return errors.New("let-go registrations must be published before binding")
	}
	if assertActive == nil || refreshTools == nil {
		return errors.New("let-go registrations require an active guard and tool refresh action")
	}
	if b.assertActive != nil {
		return errors.New("let-go registrations are already bound")
	}
	b.assertActive, b.refreshTools = assertActive, refreshTools
	return nil
}

func (b *registrationBuilder) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.assertActive, b.refreshTools = nil, nil
}

// RegisterTool returns native registration failures rather than throwing across Go frames.
// The tool remains registered if refresh fails, as in Pi's registerTool.
func (b *registrationBuilder) RegisterTool(tool extension.ToolDefinition) error {
	b.mu.Lock()
	if err := b.checkActive(); err != nil {
		b.mu.Unlock()
		return err
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil || schema == nil {
		b.mu.Unlock()
		return fmt.Errorf("Tool %q registered by extension %q must define an object parameter schema", tool.Name, b.ext.Path)
	}
	b.ext.SetRegisteredTool(extension.RegisteredTool{Definition: tool, SourceInfo: b.ext.SourceInfo})
	refresh := b.refreshTools
	b.mu.Unlock()
	if refresh != nil {
		return refresh()
	}
	return nil
}

func (b *registrationBuilder) RegisterCommand(name string, options extension.CommandOptions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkActive(); err != nil {
		return err
	}
	if b.published {
		return errors.New("let-go commands must be registered during loading")
	}
	if name == "" {
		return fmt.Errorf("Command registered by extension %q must have a non-empty string name", b.ext.Path)
	}
	if options.Handler == nil {
		return fmt.Errorf("Command /%s registered by extension %q must define handler()", name, b.ext.Path)
	}
	if _, exists := b.ext.Commands[name]; !exists {
		b.ext.CommandOrder = append(b.ext.CommandOrder, name)
	}
	b.ext.Commands[name] = extension.RegisteredCommand{Name: name, Description: options.Description, GetArgumentCompletions: options.GetArgumentCompletions, Handler: options.Handler, SourceInfo: b.ext.SourceInfo}
	return nil
}

func subscribeRegistration[E any](b *registrationBuilder, event string, call func(context.Context, E) (any, error)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkActive(); err != nil {
		return err
	}
	b.nextID++
	b.ext.AddEventHandler(event, b.nextID, func(args ...any) (any, error) {
		b.mu.Lock()
		err := b.checkActive()
		b.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if len(args) != 2 {
			return nil, fmt.Errorf("%s handler requires event and context, got %d arguments", event, len(args))
		}
		data, ok := args[0].(E)
		if !ok {
			return nil, fmt.Errorf("%s handler received event %T", event, args[0])
		}
		ctx, ok := args[1].(context.Context)
		if !ok || ctx == nil {
			return nil, fmt.Errorf("%s handler received context %T", event, args[1])
		}
		return call(ctx, data)
	})
	return nil
}

func registrationResult[R any](result R) any {
	if reflect.ValueOf(&result).Elem().IsZero() {
		return nil
	}
	return &result
}

func (b *registrationBuilder) OnSessionStart(handler func(context.Context, extension.SessionStartEvent) error) error {
	return subscribeRegistration(b, "session_start", func(ctx context.Context, event extension.SessionStartEvent) (any, error) {
		return nil, handler(ctx, event)
	})
}
func (b *registrationBuilder) OnSessionShutdown(handler func(context.Context, extension.SessionShutdownEvent) error) error {
	return subscribeRegistration(b, "session_shutdown", func(ctx context.Context, event extension.SessionShutdownEvent) (any, error) {
		return nil, handler(ctx, event)
	})
}
func (b *registrationBuilder) OnAgentStart(handler func(context.Context, extension.AgentStartEvent) error) error {
	return subscribeRegistration(b, "agent_start", func(ctx context.Context, event extension.AgentStartEvent) (any, error) {
		return nil, handler(ctx, event)
	})
}
func (b *registrationBuilder) OnAgentEnd(handler func(context.Context, extension.AgentEndEvent) error) error {
	return subscribeRegistration(b, "agent_end", func(ctx context.Context, event extension.AgentEndEvent) (any, error) { return nil, handler(ctx, event) })
}
func (b *registrationBuilder) OnAgentSettled(handler func(context.Context, extension.AgentSettledEvent) error) error {
	return subscribeRegistration(b, "agent_settled", func(ctx context.Context, event extension.AgentSettledEvent) (any, error) {
		return nil, handler(ctx, event)
	})
}
func (b *registrationBuilder) OnBeforeAgentStart(handler func(context.Context, extension.BeforeAgentStartEvent) (extension.BeforeAgentStartEventResult, error)) error {
	return subscribeRegistration(b, "before_agent_start", func(ctx context.Context, event extension.BeforeAgentStartEvent) (any, error) {
		result, err := handler(ctx, event)
		return registrationResult(result), err
	})
}
func (b *registrationBuilder) OnToolCall(handler func(context.Context, extension.ToolCallEvent) (extension.ToolCallEventResult, error)) error {
	return subscribeRegistration(b, "tool_call", func(ctx context.Context, event extension.ToolCallEvent) (any, error) {
		result, err := handler(ctx, event)
		return registrationResult(result), err
	})
}
func (b *registrationBuilder) OnToolResult(handler func(context.Context, extension.ToolResultEvent) (extension.ToolResultEventResult, error)) error {
	return subscribeRegistration(b, "tool_result", func(ctx context.Context, event extension.ToolResultEvent) (any, error) {
		result, err := handler(ctx, event)
		return registrationResult(result), err
	})
}
