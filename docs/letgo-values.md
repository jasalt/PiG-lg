# let-go value boundary (D89)

The internal let-go adapter converts JSON-like data at one boundary in `coding/extension/host/letgo/values.go`. This boundary is an inert part of the approved D89 source realization. It does not expose Go receivers, reflection, contexts, callbacks, or arbitrary VM objects.

## Host values

The boundary accepts nil, booleans, strings, signed and unsigned integers, finite floating-point values, string-keyed maps, arrays, and slices. Integers must be in the inclusive range `[-9007199254740991, 9007199254740991]`. This range prevents rounding when a native typed JSON decoder stores a number in an `any` field as `float64`. Floating-point values retain their existing IEEE-754 precision. NaN and infinity fail conversion.

Arrays and non-nil slices become vectors. Byte slices become numeric vectors, not strings. Nil slices and nil maps become nil. Empty non-nil containers stay empty.

Object keys matching `[A-Za-z_][A-Za-z0-9_]*` become keywords. All other object keys remain strings. The boundary visits host map fields in lexical order to make field-path errors repeatable. It does not promise insertion order.

Native public event and result data uses its existing JSON codec before conversion. JSON tags, custom marshalers, omission rules, and presence flags remain authoritative. A generic host-value conversion rejects structs, pointers, functions, channels, and non-string-keyed maps. Callers must use the public-data entry point only for public data, never for an API or context object.

## Interpreter values

The boundary accepts nil, booleans, safe integers, finite floats, strings, array vectors, persistent vectors, lists, maps, and persistent maps. It rejects keywords as scalar JSON values, symbols, functions, boxed Go objects, sets, lazy sequences, and other VM values. It never stringifies an unsupported value.

Object keys must be strings or unqualified keywords. A namespaced keyword fails conversion. A keyword and string with the same object-key spelling fail conversion rather than overwrite a field. Collections convert recursively. Errors identify the failing field or index. Nesting beyond 256 conversion frames fails, including cyclic maps. Width is proportional to the supplied finite collection; this boundary provides no process-memory isolation.

Typed result decoding uses the native JSON decoders. The three sealed extension unions use `UnmarshalInputEventResult`, `UnmarshalToolCallEvent`, and `UnmarshalToolResultEvent`. The native decoder decides whether a missing discriminator, unknown discriminator, null, omitted field, or wrong field type is valid. A native input-result union uses its corresponding native marshaler when sent to the interpreter. A tool result whose `:content` is a string is coerced to one text block before native decoding; this is input coercion only.

## Semantic limits

Conversion creates data snapshots. It does not preserve native aliases, object identity, or authored member order. In particular, JSON roundtripping does not implement `before_agent_start` ordered section mutation or shared collection replacement. Those behaviors require an explicit callback adapter and production-path tests. Codec tests do not prove loader, Session, or command integration.

Run the boundary tests with:

```bash
go test -race ./coding/extension/host/letgo -run 'Value|Convert' -count=10
```
