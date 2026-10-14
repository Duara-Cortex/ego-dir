// Package mcp serves the Model Context Protocol over stdio: JSON-RPC 2.0,
// one message per line. It knows nothing about ego-dir's tools; a Server is
// given a fixed list of Tools and answers tools/list and tools/call.
//
// It speaks both eras of the protocol. A request whose _meta names a
// protocol version is modern (2026-07-28 and later): it stands alone, and
// server/discover describes the server. Any other request is legacy
// (2025-11-25 and earlier), after an initialize handshake; ping is legacy
// only.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
)

// ModernVersions are the revisions, latest first, whose requests carry
// their version in _meta.
var ModernVersions = []string{"2026-07-28"}

// LegacyVersions are the revisions, latest first, that initialize
// negotiates.
var LegacyVersions = []string{"2025-11-25", "2025-06-18", "2024-11-05"}

// JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	CodeUnsupportedProtocolVersion = -32022
)

// Reserved _meta keys.
const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// ListTTL is how long, in milliseconds, a modern client may cache the
// tool list. The list is fixed for the life of the process.
const ListTTL = 3_600_000

// DefaultMaxConcurrent is how many tool calls run at once when
// Server.MaxConcurrent is not set.
const DefaultMaxConcurrent = 4

// Tool is a tool the server offers. Handler gets the call's arguments as
// sent (nil when absent) and returns the text of the result. An error from
// Handler becomes a result with isError set and the error's text, so the
// model can read it and try again.
type Tool struct {
	Name        string
	Title       string
	Description string
	InputSchema json.RawMessage
	Handler     func(ctx context.Context, arguments json.RawMessage) (string, error)
}

// Server answers MCP requests for a fixed set of tools.
type Server struct {
	Name    string
	Version string
	Tools   []Tool
	// MaxConcurrent bounds the tool calls running at once; calls beyond it
	// wait for a slot. Zero means DefaultMaxConcurrent.
	MaxConcurrent int
}

// message is any JSON-RPC 2.0 message.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message) }

