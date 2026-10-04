# let-go native registration construction (D89)

The owner selected a concrete typed registration builder for the approved let-go subset. The builder is internal adapter plumbing. It is not an implementation of `extension.API`, a new SDK, or a privileged scripting API. Unsupported methods remain absent. It does not expose a `pig.internal.*` namespace.

`coding/extension/host/letgo/registration.go` constructs native registrations for tools, commands, `session_start`, `session_shutdown`, `agent_start`, `agent_end`, `agent_settled`, `before_agent_start`, `tool_call`, and `tool_result`. The typed methods retain native payload and result types. Go errors carry registration failures that the interpreted adapter must surface as exceptions. A zero result becomes nil; explicit empty strings and false pointers remain present.

The builder initializes native event and tool registries before it publishes an `extension.Extension`. It attaches source metadata when it registers each tool and command. Native registries preserve first-registration order, replacement, shared late-tool updates, and event dispatch snapshots. Commands register during loading and become immutable at publication. This builder does not provide callback-time command registration.

The loader must bind the live native runner guard and Session tool-refresh action after publication. A late tool registration changes the shared registry before refresh. A refresh error reaches the caller and does not undo the registration, as in Pi. The builder releases its lock before refresh so host work does not run under a registration lock. A stale runner or closed builder rejects subsequent registrations and handler entry. Generation ownership still drains interpreted callbacks; closing the builder alone does not drain them.

The existing MCP construction path remains unchanged. No MCP-specific notifications or cached event contexts are used by this builder.

## Integration evidence

`Load` evaluates the selected source, registers interpreted callbacks through the builder, and publishes the native `extension.Extension`. The loader fixture tests invoke retained tools through the native runner's registered definitions after loading returns. They verify replacement order, source metadata, typed results, errors, cancellation, close, and callback-time registration through the shared registry. These tests do not prove CLI integration. Runtime source routing, trust, reload, and Session retirement remain separate obligations. Full extension-tree regression remains a required acceptance gate.

Run the focused tests with:

```bash
go test -race ./coding/extension/host/letgo -run Registration -count=10
```
