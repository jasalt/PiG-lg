# Plan: First-Version Dynamic let-go Extension Runtime for PiG

Status: implementation plan for coding agent  
Target: `github.com/MichaelKinsy/PiG` with embedded `github.com/nooga/let-go`  
Scope: dynamically load let-go/Clojure extensions in-process while using PiG's existing stable extension capability API  
Out of scope for v1: privileged PiG-internal scripting APIs, arbitrary Go-object access, general-purpose in-process plugin ABI, full let-go nREPL integration

## Objective

Add a first-class let-go extension source/runtime to PiG so a user can place or explicitly select a `.lg` extension, have PiG interpret it inside the existing `pig` process, and have the resulting extension participate in normal extension discovery, trust, load ordering, conflict detection, lifecycle dispatch, tool/command registration, `/reload`, and diagnostics.

The implementation must reuse the existing native extension contract under `coding/extension` and the existing `coding/extension/host/inproc.Runner`. Do **not** create a second extension model and do **not** route let-go through the subprocess SDK unless a specific capability cannot be represented through the native extension API.

The architectural target is:

```text
extension.lg / foo.lg
        |
        v
  let-go compiler + VM
        |
        | Clojure-facing adapter
        v
 coding/extension.API
        |
        v
 coding/extension.Extension
        |
        v
 host/inproc.Runner
        |
        v
 existing PiG Session / Agent / TUI integration
```

The extension authoring experience should be Clojure-shaped, but the semantics should come from PiG's existing extension API rather than from a new bespoke host API.

## Current architecture to preserve

Before changing code, re-read the current versions of these files and treat them as the source of truth if paths or signatures have moved:

- `coding/extension/api.go` — `extension.API`, the stable/native extension capability surface.
- `coding/extension/extension.go` — `extension.Extension`, the state container produced by loaders and consumed by the runner.
- `coding/extension/host/inproc/` — native dispatch, context actions, staleness, dynamic registrations, errors, command actions, provider/runtime binding.
- `coding/extension/source/resolve.go` — canonical extension source classification. Extend this instead of adding a parallel source-discovery mechanism.
- `coding/extension/host/subprocess/source_resolver.go` and surrounding startup/reload plumbing — understand how a resolved source currently becomes a configured extension and where the current structure is subprocess-specific versus genuinely common.
- `internal/codingagent/extension_conflicts.go` — extension ordering/conflict semantics.
- `internal/codingagent/reload_resources.go`, `coding/session_extension_hooks.go`, `cmd/pig/headless_reload.go` — reload/replacement behavior.
- `docs/extension-api-parity.md` and `docs/extension-authoring.md` — behavioral contracts that the let-go adapter must not silently weaken.

For let-go, re-read:

- `pkg/api/api.go` — embedding API (`NewLetGo`, `Def`, `Run`, load paths and host options).
- `docs/guide/embedding-in-go.md` — supported Go value/function/channel interop.
- `pkg/vm/value.go` and native function boxing — retained function values and Go/Clojure conversion.
- `pkg/rt/lang.go` — namespace loader state.

At the time this plan was written, let-go's embedding API has process-global pieces such as the namespace loader and dynamic-binding stacks. Do not assume that multiple independently executing `api.LetGo` instances are safe without proving it.

## V1 product decisions

### Source form

Support these author-facing forms:

1. An exact `.lg` file, e.g. `pig -e ./foo.lg`.
2. A directory whose conventional entry is `extension.lg`.
3. The same exact file/directory forms when reached through existing Package/Piglet/settings/project/global extension discovery.

Do not introduce a manifest just for let-go.

Resolve let-go as a factory-style extension source in the broad PiG sense: evaluating the source registers capabilities and produces one `extension.Extension`. It is in-process and interpreted, but it is not a Go dynamic plugin and is not a standalone executable.

For v1, a directory containing multiple unrelated `.lg` entry candidates is an error unless one is the exact conventional `extension.lg`. Keep source resolution deterministic.

### Author-facing namespace

Expose the stable extension API through a Clojure namespace named `pig.extension`, with a small companion `pig.context` namespace where callback-context operations need a context value.

Prefer Clojure data and functions over Go-shaped method calls. Example target syntax:

