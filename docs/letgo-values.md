# let-go value boundary (D89)

The internal let-go adapter converts JSON-like data at one boundary in `coding/extension/host/letgo/values.go`. This boundary is an inert part of the approved D89 source realization. It does not expose Go receivers, reflection, contexts, callbacks, or arbitrary VM objects.

## Host values

The boundary accepts nil, booleans, strings, signed and unsigned integers, finite floating-point values, string-keyed maps, arrays, and slices. Integers must be in the inclusive range `[-9007199254740991, 9007199254740991]`. This range prevents rounding when a native typed JSON decoder stores a number in an `any` field as `float64`. Floating-point values retain their existing IEEE-754 precision. NaN and infinity fail conversion.

Arrays and non-nil slices become vectors. Byte slices become numeric vectors, not strings. Nil slices and nil maps become nil. Empty non-nil containers stay empty.

Object keys matching `[A-Za-z_][A-Za-z0-9_]*` become keywords. All other object keys remain strings, except re-cased public keys, which are always keywords. The boundary visits host map fields in lexical order to make field-path errors repeatable. It does not promise insertion order.

Native public event and result data uses its existing JSON codec before conversion. JSON tags, custom marshalers, omission rules, and presence flags remain authoritative. A generic host-value conversion rejects structs, pointers, functions, channels, and non-string-keyed maps. Callers must use the public-data entry point only for public data, never for an API or context object.

## Interpreter values

The boundary accepts nil, booleans, safe integers, finite floats, strings, array vectors, persistent vectors, lists, maps, and persistent maps. It rejects keywords as scalar JSON values, symbols, functions, boxed Go objects, sets, lazy sequences, and other VM values. It never stringifies an unsupported value.

Object keys must be strings or unqualified keywords. A namespaced keyword fails conversion. A keyword and string with the same object-key spelling fail conversion rather than overwrite a field. Collections convert recursively. Errors identify the failing field or index. Nesting beyond 256 conversion frames fails, including cyclic maps. Width is proportional to the supplied finite collection; this boundary provides no process-memory isolation.

Typed result decoding uses the native JSON decoders. The three sealed extension unions use `UnmarshalInputEventResult`, `UnmarshalToolCallEvent`, and `UnmarshalToolResultEvent`. The native decoder decides whether a missing discriminator, unknown discriminator, null, omitted field, or wrong field type is valid. A native input-result union uses its corresponding native marshaler when sent to the interpreter. A tool result whose `:content` is a string is coerced to one text block before native decoding; this is input coercion only.

## Public key casing

Owner decision 1A on `PiG-18s.25` gives host-originated public data Kmet's kebab-case keys. Re-casing belongs to the entry point, not the value. `publicValue` re-cases event payloads, context reads (including model snapshots and session entries) and UI results. `toolResultValue` and `decodePublicValue` map returned results back. Tool arguments and `decodeValue` keep every key byte-for-byte.

A native key is re-cased only when it round-trips exactly. It must be lowerCamel: a lowercase letter, then lowercase letters and digits, with each later word starting with a single uppercase letter. `toolCallId` becomes `:tool-call-id`, `cacheWrite1h` becomes `:cache-write1h` and `inputCostPer1M` becomes `:input-cost-per1-m`. Keys with an uppercase run (an acronym such as `supportsOpenAIGrammarTools`), a leading uppercase letter, a digit first, `_` or `-` cross unchanged under the identifier rule above. Values are never re-cased, so type strings such as `before_agent_start` and `toolCall` stay native.

Returned results normalize each key back before decoding. A key that is the exact public spelling of a native key becomes that key. Every other key, including a native spelling such as `:isError`, passes unchanged. If two keys in one object name the same native field, for example `:is-error` and `:isError`, conversion fails and names the object's field path and both spellings. Host data that would publish two keys with one public spelling fails the same way. Keys that match no field pass to the native decoder, which ignores them as its strictness dictates.

The values of these native fields are opaque and keep their keys at any depth: `arguments` and `input` (model-produced tool arguments), `details`, `data`, `structuredContent`, `parameters` (JSON-schema bodies), `headers`, `samplingParams`, `chatTemplateArgs`, `chatTemplateKwargs`, `openRouterRouting`, `vercelGatewayRouting`, and the data-keyed maps `promptCache`, `thinkingLevelMap`, `toolGuidelines`, `toolSnippets` and `variants`. `coding/extension/host/letgo/casing_test.go` builds the inventory of every JSON tag reachable from the approved event, result, message, session-entry and model types by reflection. It checks each tag's round trip, that no two tags share a public spelling, that the verbatim exceptions are exactly the recorded set, and that every loosely typed field is declared either opaque or structured. A new tag or `any` field therefore fails the test until it has a decision.

## Ordered members

A Clojure map has no member order, so conversion never promises one. The one native object where order is semantic, the `before_agent_start` `sections` object, is presented as an ordered vector of `{:name :value}` maps instead of a map. Section names are data, so presenting them as values also keeps them from being re-cased. See the result hook section of [let-go registration](letgo-registration.md).

## Semantic limits

Conversion creates data snapshots. It does not preserve native aliases, object identity, or authored member order. In particular, JSON roundtripping does not implement `before_agent_start` ordered section mutation or shared collection replacement. Those behaviors require an explicit callback adapter and production-path tests. Codec tests do not prove loader, Session, or command integration.

Run the boundary tests with:

```bash
go test -race ./coding/extension/host/letgo -run 'Value|Convert|Casing' -count=10
```
