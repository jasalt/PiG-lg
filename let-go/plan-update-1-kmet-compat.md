# Plan Update 1: Kmet-Compatible let-go Extension Contract

Status: update to `plan.md`  
Applies to: first-version dynamic let-go extension runtime for PiG  
Primary goal: maximize source-level portability with `kmetia/kmet-agent` extensions without weakening PiG's native extension semantics

## Summary

Update the original plan so the first-version PiG let-go extension API is intentionally compatible with the shape of Kmet's Clojure extension contract where the two hosts expose the same underlying Pi concepts.

The core architecture from `plan.md` remains unchanged:

```text
let-go source
    |
    v
Clojure-facing adapter
    |
    v
coding/extension.API
    |
    v
coding/extension.Extension
    |
    v
coding/extension/host/inproc.Runner
```

The main change is at the author-facing boundary.

Instead of making a let-go source register capabilities as top-level evaluation side effects, use an explicit lifecycle matching Kmet:

```clojure
(ns my.extension
  (:require [pig.extension :as ext]))

(defn init [api]
  ;; register tools, commands, events, etc.
  )

(defn shutdown [api]
  ;; optional extension-owned cleanup
  )
```

The loader evaluates the namespace, resolves `init`, constructs an explicit capability map, and calls `(init api)`. `shutdown` is optional and runs when the extension generation is retired.

The second major change is to make `.cljc` a first-class source form and deliberately keep the first public PiG Clojure API close enough to Kmet that significant extension logic can run unchanged on both hosts.

## Why update the original plan

Kmet already has an extension design closely aligned with the intended PiG/let-go architecture:

- extensions are Clojure namespaces;
- extensions depend on a narrow public extension namespace rather than application internals;
- `init [api]` receives runtime-bound capabilities;
- optional `shutdown [api]` handles extension-owned cleanup;
- registrations are scoped to the extension generation and removed on unload/reload;
- tools, commands, events, flags, renderers, agent actions, model/session operations, and UI actions are exposed through capability functions/data;
- extension loading and reload create isolated extension contexts.

PiG's native `coding/extension.API` is already a typed Go projection of the same upstream Pi `ExtensionAPI`.

Therefore the let-go layer should avoid inventing a substantially different Clojure dialect when a useful common subset already exists.

The design target becomes:

```text
                     portable extension code
                             .cljc
                               |
                   +-----------+-----------+
                   |                       |
                   v                       v
             Kmet adapter             PiG adapter
          kmet.extension            pig.extension
                   |                       |
                   v                       v
             Kmet runtime        coding/extension.API
                                           |
                                           v
                                  extension.Extension
                                           |
                                           v
                                    inproc.Runner
```

## Revised v1 product decisions

### 1. Explicit lifecycle replaces top-level registration

Replace the original top-level registration target:

```clojure
(ns example
  (:require [pig.extension :as ext]))

(ext/register-tool! ...)
(ext/register-command! ...)
```

with:

```clojure
(ns example
  (:require [pig.extension :as ext]))

(defn init [api]
  (ext/register-tool! api {...})
  (ext/register-command! api {...}))

(defn shutdown [api]
  nil)
```

Reasons:

- aligns with Kmet;
- avoids registration as an evaluation side effect;
- provides a clean test seam;
- gives reload explicit generation ownership;
- allows loading/compiling a namespace before activating it;
- allows optional extension-owned teardown without weakening host-owned automatic deregistration;
- makes shared `.cljc` entry namespaces practical.

`shutdown` must not be required for deregistration. PiG still owns registration lifetime and must remove all registrations when the generation is retired. `shutdown` is only for extension-owned resources such as timers, channels, temporary files, or child processes.

### 2. `.cljc` is first-class

Support portable Clojure source directly.

Author-facing exact-file forms:

- `.lg`
- `.cljc`

Directory extension conventional entries, in preference order:

1. `extension.lg`
2. `extension.cljc`

Do not automatically treat arbitrary `.clj` as let-go-compatible in v1 unless the pinned let-go loader semantics make this unambiguous and useful. The portability target is `.cljc`.

When a directory contains both `extension.lg` and `extension.cljc`, prefer an explicit deterministic rule. Recommended v1 rule: treat it as ambiguous and require the user to select the exact file. This avoids silently choosing host-specific source over portable source.

