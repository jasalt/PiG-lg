# Writing let-go extensions (D89)

A let-go extension is Clojure source that Pig interprets in its own process. There is no SDK, toolchain or build step, no manifest, and no `lg` executable. The source is trusted code: it runs with Pig's privileges and is not sandboxed. It evaluates only from a source you selected, and a project source never evaluates before you trust the project.

This page describes what is implemented and tested. It is not a claim of parity with the Go, Rust, Python or Node SDKs, and it gives an extension no access to Pig internals.

## Workflow

```bash
pig extension init ./hello --lang let-go              # portable .cljc: extension.cljc plus hello/core.cljc
pig install ./hello --validate-only --json            # load it for real and report what init registered
pig -e ./hello                                        # load it into a session and use its tool and command
# edit extension.cljc or hello/core.cljc, then run /reload in the session
```

`--lg` scaffolds a single let-go-specific `extension.lg` instead. Use it only for source that will never leave Pig. Validation evaluates the source and calls `init`, then runs the optional `shutdown` once; it dispatches no event, tool or command. A reload loads the edited source as a fresh generation, so atoms and closures start from scratch and a removed tool or command is gone. See [registration](letgo-registration.md) for the exact reload order and [lifetime](letgo-lifetime.md) for what the tests prove about retirement.

## Source forms

| Selected | Entry |
| --- | --- |
| a file ending in `.lg` or `.cljc` | that file |
| a directory | `extension.lg` or `extension.cljc` in it. A directory with both is ambiguous and needs an exact file. |

Arbitrary `.clj` is not an entry. The entry may require helper namespaces from the same directory, as `.lg`, `.cljc` or `.clj` files; the load order is `.lg`, then `.cljc`, then `.clj`. The entry's namespace is whatever its `ns` form declares and must define `(defn init [api] ...)`. It may define `(defn shutdown [api] ...)`. The extension name is the directory or file name.

## The api map and namespaces

`init` receives a plain map of capabilities. The public namespaces are `pig.extension`, whose functions take `api` first, and `pig.context`. No `pig.internal.*` namespace exists, and the Go functions behind the map are reachable only through it. Top-level forms have no `api` and cannot register.

| Function | Shape |
| --- | --- |
| `register-tool!` | `{:name ... :description ... :parameters {JSON schema} :execute (fn [args])}`. `:contextual? true` gives `(fn [args on-update signal ctx])`; there is no arity inspection. A string `:content` in a result is one text block. |
| `register-command!` | `{:name ... :description ... :handler (fn [ctx args]) :get-argument-completions (fn [prefix])}`. `args` is the exact argument string. |
| `on-event` | `(fn [event ctx])` for `:session-start`, `:session-shutdown`, `:agent-start`, `:agent-end`, `:agent-settled`. |
| `on-before-agent-start`, `on-tool-call`, `on-tool-result` | `(fn [event])` returning a replacement map or nil. |

Registration functions return nil. Host-originated data uses kebab-case keyword keys (`:tool-call-id`, `:is-error`); tool arguments and opaque values such as `:details` and schemas keep their keys exactly. Registration maps accept the kebab-case or the native spelling of each field. The callback context is a map of snapshots (`:cwd`, `:mode`, `:has-ui`, `:model`) and host-backed functions; see [context](letgo-context.md) and [values](letgo-values.md).

## Supported and not supported

Tiers follow the Kmet compatibility plan. Tier A is the portable common subset that `.cljc` can share. Tier B has the same concept but needs a host adapter. Tier C is host-specific.

| Tier | Implemented | Not in v1 |
| --- | --- | --- |
| A | extension identity, tool and command registration, the five lifecycle events, before-agent-start, tool-call and tool-result hooks, reading all and active tools through the context | flags, shortcuts, setting active tools, send message, send user message, exec |
| B | native context reads, simple UI notify, select, confirm and input, callback-time tool registration | provider and model registration, session tree queries, renderers, resource registration |
| C | none | abort, wait-for-idle, reload, compact and session control on the context; Kmet `kmet.tui.*`; JVM or Babashka libraries; a privileged `pig.host`; compiled let-go Piglet binaries |