```clojure
(ns example
  (:require [pig.extension :as pig]
            [pig.context :as ctx]))

(pig/register-tool!
  {:name "hello"
   :description "Say hello"
   :parameters {:type "object"
                :properties {:name {:type "string"}}
                :required ["name"]}
   :execute (fn [c params]
              {:content [{:type "text"
                          :text (str "Hello " (:name params))}]})})

(pig/register-command!
  "hello"
  {:description "Say hello"
   :handler (fn [c args]
              (ctx/notify! c (str "Hello " args) :info))})

(pig/on!
  :session-start
  (fn [c event]
    nil))
```

Exact names may be adjusted for Clojure consistency during implementation, but keep the namespace split and document it. Avoid exposing raw Go receiver methods as the primary public API.

### Stable API boundary

`pig.extension` must adapt to `coding/extension.API` and its public data types. Do not directly mutate `extension.Extension` maps from let-go code if the corresponding API method exists.

Reasons:

- registration replacement/order semantics already live in the API;
- dynamic tool registration and runner refresh behavior already live there;
- event typing and stale-context behavior already have tests;
- future PiG refactors should only require changes inside the adapter.

Where Go's native API is strongly typed but let-go values are dynamic, put conversion at one explicit boundary in the adapter. Do not scatter map/string/type assertions throughout loading and callback code.

### V1 capability subset

The first usable release must implement these surfaces end-to-end:

- extension identity/source metadata;
- `registerTool` / tool execution;
- `registerCommand` / command execution;
- event registration for a deliberately selected initial event set;
- core extension context getters/actions required by those callbacks;
- UI notification and simple interactive prompts when a UI is available;
- session read operations required by representative extensions;
- runtime registration from inside a callback where the underlying native API already supports it;
- startup, project trust, explicit `-e`, Package/Piglet contribution, and `/reload`;
- normal extension conflict detection and diagnostics.

Initial event set should cover enough lifecycle and interception behavior to prove the generic adapter rather than hard-code only trivial events. Include at minimum:

- `session_start`
- `session_shutdown`
- `before_agent_start`
- `agent_start`
- `agent_end`
- `agent_settled`
- `tool_call`
- `tool_result`

Implement event conversion through a registry/table so additional stable `extension.API` events can be added mechanically. Do not build a giant handwritten switch whose shapes drift from `coding/extension`.

Defer these unless they fall out cheaply from the same adapter:

- OAuth/provider registration;
- virtual models;
- custom TUI components;
- terminal-input listeners;
- tool-card renderers;
- message/entry renderers;
- markdown transformers;
- MCP-server registration;
- full extension EventBus exposure;
- privileged `pig.agent`, `pig.session`, `pig.runtime`, or other PiG-internal APIs.

The adapter architecture must leave room for those later without changing the source format.

## Phase 0 — Feasibility gates and dependency pin

Do this before integrating source discovery.

### 0.1 Pin let-go intentionally

Add let-go as a normal Go module dependency only after resolving the Go-version constraint.

At plan-writing time:

- PiG declares `go 1.26.0` and `toolchain go1.27.1`.
- let-go main declares `go 1.27` and `toolchain go1.27.1`.

Determine whether PiG should:

- bump its `go` directive to 1.27, or
- pin a let-go release/commit compatible with the current directive.

Do not let `go get` silently make a broad toolchain/module-policy change. Record the decision in the commit/plan progress notes.

### 0.2 Prove embedding and callback retention

Add a focused experimental test package, preferably near the eventual host adapter, that proves all of these:

1. Construct an embedded let-go runtime.
2. Inject a Go function into it.
3. Evaluate source that passes a let-go `fn` back to Go.
4. Retain that function after the initial source evaluation returns.
5. Invoke it later from Go with map/string/scalar data.
6. Convert the result back into a Go value suitable for extension result handling.
7. Propagate an interpreted exception as a normal Go error with useful source information.

Do not proceed to the loader until this is green.

### 0.3 Establish the concurrency/isolation model

Write tests around the actual pinned let-go version for:

- two extension namespaces loaded sequentially;
- callbacks from two extensions invoked concurrently;
- `require` resolution for two separate extension roots;
- extension reload while no callback is executing;
- extension reload while an old callback is still retained by the old runner;
- stdout/stderr/dynamic binding behavior under concurrency.

Because let-go currently uses process-global namespace-loader state, choose one of these designs based on evidence:

**Preferred v1 fallback:** a single process-level let-go host with serialized VM entry and one distinct namespace/load root per extension generation.

**Acceptable if proven safe:** one `api.LetGo` runtime per extension generation, with all entry into let-go serialized by a process-level coordinator whenever process-global state is touched.