let-go reader conditionals may be used inside `.cljc`:

```clojure
#?(:lg ...
   :default ...)
```

Keep portable extension code free of reader conditionals wherever possible. Prefer thin host adapter namespaces over conditionals scattered through business logic.

### 3. API is an explicit Clojure map/capability object

`init` receives one `api` value.

From Clojure, this should behave as a plain map of stable capabilities rather than an exposed Go receiver.

Example conceptual shape:

```clojure
{:extension-name "review-tools"
 :extension-path "/..."
 :extension-dir "/..."

 :register-tool! fn
 :register-command! fn
 :register-shortcut! fn
 :register-flag! fn

 :on-event fn
 :emit-event! fn

 :get-all-tools fn
 :get-active-tools fn
 :set-active-tools fn

 :send-message! fn
 :send-user-message fn
 :exec fn

 :ui {...}
 :models {...}
 :session {...}}
```

The actual v1 subset may be smaller, but its structure should intentionally resemble Kmet where the same Pi capability exists.

`pig.extension` should primarily contain thin wrappers:

```clojure
(defn register-tool! [api tool]
  ((:register-tool! api) tool))

(defn on-event [api event handler]
  ((:on-event api) event handler))
```

This keeps extension code independent from the internal representation of the Go bridge.

### 4. Callback context should be Clojure-shaped

Revise Phase 3 of the original plan.

Do not make the public v1 callback context an opaque object that must be accessed only through:

```clojure
(ctx/cwd c)
(ctx/model c)
```

Instead expose a map-like Clojure value modeled after Kmet's extension callback context:

```clojure
{:cwd "/project"
 :mode :interactive
 :has-ui true
 :model {...}

 :is-idle fn
 :abort fn
 :wait-for-idle fn
 :reload fn
 :compact fn
 ...}
```

The Go bridge may internally retain an opaque host handle and use closures that validate staleness before touching PiG.

Important: map-shaped does **not** mean snapshot all live host state and lose PiG semantics.

Use this model:

```text
Clojure map
  scalar snapshot fields:
    :cwd
    :mode
    :has-ui
    :model

  closures backed by callback-scoped host handle:
    :abort
    :reload
    :compact
    :wait-for-idle
    :notify
    ...
```

Any closure that reaches the live host must preserve existing PiG stale-context and cancellation checks.

If a scalar must always be live rather than callback-scoped, expose it as a function rather than copying it once.

### 5. Prefer Kmet-compatible function signatures for the common subset

For capabilities present in both Kmet and PiG, use the same broad call shapes unless doing so would violate PiG semantics.

Target forms:

```clojure
(ext/register-tool! api tool-map)

(ext/register-command! api
  {:name "hello"
   :description "..."
   :handler (fn [ctx args] ...)})

(ext/on-event api :session-start
  (fn [event ctx] ...))

(ext/get-all-tools api)
(ext/get-active-tools api)
(ext/set-active-tools api names)

(ext/exec api "git" ["status"] {:dir "/tmp"})

(ext/send-user-message api "continue" {})
```

For event handlers, prefer Kmet's argument order:

```clojure
(fn [event ctx] ...)
```

rather than the original plan's possible:

```clojure
(fn [ctx event] ...)
```

Pick one ordering and make it consistent across every event.

For commands, keep the Kmet/Pi-style:

```clojure
(fn [ctx args] ...)
```

For tools, prefer the portable definition shape:

```clojure
{:name "foo"
 :description "..."
 :parameters {...}
 :execute (fn [args] ...)}
```

If PiG needs callback context for tool execution, support a documented extended arity without breaking the one-argument portable form:

```clojure
:execute (fn [args] ...)
```

and optionally:

```clojure
:execute (fn [ctx args] ...)
```

The bridge can inspect function arity where let-go exposes it.

Do not force all portable tools to accept PiG-specific context if Kmet tools do not need it.

## Portable compatibility layers

Define three conceptual API tiers.

### Tier A — portable common subset

This is the target for shared `.cljc`.

Initial candidates:

