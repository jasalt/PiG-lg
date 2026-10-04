package letgo

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

func TestValueConvertScalarsAndContainers(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  any
	}{
		{"nil", nil, nil}, {"bool", false, false}, {"string", "é🐷", "é🐷"},
		{"signedMin", int64(-9007199254740991), int64(-9007199254740991)},
		{"signedMax", int64(9007199254740991), int64(9007199254740991)},
		{"unsignedMaxSafe", uint64(9007199254740991), int64(9007199254740991)},
		{"jsonInteger", json.Number("9007199254740991"), int64(9007199254740991)},
		{"float", 1.25, 1.25}, {"bytes", []byte{0, 255}, []any{int64(0), int64(255)}},
		{"array", [2]int{1, 2}, []any{int64(1), int64(2)}},
		{"empty", []int{}, []any{}}, {"nilSlice", []int(nil), nil},
		{"emptyMap", map[string]any{}, map[string]any{}},
		{"nilMap", map[string]any(nil), nil},
		{"nested", map[string]any{"name": "Ada", "a/b": []any{nil, true}}, map[string]any{"name": "Ada", "a/b": []any{nil, true}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value, err := toValue(test.input)
			if err != nil {
				t.Fatal(err)
			}
			result, err := fromValue(value)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result, test.want) {
				t.Fatalf("got %#v; want %#v", result, test.want)
			}
		})
	}
}

func TestValueConvertRejectsUnsafeAndUnsupported(t *testing.T) {
	for _, input := range []any{int64(math.MinInt64), int64(math.MaxInt64), int64(-9007199254740992), uint64(9007199254740992), json.Number("9007199254740993"), uint64(math.MaxInt64) + 1, uint64(math.MaxUint64), math.NaN(), math.Inf(1), json.Number("9223372036854775808"), func() {}, make(chan int), struct{ Name string }{"opaque"}, map[int]string{1: "x"}} {
		_, err := toValue(map[string]any{"payload": []any{input}})
		if err == nil || !strings.Contains(err.Error(), "$.payload[0]") {
			t.Fatalf("%T: %v", input, err)
		}
	}
	for _, input := range []vm.Value{vm.Int(9007199254740993), vm.Symbol("x"), vm.Keyword("x"), vm.NewBoxed("secret"), vm.Float(math.Inf(-1)), nil} {
		_, err := fromValue(vm.ArrayVector{input})
		if err == nil || !strings.Contains(err.Error(), "$[0]") {
			t.Fatalf("%T: %v", input, err)
		}
	}
}

func TestValueConvertKeys(t *testing.T) {
	value, err := toValue(map[string]any{"camelCase2": 1, "_key": 2, "a-b": 3, "1key": 4, "a/b": 5, "": 6, "é": 7})
	if err != nil {
		t.Fatal(err)
	}
	lookup := value.(vm.Lookup)
	if lookup.ValueAt(vm.Keyword("camelCase2")) != vm.Int(1) || lookup.ValueAt(vm.String("a-b")) != vm.Int(3) {
		t.Fatal("incorrect key spelling")
	}
	for _, test := range []struct {
		input vm.Map
		want  string
	}{
		{vm.Map{vm.Keyword("x"): vm.Int(1), vm.String("x"): vm.Int(2)}, "duplicate object key"},
		{vm.Map{vm.Keyword("ns/x"): vm.Int(1)}, "namespaced keyword"},
		{vm.Map{vm.Int(1): vm.Int(1)}, "unsupported object key"},
	} {
		for range 20 {
			_, err := fromValue(test.input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		}
	}
	for _, value := range []vm.Value{vm.NewPersistentVector(nil), vm.NewPersistentVector([]vm.Value{vm.Int(1)})} {
		if _, err := fromValue(value); err != nil {
			t.Fatal(err)
		}
	}
	list := vm.EmptyList.Cons(vm.String("last")).Cons(vm.Int(1))
	got, err := fromValue(list)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []any{int64(1), "last"}) {
		t.Fatalf("list: %#v", got)
	}
}

