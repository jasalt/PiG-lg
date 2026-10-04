package letgo

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	codingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
)

// casingRoots are the approved event, result and context types whose JSON crosses the public boundary.
// Union members are listed explicitly because their interfaces and custom marshalers hide them from reflection.
var casingRoots = []any{
	extension.SessionStartEvent{}, extension.SessionShutdownEvent{}, extension.AgentStartEvent{}, extension.AgentEndEvent{}, extension.AgentSettledEvent{},
	extension.BeforeAgentStartEvent{}, extension.BeforeAgentStartEventResult{}, extension.ToolCallEventResult{}, extension.ToolResultEventResult{},
	extension.BashToolCallEvent{}, extension.PowerShellToolCallEvent{}, extension.ReadToolCallEvent{}, extension.EditToolCallEvent{}, extension.WriteToolCallEvent{},
	extension.GrepToolCallEvent{}, extension.FindToolCallEvent{}, extension.LsToolCallEvent{}, extension.CustomToolCallEvent{},
	extension.BashToolResultEvent{}, extension.PowerShellToolResultEvent{}, extension.ReadToolResultEvent{}, extension.EditToolResultEvent{}, extension.WriteToolResultEvent{},
	extension.GrepToolResultEvent{}, extension.FindToolResultEvent{}, extension.LsToolResultEvent{}, extension.CustomToolResultEvent{},
	extension.InputEventResultContinue{}, extension.InputEventResultHandled{}, extension.InputEventResultTransform{},
	extension.ToolInfo{}, agent.UserMessage{}, agent.AssistantMessage{}, agent.ToolResultMessage{},
	ai.UserMessage{}, ai.AssistantMessage{}, ai.ToolResultMessage{}, ai.SystemMessage{}, ai.TextContent{}, ai.ImageContent{}, ai.ThinkingContent{}, ai.ToolCall{}, ai.Usage{},
	ai.ModelCompat{}, ai.CostTier{}, ai.ModelInputLimits{},
	codingagent.SessionEntryBase{}, codingagent.MessageEntry{}, codingagent.ModelChangeEntry{}, codingagent.UsageEntry{}, codingagent.CompactionEntry{}, codingagent.BranchSummaryEntry{},
	codingagent.CustomEntry{}, codingagent.CustomMessageEntry{}, codingagent.LabelEntry{}, codingagent.SessionInfoEntry{}, codingagent.BashExecutionEntry{},
}

// verbatimTags are reachable native tags that do not round-trip through kebab-case and so cross unchanged.
var verbatimTags = []string{"supportsOpenAIGrammarTools"}

// looseStructuredFields are loosely typed fields that carry host-originated native shapes, so their keys are re-cased.
var looseStructuredFields = []string{"code", "content", "display", "images", "messages", "sourceInfo", "systemMessage", "usage"}

type casingInventory struct {
	tags  map[string][]string // tag -> declaring types
	loose map[string]bool     // tags declared with an any, raw JSON or map type
}

func reachableCasingTags(t *testing.T) casingInventory {
	t.Helper()
	inventory := casingInventory{tags: map[string][]string{}, loose: map[string]bool{}}
	seen := map[reflect.Type]bool{}
	raw := reflect.TypeFor[json.RawMessage]()
	var walk func(reflect.Type)
	walk = func(declared reflect.Type) {
		for declared.Kind() == reflect.Pointer || declared.Kind() == reflect.Slice || declared.Kind() == reflect.Array || declared.Kind() == reflect.Map {
			declared = declared.Elem()
		}
		if declared.Kind() != reflect.Struct || seen[declared] {
			return
		}
		seen[declared] = true
		for i := range declared.NumField() {
			field := declared.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if field.Anonymous && name == "" {
				walk(field.Type)
				continue
			}
			if name == "" || name == "-" || !field.IsExported() {
				continue // untagged fields belong to custom-marshaled carriers whose members are roots
			}
			inventory.tags[name] = append(inventory.tags[name], declared.Name())
			elem := field.Type
			for elem.Kind() == reflect.Pointer || elem.Kind() == reflect.Slice {
				elem = elem.Elem()
			}
			if field.Type == raw || elem.Kind() == reflect.Interface || elem.Kind() == reflect.Map || elem.ConvertibleTo(raw) && elem.Kind() != reflect.String {
				inventory.loose[name] = true
			}
			if !opaqueFields[name] {
				walk(field.Type)
			}
		}
	}
	for _, root := range casingRoots {
		walk(reflect.TypeOf(root))
	}
	// Model info is a projected map, so its keys come from a fully populated projection rather than struct tags.
	limits := ai.ModelInputLimits{MaxRequestBytes: 1}
	model := &ai.Model{ID: "m", ProviderMeta: ai.ProviderMetadata{Headers: map[string]string{"X-Key": "v"}, Compat: &ai.ModelCompat{SupportsStore: new(true)}}, ThinkingLevelMap: ai.ThinkingLevelMap{"low": nil}, SamplingParams: map[string]any{"top_p": 1}, PromptCache: ai.ModelPromptCache{"short": 300}, InputLimits: &limits}
	model.Capabilities.CostTiers = []ai.CostTier{{}}
	var collect func(map[string]any)
	collect = func(object map[string]any) {
		for name, value := range object {
			inventory.tags[name] = append(inventory.tags[name], "ModelInfo")
			if nested, ok := value.(map[string]any); ok && !opaqueFields[name] {
				collect(nested)
			}
		}
	}
	data, err := json.Marshal(extension.ModelInfo(model))
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(data, &projected); err != nil {
		t.Fatal(err)
	}
	collect(projected)
	return inventory
}

