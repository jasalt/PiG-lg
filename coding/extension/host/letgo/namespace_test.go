package letgo_test

import (
	"context"
	"testing"

	"github.com/nooga/let-go/pkg/rt"
	"github.com/nooga/let-go/pkg/vm"

	"github.com/MichaelKinsy/PiG/coding/extension/host/letgo"
)

func TestGenerationPreservesCurrentNamespaceAcrossEntries(t *testing.T) {
	host := letgo.NewRuntimeHost()
	first, err := host.NewGeneration(t.Context(), "pig.namespace.first", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	second, err := host.NewGeneration(t.Context(), "pig.namespace.second", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if _, err := first.Run(t.Context(), `(def answer "first")`); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Run(t.Context(), `(def answer "second")`); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		generation *letgo.Generation
		want       vm.String
	}{{first, "first"}, {second, "second"}, {first, "first"}} {
		got, err := test.generation.Run(t.Context(), "answer")
		if err != nil || got != test.want {
			t.Fatalf("namespace: got %v, %v; want %v", got, err, test.want)
		}
	}
}

func TestGenerationConstructionRestoresAmbientNamespace(t *testing.T) {
	previous := rt.CurrentNS.Deref()
	generation, err := letgo.NewRuntimeHost().NewGeneration(t.Context(), "pig.namespace.ambient", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := generation.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	if rt.CurrentNS.Deref() != previous {
		t.Fatal("construction changed process current namespace")
	}
}
