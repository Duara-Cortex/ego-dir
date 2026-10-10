package lsp

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
)

// JSON-RPC and LSP error codes the client produces or recognises.
const (
	codeMethodNotFound = -32601
)

// RPCError is an error response from the language server.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("language server error %d: %s", e.Code, e.Message)
}

// errClosed is returned for calls on a connection that has been closed.
var errClosed = errors.New("lsp: connection closed")

// message is any JSON-RPC 2.0 message on the wire.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// conn is a JSON-RPC 2.0 connection with LSP (Content-Length) framing.
// Calls may be made concurrently; responses are routed by id.
type conn struct {
	rwc io.ReadWriteCloser
	wmu sync.Mutex // serialises frame writes

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan *message
	closed  bool
	err     error         // why the read loop stopped
	done    chan struct{} // closed when the read loop stops

	// serverRequest answers requests the server sends to the client.
	serverRequest func(method string, params json.RawMessage) (any, *RPCError)
}

func newConn(rwc io.ReadWriteCloser, serverRequest func(string, json.RawMessage) (any, *RPCError)) *conn {
	c := &conn{
		rwc:           rwc,
		pending:       map[int64]chan *message{},
		done:          make(chan struct{}),
		serverRequest: serverRequest,
	}
	go c.readLoop()
	return c
}

// call sends a request and decodes the response's result into result
// (which may be nil). When ctx ends first, the server is sent
// $/cancelRequest and ctx.Err() is returned.
func (c *conn) call(ctx context.Context, method string, params, result any) error {
	if err := c.usable(); err != nil {
		return err
	}
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan *message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.forget(id)
		return fmt.Errorf("lsp: send %s: %w", method, err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result == nil || len(resp.Result) == 0 || string(resp.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("lsp: decode %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		_ = c.notify("$/cancelRequest", map[string]any{"id": id})
		return ctx.Err()
	case <-c.done:
		return fmt.Errorf("lsp: %s: %w", method, c.closeErr())
	}
}

// notify sends a notification.
func (c *conn) notify(method string, params any) error {
	if err := c.usable(); err != nil {
		return err
	}
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// close closes the stream and fails every pending call.
func (c *conn) close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	err := c.rwc.Close()
	<-c.done
	return err
}

// usable returns an error once the connection was closed or the server
// stopped responding, so no write is attempted on a dead stream.
func (c *conn) usable() error {
	select {
	case <-c.done:
		return c.closeErr()
	default:
		return nil
	}
}

func (c *conn) closeErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil || c.closed {
		return errClosed
	}
	return fmt.Errorf("%w: %v", errClosed, c.err)
}

func (c *conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *conn) write(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := fmt.Fprintf(c.rwc, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.rwc.Write(body)
	return err
}

func (c *conn) readLoop() {
	r := bufio.NewReader(c.rwc)
	var err error
	for {
		var body []byte
		if body, err = readFrame(r); err != nil {
			break
		}
		var m message
		if json.Unmarshal(body, &m) != nil {
			continue // not JSON-RPC; skip rather than kill the session
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			go c.answer(m)
		case m.Method != "":
			// Server notifications (logs, diagnostics, progress) are not needed.
		default:
			c.deliver(&m)
		}
	}
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	close(c.done)
}

func (c *conn) deliver(m *message) {
	id, err := strconv.ParseInt(string(m.ID), 10, 64)
	if err != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

func (c *conn) answer(m message) {
	resp := map[string]any{"jsonrpc": "2.0", "id": m.ID}
	result, rpcErr := c.serverRequest(m.Method, m.Params)
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	_ = c.write(resp)
}

// readFrame reads one Content-Length framed message body.
func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && line == "" && length == -1 {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("read header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("malformed header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("bad Content-Length %q", value)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, errors.New("missing Content-Length header")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}