func TestCasingInventoryCoversEveryReachableTag(t *testing.T) {
	inventory := reachableCasingTags(t)
	if len(inventory.tags) < 100 {
		t.Fatalf("inventory found only %d tags; reflection roots are broken", len(inventory.tags))
	}
	publicSpellings := map[string]string{}
	var verbatim []string
	for _, tag := range slices.Sorted(maps.Keys(inventory.tags)) {
		public, ok := publicKey(tag)
		if !ok {
			verbatim = append(verbatim, tag)
			public = tag
		} else if back := nativeKey(public); back != tag {
			t.Errorf("%s: public %q maps back to %q", tag, public, back)
		}
		if previous, exists := publicSpellings[public]; exists {
			t.Errorf("tags %q and %q share public spelling %q", previous, tag, public)
		}
		publicSpellings[public] = tag
		value, err := publicValue(map[string]any{tag: map[string]any{"innerKey": 1}})
		if err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		inner := value.(vm.Lookup).ValueAt(vm.Keyword(public))
		if inner == vm.NIL {
			t.Errorf("%s: public map has no %s keyword in %v", tag, public, value)
			continue
		}
		wantInner := "inner-key"
		if opaqueFields[tag] {
			wantInner = "innerKey"
		}
		if inner.(vm.Lookup).ValueAt(vm.Keyword(wantInner)) == vm.NIL {
			t.Errorf("%s: nested key should be %s in %v", tag, wantInner, inner)
		}
		plain, err := fromPublicValue(value)
		if err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		if want := map[string]any{tag: map[string]any{"innerKey": int64(1)}}; !reflect.DeepEqual(plain, want) {
			t.Errorf("%s: round trip %#v, want %#v", tag, plain, want)
		}
	}
	if !slices.Equal(verbatim, verbatimTags) {
		t.Errorf("verbatim tags %q, want %q; decide each new acronym or separator tag", verbatim, verbatimTags)
	}
	// Every loosely typed field needs an explicit opaque or structured decision, and no decision may be stale.
	for _, name := range slices.Sorted(maps.Keys(inventory.loose)) {
		if !opaqueFields[name] && !slices.Contains(looseStructuredFields, name) {
			t.Errorf("loosely typed field %q (%v) is neither opaque nor declared structured", name, inventory.tags[name])
		}
	}
	for name := range opaqueFields {
		if _, ok := inventory.tags[name]; !ok {
			t.Errorf("opaque field %q is unreachable from the approved types", name)
		}
	}
	for _, name := range looseStructuredFields {
		if !inventory.loose[name] {
			t.Errorf("structured loose field %q is not loosely typed in any approved type", name)
		}
	}
}

func TestCasingKeyRules(t *testing.T) {
	for native, public := range map[string]string{
		"type": "type", "toolCallId": "tool-call-id", "parentToolCallId": "parent-tool-call-id", "isError": "is-error",
		"cacheWrite1h": "cache-write1h", "inputCostPer1M": "input-cost-per1-m", "baseUrl": "base-url",
	} {
		if got, ok := publicKey(native); !ok || got != public {
			t.Errorf("publicKey(%q) = %q, %v; want %q", native, got, ok, public)
		}
		if got := nativeKey(public); got != native {
			t.Errorf("nativeKey(%q) = %q; want %q", public, got, native)
		}
	}
	for _, native := range []string{"supportsOpenAIGrammarTools", "baseURL", "ID", "Name", "top_p", "max-tokens", "x1", "", "1st", "a__b"} {
		if public, ok := publicKey(native); ok && native != "x1" {
			t.Errorf("publicKey(%q) = %q; it must stay verbatim", native, public)
		}
	}
	for _, public := range []string{"top_p", "X-Api-Key", "a--b", "-a", "a-", "a-1b", "streams?"} {
		if got := nativeKey(public); got != public {
			t.Errorf("nativeKey(%q) = %q; non-kebab keys stay unchanged", public, got)
		}
	}
}

