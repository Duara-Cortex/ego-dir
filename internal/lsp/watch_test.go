// Tests for document sync, watched files and noticing a stopped server.
package lsp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
)

// connectWith is connect with a client Config.
func connectWith(t *testing.T, s *lspfake.Server, cfg lsp.Config) *session {
	t.Helper()
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	clientEnd, serverEnd := net.Pipe()
	sess := &session{server: s, conn: serverEnd, done: make(chan error, 1), root: root,
		uri: lsp.FileURI(filepath.Join(root, lspfake.SampleFile))}
	go func() { sess.done <- s.Serve(context.Background(), serverEnd); serverEnd.Close() }()
	c, err := lsp.NewClientWith(ctx5(t), clientEnd, root, cfg)
	if err != nil {
		t.Fatalf("NewClientWith: %v", err)
	}
	sess.client = c
	t.Cleanup(func() { clientEnd.Close(); serverEnd.Close() })
	return sess
}

// watchedFilesCapability returns the dynamicRegistration the client
// declared for didChangeWatchedFiles, or nil when it declared none.
func watchedFilesCapability(t *testing.T, s *lspfake.Server) *bool {
	t.Helper()
	var p struct {
		Capabilities struct {
			Workspace struct {
				DidChangeWatchedFiles *struct {
					DynamicRegistration bool `json:"dynamicRegistration"`
				} `json:"didChangeWatchedFiles"`
			} `json:"workspace"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(waitFor(t, s, "initialise").Params, &p); err != nil {
		t.Fatal(err)
	}
	if w := p.Capabilities.Workspace.DidChangeWatchedFiles; w != nil {
		return &w.DynamicRegistration
	}
	return nil
}

func TestDefaultClientDoesNotOfferToWatchFiles(t *testing.T) {
	sess := sample(t)
	if got := watchedFilesCapability(t, sess.server); got != nil {
		t.Errorf("default client declared didChangeWatchedFiles (dynamicRegistration %v)", *got)
	}
	if sess.client.WatchesFiles() {
		t.Error("WatchesFiles is true with no registration")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatchFilesRegistration(t *testing.T) {
	s := lspfake.New()
	lspfake.Sample(s)
	sess := connectWith(t, s, lsp.Config{WatchFiles: true})
	if got := watchedFilesCapability(t, s); got == nil || !*got {
		t.Fatal("WatchFiles client did not declare dynamic registration of didChangeWatchedFiles")
	}
	eventually(t, "the sample's registration", sess.client.WatchesFiles)

	// Another method's registration does not count; unregistering the
	// watcher clears it.
	if _, err := s.Call(ctx5(t), "client/registerCapability", map[string]any{"registrations": []any{
		map[string]any{"id": "fmt", "method": "textDocument/formatting"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Call(ctx5(t), "client/unregisterCapability", map[string]any{"unregisterations": []any{
		map[string]any{"id": lspfake.SampleWatcherID, "method": "workspace/didChangeWatchedFiles"}}}); err != nil {
		t.Fatal(err)
	}
	if sess.client.WatchesFiles() {
		t.Error("WatchesFiles is still true after the watcher was unregistered")
	}
}

func TestDocumentSyncNotifications(t *testing.T) {
	sess := sample(t)
	ctx := ctx5(t)
	if err := sess.client.DidChange(ctx, sess.uri, 4, "package sample\n"); err != nil {
		t.Fatal(err)
	}
	if err := sess.client.DidClose(ctx, sess.uri); err != nil {
		t.Fatal(err)
	}
	events := []lsp.FileEvent{{URI: sess.uri, Type: lsp.FileChanged}, {URI: sess.uri + ".new", Type: lsp.FileCreated},
		{URI: sess.uri + ".old", Type: lsp.FileDeleted}}
	if err := sess.client.DidChangeWatchedFiles(ctx, events); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"textDocument/didChange": `{"contentChanges":[{"text":"package sample\n"}],"textDocument":{"uri":"` + sess.uri + `","version":4}}`,
		"textDocument/didClose":  `{"textDocument":{"uri":"` + sess.uri + `"}}`,
		"workspace/didChangeWatchedFiles": `{"changes":[{"uri":"` + sess.uri + `","type":2},{"uri":"` + sess.uri +
			`.new","type":1},{"uri":"` + sess.uri + `.old","type":3}]}`,
	}
	for method, params := range want {
		if got := string(waitFor(t, sess.server, method).Params); got != params {
			t.Errorf("%s params = %s, want %s", method, got, params)
		}
	}
}

func TestDoneAndAnnotateAfterACrash(t *testing.T) {
	t.Setenv(lspfake.EnvHelper, "sample")
	crash := filepath.Join(t.TempDir(), "crash")
	t.Setenv(lspfake.EnvCrashFile, crash)
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	c, err := lsp.StartWith(ctx5(t), []string{os.Args[0]}, root, lsp.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx5(t))
	select {
	case <-c.Done():
		t.Fatal("Done is closed while the server runs")
	default:
	}
	if err := os.WriteFile(crash, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = c.Hover(ctx5(t), lsp.FileURI(filepath.Join(root, lspfake.SampleFile)), pos(17, 5))
	if err == nil {
		t.Fatal("Hover succeeded on a server that crashed")
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after the server crashed")
	}
	if got := c.Annotate(err); !errors.Is(got, err) || !strings.Contains(got.Error(), lspfake.CrashNote) {
		t.Errorf("Annotate = %q, want the error with the server's stderr (%q)", got, lspfake.CrashNote)
	}
}

func TestAnnotateWithoutAProcess(t *testing.T) {
	sess := sample(t)
	err := errors.New("boom")
	if got := sess.client.Annotate(err); got != err {
		t.Errorf("Annotate on a stream client = %v, want the error unchanged", got)
	}
	if got := sess.client.Annotate(nil); got != nil {
		t.Errorf("Annotate(nil) = %v", got)
	}
}

func TestHelperLatencyFromEnvironment(t *testing.T) {
	t.Setenv(lspfake.EnvHelper, "sample")
	t.Setenv(lspfake.EnvLatency, "80ms")
	t.Setenv(lspfake.EnvInitialiseDelay, "120ms")
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	c, err := lsp.Start(ctx5(t), []string{os.Args[0]}, root)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx5(t))
	if d := time.Since(began); d < 120*time.Millisecond {
		t.Errorf("start took %v, want at least the 120ms initialise delay", d)
	}
	began = time.Now()
	if _, err := c.Hover(ctx5(t), lsp.FileURI(filepath.Join(root, lspfake.SampleFile)), pos(17, 5)); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(began); d < 80*time.Millisecond {
		t.Errorf("hover took %v, want at least the 80ms latency", d)
	}
}

func TestHelperLogsStartAndCleanExit(t *testing.T) {
	t.Setenv(lspfake.EnvHelper, "sample")
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv(lspfake.EnvLog, log)
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	c, err := lsp.Start(ctx5(t), []string{os.Args[0]}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(b))
	if len(lines) != 4 || lines[0] != "start" || lines[2] != "exit" || lines[1] != lines[3] {
		t.Errorf("log = %q, want start and exit of one pid", b)
	}
}
