# let-go native registration construction (D89)

The owner selected a concrete typed registration builder for the approved let-go subset. The builder is internal adapter plumbing. It is not an implementation of `extension.API`, a new SDK, or a privileged scripting API. Unsupported methods remain absent. It does not expose a `pig.internal.*` namespace.

`coding/extension/host/letgo/registration.go` constructs native registrations for tools, commands, `session_start`, `session_shutdown`, `agent_start`, `agent_end`, `agent_settled`, `before_agent_start`, `tool_call`, and `tool_result`. The typed methods retain native payload and result types. Go errors carry registration failures that the interpreted adapter must surface as exceptions. A zero result becomes nil; explicit empty strings and false pointers remain present.

The builder initializes native event and tool registries before it publishes an `extension.Extension`. It attaches source metadata when it registers each tool and command. Native registries preserve first-registration order, replacement, shared late-tool updates, and event dispatch snapshots. Commands register during loading and become immutable at publication. This builder does not provide callback-time command registration.

The loader must bind the live native runner guard and Session tool-refresh action after publication. A late tool registration changes the shared registry before refresh. A refresh error reaches the caller and does not undo the registration, as in Pi. The builder releases its lock before refresh so host work does not run under a registration lock. A stale runner or closed builder rejects subsequent registrations and handler entry. Generation ownership still drains interpreted callbacks; closing the builder alone does not drain them.

The existing MCP construction path remains unchanged. No MCP-specific notifications or cached event contexts are used by this builder.

## Interpreted commands

The selected source can call `(pig.extension/register-command! "name" {:description "Description" :handler (fn [c args] ...)})`. The handler receives an opaque callback token and the exact native argument string. The runner resolves conflicts and reports interpreted handler errors through its normal error listeners. Re-registering a command during loading replaces it without moving its first-registration position. Source metadata remains native registration metadata.

The native command call waits for interpreted execution to finish. No detached work is started. Cancellation while waiting for VM entry returns the original context error. Pure interpreted CPU work remains cooperative. Closing the generation drains VM entry and prevents later calls. Normal CLI-mode dispatch requires startup integration; internal runner tests alone do not prove it.

Source loading compiles and evaluates forms sequentially under the generation gate. The coordinator captures/restores the current namespace in addition to namespace registries and loaders. A strict reader pass rejects incomplete forms before the pinned multi-form compiler runs; see `UPSTREAM-ISSUES.md` LG-3 for the measured defect and its reproducible example.

## Interpreted lifecycle events

`(pig.extension/on! :session-start (fn [c event] ...))` registers an awaited native lifecycle handler. The exact public keywords are `:session-start`, `:session-shutdown`, `:agent-start`, `:agent-end`, and `:agent-settled`. The event data retains native JSON field names and type strings, including underscores in `event.type`. Unknown, namespaced, string-valued, and unsupported event names fail registration.

The event table delegates to the corresponding typed `On<Event>` method. Handler order, dispatch snapshots, and error reporting belong to the native runner. A subscription registered from a callback applies to the next dispatch snapshot, not the dispatch in progress. Handler return values are ignored only for these native no-result events. Result-bearing events are not admitted by this table. Keep the generation alive through `session_shutdown` dispatch, then invalidate and close it.

## Integration evidence

`Load` evaluates the selected source, registers interpreted callbacks through the builder, and publishes the native `extension.Extension`. The loader fixture tests invoke retained tools through the native runner's registered definitions after loading returns. They verify replacement order, source metadata, typed results, errors, cancellation, close, and callback-time registration through the shared registry. These tests do not prove CLI integration. Runtime source routing, trust, reload, and Session retirement remain separate obligations. Full extension-tree regression remains a required acceptance gate.

Run the focused tests with:

```bash
go test -race ./coding/extension/host/letgo -run Registration -count=10
```
