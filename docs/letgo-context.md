# let-go context reads (D89)

The interpreted callback receives an opaque `pig.context` handle. It is not a Go receiver or a copied Session. Each operation checks handle ownership, generation registration lifetime, and the native context's stale-runner guard. A retained handle remains usable after the callback returns while the native context remains valid. A retired generation or replaced native runner rejects it.

## Supported reads

Each function takes the callback handle.

| Function | Native source | Result |
| --- | --- | --- |
| `cwd` | `Context.CWD` | string |
| `mode` | `Context.Mode` | native mode string |
| `model` | `Context.Model`, `ModelInfo` / `AnyModelInfo` | public model snapshot or nil |
| `has-ui?` | `Context.HasUI` | boolean |
| `is-idle?` | `Context.IsIdle` | boolean |
| `get-active-tools` | `Context.GetActiveTools` | native list snapshot |
| `get-all-tools` | `Context.GetAllTools` | native metadata snapshot |
| `get-entries` | `Context.SessionManager`, native `Entries` | ordered entry snapshots |
| `get-branch` | `Context.SessionManager`, native `GetBranch` | ordered current branch snapshots |
| `request-cancelled?` | callback request context | boolean |
| `signal` | `Context.Signal` | opaque active-run signal or nil while idle |

`signal-cancelled?` takes a signal returned by `signal`. Request cancellation does not fabricate a run abort. Run abort does not fabricate request cancellation. Repeated reads of the same run signal reuse its opaque handle. Retained prior-run signal handles still observe their original run's abort. The adapter retains only the latest signal cache; authored references own any older handles.

Native getters are called on each operation. Mutable model/tool data is not cached to fabricate live state. Conversion produces snapshots and retains native JSON field spelling. A snapshot is not a mutation channel into the Session. Unknown model representations and session managers without the native read methods fail explicitly. No width/height, raw Session, runner, terminal, or UI receiver is exposed. Simple UI actions and lifecycle event adapters are separate capabilities.

## Execution and ownership

The native caller waits for callback completion. All VM entry is coordinated. Reads execute on the callback worker, not the TUI input/render loop. The later startup/event integration must preserve that execution rule. Queued VM cancellation and pure interpreted CPU work retain the coordinator's documented cooperative semantics.

A 10,000-entry native-session read benchmark drives interpreted command dispatch through `inproc.Runner`. On Linux/amd64 with Go 1.27.1, one measurement was approximately 58 ms, 48.9 MB allocated, and 1.08 million allocations per read. The allocation profile identifies persistent-map construction, JSON encoding/decoding, and recursive conversion as cost centers. This is proportional snapshot work, not a bounded-latency claim or a reason to run history reads on the input loop. Global interpreter source-form retention remains a separate unresolved reload-lifetime obligation; see `PiG-18s.21`.

Run the context regressions and benchmark with:

```bash
go test -race ./coding/extension/host/letgo -run Context -count=10
go test ./coding/extension/host/letgo -run '^$' -bench BenchmarkContextLargeSessionRead -benchmem
```
