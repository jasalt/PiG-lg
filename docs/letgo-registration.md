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

`init` can call `(pig.extension/register-command! api {:name "name" :description "Description" :handler (fn [ctx args] ...)})`, Kmet's command map. The handler receives the callback context map described in [let-go context reads](letgo-context.md) and the exact native argument string. An optional `:get-argument-completions (fn [prefix] ...)` returns a vector of `{:value ... :label ... :description ...}` maps and becomes the native `GetArgumentCompletions`; like the subprocess host's request, it runs without a cancellable context. A missing or empty `:name` fails registration. Kmet's `:argument-hint` has no native command field and fails registration with its field path, as does any key that native `CommandOptions` does not decode. The runner resolves conflicts and reports interpreted handler errors through its normal error listeners. Re-registering a command during loading replaces it without moving its first-registration position. Source metadata remains native registration metadata.

The native command call waits for interpreted execution to finish. No detached work is started. Cancellation while waiting for VM entry returns the original context error. Pure interpreted CPU work remains cooperative. Closing the generation drains VM entry and prevents later calls. Binary-level tests prove dispatch through the CLI in JSON and RPC modes; see Startup.

Source loading compiles and evaluates forms sequentially under the generation gate. The coordinator captures/restores the current namespace in addition to namespace registries and loaders. A strict reader pass rejects incomplete forms before the pinned multi-form compiler runs; see `UPSTREAM-ISSUES.md` LG-3 for the measured defect and its reproducible example.

## Interpreted tools

`(pig.extension/register-tool! api {:name "name" :parameters {...} :execute (fn [args] ...)})` registers a tool whose `:execute` receives only the tool arguments, Kmet's default. With `:contextual? true`, `:execute` receives `(fn [args on-update signal ctx])` instead. The loader never inspects arity. `on-update` takes a partial tool result and forwards it to the native `AgentToolUpdateCallback`; it fails once the tool call has returned. `signal` is the call's cancellation handle: `@signal` reports whether the call's context is cancelled, like Kmet's abort atom, and `pig.context/signal-cancelled?` accepts it. `ctx` is the call's context map. Tool argument keys stay exactly as the model produced them.

A tool result may give `:content` as a string. It is coerced to one text block on input only; native results keep their content blocks. Tool results use the public key-casing rule, so `:is-error` and `:structured-content` decode as their native fields while `:details` and the structured content value keep their keys. The remaining registration keys must name native `ToolDefinition` JSON fields. Each field accepts its native spelling (`:executionMode`) or Kmet's kebab-case one (`:execution-mode`); giving both fails with both spellings named. Only top-level keys are mapped, so `:parameters` schema bodies and every other nested value keep their keys byte-for-byte. A missing or empty `:name` fails registration. Kmet-only keys fail registration with their field path rather than being ignored: `:params` (use `:parameters` with a JSON schema), `:prepare-arguments`, `:title`, `:streams?`, `:render-call` and `:render-result`. Other unknown keys fail as unsupported registration keys. Commands follow the same key rule.

`register-tool!` and `register-command!` return nil. Kmet's registration calls return deregistration functions; PiG's native API has no unregister, so this is a documented Kmet divergence.

## Interpreted lifecycle events

`(pig.extension/on-event api :session-start (fn [event c] ...))` registers an awaited native lifecycle handler. Every event handler receives the event data first and the callback context second, matching Kmet; command handlers take `(ctx args)` and tools take the shapes above. The exact public keywords are `:session-start`, `:session-shutdown`, `:agent-start`, `:agent-end`, and `:agent-settled`. Event data uses the public key-casing rule from [let-go value boundary](letgo-values.md): native field names become kebab-case keywords such as `:previous-session-file`, while values, including the underscores in `(:type event)`, are unchanged. Unknown, namespaced, string-valued, and unsupported event names fail registration.

The event table delegates to the corresponding typed `On<Event>` method. Handler order, dispatch snapshots, and error reporting belong to the native runner. A subscription registered from a callback applies to the next dispatch snapshot, not the dispatch in progress. Handler return values are ignored only for these native no-result events. The result-bearing events `:before-agent-start`, `:tool-call` and `:tool-result` are not in this table, and `on-event` rejects them with a message naming their wrapper. Keep the generation alive through `session_shutdown` dispatch, then invalidate and close it.

## Interpreted result hooks

`(pig.extension/on-before-agent-start api (fn [event] ...))`, `on-tool-call` and `on-tool-result` register Kmet's one-argument hooks. The handler receives the public event map with kebab-case keys and no context, so it cannot reach live PiG state. It returns a replacement map or nil. A nil or empty map leaves the result unchanged.

