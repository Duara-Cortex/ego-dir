// Acceptance tests for the LSP protocol structs, positions and URIs.
package lsp_test

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

func TestCallHierarchyItemRoundTripKeepsData(t *testing.T) {
	// gopls requires the opaque "data" field to be sent back unchanged.
	in := `{"name":"TotalArea","kind":12,"detail":"example.com/sample • shapes.go",` +
		`"uri":"file:///ws/shapes.go",` +
		`"range":{"start":{"line":17,"character":0},"end":{"line":23,"character":1}},` +
		`"selectionRange":{"start":{"line":17,"character":5},"end":{"line":17,"character":14}},` +
		`"data":{"key":"TotalArea","nested":[1,2]}}`
	var item lsp.CallHierarchyItem
	if err := json.Unmarshal([]byte(in), &item); err != nil {
		t.Fatal(err)
	}
	if item.Name != "TotalArea" || item.Kind != lsp.SymbolKind(12) || item.URI != "file:///ws/shapes.go" {
		t.Errorf("decoded item = %+v", item)
	}
	want := lsp.Range{Start: lsp.Position{Line: 17, Character: 5}, End: lsp.Position{Line: 17, Character: 14}}
	if item.SelectionRange != want {
		t.Errorf("SelectionRange = %+v, want %+v", item.SelectionRange, want)
	}
	out, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal([]byte(in), &a)
	_ = json.Unmarshal(out, &b)
	if !jsonEqual(a, b) {
		t.Errorf("round trip changed the item:\n in  %s\n out %s", in, out)
	}
}

func TestCallsDecode(t *testing.T) {
	r := `{"start":{"line":27,"character":8},"end":{"line":27,"character":17}}`
	item := `{"name":"Report","kind":12,"uri":"file:///ws/shapes.go","range":` + r + `,"selectionRange":` + r + `}`

	var in []lsp.CallHierarchyIncomingCall
	if err := json.Unmarshal([]byte(`[{"from":`+item+`,"fromRanges":[`+r+`,`+r+`]}]`), &in); err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 || in[0].From.Name != "Report" || len(in[0].FromRanges) != 2 {
		t.Errorf("incoming = %+v", in)
	}

	var out []lsp.CallHierarchyOutgoingCall
	if err := json.Unmarshal([]byte(`[{"to":`+item+`,"fromRanges":[`+r+`]}]`), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].To.Name != "Report" || out[0].FromRanges[0].Start.Character != 8 {
		t.Errorf("outgoing = %+v", out)
	}
}

