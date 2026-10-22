// Tests that a document is opened once however many goroutines ask for it.
package lspsource_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lspsource"
)

// openCounter is a client whose didOpen is slow, counted per URI, and can
// fail the first time.
type openCounter struct {
	lspsource.Client
	mu        sync.Mutex
	opens     map[string]int
	failFirst bool
}

var errOpen = errors.New("open failed")

func (c *openCounter) DidOpen(ctx context.Context, uri, languageID, text string) error {
	time.Sleep(20 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens[uri]++
	if c.failFirst && c.opens[uri] == 1 {
		return errOpen
	}
	return c.Client.DidOpen(ctx, uri, languageID, text)
}

func TestConcurrentLoadsOpenOnce(t *testing.T) {
	f := setup(t)
	c := &openCounter{Client: f.client, opens: map[string]int{}}
	src := lspsource.New(c, f.root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			if _, err := src.Resolve(ctx, f.uri, 17, 5); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := c.opens[f.uri]; got != 1 {
		t.Errorf("shapes.go opened %d times, want once", got)
	}
}

func TestParallelBuildOpensEachFileOnce(t *testing.T) {
	f := setup(t)
	c := &openCounter{Client: f.client, opens: map[string]int{}}
	src := lspsource.New(c, f.root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := graph.Build(ctx, src, f.uri, 17, 5, graph.Options{Depth: 3, Kinds: []graph.EdgeKind{
		graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeReferences, graph.EdgeContains}, Parallel: 8})
	if err != nil {
		t.Fatal(err)
	}
	for uri, n := range c.opens {
		if n != 1 {
			t.Errorf("%s opened %d times, want once", uri, n)
		}
	}
}

func TestFailedLoadIsRetried(t *testing.T) {
	f := setup(t)
	c := &openCounter{Client: f.client, opens: map[string]int{}, failFirst: true}
	src := lspsource.New(c, f.root)
	ctx := context.Background()
	if _, err := src.Resolve(ctx, f.uri, 17, 5); !errors.Is(err, errOpen) {
		t.Fatalf("first Resolve: error = %v, want the open failure", err)
	}
	if _, err := src.Resolve(ctx, f.uri, 17, 5); err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if got := c.opens[f.uri]; got != 2 {
		t.Errorf("shapes.go opened %d times, want 2 (one failure, one retry)", got)
	}
}