- extension identity;
- tool registration;
- command registration;
- lifecycle events;
- basic agent events;
- before-agent-start hook;
- tool-call/tool-result hooks;
- flags;
- shortcuts where semantics match;
- get all/active tools;
- set active tools;
- send message;
- send user message;
- basic exec;
- basic model selection/thinking controls if shapes can be aligned;
- simple session state operations if shapes can be aligned.

Do not declare a capability portable merely because both systems have similarly named functions. Verify payload/result semantics.

### Tier B — same concept, host adapter required

Examples:

- provider/model registration;
- session tree queries;
- custom entry/message renderers;
- UI notification/status operations;
- richer agent control;
- resource registration;
- host-specific filesystem/HTTP helper libraries.

Shared business logic may remain `.cljc`, but entry adapters differ.

### Tier C — host-specific

Examples:

- Kmet `kmet.tui.*` component objects;
- PiG-specific native TUI/rendering objects;
- Kmet SCI/Jolt loader facilities;
- PiG privileged future `pig.host` APIs;
- JVM/Babashka-specific libraries;
- let-go-only runtime APIs.

Do not block v1 on these.

## Recommended portable source layout

Support and document this pattern:

```text
my-extension/
  extension.edn?             # Kmet-only if needed
  extension.cljc             # portable entry if feasible
  my_ext/
    core.cljc                # pure domain/extension logic
    api.cljc                 # optional portable facade
    kmet.cljc                # optional Kmet adapter
    pig.cljc                 # optional PiG adapter
```

Prefer:

```text
core.cljc
    |
    +-- host-independent transformations
    +-- tool business logic
    +-- schemas
    +-- state machines
    +-- prompt manipulation

host adapter
    |
    +-- register with Kmet/PiG
    +-- translate context differences
    +-- translate UI/session host details
```

Avoid a design where every function contains several `#?` branches.

## Optional shared compatibility facade

Do not require this for PiG v1, but shape the API so it can be added without redesign.

Potential future namespace:

```clojure
pi-clj.extension
```

with portable functions:

```clojure
(register-tool! api ...)
(register-command! api ...)
(on-event api ...)
(exec api ...)
(send-user-message api ...)
```

Host implementations would be:

```text
pi-clj.extension
    |
    +-- Kmet -> kmet.extension
    |
    +-- PiG/let-go -> pig.extension
```

For v1, `pig.extension` should be close enough to `kmet.extension` that a compatibility facade is trivial.

## Updates to Phase 0 — feasibility gates

Keep all original Phase 0 dependency, embedding, callback-retention, and concurrency work.

Add these gates.

### 0.K1 — `.cljc` reader test

Prove with the pinned let-go revision:

1. exact `.cljc` source loads;
2. namespace loading follows expected `.lg` / `.cljc` / `.clj` rules;
3. `#?(:lg ...)` selects the let-go branch;
4. `:default` works as expected;
5. shared pure Clojure forms used by Kmet compile in let-go;
6. unsupported Clojure/JVM constructs fail with source-aware diagnostics.

Create a fixture that also runs under JVM Clojure or Babashka if practical, proving it is genuinely shared source rather than merely named `.cljc`.

### 0.K2 — lifecycle invocation test

Prove:

```clojure
(defn init [api] ...)
(defn shutdown [api] ...)
```

can be resolved by symbol after namespace evaluation and invoked from Go.

Test:

- missing `init` -> load error;
- non-callable `init` -> load error;
- `init` throws -> extension activation fails cleanly;
- missing `shutdown` -> valid;
- `shutdown` throws -> diagnostic, but host-owned cleanup still proceeds.

### 0.K3 — map/function interop test

Prove a capability map containing Go-backed functions can be passed to let-go and invoked naturally as Clojure data.

Example:

```clojure
((:register-tool! api) tool)
```

This is a core compatibility requirement.

## Updates to Phase 1 — source resolution

Expand recognized source forms from the original plan.

### Exact file

Recognize:

- `.lg`
- `.cljc`

Both resolve to runtime language `let-go`.

### Directory

Recognize:

- `extension.lg`
- `extension.cljc`

Do not scan arbitrary `.cljc` files and guess an entry namespace.

The loader derives the namespace from the source's `ns` declaration and requires `init`.

### Kmet manifest compatibility

Do **not** implement full `extension.edn` compatibility in first PiG v1 unless it is nearly free.

However, avoid source-layout decisions that would make it impossible later.