Do not rely only on a mutex per extension if process-global let-go state can be changed by another extension concurrently.

Create a small owner type such as:

```go
type RuntimeHost struct {
    // owns all let-go process-global interaction
}
```

and make this the only package allowed to construct/evaluate/invoke let-go runtimes. This gives later let-go runtime improvements one containment point.

### Phase 0 acceptance

- PiG builds reproducibly with the pinned let-go dependency.
- A retained let-go callback can be invoked from Go after load.
- Error conversion preserves a useful source path/form location when available.
- The chosen concurrency model has race-oriented tests and a written invariant in package docs.

## Phase 1 — Add let-go as a recognized extension source

### 1.1 Extend canonical source resolution

Modify `coding/extension/source/resolve.go` rather than creating a let-go-only discovery path.

Add language classification for `let-go`:

- exact `.lg` file -> let-go factory source;
- directory containing `extension.lg` -> let-go factory source;
- deterministic error for ambiguous let-go roots;
- mixed-language directory behavior must stay consistent with the existing rule that one selected extension root resolves to one language/form.

Populate `source.Definition` with enough information to load the exact entry file. If `Definition.Entrypoint` is currently underused for factory forms, using it for let-go is preferable to inventing a let-go-only path field.

Set `Packable` false in v1. "Packing" has no value for an already embedded interpreter and should not accidentally route let-go through native cell planners.

### 1.2 Route resolved let-go sources through the common load inventory

Trace how `source.Definition` becomes runtime configuration. Generalize the narrowest structure necessary so let-go can travel through the same:

- discovery ordering;
- path/provenance recording;
- trust gate;
- `--no-extensions` behavior;
- explicit `-e` override behavior;
- Package/Piglet inventory;
- validation diagnostics;
- reload source list.

Do not copy the discovery implementation into a new let-go manager.

If the currently named `subprocess.ExtConfig` is acting as a de facto common extension-source descriptor, either:

- minimally generalize it and its comments to allow `RuntimeKind: "inproc-letgo"`, or
- introduce a small common resolved-config type above subprocess planning and adapt subprocess + let-go from it.

Choose the smaller change that removes semantic lies rather than adding another translation layer solely for naming purity.

### 1.3 Source-resolution tests

Cover:

- exact `.lg` file;
- `extension.lg` directory;
- missing/ambiguous entry;
- mixed let-go + Go/Python/Node markers;
- symlink/canonical path behavior consistent with other extensions;
- duplicate extension identity from two paths;
- global/project/explicit load order.

### Phase 1 acceptance

`pig ... -e ./example.lg` resolves as a known extension source and reaches a let-go loader stub with correct identity, path, trust, and ordering metadata. It does not yet need to register a working tool.

## Phase 2 — Implement the in-process let-go loader and API adapter

Create a focused package, e.g.:

```text
coding/extension/host/letgo/
    doc.go
    host.go
    runtime.go
    values.go
    api.go
    context.go
    events.go
    errors.go
```

Avoid putting let-go imports throughout PiG.

### 2.1 Loader contract

The package should expose a narrow operation conceptually like:

```go
type LoadRequest struct {
    Name         string
    SelectedPath string
    ResolvedPath string
    Entrypoint   string
    SourceInfo   extension.SourceInfo
}

func (h *RuntimeHost) Load(ctx context.Context, req LoadRequest) (LoadedExtension, error)
```

`LoadedExtension` should contain:

- the populated `extension.Extension` value consumed by `inproc.Runner`;
- an owned let-go generation/runtime handle that keeps retained callbacks alive;
- an idempotent invalidation/close function;
- enough diagnostics to name the source and generation.

The runtime owner must live as long as the runner can invoke the extension. Do not let a temporary loader object be garbage-collected while its callbacks remain registered.

### 2.2 Build the extension through the native API

Find and reuse the same concrete `extension.API` implementation/factory-construction path used by native/built-in Go extensions. The let-go adapter should receive an `extension.API` and call its methods.

Do **not** construct `extension.RegisteredTool`, `RegisteredCommand`, handler maps, etc. by hand when a public API method already performs that registration.

The desired internal relationship is:

```text
let-go form
   -> adapter function
      -> extension.API.RegisterTool / RegisterCommand / On...
         -> extension.Extension state
```

This is a central v1 requirement.

### 2.3 Install `pig.extension`

