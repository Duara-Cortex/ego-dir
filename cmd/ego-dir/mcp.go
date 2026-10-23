package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lsppool"
	"github.com/Duara-Cortex/ego-dir/internal/mcp"
)

// mcpConfig is fixed when the MCP server starts. Tool calls cannot change
// it: their arguments name a position, never a root, server or timeout.
type mcpConfig struct {
	root    string // absolute
	server  string // "" chooses by file extension
	timeout time.Duration
	idle    time.Duration // 0: a fresh language server per call
}

// defaultIdle is how long a warm language server waits for another call.
const defaultIdle = 10 * time.Minute

// shutdownTimeout bounds stopping the warm servers when stdin closes.
const shutdownTimeout = 5 * time.Second

// runMCP serves the position commands as MCP tools on stdin and stdout
// until stdin closes. Language servers are kept warm between calls unless
// --idle is 0; when stdin closes they are shut down.
func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cfg, err := parseMCPFlags(args)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "ego-dir %s: serving MCP on stdio for %s\n", currentVersion(), cfg.root)
	var srv servers = oneShot{}
	if cfg.idle > 0 {
		pool := lsppool.New(lsppool.Options{Idle: cfg.idle, Max: mcp.DefaultMaxConcurrent, StartTimeout: cfg.timeout, Log: stderr})
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := pool.Close(ctx); err != nil {
				fmt.Fprintf(stderr, "ego-dir: %v\n", err)
			}
		}()
		srv = pooled{pool}
	}
	return mcpServer(cfg, srv).Serve(context.Background(), stdin, stdout)
}

func parseMCPFlags(args []string) (mcpConfig, error) {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are reported by run
	root := fs.String("root", "", "")
	server := fs.String("server", "", "")
	timeout := fs.Duration("timeout", newRequest("").timeout, "")
	idle := fs.Duration("idle", defaultIdle, "")
	if err := fs.Parse(args); err != nil {
		return mcpConfig{}, usageError{err}
	}
	if fs.NArg() > 0 {
		return mcpConfig{}, usagef("unexpected argument %q", fs.Arg(0))
	}
	if *timeout <= 0 {
		return mcpConfig{}, usagef("--timeout must be positive")
	}
	if *idle < 0 {
		return mcpConfig{}, usagef("--idle must not be negative")
	}
	abs, err := absRoot(*root)
	if err != nil {
		return mcpConfig{}, err
	}
	return mcpConfig{root: abs, server: *server, timeout: *timeout, idle: *idle}, nil
}

func mcpServer(cfg mcpConfig, srv servers) *mcp.Server {
	s := &mcp.Server{Name: "ego-dir", Version: currentVersion()}
	for _, cmd := range []string{"inspect_ego", "resolve_definition", "find_references"} {
		s.Tools = append(s.Tools, mcp.Tool{
			Name:        cmd,
			Title:       toolTitles[cmd],
			Description: toolDescriptions[cmd],
			InputSchema: inputSchema(cmd),
			Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
				return callTool(ctx, cfg, srv, cmd, args)
			},
		})
	}
	return s
}

var toolTitles = map[string]string{
	"inspect_ego":        "Inspect ego-graph",
	"resolve_definition": "Resolve definition",
	"find_references":    "Find references",
}

var toolDescriptions = map[string]string{
	"inspect_ego": "Describe the symbol at a position and its neighbours in the code graph: " +
		"signature, doc and body, then its callers, callees, implementations and type dependencies " +
		"up to `depth` hops away. Use it to learn what a function or type means and what depends on it " +
		"without reading whole files. On a module's name (the `x` in `package x`) it describes the " +
		"module and the modules it imports. Read-only.",
	"resolve_definition": "Find where the symbol at a position is declared: its location, kind, " +
		"signature and doc. The position may be a use site or the declaration. Read-only.",
	"find_references": "List every use of the symbol at a position, as file:line:col with the line's " +
		"text. The position may be a use site or the declaration. Read-only.",
}