In particular:

- permit multi-file namespace loading rooted at the selected extension directory;
- do not hard-wire the namespace to `extension`;
- keep entry namespace and root separate in the internal load request;
- keep room for a future manifest mapping `:entry` to a namespace.

A future follow-up could let PiG understand the subset:

```clojure
{:name "my-ext"
 :entry my-ext.core
 :loader [:lg]}
```

or a shared loader descriptor, but this is explicitly not required for the first implementation.

## Updates to Phase 2 — loader and adapter

### 2.K1 — activation is separate from evaluation

Change the loader sequence to:

```text
1. create fresh let-go generation
2. install public bridge namespaces
3. evaluate/load extension namespace
4. resolve init
5. construct api capability map
6. invoke (init api)
7. obtain populated extension.Extension
8. publish only after successful activation
```

Do not let top-level forms register into PiG as the supported mechanism.

Top-level code may define vars/functions/constants and perform normal namespace initialization, but public registrations belong in `init`.

### 2.K2 — generation registration ledger

Track registrations owned by the generation.

PiG's native extension API/runner remains the authority, but the let-go host should know enough to guarantee:

- all generation registrations disappear on retirement;
- `shutdown` executes before final teardown when appropriate;
- registrations created dynamically after `init` are still generation-owned;
- a failed `init` cannot leave partial live registrations.

Prefer staging activation against an unpublished extension state so registration failure is transactional.

### 2.K3 — provide nullable/test API support

Add a small test helper inspired by Kmet's nullable extension API.

It does not need to be user-facing immediately.

Conceptually:

```go
func NewTestAPI() (apiValue vm.Value, captured *CapturedRegistrations)
```

or a Clojure-visible test namespace later.

Purpose:

- load extension namespace;
- call `init` without a live session;
- inspect registered tools/commands/events;
- unit-test extension source cheaply;
- improve future cross-host compatibility testing.

## Updates to Phase 3 — context and events

### Replace "opaque callback context" public API

Internally retain an opaque Go handle where useful, but expose a Clojure map facade.

Example:

```clojure
{:cwd "/repo"
 :mode :interactive
 :has-ui true
 :model {...}

 :is-idle (fn [] ...)
 :abort (fn [] ...)
 :wait-for-idle (fn [] ...)
 :reload (fn [] ...)
 :notify (fn [message type] ...)
}
```

Keep `pig.context` only if it adds convenient wrappers over this map.

For example:

```clojure
(ctx/notify! ctx "hello" :info)
```

may simply call:

```clojure
((:notify ctx) "hello" :info)
```

This allows portable code to inspect common scalar keys directly.

### Event names

Prefer Kmet-style kebab-case keywords:

```clojure
:session-start
:session-shutdown
:before-agent-start
:agent-start
:agent-end
:agent-settled
:tool-call
:tool-result
```

Translate them to PiG's underlying native event names internally.

Do not expose PiG's snake_case wire names as the primary Clojure API.

### Event argument order

Use:

```clojure
(fn [event ctx] ...)
```

for event handlers.

Document this as stable.

### Semantic compatibility tests

For each event marked portable, compare:

- input event map;
- handler order;
- nil behavior;
- replacement behavior;
- error behavior;
- cancellation behavior;
- whether each handler sees the original or previous handler's replacement.

Where Kmet currently differs from Pi semantics, document the difference rather than changing PiG to imitate the divergence.

The portability contract should target the semantically compatible intersection, not accidental behavior.

## Updates to Phase 4 — reload and lifecycle

Retain the original fresh-generation reload design.

Add lifecycle sequence:

```text
load replacement generation
    |
    v
evaluate namespace
    |
    v
init(new-api)
    |
    v
validate/stage extension state
    |
    v
publish new runner
    |
    v
invalidate old contexts
    |
    v
shutdown(old-api), when safe
    |
    v
host-owned deregistration / generation close
```

Exact ordering of `shutdown` relative to runner replacement must respect PiG's existing rule for in-flight callbacks.

Requirements:

- old `shutdown` cannot mutate the newly published generation;
- old API/context becomes stale after replacement;
- a failing replacement `init` leaves the current healthy generation running according to existing PiG reload policy;
- a failing old `shutdown` is diagnostic only and does not prevent teardown;
- `shutdown` is at-most-once per generation.

