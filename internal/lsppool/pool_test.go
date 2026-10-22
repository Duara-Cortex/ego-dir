package lsppool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
	"github.com/Duara-Cortex/ego-dir/internal/lsppool"
)

func TestMain(m *testing.M) {
	lspfake.RunHelperIfRequested()
	os.Exit(m.Run())
}

// fakes starts in-process Sample servers over pipes and keeps each one.
type fakes struct {
	watch     bool                  // the client offers to watch files
	configure func(*lspfake.Server) // changes each server before it serves
	failNext  int                   // starts still to fail

	mu      sync.Mutex
	servers []*fakeServer
}

type fakeServer struct {
	*lspfake.Server
	command []string
	conn    net.Conn      // the server's end
	done    chan struct{} // closed when Serve returns
}

var errStart = errors.New("start failed")

func (f *fakes) start(ctx context.Context, command []string, root string) (*lsp.Client, error) {
	f.mu.Lock()
	if f.failNext > 0 {
		f.failNext--
		f.mu.Unlock()
		return nil, errStart
	}
	s := lspfake.New()
	lspfake.Sample(s)
	if f.configure != nil {
		f.configure(s)
	}
	clientEnd, serverEnd := net.Pipe()
	fs := &fakeServer{Server: s, command: command, conn: serverEnd, done: make(chan struct{})}
	f.servers = append(f.servers, fs)
	f.mu.Unlock()
	go func() {
		_ = s.Serve(context.Background(), serverEnd)
		serverEnd.Close()
		close(fs.done)
	}()
	c, err := lsp.NewClientWith(ctx, clientEnd, root, lsp.Config{WatchFiles: f.watch})
	if err != nil {
		clientEnd.Close()
		return nil, err
	}
	return c, nil
}

func (f *fakes) started() []*fakeServer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeServer(nil), f.servers...)
}

func (fs *fakeServer) stopped() bool {
	select {
	case <-fs.done:
		return true
	default:
		return false
	}
}

// waitStopped waits for the server to receive shutdown and exit.
func (fs *fakeServer) waitStopped(t *testing.T) {
	t.Helper()
	select {
	case <-fs.done:
	case <-time.After(5 * time.Second):
		t.Fatal("server was not shut down")
	}
	if m := fs.Methods(); len(m) < 2 || m[len(m)-2] != "shutdown" || m[len(m)-1] != "exit" {
		t.Errorf("server's last messages = %v, want shutdown then exit", m)
	}
}

// count returns how many messages of method the server received.
func (fs *fakeServer) count(method string) int { return len(fs.Requests(method)) }

