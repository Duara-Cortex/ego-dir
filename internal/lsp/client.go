package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Lifecycle methods.
const (
	methodShutdown = "shutdown"
	methodExit     = "exit"
)

// handshake is one spelling of the initialise request and its matching
// notification.
type handshake struct{ request, notification string }

// handshakes are tried in order until the server accepts one.
var handshakes = []handshake{
	{"initialise", "initialised"},
	{"initialize", "initialized"},
}

// Client is a read-only LSP client for one workspace.
type Client struct {
	conn *conn
	proc *process // nil when the client was given a stream

	mu       sync.Mutex
	watchers map[string]bool // ids of didChangeWatchedFiles registrations

	closeOnce sync.Once
	closeErr  error
}

// Config chooses optional client behaviour.
type Config struct {
	// WatchFiles declares that the client sends
	// workspace/didChangeWatchedFiles, and lets the server register for it.
	WatchFiles bool
}

// TextDocumentIdentifier names a document.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// TextDocumentPositionParams is a position in a document.
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// InitialiseParams is sent in the initialise request.
type InitialiseParams struct {
	ProcessID        int               `json:"processId"`
	ClientInfo       ClientInfo        `json:"clientInfo"`
	RootURI          string            `json:"rootUri"`
	WorkspaceFolders []WorkspaceFolder `json:"workspaceFolders"`
	Capabilities     json.RawMessage   `json:"capabilities"`
}

// ClientInfo identifies the client to the server.
type ClientInfo struct {
	Name string `json:"name"`
}

// WorkspaceFolder is a root folder of the workspace.
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// InitialiseResult is the server's answer to initialise.
type InitialiseResult struct {
	Capabilities json.RawMessage `json:"capabilities"`
	ServerInfo   *struct {
		Name    string `json:"name"`
		Version string `json:"version,omitempty"`
	} `json:"serverInfo,omitempty"`
}

// clientCapabilities declares the requests ego-dir makes. Progress
// reporting is off: ego-dir has no use for it.
var clientCapabilities = json.RawMessage(`{
	"textDocument": {
		"definition": {"linkSupport": true},
		"typeDefinition": {"linkSupport": true},
		"implementation": {"linkSupport": true},
		"references": {},
		"hover": {"contentFormat": ["markdown", "plaintext"]},
		"documentSymbol": {"hierarchicalDocumentSymbolSupport": true},
		"callHierarchy": {}
	},
	"workspace": {"workspaceFolders": true},
	"window": {"workDoneProgress": false}
}`)

// watchingCapabilities is clientCapabilities with dynamic registration of
// watched-file notifications.
var watchingCapabilities = json.RawMessage(`{
	"textDocument": {
		"definition": {"linkSupport": true},
		"typeDefinition": {"linkSupport": true},
		"implementation": {"linkSupport": true},
		"references": {},
		"hover": {"contentFormat": ["markdown", "plaintext"]},
		"documentSymbol": {"hierarchicalDocumentSymbolSupport": true},
		"callHierarchy": {}
	},
	"workspace": {"workspaceFolders": true, "didChangeWatchedFiles": {"dynamicRegistration": true}},
	"window": {"workDoneProgress": false}
}`)

// methodWatchedFiles is the notification of changes to files on disk.
const methodWatchedFiles = "workspace/didChangeWatchedFiles"

// NewClient runs the initialise handshake over rwc for the workspace at
// root. The client owns rwc and closes it in Close.
func NewClient(ctx context.Context, rwc io.ReadWriteCloser, root string) (*Client, error) {
	return NewClientWith(ctx, rwc, root, Config{})
}

// NewClientWith is NewClient with optional behaviour.
func NewClientWith(ctx context.Context, rwc io.ReadWriteCloser, root string, cfg Config) (*Client, error) {
	rootURI := FileURI(root)
	c := &Client{watchers: map[string]bool{}}
	c.conn = newConn(rwc, c.answerServerRequest)
	caps := clientCapabilities
	if cfg.WatchFiles {
		caps = watchingCapabilities
	}
	params := InitialiseParams{
		ProcessID:        os.Getpid(),
		ClientInfo:       ClientInfo{Name: "ego-dir"},
		RootURI:          rootURI,
		WorkspaceFolders: []WorkspaceFolder{{URI: rootURI, Name: filepath.Base(root)}},
		Capabilities:     caps,
	}
	var errs []error
	for _, h := range handshakes {
		var result InitialiseResult
		err := c.conn.call(ctx, h.request, params, &result)
		if err == nil {
			if err := c.conn.notify(h.notification, struct{}{}); err != nil {
				_ = c.conn.close()
				return nil, fmt.Errorf("initialise language server: %w", err)
			}
			return c, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", h.request, err))
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) {
			break // the session failed; another spelling cannot help
		}
	}
	_ = c.conn.close()
	return nil, fmt.Errorf("initialise language server: %w", errors.Join(errs...))
}

