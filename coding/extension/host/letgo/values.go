package letgo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
)

// pig additive (D89): conversion admits only finite JSON data, not reflective Go receivers or arbitrary VM objects.
// Identifier keys match [A-Za-z_][A-Za-z0-9_]*; other keys remain strings.
// Maps are value snapshots, not aliases or insertion-ordered mutation channels.
// The depth limit rejects cyclic containers and bounds recursive stack use.
const valueDepthLimit = 256

// Integer values must survive native JSON decoding through float64 without rounding.
const maxSafeInteger = 1<<53 - 1

func integerValue(number int64, path string) (vm.Value, error) {
	if number < -maxSafeInteger || number > maxSafeInteger {
		return nil, fmt.Errorf("%s: integer outside exact JSON range", path)
	}
	return vm.Int(number), nil
}

// keyMode selects how object keys cross the boundary. Re-casing is a property of the entry point, never of the value.
type keyMode uint8

const (
	// verbatimKeys keeps every key byte-for-byte: tool arguments, registration maps and opaque subtrees.
	verbatimKeys keyMode = iota
	// publicKeys maps native camelCase field names to kebab-case keywords for host-originated public data and back for results.
	publicKeys
)

// opaqueFields name native fields whose values are extension, model or provider data, or maps keyed by data.
// pig additive (D89): their keys are never re-cased, at any depth.
var opaqueFields = map[string]bool{
	"arguments":            true, // model-produced tool arguments
	"chatTemplateArgs":     true, // provider chat-template values
	"chatTemplateKwargs":   true, // provider chat-template values
	"data":                 true, // custom entry data and provider handles
	"details":              true, // tool and custom-message details
	"headers":              true, // provider header maps
	"input":                true, // tool call arguments
	"openRouterRouting":    true, // provider routing config
	"parameters":           true, // JSON-schema bodies
	"promptCache":          true, // keyed by retention tier
	"samplingParams":       true, // provider parameter maps
	"structuredContent":    true, // tool JSON results
	"thinkingLevelMap":     true, // keyed by thinking level
	"toolGuidelines":       true, // keyed by tool name
	"toolSnippets":         true, // keyed by tool name
	"variants":             true, // keyed by sampling variant
	"vercelGatewayRouting": true, // provider routing config
}

// publicKey returns the kebab-case spelling of a native key, or false when the key does not round-trip exactly.
// Only lowerCamel keys without uppercase runs or separators are re-cased: toolCallId is tool-call-id, cacheWrite1h is cache-write1h.
func publicKey(native string) (string, bool) {
	public, ok := kebabCase(native)
	if !ok {
		return "", false
	}
	back, ok := camelCase(public)
	return public, ok && back == native
}

// nativeKey inverts publicKey; a key that is not the exact public spelling of a native key is returned unchanged.
func nativeKey(public string) string {
	native, ok := camelCase(public)
	if !ok {
		return public
	}
	if back, ok := kebabCase(native); !ok || back != public {
		return public
	}
	return native
}

// kebabCase splits [a-z][a-z0-9]* followed by single uppercase letters, each starting a word.
func kebabCase(native string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(native); i++ {
		c := native[i]
		switch {
		case lowerOrDigit(c) && (i > 0 || isLower(c)):
			b.WriteByte(c)
		case isUpper(c) && i > 0 && !isUpper(native[i-1]):
			b.WriteByte('-')
			b.WriteByte(c + 'a' - 'A')
		default:
			return "", false
		}
	}
	return b.String(), native != ""
}

// camelCase joins - separated [a-z][a-z0-9]* words, capitalizing every word after the first.
func camelCase(public string) (string, bool) {
	var b strings.Builder
	for i, word := range strings.Split(public, "-") {
		if word == "" || !isLower(word[0]) {
			return "", false
		}
		for j := 1; j < len(word); j++ {
			if !lowerOrDigit(word[j]) {
				return "", false
			}
		}
		if i > 0 {
			b.WriteByte(word[0] - 'a' + 'A')
			word = word[1:]
		}
		b.WriteString(word)
	}
	return b.String(), true
}

func isLower(c byte) bool      { return c >= 'a' && c <= 'z' }
func isUpper(c byte) bool      { return c >= 'A' && c <= 'Z' }
func lowerOrDigit(c byte) bool { return isLower(c) || c >= '0' && c <= '9' }

func toValue(input any) (vm.Value, error) {
	return convertToValue(reflect.ValueOf(input), "$", 0, verbatimKeys)
}

