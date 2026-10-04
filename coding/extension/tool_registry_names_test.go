package extension

import (
	"slices"
	"testing"
)

func TestRegisteredToolNamesFollowsRegistrationOrderThenName(t *testing.T) {
	tool := func(name string) RegisteredTool { return RegisteredTool{Definition: ToolDefinition{Name: name}} }

	plain := Extension{Tools: map[string]RegisteredTool{"b": tool("b"), "a": tool("a"), "z": tool("z")}, ToolOrder: []string{"z", "missing", "z", "b"}}
	if got, want := plain.RegisteredToolNames(), []string{"z", "b", "a"}; !slices.Equal(got, want) {
		t.Fatalf("Tools map names %v, want %v", got, want)
	}

	registry := Extension{}
	registry.InitializeToolRegistry()
	for _, name := range []string{"second", "first", "second"} {
		registry.SetRegisteredTool(tool(name))
	}
	if got, want := registry.RegisteredToolNames(), []string{"second", "first"}; !slices.Equal(got, want) {
		t.Fatalf("registry names %v, want %v (a replacement keeps its first position)", got, want)
	}
	empty := Extension{}
	if got := empty.RegisteredToolNames(); len(got) != 0 {
		t.Fatalf("empty extension names %v", got)
	}
}
