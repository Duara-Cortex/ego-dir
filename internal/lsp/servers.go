package lsp

import (
	"fmt"
	"path/filepath"
	"strings"
)

// language is what ego-dir knows about one file extension.
type language struct {
	id      string   // LSP languageId
	command []string // default language server
	markers []string // files that mark a project's root
}

var (
	goMarkers     = []string{"go.mod", "go.work"}
	pythonMarkers = []string{"pyproject.toml", "setup.py", "setup.cfg", "pyrightconfig.json"}
	rustMarkers   = []string{"Cargo.toml"}
	jsMarkers     = []string{"package.json"}
	phpMarkers    = []string{"composer.json"}
)

var languages = map[string]language{
	".go":  {"go", []string{"gopls"}, goMarkers},
	".py":  {"python", []string{"pyright-langserver", "--stdio"}, pythonMarkers},
	".rs":  {"rust", []string{"rust-analyzer"}, rustMarkers},
	".ts":  {"typescript", []string{"typescript-language-server", "--stdio"}, jsMarkers},
	".tsx": {"typescriptreact", []string{"typescript-language-server", "--stdio"}, jsMarkers},
	".js":  {"javascript", []string{"typescript-language-server", "--stdio"}, jsMarkers},
	".jsx": {"javascriptreact", []string{"typescript-language-server", "--stdio"}, jsMarkers},
	".php": {"php", []string{"intelephense", "--stdio"}, phpMarkers},
}

// ServerCommand returns the default language server command for path,
// chosen by its extension.
func ServerCommand(path string) ([]string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	lang, ok := languages[ext]
	if !ok {
		return nil, fmt.Errorf("no language server known for %q files (use --server)", ext)
	}
	return append([]string(nil), lang.command...), nil
}

// LanguageID returns the LSP languageId for path, or "" if unknown.
func LanguageID(path string) string {
	return languages[strings.ToLower(filepath.Ext(path))].id
}

// RootMarkers returns the names of the files that mark the root of a
// project in path's language, chosen by its extension, or nil if the
// language is unknown.
func RootMarkers(path string) []string {
	return append([]string(nil), languages[strings.ToLower(filepath.Ext(path))].markers...)
}