var modelArguments = map[string]any{"filePath": "a.go", "max_count": int64(2), "kebab-key": true, "Nested": map[string]any{"innerKey": []any{map[string]any{"deepKey": "x"}}}}

func TestCasingToolCallEventKeepsModelArgumentsByteForByte(t *testing.T) {
	event := extension.CustomToolCallEvent{ToolCallEventBase: extension.ToolCallEventBase{Type: "tool_call", ToolCallID: "call-1", ParentToolCallID: "parent"}, ToolName: "custom", Input: modelArguments}
	value, err := publicValue(event)
	if err != nil {
		t.Fatal(err)
	}
	lookup := value.(vm.Lookup)
	if lookup.ValueAt(vm.Keyword("tool-call-id")) != vm.String("call-1") || lookup.ValueAt(vm.Keyword("parent-tool-call-id")) != vm.String("parent") {
		t.Fatalf("public event %v", value)
	}
	input, err := fromValue(lookup.ValueAt(vm.Keyword("input")))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input, modelArguments) {
		t.Fatalf("input %#v, want %#v", input, modelArguments)
	}
	var decoded extension.ToolCallEvent
	if err := decodePublicValue(value, &decoded); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(event)
	got, _ := json.Marshal(decoded)
	if string(got) != string(want) {
		t.Fatalf("decoded native event\n got %s\nwant %s", got, want)
	}
}

func TestCasingToolArgumentsReachExecuteVerbatim(t *testing.T) {
	loaded := loadToolSource(t, `(ns pig.test.fixture (:require [pig.extension :as ext]))
 (def received (atom nil))
 (defn init [api]
   (ext/register-tool! api {:name "args" :parameters {:type "object"} :execute (fn [args] (reset! received args) {:content ""})}))`)
	runner := inproc.NewRunner([]extension.Extension{loaded.Extension}, t.TempDir())
	defer runner.Invalidate("test complete")
	params, _ := json.Marshal(modelArguments)
	if _, err := runner.Tools()[0].Definition.Execute(runner.DispatchContext(t.Context()), "call", params, nil); err != nil {
		t.Fatal(err)
	}
	value, err := loaded.generation.Run(t.Context(), `(deref pig.test.fixture/received)`)
	if err != nil {
		t.Fatal(err)
	}
	if value.(vm.Lookup).ValueAt(vm.Keyword("filePath")) != vm.String("a.go") {
		t.Fatalf("arguments were re-cased: %v", value)
	}
	if got, err := fromValue(value); err != nil || !reflect.DeepEqual(got, modelArguments) {
		t.Fatalf("arguments %#v, %v", got, err)
	}
}

