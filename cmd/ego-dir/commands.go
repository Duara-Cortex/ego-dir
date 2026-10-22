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
	"github.com/Duara-Cortex/ego-dir/internal/lsppool"
	"github.com/Duara-Cortex/ego-dir/internal/lspsource"
	"github.com/Duara-Cortex/ego-dir/internal/render"
)

// runCommand runs a position command given on the command line.
func runCommand(cmd string, args []string, stdout io.Writer) error {
	r, err := parseFlags(cmd, args)
	if err != nil {
		return err
	}
	o, err := r.validate()
	if err != nil {
		return err
	}
	return execute(context.Background(), cmd, o, oneShot{}, stdout)
}

// server is a language server for one call.
type server interface {
	lspsource.Client
	// Release ends the call's use of the server. err is the call's error;
	// the error returned is the one to report.
	Release(ctx context.Context, err error) error
}

// servers gives a call its language server.
type servers interface {
	Acquire(ctx context.Context, command []string, root string) (server, error)
}

// oneShot starts a server for each call and stops it when the call ends.
type oneShot struct{}

func (oneShot) Acquire(ctx context.Context, command []string, root string) (server, error) {
	c, err := lsp.Start(ctx, command, root)
	if err != nil {
		return nil, err
	}
	return ownServer{c}, nil
}

// ownServer is a server that belongs to one call.
type ownServer struct{ *lsp.Client }

func (s ownServer) Release(ctx context.Context, err error) error {
	if cerr := s.Close(ctx); cerr != nil && err == nil {
		return fmt.Errorf("stop language server: %w", cerr)
	}
	return err
}

// pooled shares warm servers between calls.
type pooled struct{ *lsppool.Pool }

func (p pooled) Acquire(ctx context.Context, command []string, root string) (server, error) {
	s, err := p.Pool.Acquire(ctx, command, root)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// execute runs a validated position command against a language server from
// srv, within o.timeout of ctx. Output is rendered into a buffer and
// written only on success.
func execute(ctx context.Context, cmd string, o *options, srv servers, stdout io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	client, err := srv.Acquire(ctx, o.server, o.root)
	if err != nil {
		return err
	}
	defer func() { err = client.Release(ctx, err) }()

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

func findReferences(ctx context.Context, c lspsource.Client, src *lspsource.Source, uri string, o *options, w io.Writer) error {
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