// publicValue uses the native JSON contract, including custom marshalers and omission rules, then re-cases native field names.
// Callers must supply host-originated public data (events, context reads, model info), never a context, API, or callback receiver.
func publicValue(input any) (vm.Value, error) {
	var err error
	var data []byte
	if result, ok := input.(extension.InputEventResult); ok {
		data, err = extension.MarshalInputEventResult(result)
	} else {
		data, err = json.Marshal(input)
	}
	if err != nil {
		return nil, fmt.Errorf("$: encode public data: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var plain any
	if err := decoder.Decode(&plain); err != nil {
		return nil, fmt.Errorf("$: decode public data: %w", err)
	}
	return convertToValue(reflect.ValueOf(plain), "$", 0, publicKeys)
}

func convertToValue(value reflect.Value, path string, depth int, mode keyMode) (vm.Value, error) {
	if depth > valueDepthLimit {
		return nil, fmt.Errorf("%s: value nesting exceeds %d", path, valueDepthLimit)
	}
	if !value.IsValid() {
		return vm.NIL, nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return vm.NIL, nil
		}
		return convertToValue(value.Elem(), path, depth+1, mode)
	}
	if value.Type() == reflect.TypeFor[json.Number]() {
		number := value.Interface().(json.Number)
		if integer, err := number.Int64(); err == nil {
			return integerValue(integer, path)
		}
		if !strings.ContainsAny(string(number), ".eE") {
			return nil, fmt.Errorf("%s: integer outside signed 64-bit range", path)
		}
		floating, err := number.Float64()
		if err != nil {
			return nil, fmt.Errorf("%s: invalid number: %w", path, err)
		}
		return finiteValue(floating, path)
	}
	switch value.Kind() {
	case reflect.Bool:
		return vm.Boolean(value.Bool()), nil
	case reflect.String:
		return vm.String(value.String()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return integerValue(value.Int(), path)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		number := value.Uint()
		if number > maxSafeInteger {
			return nil, fmt.Errorf("%s: unsigned integer outside exact JSON range", path)
		}
		return integerValue(int64(number), path)
	case reflect.Float32, reflect.Float64:
		return finiteValue(value.Float(), path)
	case reflect.Array, reflect.Slice:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return vm.NIL, nil
		}
		result := make(vm.ArrayVector, value.Len())
		for i := range value.Len() {
			item, err := convertToValue(value.Index(i), fmt.Sprintf("%s[%d]", path, i), depth+1, mode)
			if err != nil {
				return nil, err
			}
			result[i] = item
		}
		return result, nil
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%s: object keys must be strings", path)
		}
		if value.IsNil() {
			return vm.NIL, nil
		}
		keys := value.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		result := vm.EmptyPersistentMap
		spellings := make(map[string]string, len(keys))
		for _, key := range keys {
			name, itemMode := key.String(), mode
			spelling, recased := name, false
			if mode == publicKeys {
				if public, ok := publicKey(name); ok {
					spelling, recased = public, true
				}
				if opaqueFields[name] {
					itemMode = verbatimKeys
				}
			}
			if previous, exists := spellings[spelling]; exists {
				return nil, fmt.Errorf("%s: keys %q and %q have the same public spelling %q", path, previous, name, spelling)
			}
			spellings[spelling] = name
			item, err := convertToValue(value.MapIndex(key), fieldPath(path, spelling), depth+1, itemMode)
			if err != nil {
				return nil, err
			}
			var objectKey vm.Value = vm.String(spelling)
			if recased || identifierKey(spelling) {
				objectKey = vm.Keyword(spelling)
			}
			result = result.Assoc(objectKey, item).(*vm.PersistentMap)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("%s: unsupported host value %s", path, value.Type())
	}
}

func finiteValue(number float64, path string) (vm.Value, error) {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return nil, fmt.Errorf("%s: number must be finite", path)
	}
	return vm.Float(number), nil
}

func identifierKey(key string) bool {
	for i, c := range key {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return key != ""
}

func fieldPath(path, key string) string {
	if identifierKey(key) {
		return path + "." + key
	}
	return path + "[" + strconv.Quote(key) + "]"
}

func fromValue(value vm.Value) (any, error) { return convertFromValue(value, "$", 0, verbatimKeys) }

// fromPublicValue converts a returned result, mapping kebab-case keys back to native field names outside opaque fields.
func fromPublicValue(value vm.Value) (any, error) { return convertFromValue(value, "$", 0, publicKeys) }

func convertFromValue(value vm.Value, path string, depth int, mode keyMode) (any, error) {
	if depth > valueDepthLimit {
		return nil, fmt.Errorf("%s: value nesting exceeds %d", path, valueDepthLimit)
	}
	switch value := value.(type) {
	case *vm.Nil:
		if value == vm.NIL {
			return nil, nil
		}
	case vm.Boolean:
		return bool(value), nil
	case vm.Int:
		if _, err := integerValue(int64(value), path); err != nil {
			return nil, err
		}
		return int64(value), nil
	case vm.Float:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("%s: number must be finite", path)
		}
		return float64(value), nil
	case vm.String:
		return string(value), nil
	case vm.ArrayVector:
		return sequenceValue(value, path, depth, mode)
	case vm.PersistentVector:
		return sequenceValue(value, path, depth, mode)
	case *vm.PersistentVector:
		if value != nil {
			return sequenceValue(value, path, depth, mode)
		}
	case *vm.List:
		if value != nil {
			return sequenceValue(value, path, depth, mode)
		}
	case vm.Map:
		return objectValue(value, path, depth, mode)
	case *vm.PersistentMap:
		if value != nil {
			return objectValue(value, path, depth, mode)
		}
	}
	return nil, fmt.Errorf("%s: unsupported let-go value %T", path, value)
}