// inputSchema is the JSON Schema of cmd's arguments. It mirrors the
// command's flags, except root, server and timeout, which the server fixes.
func inputSchema(cmd string) json.RawMessage {
	d := newRequest(cmd)
	props := map[string]any{
		"file": map[string]any{"type": "string",
			"description": "Source file, relative to the server's root (an absolute path must be inside the root)."},
		"line": map[string]any{"type": "integer", "minimum": 1,
			"description": "1-based line number, as shown in editors."},
		"col": map[string]any{"type": "integer", "minimum": 1,
			"description": "1-based column, counted in characters (not bytes), of any character in the symbol's name."},
		"format": map[string]any{"type": "string", "enum": []string{"md", "json"}, "default": d.format,
			"description": "Output format: Markdown, or JSON (schema ego-dir/v1)."},
	}
	if cmd == "inspect_ego" {
		props["depth"] = map[string]any{"type": "integer", "minimum": 1, "maximum": graph.MaxDepth, "default": d.depth,
			"description": "How many hops from the symbol to include."}
		props["edges"] = map[string]any{"type": "string", "default": d.edges,
			"description": "Comma-separated edge kinds to follow: calls, implements, instantiates, imports, contains " +
				"(members and parents), references; or all."}
		props["direction"] = map[string]any{"type": "string", "enum": []string{"in", "out", "both"}, "default": d.direction,
			"description": "in: callers and other upstream symbols; out: callees and other downstream symbols; both."}
		props["body"] = map[string]any{"type": "string", "enum": []string{"ego", "all", "none"}, "default": d.body,
			"description": "Whose source bodies to include: the symbol's only, every symbol's, or none."}
	}
	b, err := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             []string{"file", "line", "col"},
		"additionalProperties": false,
	})
	if err != nil {
		panic(err) // the schema is built from constants
	}
	return b
}

// toolArgs are a tool call's arguments. Pointers tell a missing argument
// from a zero one.
type toolArgs struct {
	File      *string `json:"file"`
	Line      *int    `json:"line"`
	Col       *int    `json:"col"`
	Format    *string `json:"format"`
	Depth     *int    `json:"depth"`
	Edges     *string `json:"edges"`
	Direction *string `json:"direction"`
	Body      *string `json:"body"`
}

// callTool runs cmd for an MCP tool call through the same validation and
// execution as the CLI. The file must be inside the root.
func callTool(ctx context.Context, cfg mcpConfig, srv servers, cmd string, raw json.RawMessage) (string, error) {
	r, err := toolRequest(cmd, raw)
	if err != nil {
		return "", err
	}
	r.root, r.server, r.timeout, r.confine = cfg.root, cfg.server, cfg.timeout, true
	o, err := r.validate()
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := execute(ctx, cmd, o, srv, &out); err != nil {
		return "", err
	}
	return out.String(), nil
}

func toolRequest(cmd string, raw json.RawMessage) (request, error) {
	var a toolArgs
	if raw != nil {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			return request{}, fmt.Errorf("invalid arguments: %v (accepted: %s)", err, accepted(cmd))
		}
	}
	if cmd != "inspect_ego" && (a.Depth != nil || a.Edges != nil || a.Direction != nil || a.Body != nil) {
		return request{}, fmt.Errorf("depth, edges, direction and body are for inspect_ego only (accepted: %s)", accepted(cmd))
	}
	var missing []string
	if a.File == nil {
		missing = append(missing, "file")
	}
	if a.Line == nil {
		missing = append(missing, "line")
	}
	if a.Col == nil {
		missing = append(missing, "col")
	}
	if len(missing) > 0 {
		return request{}, errors.New("missing required arguments: " + strings.Join(missing, ", "))
	}
	r := newRequest(cmd)
	r.file, r.line, r.col = *a.File, *a.Line, *a.Col
	set(&r.format, a.Format)
	set(&r.depth, a.Depth)
	set(&r.edges, a.Edges)
	set(&r.direction, a.Direction)
	set(&r.body, a.Body)
	return r, nil
}

func accepted(cmd string) string {
	if cmd == "inspect_ego" {
		return "file, line, col, format, depth, edges, direction, body"
	}
	return "file, line, col, format"
}

func set[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}
