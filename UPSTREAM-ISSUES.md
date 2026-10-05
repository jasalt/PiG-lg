# Possible upstream issues

This file records reproducible upstream-dependency observations. It does not assert that an upstream maintainer has accepted a bug report. Confirm the intended contract before filing an issue. PiG adapter errors and missing local tools do not belong in this list.

## LG-1: `api.Def` redefinition does not update a retained function's Var

**Upstream:** [nooga/let-go](https://github.com/nooga/let-go), `v1.12.2`, as pinned in `go.mod`.

**Status:** Suspected embedding semantic defect. Reproduced locally on Linux/amd64 with Go 1.27.1. No upstream issue has been filed.

**Observation:** A retained interpreted function still reads the old value after the embedding API redefines a name. A fresh evaluation reads the new value. Redefining through an interpreted `(def x 2)` updates the retained function in the control case. The difference therefore depends on the native embedding operation, not ordinary lexical closure capture.

Save this program as `repro.go` outside the checkout. Run it from the PiG module with `go run /absolute/path/to/repro.go`. The module supplies the exact dependency pin.

```go
package main

import (
    "fmt"

    "github.com/nooga/let-go/pkg/api"
    "github.com/nooga/let-go/pkg/vm"
)

func main() {
    r, err := api.NewLetGo("repro")
    if err != nil { panic(err) }
    if err := r.Def("x", vm.Int(1)); err != nil { panic(err) }
    value, err := r.Run("(fn [] x)")
    if err != nil { panic(err) }
    if err := r.Def("x", vm.Int(2)); err != nil { panic(err) }
    current, err := r.Run("x")
    if err != nil { panic(err) }
    retained, err := value.(vm.Fn).Invoke(nil)
    if err != nil { panic(err) }
    fmt.Printf("current=%s retained=%s\n", current, retained)
}
```

Actual output:

```text
current=2 retained=1
```

Control: replace the second `r.Def("x", vm.Int(2))` with `r.Run("(def x 2)")`, keeping the error check. The control prints:

```text
current=2 retained=2
```

**Expected contract to confirm:** If `api.Def` means updating the existing namespace binding, both evaluations read `2`. If it intentionally creates a new Var, document that existing compiled references retain the previous Var.

**Source evidence:** `pkg/api/api.go:LetGo.Def` calls `Namespace.Def`. `pkg/vm/namespace.go:Namespace.Def` creates a new Var and replaces the registry entry. `Namespace.LookupOrAdd`, used by interpreted definitions, reuses an existing Var. The retained function still owns its reference to the old Var.

**PiG disposition:** Install native adapter functions before compiling authored callbacks. Do not rely on later `Generation.Def` calls to hot-swap a binding captured by an existing callback. PiG reload replaces the owned generation instead. This observation does not justify weakening reload or retained-callback tests.

## LG-2: `api.Run` accepts trailing forms without evaluating them

**Upstream:** [nooga/let-go](https://github.com/nooga/let-go), `v1.12.2`.

**Status:** Confirmed behavior; possible API-documentation or validation issue, not a confirmed interpreter defect. The method takes an `expr`, so accepting one expression may be intentional. No upstream issue has been filed.

Minimal program:

```go
package main

import (
    "fmt"

    "github.com/nooga/let-go/pkg/api"
)

func main() {
    r, err := api.NewLetGo("repro")
    if err != nil { panic(err) }
    _, err = r.Run(`(def first 1) (throw (ex-info "trailing form ran" {}))`)
    fmt.Printf("error=%v\n", err)
}
```

Actual output:

```text
error=<nil>
```

Control: pass `(do (def first 1) (throw (ex-info "trailing form ran" {})))`. The control returns an error containing `trailing form ran`.

**Expected contract to confirm:** A single-expression API can reject non-comment trailing forms or explicitly document that it ignores them. A source-evaluation API must evaluate every form and propagate the trailing error.

**PiG disposition:** The source loader uses `compiler.CompileMultiple` under the generation coordinator. It compiles and evaluates forms sequentially, so namespace and require effects precede later compilation. Wrapping forms in `api.Run("(do ...)")` is not a source-loader equivalent: that API compiles the entire expression before evaluating its namespace effects. The initial unwrapped call registered no tools because the first form was only a namespace declaration. These source-loader mistakes are fixed in PiG; they are not themselves upstream bugs.

## LG-3: `CompileMultiple` silently accepts EOF inside an unfinished form

**Upstream:** [nooga/let-go](https://github.com/nooga/let-go), `v1.12.2`.

**Status:** Reproduced parser error suppression. No upstream issue has been filed.

Run this program from the pinned PiG module:

```go
package main

import (
    "fmt"
    "strings"

    "github.com/nooga/let-go/pkg/api"
    "github.com/nooga/let-go/pkg/compiler"
    "github.com/nooga/let-go/pkg/rt"
    "github.com/nooga/let-go/pkg/vm"
)

func main() {
    r, err := api.NewLetGo("repro")
    if err != nil { panic(err) }
    source := "(def incomplete"
    c := compiler.NewTransientCompiler(vm.NewConsts(), rt.CurrentNS.Deref().(*vm.Namespace))
    _, _, err = c.CompileMultiple(strings.NewReader(source))
    fmt.Printf("CompileMultiple error=%v\n", err)
    _, err = r.Run(source)
    fmt.Printf("api.Run error=%v\n", err)
}
```

Actual output begins:

```text
CompileMultiple error=<nil>
api.Run error=Syntax error reading source at (<default>:1:16).
```

The second error has a nested EOF cause. Expected: the multi-form API rejects the unfinished list as well. EOF between complete forms is valid; EOF inside a form is not.

**Source evidence:** `compiler.Context.CompileMultiple` calls `isErrorEOF` on the reader error and breaks even when EOF is nested inside a syntax error. The pinned `read-all-string` primitive explicitly distinguishes these cases and propagates mid-form errors.

**PiG disposition:** `Generation.RunSource` validates source with the pinned strict `read-all-string` reader before compiling/evaluating forms. This parses twice and is a deliberate correctness cost until the multi-form compiler rejects incomplete forms. `TestLoadToolRegistrationAndSourceErrors` failed when the malformed source began returning success, and passes with the strict pre-read. No dependency patch or weaker assertion is used.

## LG-4: `vm.FormSource` never evicts and pins every parsed form

**Upstream:** [nooga/let-go](https://github.com/nooga/let-go), `v1.12.2`.

**Status:** Reproduced unbounded retention. No upstream issue has been filed. Upstream documents the cause and offers `Reset`.

Compile any source repeatedly in one process and watch the table grow:

```go
package main

import (
    "fmt"

    "github.com/nooga/let-go/pkg/api"
    "github.com/nooga/let-go/pkg/vm"
)

func main() {
    for i := 0; i < 3; i++ {
        r, err := api.NewLetGo("repro")
        if err != nil { panic(err) }
        _, _ = r.Run("(defn f [x] (let [a (inc x)] (* a 2)))")
        fmt.Println("FormSource.Len =", vm.FormSource.Len())
    }
}
```

The count rises on every round and never falls. Each entry is keyed by the parsed `*List` or `*Cons` and so keeps that form, and what it references, alive. The `FormSource.Reset` documentation states the map "never evicts" and tells callers that re-compile in a loop to reset it between rounds.

**Measured in PiG:** one representative extension (helpers, a tool, a command, an event handler) left about 190 entries per generation. Over 300 load-and-close cycles the live heap after garbage collection grew from 7.1 MiB to 32.2 MiB, about 84 KiB per generation.

**Source evidence:** `vm/source.go` declares `FormSource` as a process-wide map with `Set`, `Get`, `Len` and `Reset`, and nothing removes an entry. The compiler reads it only while compiling.

**PiG disposition:** `releaseFormSources` in `coding/extension/host/letgo/host.go` calls `Reset` at the end of every serialized VM entry, when no compile is reading the table. Runtime errors take their locations from each chunk's own source map, not from this table. `TestLifetimeRepeatedGenerationsLeaveNoOwnedState` and `TestLifetimeHeapDoesNotGrowWithGenerations` fail when the call is disabled and pass with it. No dependency patch is used.

## LG-5: the native runtime does not compile for Windows

**Upstream:** [nooga/let-go](https://github.com/nooga/let-go), `v1.12.2`, commit `9c9a3d636c4eda8b1da3d08612aeb56b3795cc1e`.

**Status:** Reproduced dependency build failure with Go 1.27.1 on Linux/amd64. No upstream issue has been filed. Windows support is a dependency contract to confirm, not an assumed upstream promise.

Run these compile-only probes from the pinned PiG module. They do not import PiG's adapter or run a Windows executable:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build github.com/nooga/let-go/pkg/rt
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build github.com/nooga/let-go/pkg/rt
```

Both fail in `pkg/rt/term.go`, beginning with:

```text
undefined: unix.SIGWINCH
undefined: unix.PollFd
undefined: unix.POLLIN
undefined: unix.Poll
```

Controls on the same dependency pin and Go toolchain pass:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build github.com/nooga/let-go/pkg/rt
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build github.com/nooga/let-go/pkg/rt
```

**Source evidence:** `pkg/rt/term.go` has the build constraint `!js && !plan9 && !wasip1`, which includes Windows. Its `setupWinch`, `nativeKeySource.readRaw` and `nativeKeySource.rawPending` implementations require Unix signals, polling and ioctls. Importing the runtime compiles these operations even when an embedder never calls a terminal primitive.

**PiG disposition:** The Windows branches of `make vet` and therefore unmodified `make check` and `make verify` fail on this dependency. The owner explicitly excludes Windows from the let-go v1 regression gate (`PiG-18s.42`); the compile failure remains deferred and unfixed. Qualification runs native and Android vet separately and omits the combined `vet` prerequisite from the remaining gates. This exception does not establish Windows support or turn a native let-go test pass into platform evidence.