Also unsupported, and rejected with an error where it would otherwise be ignored: Kmet's `:params`, `:prepare-arguments`, `:title`, `:streams?`, `:render-call`, `:render-result`, `:argument-hint`, and `tool_call` input rewriting through `:args`. A `before_agent_start` handler cannot edit the shared system prompt options (D90). Kmet's registration calls return deregistration functions; Pig's return nil.

## Portable source

Keep host calls in a thin adapter and logic in a pure core that any host can load:

```text
my-extension/
  extension.cljc      adapter: the one reader conditional, init, shutdown
  my_extension/
    core.cljc         pure logic, no host API
```

```clojure
(ns my-extension.extension
  (:require #?(:lg [pig.extension :as ext]
               :default [kmet.extension :as ext])
            [my-extension.core :as core]))
```

Use `#?(:lg ...)` only where a host genuinely differs, such as a UI call at the edge. [let-go and Kmet source compatibility](letgo-kmet-compat.md) lists the call shapes both hosts share, a gate that runs six fixtures on both, and every known difference. Source portability is not library portability: a Kmet-only library does not load on Pig.

## Execution model

Every call into interpreted code is serialized process-wide. A tool, command or event handler blocks its native caller until the interpreted function returns, and no interpreter work runs detached. Cancellation is cooperative: a call waiting for the interpreter returns the context error without entering, and a running call is not preempted, so a long pure computation holds every other interpreted callback. Interpreted code can crash, exhaust memory or block the process. Output it prints is discarded, because standard output can carry a mode's protocol.

State resets with the generation. A reload, a Session replacement and a restart each start from clean interpreter state, and the old generation runs its `shutdown` once after its last callback finishes. Keep anything that must survive a reload in files, not in atoms.

## Errors

Every failure names the source path and a phase: eval, init, register, callback or shutdown. A malformed value names its field, such as `$.params`. [Registration](letgo-registration.md#diagnostics-and-validation) lists where each surfaces, including `pig install --validate-only`.

## Conformance

`test/extension-conformance/letgo_conformance_test.go` runs the supported rows through the real loader and the in-process runner and compares each observation with the native reference, the in-process Go fixture or a native handler, so a row cannot pass on a value the interpreter produces by accident. Run `go test ./test/extension-conformance -run LetGoConformance`.

| Row | Production path | Reference |
| --- | --- | --- |
| tool success, thrown error, structured error | `ToolDefinition.Execute` | the in-process Go fixture's `echo`, `tool_error`, `tool_is_error` |
| streamed partial results | `:contextual?` `on-update` to the runner's update callback | the fixture's `update_tool` |
| command, immediate error | `RegisteredCommand.Handler` | `ping`, `command_error` |
| select, input, confirm, notify | the runner's bound UI | the recording UI's answers |
| awaited dialog, cancellation | the native caller waits; a cancelled request returns `context.Canceled` and is not reported as an extension error | the runner's own rule for a handler that returns its context's error |
| context reads | the context map over `extension.Context` | the native context's mode, cwd, UI and idle reads |
| `session_start`, `session_shutdown` | `Runner.Emit` | the event payload |
| dynamic registration | shared tool registry plus the bound `RefreshTools` | the registry and the refresh count |
| `tool_call`, `tool_result`, `before_agent_start` | `EmitToolCall`, `EmitToolResult`, `EmitBeforeAgentStart` | a native handler registered for the same event |
| reload | `cmd/pig` reload tests through the production paths | not a conformance row |

The `api` map is the capability inventory, and a test pins its keys. The rows the subset does not implement are flags, shortcuts, message, entry and tool renderers, the markdown transformer, providers and models, login and sprite definitions, status, widgets, header, footer and editor, editor and custom dialogs, send message and send user message, session name and entries, terminal input, project trust, `agent_before_settle`, context abort, wait-for-idle, reload and compact, and `prepareArguments`. No row of another SDK is weakened or skipped, and this is not a claim of parity with them. Each row fails under a compiling mutation of let-go's value conversion or dispatch: leaving a result's `:is-error` key unmapped fails the structured-error row, swapping a command's argument order fails the command, dialog and context rows, and not re-casing host event keys fails the hook rows.
