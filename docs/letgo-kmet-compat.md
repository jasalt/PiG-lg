# let-go and Kmet source compatibility (D89)

A portable `.cljc` extension runs on let-go in PiG and on Kmet when it stays inside the common subset. The goal is source portability for that subset, not identical behavior. Kmet compatibility is bounded: PiG matches Kmet call shapes only where Pi semantics are preserved.

## The gate

`test/fixtures/extensions/letgo/kmet-compat/` holds six fixtures: a tool, a command, a `session-start` event handler, a `before-agent-start` transform, `tool-call` and `tool-result` hooks, and an entry that requires a pure helper namespace. Each requires the host with `#?(:lg [pig.extension :as ext] :default [kmet.extension :as ext])`. Everything after that line is identical on both hosts. `expected.json` states each fixture's outcome once.

- PiG side: `go test ./coding/extension/host/letgo -run KmetCompat` loads each fixture through the real loader and `inproc.Runner`, drives it through the runner's emit and command paths, and compares the outcome with `expected.json`.
- Kmet side: `test/fixtures/extensions/letgo/kmet-compat/run-kmet.sh <kmet-agent checkout>` runs the same fixtures with Babashka against Kmet's `create-nullable-api` and compares with the same file. Kmet is not vendored, and the script is not part of CI or the Go test run.

Recorded runs of the harness, all six fixtures passing: Kmet `4963cc7` (the commit the plan cites) and Kmet `f4601d5b` (2026-10-04). This proves the common subset above, not that the two hosts behave alike elsewhere. It does not run Kmet's real runtime, so differences in event dispatch, command trimming and rendering are listed below rather than tested.

## Known differences

| Area | Kmet | PiG let-go | Source |
| --- | --- | --- | --- |
| Registration return value | `register-tool!` and the other registration calls return a deregistration function | They return nil. The native API has no unregister | decision 5A |
| `unregister-*`, `get-commands`, `get-all-tools` on `api` | Present | Absent. The context map has `:get-all-tools` and `:get-active-tools` | scope of D89 |
| Event keys | kebab-case keywords | kebab-case keywords mapped from native JSON names. Values stay native, so `(:type event)` is `"session_start"` and `(:reason event)` is the string `"startup"`. Compare with `(name ...)`, not a keyword | decision 1A |
| Event set | Many events | Only `:session-start`, `:session-shutdown`, `:agent-start`, `:agent-end` and `:agent-settled` through `on-event` | D89 |
| Result hooks | `on-before-agent-start`, `on-tool-call`, `on-tool-result` with one-argument handlers | The same wrappers and shape. The event maps hold PiG's native fields. `tool_result` events have `:content`, `:details`, `:is-error`, not Kmet's `:result`. `tool_call` events have `:input`, not `:args` | decision 4A |
| `tool-call` result | `{:block ...}` or `{:args transformed}` | `:block`, `:reason`, `:terminate`. `:args` is rejected with an error, since PiG has no input-rewrite result | native result type |
| `before-agent-start` options | Not documented in `extension.md` | `:system-prompt-options` is a read-only snapshot with `:selected-tools` as the raw value and `:sections` as an ordered vector of `{:name :value}`. A handler cannot edit the shared options | D90 |
| Tool `:execute` | `(fn [args])`, or `(fn [args on-update signal ctx])` with `:contextual? true` | The same. There is no `:streams?` form | decision 2A |
| Tool result `:content` | A string | A string is accepted and coerced to one text block on input. A tool-result event carries native content blocks | decision 3A |
| Kmet-only tool keys | `:params`, `:prepare-arguments`, `:title`, `:streams?`, `:render-call`, `:render-result` | Rejected with the key's field path | decision 2A |
| Command metadata | `:argument-hint`, `:get-argument-completions` | `:get-argument-completions` is supported. `:argument-hint` is rejected because native commands have no such field | native `CommandOptions` |
| Command arguments | The handler's `args` is the trimmed argument string | The exact native argument string, untrimmed | native dispatch |
| Context map | Includes `:abort`, `:wait-for-idle`, `:reload`, `:compact`, session control | Snapshots `:cwd :mode :has-ui :model` and the live reads and simple UI functions in [let-go context reads](letgo-context.md). No abort, wait-for-idle, reload or compact | decision 7B |
| Registration key spelling | kebab-case | kebab-case or the native camelCase spelling of each top-level field. Both together are rejected | PiG-18s.40 |
| Notification level | keyword | keyword or string | context map |
| Map member order | Clojure maps | Clojure maps. Native objects whose order matters are presented as vectors | [let-go value boundary](letgo-values.md) |

Rows describing Kmet come from `src/kmet/extension.md` and `src/kmet/extension.clj` and were checked against the nullable api only where the harness exercises them.
