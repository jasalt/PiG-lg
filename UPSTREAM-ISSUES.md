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

**PiG disposition:** The source loader submits all authored forms inside one `do` expression. The unwrapped call caused PiG's multi-form tool fixture to register no tools because its first form was only a namespace declaration. That loader mistake is fixed in PiG; it is not itself an upstream bug.