The returned map decodes into the native typed result: `before_agent_start` takes `:system-prompt` and `:message {:custom-type :content :display :details}`, `tool_call` takes `:block`, `:reason` and `:terminate`, and `tool_result` takes `:content`, `:details`, `:is-error`, `:structured-content` and `:usage`. Keys follow the public key-casing rule, so `:isError` also decodes. A key that names no native field fails with its field path. Kmet's `:args` for `tool_call` is rejected with a message, because PiG has no input-rewrite result. A non-map result and a type mismatch fail the same way. The native runner then orders, chains and error-handles results exactly as for every other extension: `before_agent_start` and `tool_result` record a failing handler and continue, `tool_call` stops at the first block and returns a handler error, and later `tool_result` handlers see earlier replacements. The three wrappers return nil.

The `before_agent_start` event is read-only (D90 in `docs/parity/DIVERGENCES.md`). `:selected-tools` under `:system-prompt-options` is the raw wire value, so a non-string entry that an earlier handler set stays visible. `:sections` is a vector of `{:name :value}` maps in authored order, since a map has no order; a section whose native value is null has a nil `:value`. A let-go handler cannot edit the shared options, and nothing it does to its snapshot persists. The event's `:input`, `:details` and `:structured-content` values keep their keys, but a map loses the authored member order.

The tests run through `inproc.Runner` emit methods and check blocking, replacement, chaining and error effects. They do not prove Session integration or CLI loading.

## Startup

A selected `.lg` or `.cljc` source (an exact file, or a directory whose entry is `extension.lg` or `extension.cljc`) loads on normal CLI startup through the same discovery, trust, ordering and override policy as every other extension source: `-e`, project, user and settings entries, Packages and Piglets, and `--no-extensions`, which still loads explicit `-e` paths. The interpreted source never enters the subprocess host and needs no `lg` executable.

- **Trust.** A source evaluates only after project trust is resolved. The pre-trust pass skips interpreted sources, because let-go has no `project_trust` hook and evaluating Clojure in process is more dangerous than starting a subprocess. An untrusted project source never reaches the interpreter. User-scope sources are trusted and load after the trust decision.
- **Order and conflicts.** Each interpreted extension sits at its discovery position among the subprocess extensions. The subprocess host's own plan order is unchanged. Tool and flag conflicts are detected in load order exactly as for native extensions, because the conflict check reads the extension's runtime tool registry. A conflict is reported as `Failed to load extension "<path>": Tool "<name>" conflicts with <path>` with the `-ne` hint and exit status 1.
- **Failures.** A source whose evaluation or `init` throws is reported as `Failed to load extension` with the cause and exit status 1. Its optional `shutdown` does not run, because `init` did not complete. A source that loads but whose startup then fails still has its generation retired.
- **Lifetime.** The build owns every generation it loaded. The startup set closes them on every exit path, and a Session's retirement closes them through the same `Release` that shuts the subprocess host, so `shutdown` runs once on normal exit. After the Session binds its actions, each generation binds to the live runner, so a callback-time tool registration refreshes the Session's tool set and a replaced runner rejects it.
- **Output.** Output the interpreted code prints is discarded. Standard output can carry a mode's protocol and the terminal belongs to the TUI.

Reload and Session replacement do not yet retire and recreate generations; that is `PiG-18s.17`. The subprocess host rejects interpreted sources, so a `/reload` is expected to report them as errors and leave them out of the reloaded runner. That path is untested.

`cmd/pig/letgo_startup_test.go` runs the built binary in a hermetic home: a `.lg` tool and shutdown in JSON mode with output kept off the stream, a `.cljc` directory entry with a helper namespace, a command listed and dispatched in RPC mode, a failing `init`, a tool conflict, an untrusted project source that never evaluates beside the same source evaluating once trusted, and `--no-extensions` with and without `-e`.

## Integration evidence

`Load` evaluates the selected source, calls `init`, registers interpreted callbacks through the builder, and publishes the native `extension.Extension`. The loader fixture tests invoke retained tools through the native runner's registered definitions after loading returns. They verify replacement order, source metadata, typed results, errors, cancellation, close, and callback-time registration through the shared registry. Binary-level tests cover CLI startup, described above. Staged reload and Session replacement remain `PiG-18s.17`. Full extension-tree regression remains a required acceptance gate.

Run the focused tests with:

```bash
go test -race ./coding/extension/host/letgo -run Registration -count=10
```