## Updates to Phase 5 — documentation and scaffold

### Revised scaffold

Prefer portable `.cljc` as the default let-go scaffold if Phase 0 proves it reliable:

```clojure
(ns extension
  (:require [pig.extension :as ext]))

(defn init [api]
  (ext/register-tool!
   api
   {:name "hello"
    :description "Return a greeting"
    :parameters
    {:type "object"
     :properties
     {:name {:type "string"}}
     :required ["name"]}
    :execute
    (fn [{:keys [name]}]
      {:content [{:type "text"
                  :text (str "Hello " name)}]})}))

(defn shutdown [_api]
  nil)
```

Generate `.lg` only when the user explicitly requests let-go-specific source.

### Add portability section

Documentation should include:

- which API calls intentionally match Kmet;
- `.cljc` guidance;
- `#?(:lg ...)` use;
- portable vs host-specific capabilities;
- avoiding JVM-only dependencies in shared code;
- recommended pure-core + thin-adapter structure;
- event semantics that differ between Kmet and PiG;
- examples tested on both hosts when available.

## Updates to Phase 6 — conformance

Add a second dimension of conformance.

### PiG semantic conformance

As in the original plan:

```text
native Go
subprocess Go/Rust/Python/Node
let-go
```

must observe the same PiG behavior for implemented capabilities.

### Kmet source-shape compatibility

Maintain a small set of `.cljc` fixtures that can be executed against both hosts.

At minimum:

1. tool-only extension;
2. command extension;
3. session-start event extension;
4. before-agent-start transform;
5. tool-call/tool-result transform;
6. pure helper namespace required from the entry namespace.

The goal is not complete behavioral identity between Kmet and PiG. The gate is:

- same source parses on both;
- same public extension calls are accepted;
- shared data shapes are materially equivalent;
- known semantic differences are explicit.

If CI cannot directly run Kmet, keep fixtures in a reusable location and add a standalone compatibility harness later.

## Revised implementation sequence

Update the original recommended commit sequence to:

1. **Dependency + embedding spike**
   - pin let-go;
   - retained callbacks;
   - value conversion.

2. **Portable source spike**
   - `.cljc`;
   - `:lg` reader conditionals;
   - namespace loading;
   - multi-file pure helper namespace.

3. **Lifecycle spike**
   - resolve/invoke `init [api]`;
   - optional `shutdown [api]`;
   - error behavior.

4. **Runtime coordinator**
   - process-global let-go isolation/concurrency rules.

5. **Source resolver**
   - `.lg`, `.cljc`, `extension.lg`, `extension.cljc`.

6. **Capability-map bridge skeleton**
   - construct Clojure `api`;
   - thin `pig.extension` wrappers;
   - produce a real `extension.Extension`.

7. **Tools + commands**
   - Kmet-compatible public shapes where semantically valid.

8. **Map-shaped callback context**
   - stable scalar fields;
   - live host closures;
   - stale/cancellation checks.

9. **Events**
   - kebab-case keywords;
   - `(event ctx)` signature;
   - table-driven native adapters.

10. **Startup/trust/inventory integration**

11. **Reload generations + shutdown lifecycle**

12. **Validation/test API/scaffold/docs**

13. **PiG conformance + Kmet-compatible `.cljc` fixtures**

## Revised package boundaries

Keep original Go package boundary:

```text
coding/extension/host/letgo/
```

Recommended files:

```text
doc.go
host.go
load.go
generation.go
namespace.go
values.go
functions.go
api.go
context.go
events.go
lifecycle.go
errors.go
testapi.go
```

Add embedded Clojure source namespaces:

```text
coding/extension/host/letgo/clj/
  pig/extension.cljc
  pig/context.cljc
```

Prefer `.cljc` for the bridge itself where feasible so syntax and behavior stay close to the portable target.

Do not expose `pig.internal.*` to extension authors unless required as an implementation detail. If low-level injected functions are needed, install them under a private/internal namespace and build the public facade over them.

## Revised main fixture

Replace the original top-level-registration fixture with:

```clojure
(ns pig-test.full
  (:require [pig.extension :as ext]))

(def calls (atom 0))

(defn init [api]
  (ext/register-tool!
   api
   {:name "lg_echo"
    :description "Echo text"
    :parameters
    {:type "object"
     :properties
     {:text {:type "string"}}
     :required ["text"]}
    :execute
    (fn [{:keys [text]}]
      (swap! calls inc)
      {:content [{:type "text" :text text}]
       :details {:calls @calls}})})

  (ext/register-command!
   api
   {:name "lg-status"
    :description "Show let-go extension status"
    :handler
    (fn [ctx _args]
      (when-let [notify (:notify ctx)]
        (notify (str "calls=" @calls) :info)))})

  (ext/on-event
   api
   :session-start
   (fn [_event _ctx]
     nil)))

(defn shutdown [_api]
  nil)
```

Create an equivalent Kmet fixture differing only in the required extension namespace if possible.

A stronger portability fixture may use a reader conditional only at require time:

```clojure
(ns portable.echo
  (:require
   #?(:lg [pig.extension :as ext]
      :default [kmet.extension :as ext])))
```

Everything after the `ns` form should ideally remain unchanged.

## Updated invariants

Keep all invariants from `plan.md`, with these additions.

11. **Activation is explicit.** Namespace evaluation defines code; `init [api]` activates the extension.

12. **Shutdown is advisory cleanup, not registration ownership.** Host-owned generation teardown removes registrations even if `shutdown` is absent or fails.

13. **Portable Clojure data first.** Public let-go API values should be ordinary maps, vectors, keywords, strings, numbers, booleans, nil, and functions unless a PiG capability fundamentally requires something else.

14. **Kmet compatibility is intentional but bounded.** Match Kmet signatures and shapes only when doing so preserves PiG's real semantics.

15. **No semantic regression for compatibility.** If Kmet currently approximates a Pi behavior differently, PiG keeps PiG/Pi semantics and the difference is documented.

16. **`.cljc` is the portability format.** Host-specific `.lg` remains available for let-go-only extensions.

17. **Keep reader conditionals near the edge.** Prefer host adapters over distributed `#?` branches.

18. **Library portability is separate from language portability.** Do not claim JVM/Babashka dependency compatibility merely because source is `.cljc`.

## Updated non-goals for v1

Add to the existing non-goals:

- full drop-in execution of arbitrary Kmet extensions;
- `kmet.tui.*` compatibility inside PiG;
- Babashka/JVM/Jolt library compatibility;
- `deps.edn` Maven/JAR dependency resolution for let-go extensions;
- full `extension.edn` Kmet manifest compatibility;
- emulating Kmet behaviors that intentionally diverge from Pi;
- common package/install metadata between Kmet and PiG;
- a finalized standalone `pi-clj.extension` library.

## Compatibility success criteria

In addition to the original definition of done, require:

- an exact `.cljc` extension loads dynamically in PiG through embedded let-go;
- its namespace defines `init [api]`, which PiG invokes after evaluation;
- optional `shutdown [api]` is invoked at most once on generation retirement;
- registration functions take explicit `api` as their first argument;
- callback contexts are Clojure map-shaped and preserve PiG stale/cancellation semantics through host-backed closures;
- public event keywords are kebab-case;
- event handlers use `(event ctx)`;
- at least one `.cljc` fixture containing pure helper logic and a registered tool runs with only a host-namespace adaptation between PiG and Kmet;
- no compatibility choice bypasses `coding/extension.API` or changes PiG's native extension semantics.

## Final v1 target

The first version should make this style realistic:

```clojure
(ns portable.review
  (:require
   #?(:lg [pig.extension :as ext]
      :default [kmet.extension :as ext])))

(defn classify [x]
  ;; pure portable Clojure
  ...)

(defn init [api]
  (ext/register-tool!
   api
   {:name "review"
    :description "Review an item"
    :parameters {...}
    :execute
    (fn [args]
      (classify args))})

  (ext/on-event
   api
   :session-start
   (fn [event ctx]
     ...)))

(defn shutdown [_api]
  nil)
```

The extension should not care whether its host is implemented in Clojure or Go for the common capability subset.

PiG remains authoritative for PiG semantics, Kmet remains authoritative for Kmet semantics, and `.cljc` portability exists at the deliberately aligned public Clojure boundary rather than by exposing either host's internals.
