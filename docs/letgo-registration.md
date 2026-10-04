# let-go native registration construction (D89)

The owner selected a concrete typed registration builder for the approved let-go subset. The builder is internal adapter plumbing. It is not an implementation of `extension.API`, a new SDK, or a privileged scripting API. Unsupported methods remain absent. It does not expose a `pig.internal.*` namespace.

`coding/extension/host/letgo/registration.go` constructs native registrations for tools, commands, `session_start`, `session_shutdown`, `agent_start`, `agent_end`, `agent_settled`, `before_agent_start`, `tool_call`, and `tool_result`. The typed methods retain native payload and result types. Go errors carry registration failures that the interpreted adapter must surface as exceptions. A zero result becomes nil; explicit empty strings and false pointers remain present.

The builder initializes native event and tool registries before it publishes an `extension.Extension`. It attaches source metadata when it registers each tool and command. Native registries preserve first-registration order, replacement, shared late-tool updates, and event dispatch snapshots. Commands register during loading and become immutable at publication. This builder does not provide callback-time command registration.

The loader must bind the live native runner guard and Session tool-refresh action after publication. A late tool registration changes the shared registry before refresh. A refresh error reaches the caller and does not undo the registration, as in Pi. The builder releases its lock before refresh so host work does not run under a registration lock. A stale runner or closed builder rejects subsequent registrations and handler entry. Generation ownership still drains interpreted callbacks; closing the builder alone does not drain them.

The existing MCP construction path remains unchanged. No MCP-specific notifications or cached event contexts are used by this builder.

## Activation

A selected source is a namespace that defines `(defn init [api] ...)` and may define `(defn shutdown [api] ...)`. `Load` installs the public `pig.extension` wrappers from the embedded `coding/extension/host/letgo/clj/pig/extension.cljc`. It then evaluates the source. Next it resolves `init` and `shutdown` in the namespace that is current after evaluation, normally the one declared by the source's `ns` form. Finally it calls `(init api)`. A source that declares no namespace, lacks `init`, or binds `init` or `shutdown` to a non-function fails loading. Registrations made by `init` stay in the unpublished builder. If `init` throws, `Load` closes the generation and publishes nothing.

`api` is a plain Clojure map. `:extension-name`, `:extension-path` and `:extension-dir` hold identity strings. `:register-tool!`, `:register-command!` and `:on-event` hold Go-backed functions. The wrappers `register-tool!`, `register-command!` and `on-event` take `api` as their first argument and call the corresponding map entry. Top-level forms have no `api` and cannot register. Callbacks that capture `api` may register tools after publication, as described below. No `pig.internal.*` namespace exists; the Go functions are reachable only through `api`.

`Loaded.Close` closes the registration builder first, then calls `(shutdown api)` with the same `api` map that `init` received. Shutdown runs at most once per generation. Registration attempts during shutdown fail. A thrown or panicking shutdown is joined into the `Close` error as a `shutdown` phase failure, but the generation still closes and later callbacks return `ErrClosed`. If `Close` is cancelled while waiting for VM entry, shutdown has not run, so a retry runs it. Shutdown is skipped when `init` failed, matching Kmet's rollback without shutdown. Shutdown is advisory cleanup for extension-owned resources. Host-owned deregistration does not depend on it. Reload and startup ordering of `Close` belongs to the reload and startup integration.

`LoadForTest` in `coding/extension/host/letgo/testapi.go` runs a source's `init` through the real loader without a runner or session. It returns the generation and the registered tool, command and event inventory in native order, similar to Kmet's nullable api. Callbacks that need a live native context fail as unbound. A Clojure-visible nullable api is not provided.

## Interpreted commands