Install host functions into the let-go environment. It is acceptable for the first implementation to inject low-level Go functions and define the nicer Clojure facade in a small bundled `.lg` namespace.

Prefer this split:

```text
Go primitives:     pig.internal.extension/*
Clojure facade:    pig.extension/*
```

The public extension file should only need `pig.extension` / `pig.context`.

Core operations:

- `register-tool!`
- `register-command!`
- `on!`
- optionally `register-shortcut!` and `register-flag!` if they are trivial once conversion is in place

### 2.4 Function retention

When let-go passes a function to `register-tool!`, `register-command!`, or `on!`, retain the `vm.Fn` in the extension-generation owner.

Never store only a temporary function index tied to the initial eval frame.

Wrap invocation so every callback:

1. enters the runtime through the Phase 0 serialization/coordinator invariant;
2. receives a callback-scoped context wrapper and converted data;
3. invokes the retained `vm.Fn`;
4. converts the return value;
5. converts let-go errors/panics into the normal PiG extension error path;
6. exits runtime state cleanly even on cancellation/error.

### 2.5 Value conversion

Create one conversion module with explicit supported shapes.

V1 host -> let-go:

- nil;
- bool;
- signed/unsigned integral values where safe;
- float;
- string;
- slices/arrays -> vectors;
- `map[string]any` -> keyword-keyed or string-keyed maps according to a single documented rule;
- stable public extension structs -> map representation using their JSON names.

V1 let-go -> host:

- nil;
- bool/number/string;
- vector/list -> slice;
- map -> `map[string]any` or target typed structure;
- nested JSON-like values.

Use PiG's JSON tags as the canonical field spelling for typed extension events/results. Avoid a second naming schema.

Decide and document keyword behavior. Recommended:

- incoming object keys become keywords when they are ordinary identifier-like JSON keys;
- outgoing keyword keys become their unqualified names;
- string keys remain strings;
- reject namespaced keywords where a JSON object key is required unless explicitly handled.

Do not silently stringify arbitrary let-go values.

### Phase 2 acceptance

A `.lg` extension can register one tool and one command. Both execute through `inproc.Runner`, receive arguments, return results, and report interpreted failures as normal extension diagnostics.

## Phase 3 — Context bridge and representative events

### 3.1 Opaque callback context

Do not expose raw Go `extension.Context` / command-context structs as general reflective values.

Create a small opaque host handle scoped to one callback invocation. `pig.context` functions unwrap it and call the stable extension context methods.

Example:

```clojure
(ctx/cwd c)
(ctx/model c)
(ctx/has-ui? c)
(ctx/notify! c "message" :info)
```

This prevents let-go code from becoming coupled to Go struct layout and preserves stale-context/cancellation semantics.

The wrapper must become invalid when PiG's underlying context becomes stale. Do not copy mutable live values out of the context just to avoid this check.

### 3.2 First context surface

Implement only operations needed by the v1 tools/commands/events, selecting from the existing `extension.API`/context capabilities rather than inventing equivalents.

Target:

- `cwd`
- current model/model info if stable API exposes it
- mode / `has-ui?`
- width/height where appropriate
- `is-idle?`
- cancellation query / done state
- `notify!`
- `select!`
- `confirm!`
- `input!`
- `get-entries` / `get-branch` or the stable session-read equivalents
- active/all tool inspection where already part of stable context
- dynamic `register-tool!` if native context/API supports it

If a host action is unavailable in print/RPC mode, match existing extension behavior exactly; do not invent a let-go-specific fallback.

### 3.3 Event adapter table

Build a table/registry associating let-go event keywords with native typed registration functions and conversion codecs.

Example conceptual entry:

```go
type eventAdapter struct {
    Name     string
    Register func(api extension.API, invoke HandlerInvoker) error
}
```

Each adapter should call the real typed `On<Event>` method. Do not bypass the typed native API and write directly to `Extension.Handlers` merely because let-go is dynamic.

Implement the initial event set listed above.

For events with mutable/result semantics such as `before_agent_start`, `tool_call`, and `tool_result`, add explicit tests for:

- unchanged result;
- replacement/mutation result;
- nil result;
- malformed result;
- handler error;
- handler ordering when several extensions participate.

### 3.4 Cancellation

A cancelled PiG callback must not become an immortal interpreted task.

For v1:

- make cancellation observable through `pig.context`;
- ensure host-blocking operations inherit the callback context;
- serialize invocation without holding unrelated PiG locks;
- document that pure CPU-bound let-go code is cooperative unless the VM offers a safe interrupt mechanism.

Do not claim hard preemption if let-go cannot provide it.

### Phase 3 acceptance

A representative let-go extension can:

- inspect session lifecycle;
- modify a supported `before_agent_start` result;
- intercept/observe a tool event;
- use a simple UI prompt in interactive mode;
- detect cancellation/stale context;
- dynamically add or replace a tool if the underlying API permits it.

## Phase 4 — Integrate startup, trust, reload, and lifetime ownership

### 4.1 Startup

Make let-go extensions feed into the same final `[]extension.Extension` load order used by native/subprocess extensions before `inproc.NewRunner(...)` is constructed.

Preserve existing order semantics: configured/path extensions before built-ins, and first-seen order within configured sources.

A let-go extension must therefore participate in existing:

- tool conflicts;
- command conflicts;
- flag conflicts when flags are supported;
- diagnostics;
- `/extensions`/resource listings;
- source provenance.

### 4.2 Trust

Project `.pig/extensions` let-go source is executable code. Apply exactly the same project-trust gate as other executable extension sources.

Do not create an exception because the language is interpreted.

Factory evaluation can execute arbitrary let-go code, so any pre-session inspection path must treat evaluation as code execution, not metadata parsing.

### 4.3 Reload model

`/reload` must create a **fresh extension generation** from the source file.

Required sequence:

1. Resolve sources again using the ordinary normalized resolver.
2. Load/evaluate new let-go generation(s) off to the side.
3. Build a complete replacement extension list and runner.
4. Publish the replacement using the current atomic/staged reload behavior.
5. Mark the old runner/contexts stale as existing PiG logic does.
6. Retire old let-go generation(s) only after PiG no longer permits their callbacks to execute.

Do not mutate an existing namespace in place as the primary `/reload` mechanism. Fresh-generation replacement matches PiG's extension semantics and avoids stale vars/closures leaking across reloads.

State such as a let-go `atom` should reset on reload unless persisted through an explicit PiG/session/storage mechanism.

### 4.4 Generation ownership

Add explicit generation IDs to diagnostics/logging inside the let-go host package if needed for debugging, but do not expose them as part of the user API.

Ensure:

- old retained `vm.Fn` values stay alive while old callbacks can still finish;
- new calls cannot reach old callbacks after runner replacement;
- shutdown is idempotent;
- no namespace/load-path reference keeps retired source generations accidentally active forever.

### 4.5 Reload tests

Test:

- edit `.lg` tool response -> `/reload` -> new behavior observed;
- removed tool disappears;
- added command appears;
- syntax error on replacement is reported according to existing PiG reload failure policy;
- one broken let-go extension does not erase unrelated healthy extensions beyond existing conflict/transaction semantics;
- closure/atom state resets;
- stale context captured before reload fails afterward;
- repeated reload does not grow goroutines/runtime owners unboundedly.

### Phase 4 acceptance

A user can develop a `.lg` extension by editing the file and invoking PiG `/reload`; code and registrations are replaced without restarting PiG.

## Phase 5 — Diagnostics, validation, scaffolding, and docs

### 5.1 Error presentation

Errors must name:

- extension identity;
- selected/resolved source path;
- phase (`resolve`, `compile/eval`, `register`, `callback`, `reload`);
- let-go source location/stack information when available.

Do not dump internal Go reflection errors when a conversion failure can say which extension API field/value was invalid.

Example target:

```text
extension "review-tools" (let-go): callback tool "review" failed:
/home/me/.pig/agent/extensions/review-tools/extension.lg:31: ...
```

### 5.2 Validation

Extend the existing validation path so a let-go source can be checked without entering a normal interactive session.

Validation should:

- resolve source;
- create a fresh runtime generation;
- evaluate registrations;
- verify extension identity/provenance/conflicts using the same mechanisms as other source forms;
- dispose the generation;
- not dispatch session/tool/command events just to validate registration.

### 5.3 Scaffold

Extend `pig extension init` with an explicit let-go language option only after loading works reliably.

Suggested generated file:

```clojure
(ns extension
  (:require [pig.extension :as pig]))

(pig/register-tool!
  {:name "hello"
   :description "Return a greeting"
   :parameters {:type "object"
                :properties {:name {:type "string"}}
                :required ["name"]}
   :execute (fn [_ {:keys [name]}]
              {:content [{:type "text"
                          :text (str "Hello " name)}]})})
```

