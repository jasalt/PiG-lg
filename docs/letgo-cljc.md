# Portable .cljc source on let-go (D89)

These are measured observations for pinned let-go v1.12.2. They describe interpreter behavior inside the let-go generation coordinator. They do not claim that PiG source discovery recognizes `.cljc` entries; canonical source resolution is a separate change.

## Measured behavior

- `Load` evaluates an exact `.cljc` entry file. The entry namespace comes from its `ns` form.
- `require` resolves namespaces from the generation load root, which is the entry directory by default. For each namespace it tries `.lg`, then `.cljc`, then `.clj`. A same-named `.lg` helper shadows a `.cljc` helper, and a `.cljc` helper shadows a `.clj` helper. A `.clj` helper is reachable through `require` even though `.clj` is not an extension entry form.
- Reader conditionals select `:lg` and fall back to `:default`. Splicing `#?@` and conditionals inside `ns` `:require` work. So an entry can use `(:require #?(:lg [pig.extension :as ext] :default [kmet.extension :as ext]))`.
- Two generations whose roots each define the same helper namespace stay isolated. Each tool sees its own helper.
- `test/fixtures/extensions/letgo/cljc/portable/core.cljc` prints the same report under let-go and Babashka. `coding/extension/host/letgo/cljc_test.go` asserts the let-go output against `report.golden`. Run `test/fixtures/extensions/letgo/cljc/check-bb.sh` to assert the Babashka output against the same golden. Babashka is not a repository test prerequisite.

## Portability limits

- Hash-map iteration and print order differ between let-go and Babashka. For example, `(frequencies (seq "abca"))` printed in a different key order. Portable code must not depend on unordered map order; use sorted maps where order is observable.
- let-go shims some JVM-looking forms: `java.util.UUID/randomUUID`, `System/getProperty`, `.getBytes` on strings and `definterface` all evaluate. Their success does not imply JVM semantics. Keep such calls out of shared code.
- Other JVM constructs fail at load: `Thread/sleep`, `(java.io.File. ...)`, `import`, `proxy` and `reify` of Java interfaces. The error names the entry path, the `load` phase and the unresolvable symbol, for example `Can't resolve Thread/sleep in this context`. Compile errors carry no line number.

Run the observations with:

```bash
go test -race ./coding/extension/host/letgo -run Cljc -count=3
test/fixtures/extensions/letgo/cljc/check-bb.sh
```