// response is a reply. ID is a pointer to a RawMessage so that an unknown
// id is written as null rather than left out.
type response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  any              `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

// session is one connection's state.
type session struct {
	srv   *Server
	slots chan struct{}

	mu       sync.Mutex
	w        io.Writer
	writeErr error
	inFlight map[string]*call // tool calls by id
	calls    sync.WaitGroup
}

type call struct {
	cancel    context.CancelFunc
	cancelled bool
}

// Serve reads requests from r and writes replies to w until r ends, then
// cancels the tool calls still running, waits for them and returns nil. It
// returns early only if reading or writing fails.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	limit := s.MaxConcurrent
	if limit <= 0 {
		limit = DefaultMaxConcurrent
	}
	ss := &session{srv: s, slots: make(chan struct{}, limit), w: w, inFlight: map[string]*call{}}
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		ss.abandon()
		cancel()
		ss.calls.Wait()
	}()

	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			ss.handle(ctx, bytes.TrimSpace(line))
		}
		if werr := ss.err(); werr != nil {
			return werr
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
	}
}

// abandon marks every running call cancelled: the client has gone, so
// nothing more is written.
func (ss *session) abandon() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for _, c := range ss.inFlight {
		c.cancelled = true
	}
}

func (ss *session) err() error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.writeErr
}

// handle answers one line.
func (ss *session) handle(ctx context.Context, line []byte) {
	if line[0] == '[' {
		ss.reply(nil, nil, &rpcError{Code: CodeInvalidRequest, Message: "batches are not supported"})
		return
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		if json.Valid(line) {
			ss.reply(nil, nil, &rpcError{Code: CodeInvalidRequest, Message: "a message must be a JSON object"})
		} else {
			ss.reply(nil, nil, &rpcError{Code: CodeParseError, Message: "parse error: " + err.Error()})
		}
		return
	}
	if len(m.ID) > 0 && string(m.ID) == "null" {
		m.ID = nil
	}
	if m.Method == "" {
		if m.Result == nil && m.Error == nil {
			ss.reply(m.ID, nil, &rpcError{Code: CodeInvalidRequest, Message: "a message needs a method"})
		}
		return // a response to a request this server never sends
	}
	if m.JSONRPC != "2.0" {
		if m.ID != nil {
			ss.reply(m.ID, nil, &rpcError{Code: CodeInvalidRequest, Message: `jsonrpc must be "2.0"`})
		}
		return
	}
	if m.ID == nil {
		ss.notification(m)
		return
	}
	if m.Method == "initialize" {
		result, err := ss.srv.initialize(m.Params)
		ss.reply(m.ID, result, err)
		return
	}
	modern, rerr := era(m.Params)
	if rerr != nil {
		ss.reply(m.ID, nil, rerr)
		return
	}
	switch {
	case m.Method == "server/discover" && modern:
		ss.reply(m.ID, ss.srv.discover(), nil)
	case m.Method == "server/discover":
		ss.reply(m.ID, nil, &rpcError{Code: CodeInvalidParams, Message: "server/discover needs " + metaProtocolVersion + " in _meta"})
	case m.Method == "ping" && !modern:
		ss.reply(m.ID, struct{}{}, nil)
	case m.Method == "tools/list":
		ss.reply(m.ID, ss.srv.list(modern), nil)
	case m.Method == "tools/call":
		ss.call(ctx, m, modern)
	default:
		ss.reply(m.ID, nil, &rpcError{Code: CodeMethodNotFound, Message: "method not found: " + m.Method})
	}
}

// era reports whether a request is modern: its _meta names a protocol
// version. A modern request must name a version this server speaks and
// declare the client's capabilities.
func era(params json.RawMessage) (modern bool, rerr *rpcError) {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false, nil
	}
	raw, ok := p.Meta[metaProtocolVersion]
	if !ok {
		return false, nil
	}
	var version string
	if err := json.Unmarshal(raw, &version); err != nil {
		return true, &rpcError{Code: CodeInvalidParams, Message: metaProtocolVersion + " must be a string"}
	}
	if !slices.Contains(ModernVersions, version) {
		return true, &rpcError{Code: CodeUnsupportedProtocolVersion, Message: "Unsupported protocol version",
			Data: map[string]any{"supported": supportedVersions(), "requested": version}}
	}
	var caps map[string]json.RawMessage
	if raw, ok := p.Meta[metaClientCapabilities]; !ok || json.Unmarshal(raw, &caps) != nil || caps == nil {
		return true, &rpcError{Code: CodeInvalidParams, Message: "_meta needs " + metaClientCapabilities + " (an object)"}
	}
	return true, nil
}

// supportedVersions lists every version, modern then legacy, latest first.
func supportedVersions() []string {
	return slices.Concat(ModernVersions, LegacyVersions)
}

func (ss *session) notification(m message) {
	if m.Method != "notifications/cancelled" {
		return // notifications/initialized and anything unknown need nothing
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(m.Params, &p) != nil || len(p.RequestID) == 0 {
		return
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if c, ok := ss.inFlight[string(p.RequestID)]; ok {
		c.cancelled = true
		c.cancel()
	}
}

// reply writes a response.
func (ss *session) reply(id json.RawMessage, result any, rerr *rpcError) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.writeLocked(id, result, rerr)
}

// finish ends a tool call and writes its result, unless the client
// cancelled it. Both happen under one lock, so a cancellation read before
// the result is written always suppresses it.
func (ss *session) finish(id json.RawMessage, result callResult) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	c := ss.inFlight[string(id)]
	delete(ss.inFlight, string(id))
	if c == nil || c.cancelled {
		return
	}
	ss.writeLocked(id, result, nil)
}

func (ss *session) writeLocked(id json.RawMessage, result any, rerr *rpcError) {
	if ss.writeErr != nil {
		return
	}
	resp := response{JSONRPC: "2.0", Error: rerr}
	if id != nil {
		resp.ID = &id
	}
	if rerr == nil {
		resp.Result = result
	}
	b, err := json.Marshal(resp)
	if err != nil {
		b, _ = json.Marshal(response{JSONRPC: "2.0", ID: resp.ID, Error: &rpcError{Code: CodeInternalError, Message: err.Error()}})
	}
	if _, err := ss.w.Write(append(b, '\n')); err != nil {
		ss.writeErr = fmt.Errorf("write: %w", err)
	}
}

type initializeResult struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    any        `json:"capabilities"`
	ServerInfo      serverInfo `json:"serverInfo"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// modernFields are what every modern result carries. They are left out of
// legacy results.
type modernFields struct {
	ResultType string      `json:"resultType,omitempty"`
	Meta       *resultMeta `json:"_meta,omitempty"`
}

type resultMeta struct {
	ServerInfo serverInfo `json:"io.modelcontextprotocol/serverInfo"`
}