Avoid generated manifest/config files.

### 5.4 Documentation

Add a focused let-go extension page and cross-link it from extension authoring.

Document clearly:

- `.lg` / `extension.lg` source forms;
- in-process trust implications;
- public namespaces;
- currently supported subset of `extension.API`;
- reload behavior and state reset;
- cancellation limitations;
- concurrency/serialization behavior;
- unsupported capabilities;
- that `pig.internal.*` / privileged core access does not exist in v1.

### Phase 5 acceptance

A new user can scaffold, validate, load, invoke, edit, reload, and diagnose a let-go extension using documented commands without reading PiG source.

## Phase 6 — Conformance and regression gate

Do not call the feature complete based only on adapter unit tests.

### 6.1 Add let-go to extension conformance where semantically applicable

Reuse existing cross-runtime fixtures/assertions for the capabilities implemented in v1.

The goal is not to claim full SDK parity. The goal is that an implemented capability has the same observable PiG semantics regardless of whether its handler came from:

- native/in-process Go;
- subprocess Go/Rust/Python/Node;
- let-go in-process interpretation.

For unsupported rows, record them explicitly as unsupported rather than silently skipping them.

### 6.2 Race and stress tests

Add focused tests with `-race` for the adapter and runtime coordinator:

- concurrent tool calls;
- event + tool overlap;
- reload during/after callback completion;
- two let-go extensions with separate source roots;
- repeated load/unload/reload;
- concurrent PiG operations that do not enter let-go must not be serialized by the let-go lock.

Only serialize actual let-go VM/runtime entry, not the entire agent loop.

### 6.3 Resource/leak checks

Use bounded repeated reload tests to detect obvious growth in:

- goroutines;
- retained generations;
- namespace registrations if inspectable;
- file descriptors;
- timers/watchers introduced by the adapter.

### 6.4 Full regression

Run the repository's normal extension/parity/test suites. In particular ensure the new source language does not alter existing Go/Rust/Python/Node source classification or packing behavior.

### Phase 6 acceptance

- all new let-go tests pass under `-race` where supported;
- existing extension conformance remains green;
- existing source resolver tests remain green;
- existing packed/fused/subprocess behavior is unchanged for non-let-go sources;
- no known unbounded reload leak remains.

## Recommended implementation sequence / commits

Keep commits narrow enough that failures are attributable:

1. **Dependency + embedding spike** — pin let-go, prove retained callbacks and conversion.
2. **Runtime coordinator** — process-global serialization/isolation tests, no PiG loader integration.
3. **Source resolver** — `.lg` / `extension.lg` recognition and tests.
4. **Native API bridge skeleton** — produce a real `extension.Extension` from interpreted registration.
5. **Tools + commands** — first end-to-end useful extension.
6. **Context bridge** — opaque callback context + notify/basic UI/session reads.
7. **Events** — table-driven representative event subset.
8. **Startup/trust/inventory integration** — all normal source-selection paths.
9. **Reload generations** — staged fresh VM replacement + lifecycle cleanup.
10. **Validation/scaffold/docs**.
11. **Conformance/race/stress sweep**.

Each commit after step 4 should include an executable fixture or test extension demonstrating the new behavior.

## Suggested package boundaries

Keep let-go-specific code concentrated:

```text
coding/extension/host/letgo/
  doc.go             # invariants and public internal contract
  host.go            # RuntimeHost; process-global serialization/ownership
  load.go            # source -> generation -> extension.Extension
  generation.go      # generation lifetime and retained callback ownership
  namespace.go       # bundled pig.extension / pig.context installation
  values.go          # one conversion boundary
  functions.go       # retained vm.Fn invocation helpers
  api.go             # registration bridge
  context.go         # opaque callback context bridge
  events.go          # table-driven typed event adapters
  errors.go          # source-aware errors
  *_test.go
```

If bundled Clojure facade code becomes more than a few forms, store it as embedded source files rather than Go string literals.

Keep source detection under existing `coding/extension/source`, and keep CLI/startup changes limited to routing resolved let-go definitions to this host.

## Testing fixture

Maintain one human-readable fixture that exercises the supported surface, e.g. `test/fixtures/extensions/letgo/full/extension.lg`:

