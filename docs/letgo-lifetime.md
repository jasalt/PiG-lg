# let-go generation lifetime and costs (D89)

This page records what the tests prove about the ownership of a let-go generation and what the benchmarks measure. It makes no claim beyond them: it is not a latency bound, and interpreted CPU work is not preempted.

## Ownership

A generation owns its interpreter namespaces, its helper namespaces, its registrations and its optional `shutdown`. The loader takes a generation through four steps: evaluate, `init`, bind, close. `Close` removes the namespaces, runs `shutdown` at most once and drops the interpreter state. A reload loads a new generation from clean state and retires the old one only after its runner is stale, so a callback still running finishes first.

One piece of process-wide interpreter state is not owned by a generation: the pinned interpreter keeps a table from parsed form to source location, `vm.FormSource`, that never evicts and pins each form (`UPSTREAM-ISSUES.md` LG-4). The host resets it at the end of every serialized VM entry, so a generation no longer leaves entries behind.

## What the tests prove

Run `go test -race ./coding/extension/host/letgo ./cmd/pig -run 'Lifetime|LetGoOwner|LetGoReload'`.

| Test | Proves |
| --- | --- |
| `TestLifetimeRepeatedGenerationsLeaveNoOwnedState` | 40 generations, each loaded, dispatched (command and event), invalidated and closed twice, leave the form-source table and namespace registry exactly at their baseline after every cycle. They add no goroutine started by the interpreter or the host and no open file descriptor. `shutdown` runs once per generation. |
| `TestLifetimeHeapDoesNotGrowWithGenerations` | The live heap after garbage collection stays flat over 300 more generations (3,027 KiB after 50 and 3,031 KiB after 350 on the reference run). With the form-source reset disabled it grew from 7,132 KiB to 32,240 KiB. |
| `TestLifetimeHelperNamespacesRetireWithTheirGeneration` | A `.cljc` helper namespace required by the entry is gone with its generation. |
| `TestLifetimeCancelledQueueEntriesNeverEnterOrLeakTheGate` | While a callback holds the interpreter, entries queued behind it with a context cancelled while waiting return `context.Canceled` without running, and the gate is intact afterwards. |
| `TestLifetimeNativeWorkDoesNotWaitForTheVM` | A runner with only native extensions dispatches while an interpreted callback holds the interpreter. |
| `TestLetGoOwnerRepeatedReloadsRetireEveryGenerationExactlyOnce` | 25 staged reloads with a callback overlapping each swap retire all 26 generations, each `shutdown` exactly once, and add no form source or namespace past warm-up. |
| `TestLetGoOwnerRetiresTheOldGenerationAfterItsRunningCallbackFinishes` | An old callback held open across the swap finishes, its `shutdown` waits for it, and a call to the retired generation then fails with `ErrClosed`. |
| `TestLetGoReloadPublishesFreshGenerationsAndRetiresTheOldOnes`, `TestLetGoReloadInInteractiveMode` | The production reload paths replace generations, reset atoms and retire the old ones. |

The first two fail when `releaseFormSources` is disabled. No test retries or lengthens a timeout to hide a failure. The one sleep is the 100 ms pause in the drain test that gives a wrongly eager `shutdown` time to run before the test asserts it has not; the callback it waits behind holds the interpreter, so a correct build cannot run `shutdown` during it.

## Limits

Interpreter entry is serialized process-wide. A reload cannot stage while a let-go callback is held open, and pure interpreted CPU work is cooperative, not preempted. `shutdown` cannot be bounded either. Native work that takes no interpreter entry is not delayed, as the test above shows.

## Costs

Reproduce with:

```bash
go test ./coding/extension/host/letgo -run '^$' -bench . -benchmem -benchtime=200x \
  -cpuprofile "$TMPDIR/letgo-cpu.prof" -memprofile "$TMPDIR/letgo-mem.prof"
```

One run on Linux/amd64, Go 1.27.1, AMD Ryzen 7 PRO 7840U, 200 iterations each:

| Benchmark | Time per op | Allocated per op | Allocations per op |
| --- | --- | --- | --- |
| `BenchmarkGenerationReload` (load, evaluate, `init`, close) | 0.82 ms | 478 KB | 7,652 |
| `BenchmarkLoadToolLifecycle` (load, one tool call, close) | 0.71 ms | 427 KB | 6,624 |
| `BenchmarkToolDispatch` (one tool call through the runner) | 28 µs | 20 KB | 250 |
| `BenchmarkEventDispatch` (one `session_start` with the context map) | 15 µs | 19 KB | 225 |
| `BenchmarkUICommandDispatch` | 33 µs | 20 KB | 242 |
| `BenchmarkGenerationInvoke` (one retained function call) | 5.4 µs | 7.3 KB | 20 |
| `BenchmarkContextLargeSessionRead` (10,000 session entries) | 61 ms | 50.6 MB | 1,239,811 |

The CPU profile is dominated by garbage collection and allocation. The allocation profile's largest sources are the interpreter's persistent-map construction and the JSON encode and decode around the value boundary. A reload costs under a millisecond for a representative source. A startup that evaluates many sources pays that cost once for each. These are single-machine measurements, not a guarantee.