// await waits until the server has received n messages of method (they
// arrive a moment after they are sent), and returns how many it has.
func (fs *fakeServer) await(method string, n int, match func(lspfake.Message) bool) int {
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := 0
		for _, m := range fs.Requests(method) {
			if match == nil || match(m) {
				got++
			}
		}
		if got >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// roundTrip makes a request through s and waits for the answer: the
// server handles messages in order, so everything sent before is recorded.
func roundTrip(t *testing.T, s *lsppool.Session, uri string) {
	t.Helper()
	if _, err := s.Hover(ctx5(t), uri, totalAreaPos); err != nil {
		t.Fatal(err)
	}
}

// sample copies testdata/sample into a temporary directory.
func sample(t *testing.T) (root, shapes, report string) {
	t.Helper()
	src, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(t.TempDir(), "sample")
	if err := os.CopyFS(root, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return root, lsp.FileURI(filepath.Join(root, lspfake.SampleFile)), lsp.FileURI(filepath.Join(root, lspfake.SampleReportFile))
}

func newPool(t *testing.T, f *fakes, opts lsppool.Options) *lsppool.Pool {
	t.Helper()
	if opts.Idle == 0 {
		opts.Idle = time.Minute
	}
	opts.Start = f.start
	p := lsppool.New(opts)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := p.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return p
}

func ctx5(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var gopls = []string{"gopls"}

func acquire(t *testing.T, p *lsppool.Pool, command []string, root string) *lsppool.Session {
	t.Helper()
	s, err := p.Acquire(ctx5(t), command, root)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	return s
}

// totalAreaPos is the name in "func TotalArea(".
var totalAreaPos = lsp.Position{Line: 17, Character: 5}

// callers returns the names of TotalArea's callers, sorted.
func callers(t *testing.T, s *lsppool.Session, shapes string) []string {
	t.Helper()
	items, err := s.PrepareCallHierarchy(ctx5(t), shapes, totalAreaPos)
	if err != nil || len(items) != 1 {
		t.Fatalf("PrepareCallHierarchy = %v, %v", items, err)
	}
	calls, err := s.IncomingCalls(ctx5(t), items[0])
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range calls {
		names = append(names, c.From.Name)
	}
	sort.Strings(names)
	return names
}

func TestStartsLazilyAndReuses(t *testing.T) {
	root, shapes, _ := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{})
	if n := len(f.started()); n != 0 {
		t.Fatalf("%d servers started before any call", n)
	}
	for range 3 {
		s := acquire(t, p, gopls, root)
		if _, err := s.Hover(ctx5(t), shapes, totalAreaPos); err != nil {
			t.Fatal(err)
		}
		if err := s.Release(ctx5(t), nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.started()); n != 1 {
		t.Errorf("%d servers started for three calls, want 1", n)
	}
}

func TestConcurrentCallsShareOneServer(t *testing.T) {
	root, shapes, _ := sample(t)
	f := &fakes{watch: true, configure: func(s *lspfake.Server) {
		s.Latency = 5 * time.Millisecond
		s.InitialiseDelay = 50 * time.Millisecond
	}}
	p := newPool(t, f, lsppool.Options{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			s, err := p.Acquire(ctx5(t), gopls, root)
			if err != nil {
				t.Error(err)
				return
			}
			defer s.Release(context.Background(), nil)
			for range 5 {
				if _, err := s.Hover(ctx5(t), shapes, totalAreaPos); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if n := len(f.started()); n != 1 {
		t.Errorf("%d servers started for eight concurrent calls, want 1", n)
	}
}

func TestIdleShutdown(t *testing.T) {
	root, _, _ := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{Idle: 50 * time.Millisecond})
	acquire(t, p, gopls, root).Release(ctx5(t), nil)
	first := f.started()[0]
	first.waitStopped(t)
	acquire(t, p, gopls, root).Release(ctx5(t), nil)
	if n := len(f.started()); n != 2 {
		t.Errorf("%d servers started, want a second after the first went idle", n)
	}
}

func TestUseResetsTheIdleTimer(t *testing.T) {
	root, _, _ := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{Idle: 600 * time.Millisecond})
	for range 6 {
		acquire(t, p, gopls, root).Release(ctx5(t), nil)
		time.Sleep(150 * time.Millisecond)
	}
	if n := len(f.started()); n != 1 || f.started()[0].stopped() {
		t.Errorf("a server used every 150ms was shut down by a 600ms idle timer (%d started)", n)
	}
}

func TestHeldServerIsNotShutDown(t *testing.T) {
	root, shapes, _ := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{Idle: 20 * time.Millisecond})
	s := acquire(t, p, gopls, root)
	time.Sleep(100 * time.Millisecond)
	if _, err := s.Hover(ctx5(t), shapes, totalAreaPos); err != nil {
		t.Errorf("a held server stopped: %v", err)
	}
	s.Release(ctx5(t), nil)
}

func TestMaxStopsTheLeastRecentlyUsed(t *testing.T) {
	root, _, _ := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{Max: 2})
	a, b, c := []string{"a"}, []string{"b"}, []string{"c"}
	acquire(t, p, a, root).Release(ctx5(t), nil)
	acquire(t, p, b, root).Release(ctx5(t), nil)
	acquire(t, p, a, root).Release(ctx5(t), nil) // b is now the least recently used
	acquire(t, p, c, root).Release(ctx5(t), nil)
	servers := f.started()
	if len(servers) != 3 {
		t.Fatalf("%d servers started, want 3", len(servers))
	}
	servers[1].waitStopped(t)
	if servers[0].stopped() {
		t.Error("a, used more recently than b, was stopped")
	}
}

func TestMaxWaitsWhileEveryServerIsBusy(t *testing.T) {
	root, _, _ := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{Max: 1})
	held := acquire(t, p, []string{"a"}, root)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := p.Acquire(ctx, []string{"b"}, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire beyond Max while busy = %v, want it to wait until its deadline", err)
	}
	got := make(chan error, 1)
	go func() {
		s, err := p.Acquire(ctx5(t), []string{"b"}, root)
		if err == nil {
			s.Release(context.Background(), nil)
		}
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	held.Release(ctx5(t), nil)
	if err := <-got; err != nil {
		t.Errorf("Acquire after the busy server was released: %v", err)
	}
}

func TestCancelledCallLeavesTheServerRunning(t *testing.T) {
	root, shapes, _ := sample(t)
	f := &fakes{watch: true, configure: func(s *lspfake.Server) {
		s.Handle("textDocument/references", func(ctx context.Context, _ json.RawMessage) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}}
	p := newPool(t, f, lsppool.Options{})
	s := acquire(t, p, gopls, root)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.References(ctx, shapes, totalAreaPos, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("References = %v, want the call's deadline", err)
	}
	if err := s.Release(context.Background(), context.DeadlineExceeded); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Release = %v, want the call's error back", err)
	}
	server := f.started()[0]
	if n := server.await("$/cancelRequest", 1, nil); n != 1 {
		t.Errorf("server received %d $/cancelRequest, want 1", n)
	}
	s = acquire(t, p, gopls, root)
	defer s.Release(ctx5(t), nil)
	if _, err := s.Hover(ctx5(t), shapes, totalAreaPos); err != nil {
		t.Errorf("the next call failed: %v", err)
	}
	if len(f.started()) != 1 || server.stopped() {
		t.Error("cancelling a call stopped the server")
	}
}

func TestCallerGivingUpDuringStartKeepsTheServer(t *testing.T) {
	root, _, _ := sample(t)
	f := &fakes{watch: true, configure: func(s *lspfake.Server) { s.InitialiseDelay = 200 * time.Millisecond }}
	p := newPool(t, f, lsppool.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Acquire(ctx, gopls, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire = %v, want the caller's deadline", err)
	}
	acquire(t, p, gopls, root).Release(ctx5(t), nil)
	if n := len(f.started()); n != 1 {
		t.Errorf("%d servers started, want the first start to be kept", n)
	}
}

func TestFailedStartIsRetriedByTheNextCall(t *testing.T) {
	root, _, _ := sample(t)
	f := &fakes{watch: true, failNext: 1}
	p := newPool(t, f, lsppool.Options{})
	if _, err := p.Acquire(ctx5(t), gopls, root); !errors.Is(err, errStart) {
		t.Fatalf("Acquire = %v, want the start's error", err)
	}
	acquire(t, p, gopls, root).Release(ctx5(t), nil)
}

func TestStartTimeoutIsThePools(t *testing.T) {
	root, _, _ := sample(t)
	f := &fakes{watch: true, configure: func(s *lspfake.Server) { s.InitialiseDelay = time.Second }}
	p := newPool(t, f, lsppool.Options{StartTimeout: 50 * time.Millisecond})
	if _, err := p.Acquire(ctx5(t), gopls, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Acquire = %v, want the start to time out", err)
	}
}

// didOpens waits for n didOpen of uri, and returns how many there are.
func didOpens(fs *fakeServer, uri string, n int) int {
	return fs.await("textDocument/didOpen", n, func(m lspfake.Message) bool { return strings.Contains(string(m.Params), uri) })
}

func TestDocumentsAreReferenceCounted(t *testing.T) {
	root, shapes, _ := sample(t)
	text, err := os.ReadFile(filepath.Join(root, lspfake.SampleFile))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{})
	a, b := acquire(t, p, gopls, root), acquire(t, p, gopls, root)
	for _, s := range []*lsppool.Session{a, b, a} {
		if err := s.DidOpen(ctx5(t), shapes, "go", string(text)); err != nil {
			t.Fatal(err)
		}
	}
	server := f.started()[0]
	roundTrip(t, b, shapes)
	if n := didOpens(server, shapes, 1); n != 1 {
		t.Errorf("%d didOpen for one document opened by two sessions, want 1", n)
	}
	a.Release(ctx5(t), nil)
	a.Release(ctx5(t), nil) // a second release changes nothing
	roundTrip(t, b, shapes)
	if n := server.count("textDocument/didClose"); n != 0 {
		t.Errorf("didClose sent while another session holds the document")
	}
	b.Release(ctx5(t), nil)
	if n := server.await("textDocument/didClose", 1, nil); n != 1 {
		t.Errorf("%d didClose after the last session released it, want 1", n)
	}
	c := acquire(t, p, gopls, root)
	defer c.Release(ctx5(t), nil)
	if err := c.DidOpen(ctx5(t), shapes, "go", string(text)); err != nil {
		t.Fatal(err)
	}
	if n := didOpens(server, shapes, 2); n != 2 {
		t.Errorf("%d didOpen after reopening, want 2", n)
	}
}

func TestNewTextOfAnOpenDocumentIsSentAsAChange(t *testing.T) {
	root, shapes, _ := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{})
	a, b := acquire(t, p, gopls, root), acquire(t, p, gopls, root)
	defer a.Release(ctx5(t), nil)
	defer b.Release(ctx5(t), nil)
	if err := a.DidOpen(ctx5(t), shapes, "go", "package sample\n"); err != nil {
		t.Fatal(err)
	}
	if err := b.DidOpen(ctx5(t), shapes, "go", "package sample\n\n"); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, b, shapes)
	changes := f.started()[0].Requests("textDocument/didChange")
	if len(changes) != 1 {
		t.Fatalf("%d didChange, want 1", len(changes))
	}
	var params struct {
		TextDocument   struct{ Version int }
		ContentChanges []struct{ Text string }
	}
	if err := json.Unmarshal(changes[0].Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.TextDocument.Version != 2 || len(params.ContentChanges) != 1 || params.ContentChanges[0].Text != "package sample\n\n" {
		t.Errorf("didChange = %s, want version 2 with the full new text", changes[0].Params)
	}
}

func TestEditOnDiskIsSentToTheServer(t *testing.T) {
	root, shapes, report := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{})
	s := acquire(t, p, gopls, root)
	if got := callers(t, s, shapes); !reflect.DeepEqual(got, []string{"Report"}) {
		t.Fatalf("callers = %v", got)
	}
	s.Release(ctx5(t), nil)

	appendSummary(t, root)
	s = acquire(t, p, gopls, root)
	defer s.Release(ctx5(t), nil)
	if got := callers(t, s, shapes); !reflect.DeepEqual(got, []string{"Report", "Summary"}) {
		t.Errorf("callers after the edit = %v, want [Report Summary]", got)
	}
	server := f.started()[0]
	changes := server.Requests("workspace/didChangeWatchedFiles")
	if len(f.started()) != 1 || len(changes) != 1 {
		t.Fatalf("%d servers, %d didChangeWatchedFiles; want the edit sent to the one server", len(f.started()), len(changes))
	}
	want := `{"changes":[{"uri":"` + report + `","type":2}`
	if real, err := filepath.EvalSymlinks(root); err == nil && real != root {
		// Named again under the resolved root (a macOS temporary directory).
		want += `,{"uri":"` + lsp.FileURI(filepath.Join(real, lspfake.SampleReportFile)) + `","type":2}`
	}
	if want += `]}`; string(changes[0].Params) != want {
		t.Errorf("didChangeWatchedFiles = %s, want %s", changes[0].Params, want)
	}
}

