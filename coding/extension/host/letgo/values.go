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

func toValue(input any) (vm.Value, error) {
	return convertToValue(reflect.ValueOf(input), "$", 0)
}

// publicValue uses the native JSON contract, including custom marshalers and omission rules.
// Callers must supply public event/result data, never a context, API, or callback receiver.
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
	return toValue(plain)
}

func convertToValue(value reflect.Value, path string, depth int) (vm.Value, error) {
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
		return convertToValue(value.Elem(), path, depth+1)
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
			item, err := convertToValue(value.Index(i), fmt.Sprintf("%s[%d]", path, i), depth+1)
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
		for _, key := range keys {
			name := key.String()
			item, err := convertToValue(value.MapIndex(key), fieldPath(path, name), depth+1)
			if err != nil {
				return nil, err
			}
			var objectKey vm.Value = vm.String(name)
			if identifierKey(name) {
				objectKey = vm.Keyword(name)
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

func fromValue(value vm.Value) (any, error) { return convertFromValue(value, "$", 0) }

func convertFromValue(value vm.Value, path string, depth int) (any, error) {
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
		return sequenceValue(value, path, depth)
	case vm.PersistentVector:
		return sequenceValue(value, path, depth)
	case *vm.PersistentVector:
		if value != nil {
			return sequenceValue(value, path, depth)
		}
	case *vm.List:
		if value != nil {
			return sequenceValue(value, path, depth)
		}
	case vm.Map:
		return objectValue(value, path, depth)
	case *vm.PersistentMap:
		if value != nil {
			return objectValue(value, path, depth)
		}
	}
	return nil, fmt.Errorf("%s: unsupported let-go value %T", path, value)
}

func sequenceValue(value vm.Sequable, path string, depth int) (any, error) {
	result := make([]any, 0)
	for seq, i := value.Seq(), 0; i < value.(vm.Counted).RawCount() && seq != nil; seq, i = seq.Next(), i+1 {
		item, err := convertFromValue(seq.First(), fmt.Sprintf("%s[%d]", path, len(result)), depth+1)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func objectValue(value vm.Sequable, path string, depth int) (any, error) {
	// Normalize all keys before values so a collision never silently overwrites a field.
	fields := make(map[string]vm.Value)
	for seq, i := value.Seq(), 0; i < value.(vm.Counted).RawCount() && seq != nil; seq, i = seq.Next(), i+1 {
		entry := seq.First().(vm.Seq)
		key := entry.First()
		var name string
		switch key := key.(type) {
		case vm.String:
			name = string(key)
		case vm.Keyword:
			if strings.Contains(string(key), "/") {
				return nil, fmt.Errorf("%s: namespaced keyword object key %q", path, string(key))
			}
			name = string(key)
		default:
			return nil, fmt.Errorf("%s: unsupported object key %T", path, key)
		}
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("%s: duplicate object key %q", path, name)
		}
		fields[name] = entry.Next().First()
	}
	result := make(map[string]any, len(fields))
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		item, err := convertFromValue(fields[name], fieldPath(path, name), depth+1)
		if err != nil {
			return nil, err
		}
		result[name] = item
	}
	return result, nil
}

// toolResultValue uses native text/image content decoding and retains explicit result-member presence.
// Map conversion does not supply authored insertion order; MemberOrder records deterministic snapshot order only.
func toolResultValue(value vm.Value) (agent.AgentToolResult, error) {
	plain, err := fromValue(value)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	object, ok := plain.(map[string]any)
	if !ok {
		return agent.AgentToolResult{}, fmt.Errorf("$: tool result must be an object")
	}
	if _, ok := object["content"].([]any); !ok {
		return agent.AgentToolResult{}, fmt.Errorf("$.content: tool result requires an array")
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

// decodeValue delegates typed unions and presence flags to their native JSON decoders.
// It does not preserve object aliases or ordered mutation semantics.
func decodeValue(value vm.Value, target any) error {
	plain, err := fromValue(value)
	if err != nil {
		return err
	}
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