func TestValueConvertPublicPresence(t *testing.T) {
	// Native extension JSON is the denominator, not reflected Go field names.
	empty := ""
	for _, input := range []extension.BeforeAgentStartEventResult{{}, {SystemPrompt: &empty}} {
		value, err := publicValue(input)
		if err != nil {
			t.Fatal(err)
		}
		var got extension.BeforeAgentStartEventResult
		if err := decodePublicValue(value, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, input) {
			t.Fatalf("got %#v; want %#v", got, input)
		}
	}
	for _, test := range []struct {
		value vm.Value
		want  *bool
	}{
		{vm.Map{}, nil}, {vm.Map{vm.Keyword("is-error"): vm.NIL}, nil}, {vm.Map{vm.Keyword("is-error"): vm.FALSE}, new(false)}, {vm.Map{vm.Keyword("isError"): vm.FALSE}, new(false)},
	} {
		var got extension.ToolResultEventResult
		if err := decodePublicValue(test.value, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.IsError, test.want) {
			t.Fatalf("isError: %#v", got.IsError)
		}
	}
	var result extension.BeforeAgentStartEventResult
	if err := decodePublicValue(vm.Map{vm.Keyword("system-prompt"): vm.Int(1)}, &result); err == nil || !strings.Contains(err.Error(), "systemPrompt") {
		t.Fatalf("typed error: %v", err)
	}
	event := extension.BeforeAgentStartEvent{Type: "before_agent_start", Prompt: "test", SystemPrompt: "base"}
	value, err := publicValue(event)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	object := plain.(map[string]any)
	if object["system-prompt"] != "base" {
		t.Fatal(object)
	}
	if _, exists := object["images"]; exists {
		t.Fatal("omitempty images was exposed")
	}
}

func TestValueConvertNativeUnions(t *testing.T) {
	for _, input := range []extension.InputEventResult{extension.InputEventResultContinue{}, extension.InputEventResultHandled{}, extension.InputEventResultTransform{Text: "new text"}} {
		value, err := publicValue(input)
		if err != nil {
			t.Fatal(err)
		}
		var got extension.InputEventResult
		if err := decodePublicValue(value, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, input) {
			t.Fatalf("got %#v; want %#v", got, input)
		}
	}
	var inputResult extension.InputEventResult
	if err := decodePublicValue(vm.Map{vm.Keyword("action"): vm.String("unknown")}, &inputResult); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("union error: %v", err)
	}
	var call extension.ToolCallEvent
	if err := decodePublicValue(vm.Map{vm.Keyword("tool-name"): vm.String("custom")}, &call); err != nil {
		t.Fatal(err)
	}
	if _, ok := call.(extension.CustomToolCallEvent); !ok {
		t.Fatalf("custom union: %T", call)
	}
	var result extension.ToolResultEvent
	if err := decodePublicValue(vm.Map{vm.Keyword("tool-name"): vm.String("bash")}, &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(extension.BashToolResultEvent); !ok {
		t.Fatalf("builtin union: %T", result)
	}
	if err := decodePublicValue(vm.Map{}, &call); err == nil || !strings.Contains(err.Error(), "toolName") {
		t.Fatalf("missing discriminator: %v", err)
	}
}

func TestValueConvertStressAndCycles(t *testing.T) {
	input := make([]int, 10000)
	for i := range input {
		input[i] = i
	}
	value, err := toValue(input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range got.([]any) {
		if item != int64(i) {
			t.Fatalf("element %d: %v", i, item)
		}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := toValue(cycle); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("cycle: %v", err)
	}
	cyclicVM := vm.Map{}
	cyclicVM[vm.Keyword("self")] = cyclicVM
	if _, err := fromValue(cyclicVM); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("VM cycle: %v", err)
	}
}