func TestNoChangeNoNotification(t *testing.T) {
	root, _, _ := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{})
	for range 3 {
		acquire(t, p, gopls, root).Release(ctx5(t), nil)
	}
	if n := f.started()[0].count("workspace/didChangeWatchedFiles"); n != 0 {
		t.Errorf("%d didChangeWatchedFiles with nothing changed", n)
	}
}

const summary = "\n// Summary is the area of no shapes.\nfunc Summary() float64 { return sample.TotalArea(nil) }\n"

func appendSummary(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, lspfake.SampleReportFile)
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(text, summary...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestServerThatDoesNotWatchIsRestarted(t *testing.T) {
	root, shapes, _ := sample(t)
	f := &fakes{watch: false}
	p := newPool(t, f, lsppool.Options{})
	acquire(t, p, gopls, root).Release(ctx5(t), nil)
	appendSummary(t, root)
	s := acquire(t, p, gopls, root)
	defer s.Release(ctx5(t), nil)
	if got := callers(t, s, shapes); !reflect.DeepEqual(got, []string{"Report", "Summary"}) {
		t.Errorf("callers after the edit = %v, want [Report Summary]", got)
	}
	servers := f.started()
	if len(servers) != 2 {
		t.Fatalf("%d servers started, want a restart", len(servers))
	}
	servers[0].waitStopped(t)
}

func TestModuleFileChangeRestarts(t *testing.T) {
	root, _, _ := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{})
	acquire(t, p, gopls, root).Release(ctx5(t), nil)
	gomod := filepath.Join(root, "go.mod")
	text, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gomod, append(text, "\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	acquire(t, p, gopls, root).Release(ctx5(t), nil)
	servers := f.started()
	if len(servers) != 2 {
		t.Fatalf("%d servers started, want a restart after go.mod changed", len(servers))
	}
	servers[0].waitStopped(t)
	if n := servers[0].count("workspace/didChangeWatchedFiles"); n != 0 {
		t.Error("the replaced server was sent the change")
	}
}

func TestRestartWaitsForCallsOnTheOldServer(t *testing.T) {
	root, shapes, _ := sample(t)
	f := &fakes{watch: false}
	p := newPool(t, f, lsppool.Options{})
	old := acquire(t, p, gopls, root)
	appendSummary(t, root)
	fresh := acquire(t, p, gopls, root)
	defer fresh.Release(ctx5(t), nil)
	if _, err := old.Hover(ctx5(t), shapes, totalAreaPos); err != nil {
		t.Errorf("the old server stopped while a call held it: %v", err)
	}
	old.Release(ctx5(t), nil)
	f.started()[0].waitStopped(t)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestCrashIsReportedAndTheNextCallRestarts(t *testing.T) {
	root, shapes, _ := sample(t)
	t.Setenv(lspfake.EnvHelper, "sample")
	crash := filepath.Join(t.TempDir(), "crash")
	t.Setenv(lspfake.EnvCrashFile, crash)
	var log syncBuffer
	p := lsppool.New(lsppool.Options{Idle: time.Minute, Log: &log})
	defer p.Close(ctx5(t))
	helper := []string{os.Args[0]}

	s := acquire(t, p, helper, root)
	if err := os.WriteFile(crash, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Hover(ctx5(t), shapes, totalAreaPos)
	if err == nil {
		t.Fatal("Hover succeeded on a crashed server")
	}
	if err := s.Release(ctx5(t), err); !strings.Contains(err.Error(), lspfake.CrashNote) {
		t.Errorf("the call's error = %q, want the server's stderr (%q)", err, lspfake.CrashNote)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(log.String(), "stopped unexpectedly") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := log.String(); !strings.Contains(got, "stopped unexpectedly") || !strings.Contains(got, lspfake.CrashNote) {
		t.Errorf("log = %q, want a note with the server's stderr", got)
	}

	s = acquire(t, p, helper, root)
	defer s.Release(ctx5(t), nil)
	if _, err := s.Hover(ctx5(t), shapes, totalAreaPos); err != nil {
		t.Errorf("the call after the crash failed: %v", err)
	}
}

// A connection that dies between calls fails at most the call that meets
// it, quickly, and the next call gets a new server.
func TestDeadConnectionIsReplaced(t *testing.T) {
	root, shapes, _ := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{})
	acquire(t, p, gopls, root).Release(ctx5(t), nil)
	f.started()[0].conn.Close()
	call := func() error {
		s, err := p.Acquire(ctx5(t), gopls, root)
		if err != nil {
			return err
		}
		_, err = s.Hover(ctx5(t), shapes, totalAreaPos)
		return s.Release(ctx5(t), err)
	}
	if err := call(); err != nil && !strings.Contains(err.Error(), "connection closed") {
		t.Errorf("first call after the connection died = %v, want success or a closed connection", err)
	}
	if err := call(); err != nil {
		t.Errorf("second call after the connection died: %v", err)
	}
	if n := len(f.started()); n != 2 {
		t.Errorf("%d servers started, want a replacement", n)
	}
}

func TestCloseShutsEveryServerDown(t *testing.T) {
	root, _, _ := sample(t)
	f := &fakes{watch: true}
	p := lsppool.New(lsppool.Options{Idle: time.Minute, Start: f.start})
	acquire(t, p, []string{"a"}, root).Release(ctx5(t), nil)
	acquire(t, p, []string{"b"}, root).Release(ctx5(t), nil)
	if err := p.Close(ctx5(t)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, s := range f.started() {
		s.waitStopped(t)
	}
	if _, err := p.Acquire(ctx5(t), gopls, root); !errors.Is(err, lsppool.ErrClosed) {
		t.Errorf("Acquire after Close = %v, want ErrClosed", err)
	}
	if err := p.Close(ctx5(t)); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestCloseShutsAProcessDownCleanly(t *testing.T) {
	root, _, _ := sample(t)
	t.Setenv(lspfake.EnvHelper, "sample")
	var log syncBuffer
	p := lsppool.New(lsppool.Options{Idle: time.Minute, Log: &log})
	acquire(t, p, []string{os.Args[0]}, root).Release(ctx5(t), nil)
	if err := p.Close(ctx5(t)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := log.String(); got != "" {
		t.Errorf("log = %q, want a clean shutdown", got)
	}
}

func TestCloseAbandonsAStartUnderWay(t *testing.T) {
	root, _, _ := sample(t)
	f := &fakes{watch: true, configure: func(s *lspfake.Server) { s.InitialiseDelay = 10 * time.Second }}
	p := lsppool.New(lsppool.Options{Idle: time.Minute, Start: f.start})
	got := make(chan error, 1)
	go func() {
		_, err := p.Acquire(context.Background(), gopls, root)
		got <- err
	}()
	for len(f.started()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	began := time.Now()
	if err := p.Close(ctx5(t)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if d := time.Since(began); d > 3*time.Second {
		t.Errorf("Close took %v while a server was starting", d)
	}
	if err := <-got; err == nil {
		t.Error("Acquire succeeded after Close abandoned its start")
	}
}

// A call that never opens the edited file still sees the edit when another
// call holds that file open with its old text.
func TestEditToADocumentAnotherCallHoldsIsSeen(t *testing.T) {
	root, shapes, report := sample(t)
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{})
	original, err := os.ReadFile(filepath.Join(root, lspfake.SampleReportFile))
	if err != nil {
		t.Fatal(err)
	}
	a := acquire(t, p, gopls, root)
	defer a.Release(ctx5(t), nil)
	if err := a.DidOpen(ctx5(t), report, "go", string(original)); err != nil {
		t.Fatal(err)
	}
	appendSummary(t, root)
	b := acquire(t, p, gopls, root)
	defer b.Release(ctx5(t), nil)
	if got := callers(t, b, shapes); !reflect.DeepEqual(got, []string{"Report", "Summary"}) {
		t.Errorf("callers = %v, want [Report Summary]: the open copy hid the edit", got)
	}
}

// Through a symbolic link to the root, each change is sent by both names.
func TestChangesUnderASymlinkedRootAreSentByBothNames(t *testing.T) {
	real, _, _ := sample(t)
	real, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("cannot make a symbolic link:", err)
	}
	f := &fakes{watch: true}
	p := newPool(t, f, lsppool.Options{})
	acquire(t, p, gopls, link).Release(ctx5(t), nil)
	appendSummary(t, link)
	acquire(t, p, gopls, link).Release(ctx5(t), nil)
	changes := f.started()[0].await("workspace/didChangeWatchedFiles", 1, nil)
	if changes != 1 {
		t.Fatalf("%d didChangeWatchedFiles, want 1", changes)
	}
	viaLink := lsp.FileURI(filepath.Join(link, lspfake.SampleReportFile))
	viaReal := lsp.FileURI(filepath.Join(real, lspfake.SampleReportFile))
	want := `{"changes":[{"uri":"` + viaLink + `","type":2},{"uri":"` + viaReal + `","type":2}]}`
	if got := string(f.started()[0].Requests("workspace/didChangeWatchedFiles")[0].Params); got != want {
		t.Errorf("didChangeWatchedFiles = %s, want %s", got, want)
	}
}
