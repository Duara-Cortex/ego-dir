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
)

// Lifecycle methods. Project convention is British spelling, which differs
// from the specification's spelling of the first two.
const (
	methodInitialise  = "initialise"
	methodInitialised = "initialised"
	methodShutdown    = "shutdown"
	methodExit        = "exit"
)

// Client is a read-only LSP client for one workspace.
type Client struct {
	conn *conn
	proc *process // nil when the client was given a stream

	closeOnce sync.Once
	closeErr  error
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

// NewClient runs the initialise handshake over rwc for the workspace at
// root. The client owns rwc and closes it in Close.
func NewClient(ctx context.Context, rwc io.ReadWriteCloser, root string) (*Client, error) {
	rootURI := FileURI(root)
	c := &Client{conn: newConn(rwc, answerServerRequest)}
	params := InitialiseParams{
		ProcessID:        os.Getpid(),
		ClientInfo:       ClientInfo{Name: "ego-dir"},
		RootURI:          rootURI,
		WorkspaceFolders: []WorkspaceFolder{{URI: rootURI, Name: filepath.Base(root)}},
		Capabilities:     clientCapabilities,
	}
	var result InitialiseResult
	if err := c.conn.call(ctx, methodInitialise, params, &result); err != nil {
		_ = c.conn.close()
		return nil, fmt.Errorf("initialise language server: %w", err)
	}
	if err := c.conn.notify(methodInitialised, struct{}{}); err != nil {
		_ = c.conn.close()
		return nil, fmt.Errorf("initialise language server: %w", err)
	}
	return c, nil
}

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
