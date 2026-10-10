package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestReadFrame(t *testing.T) {
	in := "content-length: 2\r\nContent-Type: x\r\n\r\n{}"
	if got, err := readFrame(bufio.NewReader(strings.NewReader(in))); err != nil || string(got) != "{}" {
		t.Fatalf("readFrame = %q, %v", got, err)
	}
	if _, err := readFrame(bufio.NewReader(strings.NewReader(""))); err != io.EOF {
		t.Errorf("readFrame(empty) = %v, want io.EOF", err)
	}
	for _, bad := range []string{
		"Content-Type: x\r\n\r\n{}",
		"Content-Length: -1\r\n\r\n",
		"Content-Length: x\r\n\r\n",
		"no colon\r\n\r\n",
		"Content-Length: 9\r\n\r\n{}",
		"Content-Length: 2\r\n",
	} {
		if _, err := readFrame(bufio.NewReader(strings.NewReader(bad))); err == nil {
			t.Errorf("readFrame(%q) succeeded", bad)
		}
	}
}

// rawServer gives a test direct control of the server side of a conn.
func rawServer(t *testing.T) (*conn, net.Conn, *bufio.Reader) {
	t.Helper()
	client, server := net.Pipe()
	c := newConn(client, answerServerRequest)
	t.Cleanup(func() { c.close(); server.Close() })
	return c, server, bufio.NewReader(server)
}

func sendFrame(t *testing.T, w io.Writer, body string) {
	t.Helper()
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body); err != nil {
		t.Fatal(err)
	}
}

func TestConnSkipsJunkAndStrayResponses(t *testing.T) {
	c, server, r := rawServer(t)
	done := make(chan error, 1)
	var result string
	go func() { done <- c.call(context.Background(), "x", nil, &result) }()

	body, err := readFrame(r)
	if err != nil {
		t.Fatal(err)
	}
	var req message
	_ = json.Unmarshal(body, &req)

	sendFrame(t, server, `not json`)                                // skipped
	sendFrame(t, server, `{"jsonrpc":"2.0","id":"abc","result":1}`) // non-numeric id: ignored
	sendFrame(t, server, `{"jsonrpc":"2.0","id":999,"result":1}`)   // unknown id: ignored
	sendFrame(t, server, `{"jsonrpc":"2.0","method":"window/logMessage","params":{}}`)
	sendFrame(t, server, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":"ok"}`)
	if err := <-done; err != nil || result != "ok" {
		t.Errorf("call = %q, %v", result, err)
	}
}

func TestConnBadResultType(t *testing.T) {
	c, server, r := rawServer(t)
	done := make(chan error, 1)
	var result int
	go func() { done <- c.call(context.Background(), "x", nil, &result) }()
	body, _ := readFrame(r)
	var req message
	_ = json.Unmarshal(body, &req)
	sendFrame(t, server, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":"text"}`)
	if err := <-done; err == nil {
		t.Error("decoding a string into an int succeeded")
	}
}

func TestConnClosedByPeerReportsCause(t *testing.T) {
	c, server, r := rawServer(t)
	done := make(chan error, 1)
	go func() { done <- c.call(context.Background(), "x", nil, nil) }()
	_, _ = readFrame(r)
	sendFrame(t, server, "") // Content-Length: 0 is not JSON; then a broken header
	_, _ = server.Write([]byte("garbage\r\n\r\n"))
	err := <-done
	if !errors.Is(err, errClosed) || !strings.Contains(err.Error(), "malformed header") {
		t.Errorf("error = %v, want errClosed with the cause", err)
	}
	if err := c.notify("y", nil); !errors.Is(err, errClosed) {
		t.Errorf("notify after the connection failed = %v, want errClosed", err)
	}
	if err := c.call(context.Background(), "z", nil, nil); !errors.Is(err, errClosed) {
		t.Errorf("call after the connection failed = %v, want errClosed", err)
	}
}

func TestConnUseAfterClose(t *testing.T) {
	c, _, _ := rawServer(t)
	if err := c.close(); err != nil {
		t.Fatal(err)
	}
	if err := c.close(); err != nil {
		t.Errorf("second close = %v", err)
	}
	if err := c.call(context.Background(), "x", nil, nil); !errors.Is(err, errClosed) {
		t.Errorf("call after close = %v", err)
	}
	if err := c.notify("x", nil); !errors.Is(err, errClosed) {
		t.Errorf("notify after close = %v", err)
	}
}

func TestConnWriteFailure(t *testing.T) {
	c, server, _ := rawServer(t)
	server.Close()
	// Wait for the read loop to notice, without calling close().
	<-c.done
	if err := c.call(context.Background(), "x", nil, nil); err == nil {
		t.Error("call on a broken pipe succeeded")
	}
	if err := c.write(func() {}); err == nil {
		t.Error("write of an unmarshalable value succeeded")
	}
}

func TestProcessWaitKillsOnTimeout(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("needs sleep")
	}
	cmd := exec.Command("sleep", "30")
	p := &process{cmd: cmd, stderr: &tailBuffer{max: 16}, exited: make(chan struct{})}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.err = cmd.Wait(); close(p.exited) }()
	_, _ = p.stderr.Write([]byte("still indexing"))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := p.wait(ctx)
	if err == nil || !strings.Contains(err.Error(), "did not exit") || !strings.Contains(err.Error(), "still indexing") {
		t.Errorf("wait = %v, want a timeout error with stderr", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("wait did not kill the process promptly")
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 5}
	_, _ = b.Write([]byte("abc"))
	_, _ = b.Write([]byte("defg"))
	if got := b.String(); got != "cdefg" {
		t.Errorf("tail = %q, want %q", got, "cdefg")
	}
}

func TestAnswerServerRequestUnknown(t *testing.T) {
	if _, err := answerServerRequest("client/somethingNew", nil); err == nil || err.Code != codeMethodNotFound {
		t.Errorf("unknown request error = %v, want MethodNotFound", err)
	}
}