`init` can call `(pig.extension/register-command! api {:name "name" :description "Description" :handler (fn [ctx args] ...)})`, Kmet's command map. The handler receives an opaque callback token and the exact native argument string. An optional `:get-argument-completions (fn [prefix] ...)` returns a vector of `{:value ... :label ... :description ...}` maps and becomes the native `GetArgumentCompletions`; like the subprocess host's request, it runs without a cancellable context. A missing or empty `:name` fails registration. Kmet's `:argument-hint` has no native command field and fails registration with its field path, as does any key that native `CommandOptions` does not decode. The runner resolves conflicts and reports interpreted handler errors through its normal error listeners. Re-registering a command during loading replaces it without moving its first-registration position. Source metadata remains native registration metadata.

The native command call waits for interpreted execution to finish. No detached work is started. Cancellation while waiting for VM entry returns the original context error. Pure interpreted CPU work remains cooperative. Closing the generation drains VM entry and prevents later calls. Normal CLI-mode dispatch requires startup integration; internal runner tests alone do not prove it.

Source loading compiles and evaluates forms sequentially under the generation gate. The coordinator captures/restores the current namespace in addition to namespace registries and loaders. A strict reader pass rejects incomplete forms before the pinned multi-form compiler runs; see `UPSTREAM-ISSUES.md` LG-3 for the measured defect and its reproducible example.

## Interpreted tools

`(pig.extension/register-tool! api {:name "name" :parameters {...} :execute (fn [args] ...)})` registers a tool whose `:execute` receives only the tool arguments, Kmet's default. With `:contextual? true`, `:execute` receives `(fn [args on-update signal ctx])` instead. The loader never inspects arity. `on-update` takes a partial tool result and forwards it to the native `AgentToolUpdateCallback`; it fails once the tool call has returned. `signal` is the call's cancellation handle: `@signal` reports whether the call's context is cancelled, like Kmet's abort atom, and `pig.context/signal-cancelled?` accepts it. `ctx` is the call's opaque callback token. Tool argument keys stay exactly as the model produced them.

A tool result may give `:content` as a string. It is coerced to one text block on input only; native results keep their content blocks. The remaining keys must be native `ToolDefinition` JSON fields. A missing or empty `:name` fails registration. Kmet-only keys fail registration with their field path rather than being ignored: `:params` (use `:parameters` with a JSON schema), `:prepare-arguments`, `:title`, `:streams?`, `:render-call` and `:render-result`. Other unknown keys, including Kmet's kebab-case spellings of native fields such as `:execution-mode`, fail as unsupported registration keys.

`register-tool!` and `register-command!` return nil. Kmet's registration calls return deregistration functions; PiG's native API has no unregister, so this is a documented Kmet divergence.

## Interpreted lifecycle events

`(pig.extension/on-event api :session-start (fn [event c] ...))` registers an awaited native lifecycle handler. Every event handler receives the event data first and the callback context second, matching Kmet; command handlers take `(ctx args)` and tools take the shapes above. The exact public keywords are `:session-start`, `:session-shutdown`, `:agent-start`, `:agent-end`, and `:agent-settled`. The event data retains native JSON field names and type strings, including underscores in `event.type`. Unknown, namespaced, string-valued, and unsupported event names fail registration.

The event table delegates to the corresponding typed `On<Event>` method. Handler order, dispatch snapshots, and error reporting belong to the native runner. A subscription registered from a callback applies to the next dispatch snapshot, not the dispatch in progress. Handler return values are ignored only for these native no-result events. Result-bearing events are not admitted by this table. Keep the generation alive through `session_shutdown` dispatch, then invalidate and close it.

## Integration evidence

`Load` evaluates the selected source, calls `init`, registers interpreted callbacks through the builder, and publishes the native `extension.Extension`. The loader fixture tests invoke retained tools through the native runner's registered definitions after loading returns. They verify replacement order, source metadata, typed results, errors, cancellation, close, and callback-time registration through the shared registry. These tests do not prove CLI integration. Runtime source routing, trust, reload, and Session retirement remain separate obligations. Full extension-tree regression remains a required acceptance gate.

Run the focused tests with:

```bash
go test -race ./coding/extension/host/letgo -run Registration -count=10
```
