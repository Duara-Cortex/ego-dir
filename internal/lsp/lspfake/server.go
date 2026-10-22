// Package lspfake is a scripted Language Server Protocol server for tests.
//
// It speaks JSON-RPC 2.0 with Content-Length framing over any io.ReadWriter.
// It is deliberately independent of the production client in internal/lsp,
// so client tests never check the client against itself.
package lspfake

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// JSON-RPC and LSP error codes used by the fake.
const (
	CodeMethodNotFound   = -32601
	CodeInvalidParams    = -32602
	CodeInternalError    = -32603
	CodeRequestCancelled = -32800
)

// Message is any JSON-RPC 2.0 message as received from the client.
// A request has Method and ID, a notification has Method only and a
// response (to a server-initiated request) has ID and Result or Error.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// IsRequest reports whether m is a request (method and id).
func (m Message) IsRequest() bool { return m.Method != "" && len(m.ID) > 0 }

// IsNotification reports whether m is a notification (method, no id).
func (m Message) IsNotification() bool { return m.Method != "" && len(m.ID) == 0 }

// Error is a JSON-RPC error object. Handlers return it to choose the code.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message) }

// Handler answers one request or notification. For notifications the result
// is ignored. ctx is cancelled when the client sends $/cancelRequest for the
// request or when Serve returns.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server is a scripted LSP server. Configure it with Handle or Result and
// the fields below, then call Serve. Requests are handled concurrently, one
// goroutine each.
type Server struct {
	// Latency delays the answer to every request other than the initialise
	// handshake and shutdown, as a slow server would.
	Latency time.Duration
	// InitialiseDelay delays the answer to the initialise request, as a
	// server loading a workspace would.
	InitialiseDelay time.Duration
	// BeforeRequest, when set, is called with the method of each request
	// before it is answered.
	BeforeRequest func(method string)

	mu       sync.Mutex
	handlers map[string]Handler
	received []Message
	cancels  map[string]context.CancelFunc // in-flight client requests by id
	pending  map[string]chan Message       // server-initiated calls by id
	nextID   int

	wmu sync.Mutex
	w   io.Writer
}

// New returns a server that answers initialise with empty capabilities and
// shutdown with null. Both can be overridden.
func New() *Server {
	s := &Server{
		handlers: map[string]Handler{},
		cancels:  map[string]context.CancelFunc{},
		pending:  map[string]chan Message{},
	}
	s.Result("initialise", map[string]any{"capabilities": map[string]any{}})
	s.Result("shutdown", nil)
	return s
}

// Handle registers h for method, replacing any earlier handler.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// Result registers a fixed result for method.
func (s *Server) Result(method string, v any) {
	s.Handle(method, func(context.Context, json.RawMessage) (any, error) { return v, nil })
}

// Received returns a copy of every message received so far, in arrival order.
func (s *Server) Received() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.received...)
}

// Methods returns the method of every request and notification received so
// far, in arrival order. Responses are skipped.
func (s *Server) Methods() []string {
	var out []string
	for _, m := range s.Received() {
		if m.Method != "" {
			out = append(out, m.Method)
		}
	}
	return out
}

// Requests returns every request or notification received for method.
func (s *Server) Requests(method string) []Message {
	var out []Message
	for _, m := range s.Received() {
		if m.Method == method {
			out = append(out, m)
		}
	}
	return out
}

// Serve reads messages from rw until the client sends the exit notification
// (returns nil) or the stream ends (returns io.EOF or the read error).
// In-flight handlers are cancelled and waited for before Serve returns.
func (s *Server) Serve(ctx context.Context, rw io.ReadWriter) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
		s.wmu.Lock()
		s.w = nil
		s.wmu.Unlock()
	}()

	s.wmu.Lock()
	s.w = rw
	s.wmu.Unlock()

	r := bufio.NewReader(rw)
	for {
		body, err := ReadFrame(r)
		if err != nil {
			return err
		}
		var m Message
		if err := json.Unmarshal(body, &m); err != nil {
			return fmt.Errorf("lspfake: decode message: %w", err)
		}
		s.mu.Lock()
		s.received = append(s.received, m)
		s.mu.Unlock()

		switch {
		case m.Method == "":
			s.deliver(m)
		case m.Method == "exit":
			return nil
		case m.Method == "$/cancelRequest":
			s.cancelRequest(m.Params)
		case m.IsNotification():
			if h := s.handler(m.Method); h != nil {
				_, _ = h(ctx, m.Params)
			}
		default:
			reqCtx, reqCancel := context.WithCancel(ctx)
			s.mu.Lock()
			s.cancels[string(m.ID)] = reqCancel
			s.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer reqCancel()
				s.answer(reqCtx, m)
				s.mu.Lock()
				delete(s.cancels, string(m.ID))
				s.mu.Unlock()
			}()
		}
	}
}

// Call sends a server-initiated request to the client and waits for its
// response. It is meant to be used from inside a Handler while Serve runs.
func (s *Server) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	s.nextID++
	id := strconv.Quote("fake-" + strconv.Itoa(s.nextID))
	ch := make(chan Message, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	if err := s.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Notify sends a server-initiated notification to the client.
func (s *Server) Notify(method string, params any) error {
	return s.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *Server) handler(method string) Handler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handlers[method]
}

func (s *Server) answer(ctx context.Context, m Message) {
	resp := map[string]any{"jsonrpc": "2.0", "id": m.ID}
	if s.BeforeRequest != nil {
		s.BeforeRequest(m.Method)
	}
	delay := s.Latency
	switch m.Method {
	case "initialise", "initialize":
		delay = s.InitialiseDelay
	case "shutdown":
		delay = 0
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			resp["error"] = &Error{Code: CodeRequestCancelled, Message: ctx.Err().Error()}
			_ = s.write(resp)
			return
		}
	}
	h := s.handler(m.Method)
	if h == nil {
		resp["error"] = &Error{Code: CodeMethodNotFound, Message: "method not found: " + m.Method}
		_ = s.write(resp)
		return
	}
	result, err := h(ctx, m.Params)
	var rpcErr *Error
	switch {
	case err == nil:
		resp["result"] = result
	case errors.As(err, &rpcErr):
		resp["error"] = rpcErr
	case ctx.Err() != nil:
		resp["error"] = &Error{Code: CodeRequestCancelled, Message: err.Error()}
	default:
		resp["error"] = &Error{Code: CodeInternalError, Message: err.Error()}
	}
	_ = s.write(resp)
}

func (s *Server) deliver(m Message) {
	s.mu.Lock()
	ch := s.pending[string(m.ID)]
	s.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

func (s *Server) cancelRequest(params json.RawMessage) {
	var p struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancels[string(p.ID)]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Server) write(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.w == nil {
		return errors.New("lspfake: server is not serving")
	}
	return WriteFrame(s.w, body)
}

// WriteFrame writes body with an LSP Content-Length header.
func WriteFrame(w io.Writer, body []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// ReadFrame reads one Content-Length framed message body from r.
func ReadFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" && length == -1 {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("lspfake: read header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("lspfake: malformed header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("lspfake: bad Content-Length %q", value)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, errors.New("lspfake: missing Content-Length header")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("lspfake: read body: %w", err)
	}
	return body, nil
}