func sequenceValue(value vm.Sequable, path string, depth int, mode keyMode) (any, error) {
	result := make([]any, 0)
	for seq, i := value.Seq(), 0; i < value.(vm.Counted).RawCount() && seq != nil; seq, i = seq.Next(), i+1 {
		item, err := convertFromValue(seq.First(), fmt.Sprintf("%s[%d]", path, len(result)), depth+1, mode)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func objectValue(value vm.Sequable, path string, depth int, mode keyMode) (any, error) {
	// Normalize all keys before values so a collision never silently overwrites a field.
	// In public mode re-casing is the normalization, so :is-error and :isError together are a collision.
	fields := make(map[string]vm.Value)
	spellings := make(map[string]string)
	for seq, i := value.Seq(), 0; i < value.(vm.Counted).RawCount() && seq != nil; seq, i = seq.Next(), i+1 {
		entry := seq.First().(vm.Seq)
		key := entry.First()
		var spelling string
		switch key := key.(type) {
		case vm.String:
			spelling = string(key)
		case vm.Keyword:
			if strings.Contains(string(key), "/") {
				return nil, fmt.Errorf("%s: namespaced keyword object key %q", path, string(key))
			}
			spelling = string(key)
		default:
			return nil, fmt.Errorf("%s: unsupported object key %T", path, key)
		}
		name := spelling
		if mode == publicKeys {
			name = nativeKey(spelling)
		}
		if previous, exists := spellings[name]; exists {
			if previous == spelling {
				return nil, fmt.Errorf("%s: duplicate object key %q", path, name)
			}
			return nil, fmt.Errorf("%s: keys %q and %q both name field %q", path, min(previous, spelling), max(previous, spelling), name)
		}
		spellings[name] = spelling
		fields[name] = entry.Next().First()
	}
	result := make(map[string]any, len(fields))
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		itemMode := mode
		if opaqueFields[name] {
			itemMode = verbatimKeys
		}
		item, err := convertFromValue(fields[name], fieldPath(path, spellings[name]), depth+1, itemMode)
		if err != nil {
			return nil, err
		}
		result[name] = item
	}
	return result, nil
}

// toolResultValue uses native text/image content decoding and retains explicit result-member presence.
// Map conversion does not supply authored insertion order; MemberOrder records deterministic snapshot order only.
// Results are public data: :is-error decodes as isError while :details keeps its keys.
func toolResultValue(value vm.Value) (agent.AgentToolResult, error) {
	plain, err := fromPublicValue(value)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	object, ok := plain.(map[string]any)
	if !ok {
		return agent.AgentToolResult{}, fmt.Errorf("$: tool result must be an object")
	}
	// pig additive (D89): Kmet's string :content is input shorthand for one text block; native output keeps blocks.
	switch content := object["content"].(type) {
	case []any:
	case string:
		object["content"] = []any{map[string]any{"type": "text", "text": content}}
	default:
		return agent.AgentToolResult{}, fmt.Errorf("$.content: tool result requires a string or an array")
	}
	data, err := json.Marshal(object)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	var message ai.ToolResultMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("$: tool result: %w", err)
	}
	var extra struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
		Terminate         bool            `json:"terminate"`
	}
	if err := json.Unmarshal(data, &extra); err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("$: tool result: %w", err)
	}
	return agent.AgentToolResult{Content: message.Content, Details: message.Details, IsError: message.IsError, Usage: message.Usage, StructuredContent: extra.StructuredContent, Terminate: extra.Terminate, MemberOrder: slices.Sorted(maps.Keys(object))}, nil
}

// decodeValue delegates typed unions and presence flags to their native JSON decoders, keeping keys verbatim.
// It does not preserve object aliases or ordered mutation semantics.
func decodeValue(value vm.Value, target any) error {
	plain, err := fromValue(value)
	if err != nil {
		return err
	}
	return decodePlain(plain, target)
}

// decodePublicValue decodes a typed result returned for host-originated public data, mapping kebab-case keys back first.
// Unknown keys pass to the native decoder, which decides whether to ignore them.
func decodePublicValue(value vm.Value, target any) error {
	plain, err := fromPublicValue(value)
	if err != nil {
		return err
	}
	return decodePlain(plain, target)
}

func decodePlain(plain any, target any) error {
	data, err := json.Marshal(plain)
	if err != nil {
		return fmt.Errorf("$: encode result: %w", err)
	}
	switch target := target.(type) {
	case *extension.InputEventResult:
		*target, err = extension.UnmarshalInputEventResult(data)
	case *extension.ToolCallEvent:
		*target, err = extension.UnmarshalToolCallEvent(data)
	case *extension.ToolResultEvent:
		*target, err = extension.UnmarshalToolResultEvent(data)
	default:
		err = json.Unmarshal(data, target)
	}
	if err != nil {
		return fmt.Errorf("$: decode result: %w", err)
	}
	return nil
}
