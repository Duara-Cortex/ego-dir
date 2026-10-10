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
}

var languages = map[string]language{
	".go":  {"go", []string{"gopls"}},
	".py":  {"python", []string{"pyright-langserver", "--stdio"}},
	".rs":  {"rust", []string{"rust-analyzer"}},
	".ts":  {"typescript", []string{"typescript-language-server", "--stdio"}},
	".tsx": {"typescriptreact", []string{"typescript-language-server", "--stdio"}},
	".js":  {"javascript", []string{"typescript-language-server", "--stdio"}},
	".jsx": {"javascriptreact", []string{"typescript-language-server", "--stdio"}},
	".php": {"php", []string{"intelephense", "--stdio"}},
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
