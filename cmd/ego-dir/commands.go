package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lsp"
	"github.com/Duara-Cortex/ego-dir/internal/lspsource"
	"github.com/Duara-Cortex/ego-dir/internal/render"
)

// runCommand runs a position command against a freshly started language
// server. Output is rendered into a buffer and written only on success.
func runCommand(cmd string, args []string, stdout io.Writer) (err error) {
	o, err := parseOptions(cmd, args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()

	client, err := lsp.Start(ctx, o.server, o.root)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := client.Close(ctx); cerr != nil && err == nil {
			err = fmt.Errorf("stop language server: %w", cerr)
		}
	}()

	src := lspsource.New(client, o.root)
	uri := lsp.FileURI(o.path)
	var out bytes.Buffer
	switch cmd {
	case "inspect_ego":
		err = inspectEgo(ctx, src, uri, o, &out)
	case "resolve_definition":
		err = resolveDefinition(ctx, src, uri, o, &out)
	case "find_references":
		err = findReferences(ctx, client, src, uri, o, &out)
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(out.Bytes())
	return err
}

func inspectEgo(ctx context.Context, src *lspsource.Source, uri string, o *options, w io.Writer) error {
	g, err := graph.Build(ctx, src, uri, o.pos.Line, o.pos.Character, o.build)
	if err != nil {
		return err
	}
	if o.json {
		return render.JSON(w, g, o.root)
	}
	return render.Markdown(w, g, o.root)
}

func resolveDefinition(ctx context.Context, src *lspsource.Source, uri string, o *options, w io.Writer) error {
	n, err := src.Resolve(ctx, uri, o.pos.Line, o.pos.Character)
	if err != nil {
		return err
	}
	if o.json {
		return render.DefinitionJSON(w, n, o.root)
	}
	return render.Definition(w, n, o.root)
}

func findReferences(ctx context.Context, c *lsp.Client, src *lspsource.Source, uri string, o *options, w io.Writer) error {
	target, err := src.Resolve(ctx, uri, o.pos.Line, o.pos.Character)
	if err != nil {
		return err
	}
	if !target.Located() {
		return fmt.Errorf("%s is a module: find_references does not support modules", target.Qualified)
	}
	// Ask about the declaration, so a use site and its declaration give the
	// same answer.
	at := lsp.Position{Line: target.Selection.StartLine - 1}
	targetLines, err := fileLines(target.URI)
	if err != nil {
		return err
	}
	if at, err = lsp.EditorToPosition(lineAt(targetLines, at.Line), target.Selection.StartLine, target.Selection.StartCol); err != nil {
		return err
	}
	locs, err := c.References(ctx, target.URI, at, false)
	if err != nil {
		return err
	}
	files := map[string][]string{target.URI: targetLines}
	refs := make([]render.Reference, 0, len(locs))
	for _, loc := range locs {
		lines, ok := files[loc.URI]
		if !ok {
			if lines, err = fileLines(loc.URI); err != nil {
				return err
			}
			files[loc.URI] = lines
		}
		text := lineAt(lines, loc.Range.Start.Line)
		line, col := lsp.PositionToEditor(text, loc.Range.Start)
		refs = append(refs, render.Reference{URI: loc.URI, Line: line, Col: col, Text: strings.TrimSpace(text)})
	}
	if o.json {
		return render.ReferencesJSON(w, target, refs, o.root)
	}
	return render.References(w, target, refs, o.root)
}

func fileLines(uri string) ([]string, error) {
	path, err := lsp.URIToPath(uri)
	if err != nil {
		return nil, err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n"), nil
}

func lineAt(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return ""
	}
	return lines[i]
}