// answerServerRequest replies to requests a server sends to the client,
// remembering which registrations are for watched files.
func (c *Client) answerServerRequest(method string, params json.RawMessage) (any, *RPCError) {
	switch method {
	case "client/registerCapability":
		var p struct {
			Registrations []struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			} `json:"registrations"`
		}
		_ = json.Unmarshal(params, &p)
		c.mu.Lock()
		for _, r := range p.Registrations {
			if r.Method == methodWatchedFiles {
				c.watchers[r.ID] = true
			}
		}
		c.mu.Unlock()
	case "client/unregisterCapability":
		var p struct {
			Unregistrations []struct {
				ID string `json:"id"`
			} `json:"unregisterations"`
		}
		_ = json.Unmarshal(params, &p)
		c.mu.Lock()
		for _, r := range p.Unregistrations {
			delete(c.watchers, r.ID)
		}
		c.mu.Unlock()
	}
	return answerServerRequest(method, params)
}

// WatchesFiles reports whether the server has registered for
// workspace/didChangeWatchedFiles.
func (c *Client) WatchesFiles() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.watchers) > 0
}

// Done is closed when the connection to the server ends, whether by Close
// or because the server stopped.
func (c *Client) Done() <-chan struct{} { return c.conn.done }

// Annotate adds the end of a started server's stderr to err, once the
// server has stopped. It waits briefly for the process to exit, so its last
// words are kept. A client given a stream returns err as it is.
func (c *Client) Annotate(err error) error {
	if err == nil || c.proc == nil {
		return err
	}
	select {
	case <-c.proc.exited:
	case <-time.After(annotateWait):
	}
	return c.proc.annotate(err)
}

// annotateWait is how long Annotate waits for a stopped server to exit.
const annotateWait = 2 * time.Second

// answerServerRequest replies to requests a server sends to the client.
// The ones servers commonly block on are acknowledged; others are refused.
func answerServerRequest(method string, params json.RawMessage) (any, *RPCError) {
	switch method {
	case "window/workDoneProgress/create", "client/registerCapability", "client/unregisterCapability",
		"window/showMessageRequest", "workspace/workspaceFolders":
		return nil, nil
	case "workspace/configuration":
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(params, &p)
		return make([]any, len(p.Items)), nil // one null (defaults) per item
	default:
		return nil, &RPCError{Code: codeMethodNotFound, Message: "ego-dir does not support " + method}
	}
}

// Close runs the shutdown/exit sequence, closes the connection and, for a
// started server, waits for the process to end (killing it if ctx ends
// first). Close is safe to call more than once.
func (c *Client) Close(ctx context.Context) error {
	c.closeOnce.Do(func() {
		var errs []error
		if err := c.conn.call(ctx, methodShutdown, nil, nil); err != nil {
			errs = append(errs, fmt.Errorf("shutdown: %w", err))
		}
		_ = c.conn.notify(methodExit, nil)
		if c.proc != nil {
			errs = append(errs, c.proc.wait(ctx))
		}
		_ = c.conn.close()
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}

// DidOpen tells the server a document is open, with its full text.
func (c *Client) DidOpen(ctx context.Context, uri, languageID, text string) error {
	return c.conn.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": languageID, "version": 1, "text": text},
	})
}

// DidChange replaces the whole text of an open document.
func (c *Client) DidChange(ctx context.Context, uri string, version int, text string) error {
	return c.conn.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": version},
		"contentChanges": []map[string]any{{"text": text}},
	})
}

// DidClose tells the server a document is closed; it reads it from disk
// again.
func (c *Client) DidClose(ctx context.Context, uri string) error {
	return c.conn.notify("textDocument/didClose", map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
	})
}

// FileChangeType is how a watched file changed.
type FileChangeType int

// File change types.
const (
	FileCreated FileChangeType = 1
	FileChanged FileChangeType = 2
	FileDeleted FileChangeType = 3
)

// FileEvent is one changed file.
type FileEvent struct {
	URI  string         `json:"uri"`
	Type FileChangeType `json:"type"`
}

// DidChangeWatchedFiles tells the server files changed on disk.
func (c *Client) DidChangeWatchedFiles(ctx context.Context, changes []FileEvent) error {
	return c.conn.notify(methodWatchedFiles, map[string]any{"changes": changes})
}

func positionParams(uri string, pos Position) TextDocumentPositionParams {
	return TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: pos}
}

// Definition returns where the symbol at pos is declared.
func (c *Client) Definition(ctx context.Context, uri string, pos Position) ([]Location, error) {
	return c.locations(ctx, "textDocument/definition", positionParams(uri, pos))
}

// TypeDefinition returns where the type of the symbol at pos is declared.
func (c *Client) TypeDefinition(ctx context.Context, uri string, pos Position) ([]Location, error) {
	return c.locations(ctx, "textDocument/typeDefinition", positionParams(uri, pos))
}

// Implementation returns the implementations of the interface (or the
// interfaces of the type) at pos.
func (c *Client) Implementation(ctx context.Context, uri string, pos Position) ([]Location, error) {
	return c.locations(ctx, "textDocument/implementation", positionParams(uri, pos))
}

