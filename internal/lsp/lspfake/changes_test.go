// Tests for how the Sample scenario learns of edits, and for scripted
// latency and request hooks.
package lspfake_test

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
)

// summary adds a caller of TotalArea to report.go.
const summary = "\n// Summary is the area of no shapes.\nfunc Summary() float64 { return sample.TotalArea(nil) }\n"

// copiedSample copies testdata/sample into a temporary directory and
// returns its root and the URIs of shapes.go and report.go.
func copiedSample(t *testing.T) (root, shapes, report string) {
	t.Helper()
	src, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(t.TempDir(), "sample")
	if err := os.CopyFS(root, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return root, fileURI(filepath.Join(root, lspfake.SampleFile)), fileURI(filepath.Join(root, lspfake.SampleReportFile))
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

// initialised starts the Sample scenario on root with the given client
// capabilities and sends the initialised notification.
func initialised(t *testing.T, s *lspfake.Server, root string, caps map[string]any) *peer {
	t.Helper()
	lspfake.Sample(s)
	p := start(t, s)
	if m := p.call(0, "initialise", map[string]any{"rootUri": fileURI(root), "capabilities": caps}); m.Error != nil {
		t.Fatalf("initialise: %v", m.Error)
	}
	p.send(map[string]any{"method": "initialised", "params": map[string]any{}})
	return p
}

// callers returns the names of TotalArea's callers, sorted.
func callers(t *testing.T, p *peer, shapes string, id int) []string {
	t.Helper()
	m := p.call(id, "textDocument/prepareCallHierarchy", at(shapes, 17, 5))
	var items []json.RawMessage
	if err := json.Unmarshal(m.Result, &items); err != nil || len(items) != 1 {
		t.Fatalf("prepare = %s, %v", m.Result, m.Error)
	}
	m = p.call(id+1, "callHierarchy/incomingCalls", map[string]any{"item": items[0]})
	var calls []struct {
		From struct{ Name string } `json:"from"`
	}
	if err := json.Unmarshal(m.Result, &calls); err != nil {
		t.Fatalf("incomingCalls = %s, %v", m.Result, m.Error)
	}
	var names []string
	for _, c := range calls {
		names = append(names, c.From.Name)
	}
	sort.Strings(names)
	return names
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestSampleSeesDiskEditsOnlyWhenTold(t *testing.T) {
	root, shapes, report := copiedSample(t)
	p := initialised(t, lspfake.New(), root, nil)
	if got := callers(t, p, shapes, 1); !reflect.DeepEqual(got, []string{"Report"}) {
		t.Fatalf("callers = %v, want [Report]", got)
	}
	appendFile(t, filepath.Join(root, lspfake.SampleReportFile), summary)
	if got := callers(t, p, shapes, 3); !reflect.DeepEqual(got, []string{"Report"}) {
		t.Errorf("callers before the change is reported = %v, want the stale [Report]", got)
	}
	p.send(map[string]any{"method": "workspace/didChangeWatchedFiles",
		"params": map[string]any{"changes": []any{map[string]any{"uri": report, "type": 2}}}})
	if got := callers(t, p, shapes, 5); !reflect.DeepEqual(got, []string{"Report", "Summary"}) {
		t.Errorf("callers after the change is reported = %v, want [Report Summary]", got)
	}
}

func TestSampleOpenDocumentsWinUntilClosed(t *testing.T) {
	root, shapes, report := copiedSample(t)
	p := initialised(t, lspfake.New(), root, nil)
	original, err := os.ReadFile(filepath.Join(root, lspfake.SampleReportFile))
	if err != nil {
		t.Fatal(err)
	}
	p.send(map[string]any{"method": "textDocument/didOpen", "params": map[string]any{"textDocument": map[string]any{
		"uri": report, "languageId": "go", "version": 1, "text": string(original) + summary}}})
	if got := callers(t, p, shapes, 1); !reflect.DeepEqual(got, []string{"Report", "Summary"}) {
		t.Errorf("callers with the edit open = %v, want [Report Summary]", got)
	}
	p.send(map[string]any{"method": "textDocument/didChange", "params": map[string]any{
		"textDocument":   map[string]any{"uri": report, "version": 2},
		"contentChanges": []any{map[string]any{"text": string(original)}}}})
	if got := callers(t, p, shapes, 3); !reflect.DeepEqual(got, []string{"Report"}) {
		t.Errorf("callers after didChange back = %v, want [Report]", got)
	}
	p.send(map[string]any{"method": "textDocument/didChange", "params": map[string]any{
		"textDocument":   map[string]any{"uri": report, "version": 3},
		"contentChanges": []any{map[string]any{"text": string(original) + summary}}}})
	p.send(map[string]any{"method": "textDocument/didClose", "params": map[string]any{"textDocument": map[string]any{"uri": report}}})
	if got := callers(t, p, shapes, 5); !reflect.DeepEqual(got, []string{"Report"}) {
		t.Errorf("callers after didClose = %v, want [Report] as on disk", got)
	}
}

func TestSampleRegistersForWatchedFiles(t *testing.T) {
	root, _, _ := copiedSample(t)
	caps := map[string]any{"workspace": map[string]any{"didChangeWatchedFiles": map[string]any{"dynamicRegistration": true}}}
	p := initialised(t, lspfake.New(), root, caps)
	m := p.recv()
	if m.Method != "client/registerCapability" || len(m.ID) == 0 {
		t.Fatalf("after initialised got %+v, want a registerCapability request", m)
	}
	var params struct {
		Registrations []struct{ ID, Method string }
	}
	if err := json.Unmarshal(m.Params, &params); err != nil || len(params.Registrations) != 1 ||
		params.Registrations[0].ID != lspfake.SampleWatcherID || params.Registrations[0].Method != "workspace/didChangeWatchedFiles" {
		t.Errorf("registration = %s", m.Params)
	}
	p.send(map[string]any{"id": json.RawMessage(m.ID), "result": nil})
}

func TestSampleDoesNotRegisterUnlessAllowed(t *testing.T) {
	root, shapes, _ := copiedSample(t)
	p := initialised(t, lspfake.New(), root, map[string]any{})
	// The next message is the answer to this request, not a registration.
	if m := p.call(1, "textDocument/hover", at(shapes, 17, 5)); m.Method != "" || string(m.ID) != "1" {
		t.Errorf("got %+v, want the hover answer", m)
	}
}

func TestLatency(t *testing.T) {
	s := lspfake.New()
	s.Latency = 60 * time.Millisecond
	s.Result("textDocument/hover", nil)
	p := start(t, s)
	began := time.Now()
	p.call(0, "initialise", map[string]any{})
	if d := time.Since(began); d >= s.Latency {
		t.Errorf("initialise took %v; Latency must not apply to it", d)
	}
	began = time.Now()
	if m := p.call(1, "textDocument/hover", map[string]any{}); m.Error != nil {
		t.Fatal(m.Error)
	}
	if d := time.Since(began); d < s.Latency {
		t.Errorf("hover took %v, want at least %v", d, s.Latency)
	}
}

func TestInitialiseDelay(t *testing.T) {
	s := lspfake.New()
	s.InitialiseDelay = 60 * time.Millisecond
	p := start(t, s)
	began := time.Now()
	p.call(0, "initialise", map[string]any{})
	if d := time.Since(began); d < s.InitialiseDelay {
		t.Errorf("initialise took %v, want at least %v", d, s.InitialiseDelay)
	}
}

func TestLatencyIsCancellable(t *testing.T) {
	s := lspfake.New()
	s.Latency = 10 * time.Second
	s.Result("textDocument/hover", nil)
	p := start(t, s)
	p.send(map[string]any{"id": 7, "method": "textDocument/hover", "params": map[string]any{}})
	p.send(map[string]any{"method": "$/cancelRequest", "params": map[string]any{"id": 7}})
	if m := p.recv(); m.Error == nil || m.Error.Code != lspfake.CodeRequestCancelled {
		t.Errorf("cancelled request answered %+v, want RequestCancelled", m)
	}
}

func TestBeforeRequest(t *testing.T) {
	s := lspfake.New()
	var mu sync.Mutex
	var seen []string
	s.BeforeRequest = func(method string) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, method)
	}
	s.Result("textDocument/hover", nil)
	p := start(t, s)
	p.call(0, "initialise", map[string]any{})
	p.send(map[string]any{"method": "initialised", "params": map[string]any{}})
	p.call(1, "textDocument/hover", map[string]any{})
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"initialise", "textDocument/hover"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("BeforeRequest saw %v, want %v (requests only)", seen, want)
	}
}
