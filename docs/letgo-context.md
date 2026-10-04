# let-go context reads (D89)

Every callback receives its context as a plain persistent map, the shape of Kmet's ctx: `(fn [ctx args] ...)` for commands, `(fn [event ctx] ...)` for events and the last argument of a `:contextual? true` tool. The map holds no Go receiver and no copied Session. `:cwd`, `:mode`, `:has-ui` and `:model` are snapshots taken when the callback starts, so plain Clojure reads them with `(:cwd ctx)`. Every other key holds a host-backed function that reads or acts on the native context when called. A scalar that must be live is a function, not a copy.

Each function validates the handle it captured on every call: ownership, generation registration lifetime and the native context's stale-runner guard. Like a captured Pi ctx, a retained function remains usable after its callback returns while the native context remains valid. A retired generation or a replaced runner rejects it with the native stale error. A callback that runs without a native binding, as under `LoadForTest`, gets nil snapshots and functions that fail as unbound. Every function enters PiG through the generation's callback context, so a host call that synchronously re-enters the interpreter fails with `ErrReentrant`, and none holds a PiG lock while it waits.

## Context map keys

Keys use the kebab-case spelling decided on `PiG-18s.25`. Snapshot keys hold values. Function keys are called with the arguments shown.

| Key | Native source | Result |
| --- | --- | --- |
| `:cwd` | `Context.CWD` | string snapshot |
| `:mode` | `Context.Mode` | native mode string snapshot |
| `:has-ui` | `Context.HasUI` | boolean snapshot |
| `:model` | `Context.Model`, `ModelInfo` / `AnyModelInfo` | public model snapshot or nil |
| `:is-idle` | `Context.IsIdle` | `(fn [])` live boolean |
| `:get-active-tools` | `Context.GetActiveTools` | `(fn [])` native list snapshot |
| `:get-all-tools` | `Context.GetAllTools` | `(fn [])` native metadata snapshot |
| `:get-entries` | `Context.SessionManager`, native `Entries` | `(fn [])` ordered entry snapshots |
| `:get-branch` | `Context.SessionManager`, native `GetBranch` | `(fn [])` ordered current branch snapshots |
| `:request-cancelled` | callback request context | `(fn [])` boolean |
| `:signal` | `Context.Signal` | `(fn [])` opaque active-run signal or nil while idle |
| `:notify`, `:select`, `:confirm`, `:input` | native UI binding | see Simple UI actions |

The `pig.context` namespace keeps thin wrappers over the map: `(cwd ctx)`, `(mode ctx)`, `(model ctx)`, `(has-ui? ctx)`, `(is-idle? ctx)`, `(request-cancelled? ctx)`, `(signal ctx)`, `(get-active-tools ctx)`, `(get-all-tools ctx)`, `(get-entries ctx)`, `(get-branch ctx)` and the UI wrappers below. For example `(ctx/notify! ctx msg :info)` is `((:notify ctx) msg :info)`. `signal-cancelled?` is the one native function, because it takes a signal. The wrappers are source in `coding/extension/host/letgo/clj/pig/context.cljc`, not a privileged namespace. There is no `pig.internal.*` namespace. Per decision 7B the map adds no abort, wait-for-idle, reload, compact or session-control action.

`signal-cancelled?` takes a signal returned by `signal` or passed to a contextual tool. `@signal` reports the same state. Request cancellation does not fabricate a run abort. Run abort does not fabricate request cancellation. Repeated reads of the same run signal reuse its opaque handle. Retained prior-run signal handles still observe their original run's abort. The adapter retains only the latest signal cache; authored references own any older handles.

Native getters are called on each operation. Mutable model/tool data is not cached to fabricate live state. Conversion produces snapshots. Native field names become kebab-case keywords under the public key-casing rule in [let-go value boundary](letgo-values.md), so a model snapshot has `:context-window` and an entry has `:parent-id`; opaque values such as tool `:parameters`, model `:headers` and custom entry `:data` keep their keys. A snapshot is not a mutation channel into the Session. Unknown model representations and session managers without the native read methods fail explicitly. No width/height, raw Session, runner, terminal, or UI receiver is exposed. Simple UI actions and lifecycle event adapters are separate capabilities.

## Simple UI actions

The map holds `(:notify ctx)`, `(:select ctx)`, `(:confirm ctx)` and `(:input ctx)`, wrapped by `pig.context` as `(notify! ctx message [kind])`, `(select! ctx title options)`, `(confirm! ctx title message)`, and `(input! ctx title [placeholder])`. Square brackets denote optional positional arguments, not vector syntax. `options` is a collection of strings. Notification kind is a string or, as in Kmet, a keyword such as `:warning`, and defaults to `"info"`; input placeholder defaults to the empty string. This subset does not expose dialog options, custom renderers, or a native UI receiver.

Each function validates the context and delegates to the current native UI binding. Dialog calls wait for the native response and propagate its error. The request context includes the coordinator's reentry marker, so a host that synchronously calls back into the interpreter receives `ErrReentrant` instead of deadlocking. Native no-UI bindings retain native results: select/input return the empty string and confirm returns false. The bridge does not fabricate another mode-specific fallback. An explicitly bound RPC or TUI UI remains the host's implementation.

Callback-time `(pig.extension/register-tool! api tool)` updates the shared native registry. After `Loaded.Bind` connects the builder to a live native context, registration also invokes the native tool-refresh action. A refresh failure reaches the callback but does not undo the registered tool. Replacement keeps the native first-registration order. Stale bindings reject registration.

## Execution and ownership

The native caller waits for callback completion. All VM entry is coordinated. Reads execute on the callback worker, not the TUI input/render loop. The later startup/event integration must preserve that execution rule. Queued VM cancellation and pure interpreted CPU work retain the coordinator's documented cooperative semantics.

A 10,000-entry native-session read benchmark drives interpreted command dispatch through `inproc.Runner`. On Linux/amd64 with Go 1.27.1, one measurement was approximately 58 ms, 48.9 MB allocated, and 1.08 million allocations per read. The allocation profile identifies persistent-map construction, JSON encoding/decoding, and recursive conversion as cost centers. This is proportional snapshot work, not a bounded-latency claim or a reason to run history reads on the input loop. Global interpreter source-form retention remains a separate unresolved reload-lifetime obligation; see `PiG-18s.21`.

Run the context regressions and benchmark with:

```bash
go test -race ./coding/extension/host/letgo -run Context -count=10
go test ./coding/extension/host/letgo -run '^$' -bench BenchmarkContextLargeSessionRead -benchmem
```
