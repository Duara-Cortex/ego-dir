package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// defaultEdges leaves out references, which with call hierarchy available
// duplicate calls, and contains, which lists a whole module or type.
const defaultEdges = "calls,implements,instantiates,imports"

// options is a validated command line.
type options struct {
	root    string   // absolute workspace root
	path    string   // absolute source file
	lines   []string // the file's lines
	pos     lsp.Position
	server  []string
	json    bool
	timeout time.Duration
	build   graph.Options // inspect_ego only
}

// parseOptions parses and validates flags for cmd. It reads the file to
// check the position, so every input error is reported before a language
// server is started. All errors are usage errors.
func parseOptions(cmd string, args []string) (*options, error) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are reported by run
	file := fs.String("file", "", "")
	line := fs.Int("line", 0, "")
	col := fs.Int("col", 0, "")
	root := fs.String("root", "", "")
	server := fs.String("server", "", "")
	format := fs.String("format", "md", "")
	timeout := fs.Duration("timeout", 30*time.Second, "")
	var depth *int
	var edges, direction, body *string
	if cmd == "inspect_ego" {
		depth = fs.Int("depth", 1, "")
		edges = fs.String("edges", defaultEdges, "")
		direction = fs.String("direction", "both", "")
		body = fs.String("body", "ego", "")
	}
	if err := fs.Parse(args); err != nil {
		return nil, usageError{err}
	}
	if fs.NArg() > 0 {
		return nil, usagef("unexpected argument %q", fs.Arg(0))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	for _, required := range []string{"file", "line", "col"} {
		if !set[required] {
			return nil, usagef("--%s is required", required)
		}
	}

	o := &options{timeout: *timeout}
	switch *format {
	case "md":
	case "json":
		o.json = true
	default:
		return nil, usagef("--format must be md or json, not %q", *format)
	}
	if o.timeout <= 0 {
		return nil, usagef("--timeout must be positive")
	}

	var err error
	if o.root, err = absRoot(*root); err != nil {
		return nil, err
	}
	o.path = *file
	if !filepath.IsAbs(o.path) {
		o.path = filepath.Join(o.root, o.path)
	}
	src, err := os.ReadFile(o.path)
	if err != nil {
		return nil, usageError{err}
	}
	o.lines = strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	if *line < 1 || *line > len(o.lines) {
		return nil, usagef("--line %d is outside %s (1-%d)", *line, *file, len(o.lines))
	}
	if o.pos, err = lsp.EditorToPosition(o.lines[*line-1], *line, *col); err != nil {
		return nil, usageError{err}
	}

	if *server != "" {
		o.server = strings.Fields(*server)
	} else if o.server, err = lsp.ServerCommand(o.path); err != nil {
		return nil, usageError{err}
	}

	if cmd == "inspect_ego" {
		if o.build, err = buildOptions(*depth, *edges, *direction, *body); err != nil {
			return nil, err
		}
	}
	return o, nil
}

func absRoot(root string) (string, error) {
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", usageError{err}
		}
		return wd, nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", usageError{err}
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", usagef("--root %q is not a directory", root)
	}
	return abs, nil
}

func buildOptions(depth int, edges, direction, body string) (graph.Options, error) {
	if depth < 1 || depth > graph.MaxDepth {
		return graph.Options{}, usagef("--depth must be 1-%d, not %d", graph.MaxDepth, depth)
	}
	kinds, err := graph.ParseEdgeKinds(edges)
	if err != nil {
		return graph.Options{}, usageError{err}
	}
	dirs := map[string]graph.Direction{"both": graph.DirBoth, "in": graph.DirIn, "out": graph.DirOut}
	dir, ok := dirs[direction]
	if !ok {
		return graph.Options{}, usagef("--direction must be in, out or both, not %q", direction)
	}
	bodies := map[string]graph.BodyMode{"ego": graph.BodyEgo, "all": graph.BodyAll, "none": graph.BodyNone}
	mode, ok := bodies[body]
	if !ok {
		return graph.Options{}, usagef("--body must be ego, all or none, not %q", body)
	}
	return graph.Options{Depth: depth, Kinds: kinds, Direction: dir, Body: mode}, nil
}