func TestLocationJSON(t *testing.T) {
	loc := lsp.Location{URI: "file:///ws/a.go", Range: lsp.Range{End: lsp.Position{Line: 1, Character: 2}}}
	b, err := json.Marshal(loc)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"uri":"file:///ws/a.go","range":{"start":{"line":0,"character":0},"end":{"line":1,"character":2}}}`
	if string(b) != want {
		t.Errorf("Location JSON = %s, want %s", b, want)
	}
}

func TestEditorToPosition(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		line, col int
		want      lsp.Position
	}{
		{"ascii start", "func main() {", 1, 1, lsp.Position{Line: 0, Character: 0}},
		{"ascii middle", "func main() {", 3, 6, lsp.Position{Line: 2, Character: 5}},
		{"end of line", "abc", 1, 4, lsp.Position{Line: 0, Character: 3}},
		{"two-byte rune", "héllo := 1", 1, 3, lsp.Position{Line: 0, Character: 2}},
		{"after astral rune", "a😀b", 1, 3, lsp.Position{Line: 0, Character: 3}}, // 😀 is 2 UTF-16 units
		{"tab counts as one", "\tx", 1, 2, lsp.Position{Line: 0, Character: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lsp.EditorToPosition(tc.text, tc.line, tc.col)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("EditorToPosition(%q, %d, %d) = %+v, want %+v", tc.text, tc.line, tc.col, got, tc.want)
			}
			l, c := lsp.PositionToEditor(tc.text, got)
			if l != tc.line || c != tc.col {
				t.Errorf("PositionToEditor(%+v) = %d:%d, want %d:%d", got, l, c, tc.line, tc.col)
			}
		})
	}
	for _, bad := range []struct{ line, col int }{{0, 1}, {1, 0}, {-1, 1}, {1, 5}} {
		if _, err := lsp.EditorToPosition("abc", bad.line, bad.col); err == nil {
			t.Errorf("EditorToPosition(%q, %d, %d) succeeded, want error", "abc", bad.line, bad.col)
		}
	}
}

func TestFileURI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	if got := lsp.FileURI("/tmp/a b/c.go"); got != "file:///tmp/a%20b/c.go" {
		t.Errorf("FileURI = %q, want %q", got, "file:///tmp/a%20b/c.go")
	}
	for _, p := range []string{"/tmp/a b/c.go", "/tmp/é/ü.go", "/x/y#z.go", "/x/100%.go"} {
		back, err := lsp.URIToPath(lsp.FileURI(p))
		if err != nil {
			t.Errorf("URIToPath(FileURI(%q)) error: %v", p, err)
			continue
		}
		if back != p {
			t.Errorf("URIToPath(FileURI(%q)) = %q", p, back)
		}
	}
	rel := lsp.FileURI("rel.go")
	abs, _ := filepath.Abs("rel.go")
	if rel != lsp.FileURI(abs) {
		t.Errorf("FileURI(relative) = %q, want it made absolute (%q)", rel, lsp.FileURI(abs))
	}
	if _, err := lsp.URIToPath("https://example.com/a.go"); err == nil {
		t.Error("URIToPath accepted a non-file URI")
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestLocationLink(t *testing.T) {
	in := `{"targetUri":"file:///ws/a.go",` +
		`"targetRange":{"start":{"line":2,"character":0},"end":{"line":6,"character":1}},` +
		`"targetSelectionRange":{"start":{"line":3,"character":4},"end":{"line":3,"character":9}}}`
	var link lsp.LocationLink
	if err := json.Unmarshal([]byte(in), &link); err != nil {
		t.Fatal(err)
	}
	want := lsp.Location{URI: "file:///ws/a.go", Range: lsp.Range{Start: lsp.Position{Line: 3, Character: 4}, End: lsp.Position{Line: 3, Character: 9}}}
	if got := link.Location(); got != want {
		t.Errorf("Location() = %+v, want %+v (the name, not the whole declaration)", got, want)
	}
}

func TestDocumentSymbolDecode(t *testing.T) {
	r := `{"start":{"line":3,"character":0},"end":{"line":5,"character":1}}`
	in := `[{"name":"Shape","kind":11,"range":` + r + `,"selectionRange":` + r +
		`,"children":[{"name":"Area","kind":6,"range":` + r + `,"selectionRange":` + r + `}]}]`
	var syms []lsp.DocumentSymbol
	if err := json.Unmarshal([]byte(in), &syms); err != nil {
		t.Fatal(err)
	}
	if len(syms) != 1 || syms[0].Kind != lsp.SymbolKindInterface || len(syms[0].Children) != 1 ||
		syms[0].Children[0].Name != "Area" || syms[0].Children[0].Kind != lsp.SymbolKindMethod {
		t.Errorf("decoded = %+v", syms)
	}
}

func TestPositionToEditorEdgeCases(t *testing.T) {
	// Inside a surrogate pair: maps to the character it belongs to.
	if l, c := lsp.PositionToEditor("a😀b", lsp.Position{Line: 4, Character: 2}); l != 5 || c != 2 {
		t.Errorf("mid-surrogate = %d:%d, want 5:2", l, c)
	}
	// Past the end of the line: clamped to one past the last character.
	if _, c := lsp.PositionToEditor("abc", lsp.Position{Character: 99}); c != 4 {
		t.Errorf("past end col = %d, want 4", c)
	}
	// Invalid UTF-8 counts as one character of one unit.
	p, err := lsp.EditorToPosition("a\xffb", 1, 3)
	if err != nil || p.Character != 2 {
		t.Errorf("invalid UTF-8 = %+v, %v; want character 2", p, err)
	}
}

func TestURIToPathErrors(t *testing.T) {
	for _, bad := range []string{"file:", "file:///%zz", "%"} {
		if _, err := lsp.URIToPath(bad); err == nil {
			t.Errorf("URIToPath(%q) succeeded, want error", bad)
		}
	}
}