// References returns the uses of the symbol at pos, and its declaration
// when includeDeclaration is set.
func (c *Client) References(ctx context.Context, uri string, pos Position, includeDeclaration bool) ([]Location, error) {
	params := struct {
		TextDocumentPositionParams
		Context struct {
			IncludeDeclaration bool `json:"includeDeclaration"`
		} `json:"context"`
	}{TextDocumentPositionParams: positionParams(uri, pos)}
	params.Context.IncludeDeclaration = includeDeclaration
	return c.locations(ctx, "textDocument/references", params)
}

// locations makes a request whose result is Location, Location[],
// LocationLink[] or null, and returns it as []Location.
func (c *Client) locations(ctx context.Context, method string, params any) ([]Location, error) {
	var raw json.RawMessage
	if err := c.conn.call(ctx, method, params, &raw); err != nil {
		return nil, err
	}
	locs, err := decodeLocations(raw)
	if err != nil {
		return nil, fmt.Errorf("lsp: decode %s result: %w", method, err)
	}
	return locs, nil
}

func decodeLocations(raw json.RawMessage) ([]Location, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] != '[' {
		raw = append(append([]byte{'['}, raw...), ']')
	}
	var items []struct {
		Location
		LocationLink
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	out := make([]Location, 0, len(items))
	for _, it := range items {
		if it.TargetURI != "" {
			out = append(out, it.LocationLink.Location())
		} else {
			out = append(out, it.Location)
		}
	}
	return out, nil
}

// Hover returns the hover text for pos, as Markdown. Code blocks from
// MarkedString results are fenced; null gives "".
func (c *Client) Hover(ctx context.Context, uri string, pos Position) (string, error) {
	var result struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := c.conn.call(ctx, "textDocument/hover", positionParams(uri, pos), &result); err != nil {
		return "", err
	}
	text, err := hoverText(result.Contents)
	if err != nil {
		return "", fmt.Errorf("lsp: decode hover result: %w", err)
	}
	return text, nil
}

// hoverText flattens MarkupContent, MarkedString or MarkedString[].
func hoverText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	switch raw[0] {
	case '"':
		var s string
		err := json.Unmarshal(raw, &s)
		return s, err
	case '[':
		var parts []json.RawMessage
		if err := json.Unmarshal(raw, &parts); err != nil {
			return "", err
		}
		texts := make([]string, 0, len(parts))
		for _, p := range parts {
			t, err := hoverText(p)
			if err != nil {
				return "", err
			}
			texts = append(texts, t)
		}
		return strings.Join(texts, "\n\n"), nil
	default:
		var obj struct {
			Kind     string  `json:"kind"`
			Language *string `json:"language"`
			Value    string  `json:"value"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return "", err
		}
		if obj.Language != nil {
			return "```" + *obj.Language + "\n" + obj.Value + "\n```", nil
		}
		return obj.Value, nil
	}
}

// DocumentSymbols returns the document's outline. A flat
// SymbolInformation[] answer is converted, with each symbol's location
// used as both its range and its selection range.
func (c *Client) DocumentSymbols(ctx context.Context, uri string) ([]DocumentSymbol, error) {
	var raw []struct {
		DocumentSymbol
		Location *Location `json:"location"`
	}
	params := map[string]any{"textDocument": TextDocumentIdentifier{URI: uri}}
	if err := c.conn.call(ctx, "textDocument/documentSymbol", params, &raw); err != nil {
		return nil, err
	}
	out := make([]DocumentSymbol, 0, len(raw))
	for _, s := range raw {
		if s.Location != nil {
			s.DocumentSymbol.Range = s.Location.Range
			s.DocumentSymbol.SelectionRange = s.Location.Range
		}
		out = append(out, s.DocumentSymbol)
	}
	return out, nil
}

// PrepareCallHierarchy returns the callable symbol at pos, if any.
func (c *Client) PrepareCallHierarchy(ctx context.Context, uri string, pos Position) ([]CallHierarchyItem, error) {
	var items []CallHierarchyItem
	err := c.conn.call(ctx, "textDocument/prepareCallHierarchy", positionParams(uri, pos), &items)
	return items, err
}

// IncomingCalls returns the callers of item.
func (c *Client) IncomingCalls(ctx context.Context, item CallHierarchyItem) ([]CallHierarchyIncomingCall, error) {
	var calls []CallHierarchyIncomingCall
	err := c.conn.call(ctx, "callHierarchy/incomingCalls", map[string]any{"item": item}, &calls)
	return calls, err
}

// OutgoingCalls returns the callees of item.
func (c *Client) OutgoingCalls(ctx context.Context, item CallHierarchyItem) ([]CallHierarchyOutgoingCall, error) {
	var calls []CallHierarchyOutgoingCall
	err := c.conn.call(ctx, "callHierarchy/outgoingCalls", map[string]any{"item": item}, &calls)
	return calls, err
}