```clojure
(ns pig-test.full
  (:require [pig.extension :as pig]
            [pig.context :as ctx]))

(def calls (atom 0))

(pig/register-tool!
  {:name "lg_echo"
   :description "Echo text"
   :parameters {:type "object"
                :properties {:text {:type "string"}}
                :required ["text"]}
   :execute (fn [c {:keys [text]}]
              (swap! calls inc)
              {:content [{:type "text" :text text}]
               :details {:calls @calls}})})

(pig/register-command!
  "lg-status"
  {:description "Show let-go extension status"
   :handler (fn [c _]
              (ctx/notify! c (str "calls=" @calls) :info))})

(pig/on! :session-start
  (fn [_ _] nil))
```

Use separate fixtures for malformed registration, thrown callback, cancellation, event result mutation, dynamic registration, and reload.

## Important invariants for the coding agent

1. **One extension model.** let-go produces the same `extension.Extension` consumed by `inproc.Runner`; it does not introduce `LetGoExtension` throughout the product.
2. **One discovery model.** extend existing source resolution and trust/load ordering; no parallel `.lg` scanning service.
3. **Stable API first.** public let-go functions adapt to `coding/extension.API`; do not expose arbitrary PiG internals in v1.
4. **Fresh reload generations.** never rely on mutating the old interpreter namespace into the new version.
5. **Centralized conversion.** JSON-like/event conversion lives in one package boundary and has exhaustive tests.
6. **Centralized VM ownership.** only the let-go host package manages process-global let-go state and invocation serialization.
7. **No fake isolation claims.** let-go code is in-process and trusted; a panic/resource abuse can affect PiG unless explicitly contained.
8. **Do not regress subprocess semantics.** Go/Rust/Python/Node source resolution, cell planning, fused Go, and subprocess host behavior should not change merely to accommodate let-go.
9. **Do not broaden scope opportunistically.** privileged agent/session/TUI scripting is a future layer; v1 proves dynamic extension loading through the stable capability API.
10. **Test behavior, not implementation shape.** where PiG already has parity/conformance tests, extend those rather than duplicating assertions in a let-go-only suite.

## Explicit non-goals for v1

Do not implement these as part of this plan unless required to make the scoped API function correctly:

- `plugin.Open` or shared-library loading;
- compiling let-go extensions into Piglet binaries;
- AOT lowering of extension source;
- a general Clojure scripting API over PiG internals;
- exposing `*coding.Session`, `Agent`, `inproc.Runner`, registry structs, or TUI objects to let-go;
- nREPL/CIDER live connection to PiG;
- hot-patching an individual function without `/reload`;
- sandbox/security isolation inside the PiG process;
- multiple let-go VM executions in parallel before thread/process-global safety is proven;
- full feature parity with Node/Go/Rust/Python extension SDKs.

## Follow-up after v1

Once this implementation is stable, evaluate follow-up work independently:

- expand `pig.extension` toward the complete stable `extension.API` surface;
- provider/OAuth and virtual-model support;
- renderers and custom UI;
- EventBus exposure;
- nREPL for interactive extension development;
- finer-grained reload/eval for development;
- privileged opt-in `pig.host` APIs for PiG-specific scripting;
- capability permissions for privileged APIs;
- removal of serialization if let-go gains provable per-runtime isolation;
- AOT or cached bytecode for startup optimization if interpretation becomes measurable.

Do not let these delay the first version whose success criterion is simple: **a trusted `.lg` source is discovered like any other PiG extension, interpreted in-process, registers capabilities through the stable native extension API, and is replaceable with `/reload`.**

## Definition of done

The first version is done when all of the following are demonstrated in automated tests and one documented example:

- `pig -e ./example.lg` loads without an external `lg` executable.
- a project/global/package-selected `.lg` extension follows existing trust and ordering rules.
- the extension registers a tool through native `extension.API` and the model can invoke it.
- the extension registers a slash command and it works in the applicable modes.
- supported lifecycle/tool events dispatch to retained let-go functions and can return supported result mutations.
- context operations use an opaque wrapper and preserve stale/cancellation behavior.
- `/reload` re-reads source, constructs a fresh interpreter generation, replaces registrations, and invalidates the old generation according to existing runner semantics.
- a broken let-go extension produces a source-aware diagnostic without corrupting unrelated extension registrations beyond existing PiG load/reload policy.
- repeated reload is bounded and race-tested.
- existing non-let-go extension resolution, subprocess cells, packed cells, fused Go, and extension conformance remain green.