func TestCasingToolResultDecodesPublicKeysAndKeepsOpaqueKeys(t *testing.T) {
	result, err := toolResultValue(vm.Map{
		vm.Keyword("content"):            vm.ArrayVector{vm.Map{vm.Keyword("type"): vm.String("image"), vm.Keyword("data"): vm.String("AA=="), vm.Keyword("mime-type"): vm.String("image/png")}},
		vm.Keyword("is-error"):           vm.TRUE,
		vm.Keyword("details"):            vm.Map{vm.Keyword("someKey"): vm.Map{vm.Keyword("nested-key"): vm.Int(1)}, vm.String("snake_key"): vm.Int(2)},
		vm.Keyword("structured-content"): vm.Map{vm.Keyword("camelKey"): vm.Int(3), vm.Keyword("kebab-key"): vm.Int(4)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("result %#v", result)
	}
	image, ok := result.Content[0].(ai.ImageContent)
	if !ok || image.MimeType != "image/png" {
		t.Fatalf("content %#v", result.Content)
	}
	if details, _ := json.Marshal(result.Details); string(details) != `{"snake_key":2,"someKey":{"nested-key":1}}` {
		t.Fatalf("details %s", details)
	}
	if string(result.StructuredContent) != `{"camelKey":3,"kebab-key":4}` {
		t.Fatalf("structured content %s", result.StructuredContent)
	}
	if !slices.Equal(result.MemberOrder, []string{"content", "details", "isError", "structuredContent"}) {
		t.Fatalf("member order %q", result.MemberOrder)
	}
}

func TestCasingCollisionIsRejectedWithFieldPath(t *testing.T) {
	for _, test := range []struct {
		value vm.Value
		want  string
	}{
		{vm.Map{vm.Keyword("content"): vm.ArrayVector{}, vm.Keyword("is-error"): vm.TRUE, vm.Keyword("isError"): vm.FALSE}, `$: keys "is-error" and "isError" both name field "isError"`},
		{vm.Map{vm.Keyword("content"): vm.ArrayVector{vm.Map{vm.Keyword("type"): vm.String("image"), vm.Keyword("mime-type"): vm.String("a"), vm.String("mimeType"): vm.String("b")}}}, `$.content[0]: keys "mime-type" and "mimeType" both name field "mimeType"`},
	} {
		if _, err := toolResultValue(test.value); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("want %q, got %v", test.want, err)
		}
	}
	// Opaque data may legitimately hold both spellings.
	if _, err := toolResultValue(vm.Map{vm.Keyword("content"): vm.ArrayVector{}, vm.Keyword("details"): vm.Map{vm.Keyword("is-error"): vm.TRUE, vm.Keyword("isError"): vm.FALSE}}); err != nil {
		t.Fatalf("opaque details collision rejected: %v", err)
	}
	// Host data cannot publish two keys with one public spelling either.
	if _, err := publicValue(map[string]any{"toolCallId": "a", "tool-call-id": "b"}); err == nil || !strings.Contains(err.Error(), `same public spelling "tool-call-id"`) {
		t.Fatalf("host collision: %v", err)
	}
}

func TestCasingAgentEndMessagesRecaseHostKeysOnly(t *testing.T) {
	assistant := agent.AssistantMessage{Role: "assistant", StopReason: ai.StopReasonToolUse, Content: []ai.AssistantContentBlock{ai.ToolCall{ID: "call-1", Name: "custom", Arguments: ai.JsonObject{"filePath": "a.go"}}}, Usage: &ai.Usage{CacheRead: 5}}
	toolResult := agent.ToolResultMessage{Role: "toolResult", ToolCallID: "call-1", ToolName: "custom", Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "ok"}}, Details: map[string]any{"camelKey": 1}}
	value, err := publicValue(extension.AgentEndEvent{Type: "agent_end", Messages: []extension.AgentMessage{agent.AgentMessage{Assistant: &assistant}, agent.AgentMessage{ToolResult: &toolResult}}})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	messages := plain.(map[string]any)["messages"].([]any)
	first, second := messages[0].(map[string]any), messages[1].(map[string]any)
	if first["stop-reason"] != "toolUse" || first["usage"].(map[string]any)["cache-read"] != int64(5) {
		t.Fatalf("assistant %#v", first)
	}
	call := first["content"].([]any)[0].(map[string]any)
	if call["type"] != "toolCall" || !reflect.DeepEqual(call["arguments"], map[string]any{"filePath": "a.go"}) {
		t.Fatalf("tool call %#v", call)
	}
	if second["tool-call-id"] != "call-1" || second["is-error"] != false || !reflect.DeepEqual(second["details"], map[string]any{"camelKey": int64(1)}) {
		t.Fatalf("tool result %#v", second)
	}
}

func TestCasingSessionEntriesAndModelInfo(t *testing.T) {
	manager := codingagent.NewSession("casing", t.TempDir())
	if _, err := manager.AppendCustomEntry("my-state", map[string]any{"camelKey": map[string]any{"snake_key": 1}}); err != nil {
		t.Fatal(err)
	}
	value, err := publicValue(manager.Entries())
	if err != nil {
		t.Fatal(err)
	}
	plain, err := fromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	entry := plain.([]any)[0].(map[string]any)
	if entry["custom-type"] != "my-state" || !reflect.DeepEqual(entry["data"], map[string]any{"camelKey": map[string]any{"snake_key": int64(1)}}) {
		t.Fatalf("entry %#v", entry)
	}
	if _, ok := entry["parent-id"]; !ok {
		t.Fatalf("entry parent id not re-cased: %#v", entry)
	}
	model := &ai.Model{ID: "m", ProviderMeta: ai.ProviderMetadata{BaseURL: "http://h", Headers: map[string]string{"X-Api-Key": "k"}, Compat: &ai.ModelCompat{SupportsStore: new(true), SupportsOpenAIGrammarTools: new(false)}}, SamplingParams: map[string]any{"top_p": 0.5}}
	model.Capabilities.ContextWindow = 1000
	value, err = publicValue(extension.ModelInfo(model))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err = fromValue(value); err != nil {
		t.Fatal(err)
	}
	info := plain.(map[string]any)
	compat := info["compat"].(map[string]any)
	if info["base-url"] != "http://h" || info["context-window"] != int64(1000) || info["headers"].(map[string]any)["X-Api-Key"] != "k" ||
		info["sampling-params"].(map[string]any)["top_p"] != 0.5 || compat["supports-store"] != true || compat["supportsOpenAIGrammarTools"] != false {
		t.Fatalf("model info %#v", info)
	}
}
