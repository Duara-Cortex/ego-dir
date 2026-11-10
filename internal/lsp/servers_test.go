package lsp

import (
	"slices"
	"testing"
)

func TestRootMarkers(t *testing.T) {
	cases := []struct {
		path string
		want []string
	}{
		{"a/b.go", []string{"go.mod", "go.work"}},
		{"a/b.py", []string{"pyproject.toml", "setup.py", "setup.cfg", "pyrightconfig.json"}},
		{"a/b.rs", []string{"Cargo.toml"}},
		{"a/b.ts", []string{"package.json"}},
		{"a/b.TSX", []string{"package.json"}},
		{"a/b.js", []string{"package.json"}},
		{"a/b.jsx", []string{"package.json"}},
		{"a/b.php", []string{"composer.json"}},
		{"a/b.txt", nil},
		{"Makefile", nil},
	}
	for _, c := range cases {
		if got := RootMarkers(c.path); !slices.Equal(got, c.want) {
			t.Errorf("RootMarkers(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestRootMarkersIsACopy(t *testing.T) {
	RootMarkers("a.go")[0] = "changed"
	if got := RootMarkers("a.go")[0]; got != "go.mod" {
		t.Errorf("a caller changed the table: %q", got)
	}
}
