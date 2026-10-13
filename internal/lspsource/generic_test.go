// Acceptance test for a language without its own Language parts: members
// are nested in the outline, as in object-oriented languages, and there
// are no modules.
package lspsource_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lsp"
	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
	"github.com/Duara-Cortex/ego-dir/internal/lspsource"
)

func TestGenericLanguageNestsMembers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.py")
	if err := os.WriteFile(path, []byte("class C:\n    def m(self):\n        pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := lsp.FileURI(path)
	rng := func(sl, sc, el, ec int) map[string]any {
		return map[string]any{"start": map[string]int{"line": sl, "character": sc}, "end": map[string]int{"line": el, "character": ec}}
	}
	classSel, methodSel := rng(0, 6, 0, 7), rng(1, 8, 1, 9)

	s := lspfake.New()
	s.Result("textDocument/documentSymbol", []any{map[string]any{
		"name": "C", "kind": lsp.SymbolKindClass, "range": rng(0, 0, 2, 12), "selectionRange": classSel,
		"children": []any{map[string]any{"name": "m", "kind": lsp.SymbolKindMethod, "range": rng(1, 4, 2, 12), "selectionRange": methodSel}},
	}})
	s.Handle("textDocument/definition", func(_ context.Context, p json.RawMessage) (any, error) {
		var params struct {
			Position lsp.Position `json:"position"`
		}
		_ = json.Unmarshal(p, &params)
		if params.Position.Line == 0 {
			return map[string]any{"uri": uri, "range": classSel}, nil
		}
		return map[string]any{"uri": uri, "range": methodSel}, nil
	})
	for _, m := range []string{"textDocument/didOpen", "initialised", "initialized", "textDocument/hover", "textDocument/prepareCallHierarchy"} {
		s.Result(m, nil)
	}
	clientEnd, serverEnd := net.Pipe()
	go s.Serve(context.Background(), serverEnd)
	t.Cleanup(func() { clientEnd.Close(); serverEnd.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := lsp.NewClient(ctx, clientEnd, root)
	if err != nil {
		t.Fatal(err)
	}
	src := lspsource.New(c, root)
	build := func(line, char int, dir graph.Direction) []string {
		t.Helper()
		g, err := graph.Build(ctx, src, uri, line, char, graph.Options{Depth: 2, Kinds: allKinds, Direction: dir})
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Validate(); err != nil {
			t.Fatal(err)
		}
		nodes, edges := summary(g)
		return append(nodes, edges...)
	}

	// Out from the class: its method. In: nothing, as there are no modules.
	if got, want := build(0, 6, graph.DirOut), []string{"C@1 hop0", "m@2 hop1", "C@1 -contains-> m@2 x1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("class, out = %v, want %v", got, want)
	}
	if got, want := build(0, 6, graph.DirIn), []string{"C@1 hop0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("class, in = %v, want %v", got, want)
	}
	// In from the method: its class, found through the outline.
	if got, want := build(1, 8, graph.DirIn), []string{"m@2 hop0", "C@1 hop1", "C@1 -contains-> m@2 x1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("method, in = %v, want %v", got, want)
	}
	// The start of the file is not a module header.
	if got := build(0, 0, graph.DirBoth); got[0] != "C@1 hop0" {
		t.Errorf("start of the file = %v, want the class as the ego", got)
	}
}
