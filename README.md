<!--
SPDX-FileCopyrightText: Copyright Hewlett Packard Enterprise Development LP
SPDX-License-Identifier: MIT
-->

# PiG-lg

This experimental fork explores trusted, in-process Clojure extensions using [let-go](https://github.com/nooga/let-go), with `.cljc` source portability between PiG and Kmet. It is not a separate supported PiG release or a claim of full Kmet compatibility.

For the underlying agent, installation, and general documentation, read the **[original PiG README](https://github.com/MichaelKinsy/PiG/blob/main/README.md)**. PiG is a faithful Go implementation of [Pi](https://github.com/earendil-works/pi), the reference implementation ([Pi documentation](https://pi.dev/docs/latest)). Michael Kinsy created PiG, which was originally developed at Hewlett Packard Enterprise. This fork does not imply endorsement by PiG, Pi, or HPE.

## What this fork explores

- Load explicitly selected `.lg` and `.cljc` extensions without an extension compiler, SDK, or separate `lg` executable.
- Register supported capabilities through PiG's existing native extension runner.
- Share pure Clojure logic between PiG and Kmet, with reader conditionals at host boundaries.
- Determine which generic host bindings and runtime facilities real extensions need.

The binary remains `pig`. The interpreted source realization is recorded as [D89](docs/additive-features.md#d89-approved-let-go-source-realization). The experiments do not introduce a general plugin loader or privileged `pig.internal.*` scripting API.

## Current integration state

The following describes the implementation in this checkout, not the capabilities of an arbitrary installed PiG binary.

| Area | Current scope |
| --- | --- |
| Source selection | Exact `.lg` or `.cljc` files; directories containing `extension.lg` or `extension.cljc`. Select an exact file if both entries exist. |
| Startup | Normal extension discovery and explicit `-e`, subject to project trust. `--no-extensions` still permits explicitly selected `-e` sources. |
| Registration | Tools, commands, five lifecycle events, and before-agent-start/tool-call/tool-result hooks. |
| Context and UI | Model and working-directory snapshots, supported live reads, notifications, select, confirm, and input dialogs. |
| Reload | Fresh generations, removed registrations, and retirement of old callbacks; optional `shutdown` runs when a successfully initialized generation closes. |
| Authoring | `.cljc` scaffolding by default; `--lg` selects let-go-only source. Validation evaluates source and calls `init`. |
| Portability | A tested common subset, not JVM, Babashka-library, or complete Kmet API compatibility. |

Important limits:

- Resolved model authentication, keyed footer status, theme APIs, and model-selection events are not exposed by the documented let-go subset.
- Providers, custom renderers, widgets, and many other SDK capabilities remain outside that subset.
- The before-agent-start system-prompt options are read-only ([D90](docs/parity/DIVERGENCES.md)).
- Interpreter entry is serialized process-wide. Pure interpreted CPU work is cooperative, not preempted. Do not assume Go-style or Kmet-style background execution.
- Printed output is discarded. Use tool results or supported UI calls rather than `println` for visible output.

See the [author guide](docs/letgo-extensions.md), [registration and reload contract](docs/letgo-registration.md), [context API](docs/letgo-context.md), [value boundary](docs/letgo-values.md), and [lifetime evidence and limitations](docs/letgo-lifetime.md). The [Kmet compatibility document](docs/letgo-kmet-compat.md) lists measured common behavior and known differences. These documents and their cited tests define the scope; this README does not claim that all gates were rerun for this documentation change.

## Experiment on Linux

Build this checkout with **Go 1.27.1**. Do not rely on an older `pig` elsewhere on `PATH` or an upstream release to contain this experiment.

```bash
GOTOOLCHAIN=go1.27.1 go build -o bin/pig ./cmd/pig
./bin/pig --version
```

Create an isolated configuration directory and a portable example outside the repository:

```bash
export PIG_HOME="$(mktemp -d)"
export PIG_CODING_AGENT_DIR="$PIG_HOME/agent"
EXPERIMENT_DIR="$(mktemp -d)"

./bin/pig extension init "$EXPERIMENT_DIR/hello" --lang let-go
./bin/pig install "$EXPERIMENT_DIR/hello" --validate-only --json
./bin/pig --no-extensions -e "$EXPERIMENT_DIR/hello/extension.cljc"
```

In the session, run `/hello Linux`. The scaffold displays a greeting through a notification. It also registers a `hello_ping` tool. Configure a provider in this isolated home if you want the model to call that tool. The example does not copy your usual PiG credentials.

Edit the generated source, then run `/reload` in the session. For a let-go-only example, add `--lg` to the scaffold command and select the generated `extension.lg` instead.

You can also select the checked-in fixtures from the repository root:

```bash
./bin/pig --no-extensions -e ./test/fixtures/extensions/letgo/full/extension.lg
./bin/pig --no-extensions -e ./test/fixtures/extensions/letgo/cljc/extension.cljc
```

These are test fixtures, not complete user-facing plugins. Prefer the scaffold for an interactive first experiment.

If PiG reports `spawn ... extension.cljc: permission denied`, it tried to execute the source instead of interpreting it. Do not fix this with `chmod`. Rebuild this checkout and use `./bin/pig`; if the error persists, investigate source routing.

**Only load trusted source.** Validation also executes extension code. Let-go runs inside PiG with your privileges and is not sandboxed. An isolated configuration directory separates settings; it does not isolate filesystem access, networking, or resource consumption. Use an OS sandbox or VM when needed.

### Focused checks

```bash
GOTOOLCHAIN=go1.27.1 go test ./coding/extension/host/letgo
GOTOOLCHAIN=go1.27.1 go test ./test/extension-conformance -run LetGoConformance
GOTOOLCHAIN=go1.27.1 go test ./cmd/pig -run LetGo
```

These exercise the loader, supported native-reference conformance, and CLI integration. They are not substitutes for the repository-wide gates. The [Kmet compatibility guide](docs/letgo-kmet-compat.md#the-gate) describes the separate cross-host fixture check and its limitations.

## Case study: porting Codex usage

Codex usage is a useful boundary test because it needs more than command registration. It resolves host-managed credentials, queries usage and reset-credit endpoints, displays remaining quota, refreshes on model changes and a timer, and prevents stale responses from updating the UI.

The assessment compares an existing Kmet implementation with an existing PiG Go SDK factory. **The `.cljc` port is not implemented.** Its required capability expansion is not approved by this README.

| Concern | Porting approach |
| --- | --- |
| Usage windows, percentages, and reset results | Share pure `.cljc` logic and common input/output fixtures. |
| Host authentication | Bind existing PiG auth-resolution semantics; never bypass refresh and configured headers by reading auth files. |
| Footer and events | Add generic bindings for existing status/theme and model-selection capabilities, not Codex-specific host code. |
| Explicit output | PiG Go uses notifications, already available in let-go. Kmet uses a different interactive presentation. Choose the target behavior explicitly. |
| HTTP, JSON, JWT, and dates | Audit interpreter facilities and isolate host-specific libraries behind adapters. `.cljc` does not make JVM dependencies portable. |
| Polling and cancellation | Preserve generation ownership, superseded-request cancellation, stale-publication checks, and shutdown draining without holding the interpreter lock during network waits. |
| Reset redemption | Treat activation as an account mutation. Do not retry automatically: cancellation does not prove nondelivery. Test with dummy accounts. |

The Go factory already separates its host adapter, domain logic, HTTP transport, and lifetime owner. It is a behavioral reference for PiG, not an instruction to translate goroutines directly into interpreted callbacks. The Kmet and Go versions also differ in originator headers, output, credential redaction, and some formatting rules.

Read **[the detailed Codex portability assessment and plan](let-go/plan-port-codex-usage.md)** for source references, exact binding gaps, runtime ownership requirements, behavior choices, core-versus-fork boundaries, and verification requirements. Its source references use commit-pinned GitHub links to the Kmet and PiG extension repositories.

## Design records

- [Original let-go integration plan](let-go/plan.md)
- [Kmet compatibility plan update](let-go/plan-update-1-kmet-compat.md)
- [Codex usage portability assessment](let-go/plan-port-codex-usage.md)

Plans record design intent, not implementation completion. Use the implementation contracts and cited tests above to assess current support. Generic host changes belong in separately reviewable patches; Codex endpoints, polling policy, and presentation stay in the external extension.

## Attribution and licensing

The fork retains PiG's legal notices. See [LICENSE](LICENSE), [NOTICE](NOTICE), and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). The Codex assessment links the original extension and its MIT license. Preserve those notices when adapting its code.
