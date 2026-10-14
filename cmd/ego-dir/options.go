package main

import (
	"errors"
	"flag"
	"io"
	"io/fs"
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

// options is a validated request.
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

// request is a position command's input before validation, from flags or
// from an MCP tool call.
type request struct {
	cmd     string
	file    string
	line    int
	col     int
	root    string
	server  string
	format  string
	timeout time.Duration
	// inspect_ego only
	depth     int
	edges     string
	direction string
	body      string
	// confine requires file to resolve inside root, symbolic links
	// included. The MCP server sets it; the CLI trusts its user.
	confine bool
}

// newRequest returns a request for cmd holding every default.
func newRequest(cmd string) request {
	return request{cmd: cmd, format: "md", timeout: 30 * time.Second,
		depth: 1, edges: defaultEdges, direction: "both", body: "ego"}
}

// parseFlags parses the flags of cmd into a request. All errors are usage
// errors.
func parseFlags(cmd string, args []string) (request, error) {
	r := newRequest(cmd)
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are reported by run
	fs.StringVar(&r.file, "file", "", "")
	fs.IntVar(&r.line, "line", 0, "")
	fs.IntVar(&r.col, "col", 0, "")
	fs.StringVar(&r.root, "root", "", "")
	fs.StringVar(&r.server, "server", "", "")
	fs.StringVar(&r.format, "format", r.format, "")
	fs.DurationVar(&r.timeout, "timeout", r.timeout, "")
	if cmd == "inspect_ego" {
		fs.IntVar(&r.depth, "depth", r.depth, "")
		fs.StringVar(&r.edges, "edges", r.edges, "")
		fs.StringVar(&r.direction, "direction", r.direction, "")
		fs.StringVar(&r.body, "body", r.body, "")
	}
	if err := fs.Parse(args); err != nil {
		return request{}, usageError{err}
	}
	if fs.NArg() > 0 {
		return request{}, usagef("unexpected argument %q", fs.Arg(0))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	for _, required := range []string{"file", "line", "col"} {
		if !set[required] {
			return request{}, usagef("--%s is required", required)
		}
	}
	return r, nil
}

// validate checks r and resolves it into options. It reads the file to
// check the position, so every input error is reported before a language
// server is started. All errors are usage errors.
func (r request) validate() (*options, error) {
	o := &options{timeout: r.timeout}
	switch r.format {
	case "md":
	case "json":
		o.json = true
	default:
		return nil, usagef("--format must be md or json, not %q", r.format)
	}
	if o.timeout <= 0 {
		return nil, usagef("--timeout must be positive")
	}

	var err error
	if o.root, err = absRoot(r.root); err != nil {
		return nil, err
	}
	o.path = r.file
	if !filepath.IsAbs(o.path) {
		o.path = filepath.Join(o.root, o.path)
	}
	if r.confine {
		if err := insideRoot(o.root, o.path, r.file); err != nil {
			return nil, err
		}
	}
	src, err := os.ReadFile(o.path)
	if err != nil {
		return nil, usageError{err}
	}
	o.lines = strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	if r.line < 1 || r.line > len(o.lines) {
		return nil, usagef("--line %d is outside %s (1-%d)", r.line, r.file, len(o.lines))
	}
	if o.pos, err = lsp.EditorToPosition(o.lines[r.line-1], r.line, r.col); err != nil {
		return nil, usageError{err}
	}

	if r.server != "" {
		o.server = strings.Fields(r.server)
	} else if o.server, err = lsp.ServerCommand(o.path); err != nil {
		return nil, usageError{err}
	}

	if r.cmd == "inspect_ego" {
		if o.build, err = buildOptions(r.depth, r.edges, r.direction, r.body); err != nil {
			return nil, err
		}
	}
	return o, nil
}

// insideRoot reports a usage error unless path, with every symbolic link
// followed, is inside root. file is the path as given, for messages, which
// never name anything outside root.
func insideRoot(root, path, file string) error {
	outside := usagef("%s is outside the root", file)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return usagef("cannot resolve the root")
	}
	// By name first, so nothing outside the root is looked at. The root
	// may be written either way: /var/x or /private/var/x on macOS.
	if !within(root, path) && !within(realRoot, path) {
		return outside
	}
	realPath, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		return usagef("%s does not exist", file)
	} else if err != nil {
		return usagef("cannot resolve %s", file)
	}
	if !within(realRoot, realPath) {
		return outside
	}
	return nil
}

// within reports whether path is root or below it, comparing names only.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
