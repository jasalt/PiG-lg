package letgo_test

import (
	"strings"
	"testing"

	"github.com/nooga/let-go/pkg/api"
	"github.com/nooga/let-go/pkg/vm"
)

// TestEmbeddingRetainsCallbacks probes the pinned interpreter before any PiG
// extension loader depends on its callback lifetime or conversion behavior.
func TestEmbeddingRetainsCallbacks(t *testing.T) {
	runtime, err := api.NewLetGo("pig.embedding")
	if err != nil {
		t.Fatal(err)
	}
	var callback vm.Fn
	if err := runtime.Def("capture", func(fn vm.Fn) { callback = fn }); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(`(capture (fn [m s n] {:content [{:type "text" :text (str (get m "name") s n)}]}))`); err != nil {
		t.Fatal(err)
	}
	if callback == nil {
		t.Fatal("interpreted fn did not reach Go")
	}
	values := []vm.Value{vm.Map{vm.String("name"): vm.String("Ada")}, vm.String("-"), vm.Int(7)}
	result, err := callback.Invoke(values)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.String(), "Ada-7") {
		t.Fatalf("callback result cannot become tool content: %s", result)
	}
	content := result.(vm.Lookup).ValueAt(vm.Keyword("content"))
	items, ok := content.(vm.Sequable)
	if !ok || items.Seq() == nil {
		t.Fatalf("callback content is not a sequence: %T", content)
	}
	text := items.Seq().First().(vm.Lookup).ValueAt(vm.Keyword("text"))
	if got := text.Unbox(); got != "Ada-7" {
		t.Fatalf("converted tool text = %v, want Ada-7", got)
	}
}

func TestEmbeddingInterpreterError(t *testing.T) {
	runtime, err := api.NewLetGo("pig.embedding.errors")
	if err != nil {
		t.Fatal(err)
	}
	var callback vm.Fn
	if err := runtime.Def("capture", func(fn vm.Fn) { callback = fn }); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(`(capture (fn [] (throw (ex-info "extension failed" {:line 31}))))`); err != nil {
		t.Fatal(err)
	}
	_, err = callback.Invoke(nil)
	if err == nil || !strings.Contains(err.Error(), "extension failed") {
		t.Fatalf("interpreted error: %v", err)
	}
	// Reader errors carry a source form location; runtime exceptions carry a
	// normal Go error, whose source is added by the future loader's path wrapper.
	_, err = runtime.Run("(def broken\n (")
	if err == nil || !strings.Contains(err.Error(), "source at (<default>:2:") {
		t.Fatalf("reader location: %v", err)
	}
}