func (s *Server) complete(modern bool) modernFields {
	if !modern {
		return modernFields{}
	}
	return modernFields{ResultType: "complete", Meta: &resultMeta{ServerInfo: serverInfo{Name: s.Name, Version: s.Version}}}
}

// cacheFields are the caching hints of a modern list or discover result.
type cacheFields struct {
	TTLMs      *int   `json:"ttlMs,omitempty"`
	CacheScope string `json:"cacheScope,omitempty"`
}

func cacheable(modern bool) cacheFields {
	if !modern {
		return cacheFields{}
	}
	ttl := ListTTL
	return cacheFields{TTLMs: &ttl, CacheScope: "public"}
}

func capabilities() map[string]any {
	return map[string]any{"tools": struct{}{}}
}

type discoverResult struct {
	modernFields
	cacheFields
	SupportedVersions []string `json:"supportedVersions"`
	Capabilities      any      `json:"capabilities"`
}

func (s *Server) discover() discoverResult {
	return discoverResult{modernFields: s.complete(true), cacheFields: cacheable(true),
		SupportedVersions: supportedVersions(), Capabilities: capabilities()}
}

func (s *Server) initialize(params json.RawMessage) (*initializeResult, *rpcError) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ProtocolVersion == "" {
		return nil, &rpcError{Code: CodeInvalidParams, Message: "initialize needs a protocolVersion"}
	}
	version := LegacyVersions[0]
	if slices.Contains(LegacyVersions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	return &initializeResult{
		ProtocolVersion: version,
		Capabilities:    capabilities(),
		ServerInfo:      serverInfo{Name: s.Name, Version: s.Version},
	}, nil
}

type toolInfo struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations annotations     `json:"annotations"`
}

// annotations tell the client every tool only reads.
type annotations struct {
	ReadOnly    bool `json:"readOnlyHint"`
	Destructive bool `json:"destructiveHint"`
	Idempotent  bool `json:"idempotentHint"`
	OpenWorld   bool `json:"openWorldHint"`
}

type listResult struct {
	modernFields
	cacheFields
	Tools []toolInfo `json:"tools"`
}

// list returns every tool, in the order given, on one page.
func (s *Server) list(modern bool) listResult {
	tools := make([]toolInfo, len(s.Tools))
	for i, t := range s.Tools {
		tools[i] = toolInfo{Name: t.Name, Title: t.Title, Description: t.Description, InputSchema: t.InputSchema,
			Annotations: annotations{ReadOnly: true, Idempotent: true}}
	}
	return listResult{modernFields: s.complete(modern), cacheFields: cacheable(modern), Tools: tools}
}

func (s *Server) tool(name string) (Tool, bool) {
	for _, t := range s.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	modernFields
	Content []content `json:"content"`
	IsError bool      `json:"isError"`
}

// call starts a tool call in its own goroutine, so pings and cancellations
// are still read while it runs.
func (ss *session) call(ctx context.Context, m message, modern bool) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil || p.Name == "" {
		ss.reply(m.ID, nil, &rpcError{Code: CodeInvalidParams, Message: "tools/call needs a tool name"})
		return
	}
	t, ok := ss.srv.tool(p.Name)
	if !ok {
		ss.reply(m.ID, nil, &rpcError{Code: CodeInvalidParams, Message: "unknown tool: " + p.Name})
		return
	}
	if string(p.Arguments) == "null" {
		p.Arguments = nil
	}

	ctx, cancel := context.WithCancel(ctx)
	ss.mu.Lock()
	if _, busy := ss.inFlight[string(m.ID)]; busy {
		ss.mu.Unlock()
		cancel()
		ss.reply(nil, nil, &rpcError{Code: CodeInvalidRequest, Message: "request id already in use: " + string(m.ID)})
		return
	}
	ss.inFlight[string(m.ID)] = &call{cancel: cancel}
	ss.mu.Unlock()

	ss.calls.Add(1)
	go func() {
		defer ss.calls.Done()
		defer cancel()
		text, err := ss.run(ctx, t, p.Arguments)
		res := callResult{modernFields: ss.srv.complete(modern), Content: []content{{Type: "text", Text: text}}}
		if err != nil {
			res.Content[0].Text, res.IsError = err.Error(), true
		}
		ss.finish(m.ID, res)
	}()
}

// run waits for a free slot, then runs the tool.
func (ss *session) run(ctx context.Context, t Tool, args json.RawMessage) (text string, err error) {
	select {
	case ss.slots <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-ss.slots }()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s failed: %v", t.Name, r)
		}
	}()
	return t.Handler(ctx, args)
}
