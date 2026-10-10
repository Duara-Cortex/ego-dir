package lsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// stderrTail is how much of a server's stderr is kept for error messages.
const stderrTail = 4096

// process is a language server child process.
type process struct {
	cmd    *exec.Cmd
	stderr *tailBuffer
	exited chan struct{} // closed once Wait returns
	err    error         // Wait's result, valid after exited is closed
}

// Start launches a language server with command (program and arguments)
// in root, and runs the initialise handshake. The server's stderr is kept
// (the last few KB) and included in errors.
func Start(ctx context.Context, command []string, root string) (*Client, error) {
	if len(command) == 0 || command[0] == "" {
		return nil, errors.New("start language server: empty command")
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("start language server %q: %w", command[0], err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("start language server %q: %w", command[0], err)
	}
	p := &process{cmd: cmd, stderr: &tailBuffer{max: stderrTail}, exited: make(chan struct{})}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start language server %q: %w", command[0], err)
	}
	go func() {
		p.err = cmd.Wait()
		close(p.exited)
	}()

	c, err := NewClient(ctx, &stdio{stdout, stdin}, root)
	if err != nil {
		p.kill()
		return nil, p.annotate(fmt.Errorf("language server %q: %w", command[0], err))
	}
	c.proc = p
	return c, nil
}

// wait waits for the process to exit, killing it if ctx ends first. A
// non-zero exit is an error that includes the end of its stderr.
func (p *process) wait(ctx context.Context) error {
	select {
	case <-p.exited:
	case <-ctx.Done():
		p.kill()
		return p.annotate(fmt.Errorf("language server did not exit: %w", ctx.Err()))
	}
	if p.err != nil {
		return p.annotate(fmt.Errorf("language server: %w", p.err))
	}
	return nil
}

func (p *process) kill() {
	_ = p.cmd.Process.Kill()
	<-p.exited
}

// annotate adds the tail of the server's stderr to err.
func (p *process) annotate(err error) error {
	if tail := strings.TrimSpace(p.stderr.String()); tail != "" {
		return fmt.Errorf("%w\nserver stderr:\n%s", err, tail)
	}
	return err
}

// stdio joins a child's stdout and stdin into one stream.
type stdio struct {
	io.ReadCloser
	w io.WriteCloser
}

func (s *stdio) Write(b []byte) (int, error) { return s.w.Write(b) }

func (s *stdio) Close() error {
	return errors.Join(s.w.Close(), s.ReadCloser.Close())
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
