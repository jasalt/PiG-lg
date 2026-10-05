# Plan: port Codex usage to portable let-go source

## Status and scope

This document records a source-based architecture assessment. It does not approve an expansion of PiG's let-go API or claim a working port. Tracking issue: `PiG-p1f`.

Port the [Kmet extension](https://github.com/jasalt/kmet-extensions/tree/ab36bf230cbec7119177bba717e511dd9b4c0ccc/codex-usage) into `../../pig-lg-extensions/codex-usage/` as `.cljc` source. Use the existing [PiG Go extension](https://github.com/jasalt/pig-extensions/tree/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage) as the PiG behavior and lifecycle reference. Use Kmet as the source-portability reference. The two implementations are not behaviorally identical.

No extension files or host changes have been made for this port. No build, test suite, live account request, or reset redemption was run during the assessment. Existing test claims below belong to the referenced projects and have not been independently revalidated.

## Existing implementations

### Kmet

The [Kmet implementation](https://github.com/jasalt/kmet-extensions/blob/ab36bf230cbec7119177bba717e511dd9b4c0ccc/codex-usage/src/codex_usage/core.clj) combines domain logic, host integration, transport, presentation, and polling in one namespace.

It depends on `kmet.extension`, `kmet.libs.http`, `kmet.libs.json`, `kmet.libs.crypto`, `kmet.libs.concurrent`, `kmet.tui.theme`, `clojure.core.async`, and JVM date APIs. These dependencies are not portable merely because the entry file changes to `.cljc`.

It resolves the selected model's authentication through `get-api-key-and-headers`. It does not read credentials directly from disk. It refreshes at session start, model selection, agent settlement, and every five minutes. Generation checks suppress stale responses. Shutdown invalidates work and closes the polling stop channel.

See the [Kmet README](https://github.com/jasalt/kmet-extensions/blob/ab36bf230cbec7119177bba717e511dd9b4c0ccc/codex-usage/README.md), [tests](https://github.com/jasalt/kmet-extensions/blob/ab36bf230cbec7119177bba717e511dd9b4c0ccc/codex-usage/test/codex_usage/core_test.clj), and [host smoke script](https://github.com/jasalt/kmet-extensions/blob/ab36bf230cbec7119177bba717e511dd9b4c0ccc/codex-usage/scripts/smoke.bb).

### PiG Go factory

The Go implementation already separates the proposed architectural layers:

| Layer | Source | Responsibility |
| --- | --- | --- |
| Domain and presentation | [`core.go`](https://github.com/jasalt/pig-extensions/blob/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage/core.go) | Window validation, percentages, cards, credits, date formatting, and reset-result interpretation |
| Host adapter | [`extension.go`](https://github.com/jasalt/pig-extensions/blob/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage/extension.go) | SDK context adaptation, command registration, lifecycle events, status, and notifications |
| Transport | [`http.go`](https://github.com/jasalt/pig-extensions/blob/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage/http.go) | Connection resolution, JWT decoding, HTTP, redirect policy, redaction, and redemption IDs |
| Lifetime ownership | [`owner.go`](https://github.com/jasalt/pig-extensions/blob/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage/owner.go) | Polling, cancellation, model/session epochs, refresh generations, publication guards, and shutdown |

`handlerView` adapts SDK context operations into a small `view` of functions. The owner receives connection resolution, session/model identity, cancellation, status, and notification functions instead of the entire SDK.

This is a Go SDK factory, normally hosted in a subprocess. It is not evidence that the extension runs in-process merely because its implementation language is Go. PiG let-go executes in-process under a serialized interpreter coordinator.

See the [Go README](https://github.com/jasalt/pig-extensions/blob/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage/README.md) for its qualification scope and commands. It explicitly excludes interactive reload and packed-placement qualification from its local completion claims.

## Proposed source structure

```text
codex-usage/
  extension.cljc
  codex_usage/
    core.cljc
    pig.cljc
    kmet.cljc
```

Keep validation, quota calculations, credit/reset interpretation, and presentation data in `core.cljc`. Keep host calls and runtime dependencies in the adapters. Select the adapter with reader conditionals at the entry boundary, such as `#?(:lg ... :default ...)` for the intended PiG and Kmet hosts.

Do not place JVM classes or `kmet.libs.*` dependencies in the shared core. Treat local-time formatting as an injected service or adapter operation. Share test fixtures and observable rules rather than forcing both hosts to expose identical APIs.

The PiG entry uses `init` and optional `shutdown`. Kmet discovery and packaging remain a separate host concern; the proposed directory layout alone does not prove Kmet installation compatibility.

See [Writing let-go extensions](../docs/letgo-extensions.md), [Kmet compatibility](../docs/letgo-kmet-compat.md), and [portable reader behavior](../docs/letgo-cljc.md). Some older documentation contains stale startup statements; the [registration document](../docs/letgo-registration.md#startup) records normal CLI startup integration.

## Missing PiG capabilities

The existing Go adapter establishes concrete SDK callers for most required host behavior. Extend let-go by binding existing native extension semantics, not by introducing a Codex-specific host service or a fifth SDK.

| Requirement | Existing Go caller | Current let-go assessment |
| --- | --- | --- |
| Selected model | `GetModelInfo` | Context already supplies a model snapshot |
| Resolved credentials and headers | `GetModelAuth` | Not exposed by the documented let-go subset |
| Resolved model metadata | `ModelRegistry().Find` | Audit the model snapshot and auth result before adding another lookup API |
| Keyed footer status | `SetStatus` | Not exposed |
| Theme styling | `UITheme().Fg` | Not exposed by the documented simple UI subset |
| Explicit output | `Notify` | Already supported through callback context |
| Model-selection refresh | `EventModelSelect` | Not in the supported let-go lifecycle event set |
| Session/model identity | `GetSessionID`, `ModelQualified` | Model snapshot exists; the complete identity/publication contract needs a binding audit |
| Lifetime and cancellation | `Done`, `Err`, plus extension-owned contexts | Request/run cancellation exists, but background generation ownership needs an explicit design |

The SDK names above describe existing Go callers, not approved Clojure API signatures. Audit the underlying native extension API, upstream Pi semantics, and protocol before selecting binding names and return shapes.

The relevant let-go contract is documented in [context reads](../docs/letgo-context.md), [registration](../docs/letgo-registration.md), and [the supported subset](../docs/letgo-extensions.md#supported-and-not-supported). The current public registration wrappers are in [`pig/extension.cljc`](../coding/extension/host/letgo/clj/pig/extension.cljc).

Persistent conversation output is not required to match the Go extension. The Go implementation uses notifications. Kmet's `ui-chat-info` output is a separate presentation difference that needs an explicit decision if Kmet behavior is the target.

## Transport and interpreter ownership

Go obtains HTTP, JSON, base64, UUID generation, clocks, and concurrency from its runtime and standard library. These are not all PiG host API gaps. First audit the pinned interpreter's usable facilities. Add narrow runtime libraries only where needed, separately from extension-host bindings.

The Go owner maintains an extension-lifetime cancellation tree, a model/session epoch, and a refresh generation. It cancels superseded refreshes, checks live identity before publication, owns its ticker and HTTP transport, and joins work at shutdown. Completed request contexts do not become the lifetime of subsequent polling requests.

The current let-go coordinator serializes interpreter entry process-wide. Pure interpreted CPU work is cooperative, not preempted. Exposing `future`, unrestricted goroutines, or Kmet-style `spawn` does not establish safe ownership.

A proposed asynchronous mechanism must establish these contracts:

- Network waits do not hold the interpreter execution lock.
- Interpreted callbacks enter through the existing coordinator.
- Each task belongs to one loaded generation.
- Reload and shutdown cancel owned operations and drain callbacks.
- Retired generations cannot publish status or notifications.
- Superseded responses cannot replace newer results.
- Error propagation and cancellation have explicit owners.
- Interpreter reentry cannot deadlock shutdown or callback completion.

These are design requirements, not guarantees of the current implementation. Use the Go owner as a behavioral reference, not as code to translate mechanically. See [let-go lifetime](../docs/letgo-lifetime.md) and [context ownership](../docs/letgo-context.md#execution-and-ownership).

## Behavior choices before implementation

| Area | Kmet source | PiG Go source |
| --- | --- | --- |
| Native originator | `pi` | `pig`, citing PiG D26 |
| Explicit command output | Interactive chat-info; notification fallback | Notifications |
| Adapter base URL | Selected model base URL | Auth-resolved base URL, then model metadata |
| Cross-origin redirects | Delegated to Kmet HTTP library; not assessed here | Explicitly strips request headers when origin changes |
| Credential redaction | No explicit extension-level scrubber | Scrubs raw and encoded secrets from errors and displayed data |
| Percentage/date semantics | Clojure/JVM formatting | Explicit JavaScript rounding and date handling, with a documented limited locale scope |
| Reset result validation | Kmet implementation rules | Explicit truthiness/type handling and recognized-result validation |
| Background work | Kmet concurrency and core.async | Go contexts, ticker, wait groups, and publication guards |

Do not silently choose one implementation's behavior for every shared fixture. Record which rules are shared and which belong in host adapters. Preserve the no-direct-auth-file-read rule. Resolve credentials through the host so refresh, configured headers, and errors retain host semantics.

Reset redemption is an account mutation. Keep exact-ID activation and do not add automatic retries. Cancellation or a transport error does not prove that the server did not redeem the credit. Use dummy credentials and local endpoints for development.

## Core proposal and fork maintenance

PiG currently approves a narrow interpreted source realization under [D89](../docs/additive-features.md#d89-approved-let-go-source-realization). Expanding it requires explicit scope approval. Follow [extension authoring](../docs/extension-authoring.md) and [extension API parity](../docs/extension-api-parity.md).

Keep the core changes product-neutral. Codex endpoint URLs, JWT claim names, polling intervals, reset policy, and presentation belong in the external extension.

Separate the work into independently reviewable changes:

1. Bind existing auth, status/theme, identity, and event capabilities that have concrete callers.
2. Supply missing generic transport/data facilities after auditing the interpreter.
3. Establish owned asynchronous execution and generation retirement semantics.
4. Implement the external extension and its shared fixtures.

Keep one current API shape. Do not add negotiation or compatibility readers merely to support a private fork. A missing required capability must fail clearly rather than fabricate data or silently disable essential behavior. Do not duplicate credential management or modify Codex provider internals to serve this extension.

Implement command-driven usage and resets before event refresh and periodic polling. This sequence reduces speculative infrastructure; it does not declare a reduced implementation equivalent to the original.

## Verification requirements

Use shared input/output fixtures for empty, ordinary, boundary, and malformed usage/credit/reset payloads. Include weekly-window selection, percentage rounding, expiry ordering, and invalid reset-result types.

Use host-specific tests for resolved OAuth and adapter authentication, configured headers, local-time presentation, notification/status behavior, model/session replacement, overlapping refreshes, cancellation, disconnect, reload, and shutdown. Assert that stale work cannot publish and that owned resources are released.

Compare new let-go bindings with the native reference through the [extension conformance suite](../test/extension-conformance/letgo_conformance_test.go). Add real CLI startup and reload evidence; registration-only validation does not prove runtime behavior. Measure latency, allocations, backpressure, and retained resources for the asynchronous path.

Existing Go evidence sources include [core tests](https://github.com/jasalt/pig-extensions/blob/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage/core_test.go), [HTTP tests](https://github.com/jasalt/pig-extensions/blob/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage/http_test.go), [owner tests](https://github.com/jasalt/pig-extensions/blob/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage/owner_test.go), [metadata race tests](https://github.com/jasalt/pig-extensions/blob/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage/metadata_race_test.go), and [reset type tests](https://github.com/jasalt/pig-extensions/blob/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage/reset_types_test.go). Their existence is not a claim that they passed in this assessment.

## Provenance

Both extensions identify the original Pi extension as [`contrib/pi-codex-usage.ts` at commit `94f45568b4bd7842b1aef362cc3ba883b1312951`](https://github.com/jasalt/chatgpt-openai-api-adapter/blob/94f45568b4bd7842b1aef362cc3ba883b1312951/contrib/pi-codex-usage.ts). Preserve the original attribution and [MIT license](https://github.com/jasalt/pig-extensions/blob/a62386badad7b9550ad99dda237e2104e3c56edb/extensions/codex-usage/LICENSE) when adapting code.

Source references use public GitHub permalinks pinned to Kmet commit `ab36bf230cbec7119177bba717e511dd9b4c0ccc` and PiG extension commit `a62386badad7b9550ad99dda237e2104e3c56edb`. All 15 referenced files were retrieved from those commits and matched the assessed local files byte-for-byte. The local PiG extension checkout had a different commit ID; the public snapshot preserves the referenced content. The proposed output directory remains a local workspace path.
