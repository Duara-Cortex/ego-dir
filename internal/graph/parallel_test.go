// Tests for fetching a hop's neighbours concurrently.
package graph_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
)

// randomSource is a fakeSource over n nodes and random edges of every kind,
// some of them parallel (same ends, different weights).
func randomSource(seed int64, n, edges int) *fakeSource {
	r := rand.New(rand.NewSource(seed))
	var es []fakeEdge
	for range edges {
		es = append(es, fakeEdge{
			from:   fmt.Sprintf("N%02d", r.Intn(n)),
			to:     fmt.Sprintf("N%02d", r.Intn(n)),
			kind:   allKinds[r.Intn(len(allKinds))],
			weight: 1 + r.Intn(3),
		})
	}
	return newSource(es...)
}

// jittery delays each Neighbors call by a random few hundred microseconds,
// so concurrent calls finish in a different order each time.
type jittery struct {
	*fakeSource
	mu sync.Mutex
	r  *rand.Rand
}

func (j *jittery) Neighbors(ctx context.Context, n graph.Node, dir graph.Direction, kinds []graph.EdgeKind) ([]graph.Neighbor, error) {
	j.mu.Lock()
	d := time.Duration(j.r.Intn(400)) * time.Microsecond
	j.mu.Unlock()
	time.Sleep(d)
	return j.fakeSource.Neighbors(ctx, n, dir, kinds)
}

func buildFrom(t *testing.T, src graph.SymbolSource, ego graph.Node, opts graph.Options) *graph.EgoGraph {
	t.Helper()
	g, err := graph.Build(context.Background(), src, ego.URI, ego.Selection.StartLine-1, ego.Selection.StartCol-1, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g
}

func TestParallelMatchesSerial(t *testing.T) {
	kindSets := [][]graph.EdgeKind{
		allKinds,
		{graph.EdgeCalls},
		{graph.EdgeCalls, graph.EdgeImplements},
		{graph.EdgeContains, graph.EdgeReferences, graph.EdgeImports},
	}
	for seed := int64(1); seed <= 8; seed++ {
		base := randomSource(seed, 24, 70)
		for depth := 1; depth <= graph.MaxDepth; depth++ {
			for _, dir := range []graph.Direction{graph.DirBoth, graph.DirIn, graph.DirOut} {
				for ki, kinds := range kindSets {
					ego := base.nodes[fmt.Sprintf("N%02d", seed%24)]
					opts := graph.Options{Depth: depth, Kinds: kinds, Direction: dir, Body: graph.BodyAll, Parallel: 1}
					want := buildFrom(t, randomSource(seed, 24, 70), ego, opts)
					for _, p := range []int{0, 2, 4, 8} {
						opts.Parallel = p
						src := &jittery{fakeSource: randomSource(seed, 24, 70), r: rand.New(rand.NewSource(seed + int64(p)))}
						if got := buildFrom(t, src, ego, opts); !reflect.DeepEqual(got, want) {
							t.Fatalf("seed %d depth %d dir %d kinds %d parallel %d: graph differs from serial\n got %+v\nwant %+v",
								seed, depth, dir, ki, p, got, want)
						}
					}
				}
			}
		}
	}
}

// gauge records how many Neighbors calls run at once.
type gauge struct {
	*fakeSource
	delay      time.Duration
	now, peak  atomic.Int32
	totalCalls atomic.Int32
}

func (g *gauge) Neighbors(ctx context.Context, n graph.Node, dir graph.Direction, kinds []graph.EdgeKind) ([]graph.Neighbor, error) {
	cur := g.now.Add(1)
	defer g.now.Add(-1)
	g.totalCalls.Add(1)
	for {
		peak := g.peak.Load()
		if cur <= peak || g.peak.CompareAndSwap(peak, cur) {
			break
		}
	}
	time.Sleep(g.delay)
	return g.fakeSource.Neighbors(ctx, n, dir, kinds)
}

// fan is an ego T calling twenty functions, each calling one more.
func fan() *fakeSource {
	var es []fakeEdge
	for i := range 20 {
		es = append(es, calls("T", fmt.Sprintf("C%02d", i)), calls(fmt.Sprintf("C%02d", i), fmt.Sprintf("D%02d", i)))
	}
	return newSource(es...)
}

func TestParallelBound(t *testing.T) {
	for _, p := range []int{1, 3, 4, 8} {
		src := &gauge{fakeSource: fan(), delay: 2 * time.Millisecond}
		buildFrom(t, src, src.nodes["T"], graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirOut, Parallel: p})
		if got := src.peak.Load(); got != int32(p) {
			t.Errorf("parallel %d: at most %d calls ran at once, want exactly %d", p, got, p)
		}
	}
	src := &gauge{fakeSource: fan(), delay: 2 * time.Millisecond}
	buildFrom(t, src, src.nodes["T"], graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirOut})
	if got := src.peak.Load(); got != graph.DefaultParallel {
		t.Errorf("default: at most %d calls ran at once, want %d", got, graph.DefaultParallel)
	}
}

// failing fails Neighbors for one node and holds every other call until
// its context is cancelled. The failure waits until holdFor calls are
// holding, so they are running when it comes.
type failing struct {
	*fakeSource
	bad       string
	holdFor   int32
	holding   atomic.Int32
	cancelled atomic.Int32
}

func (f *failing) Neighbors(ctx context.Context, n graph.Node, dir graph.Direction, kinds []graph.EdgeKind) ([]graph.Neighbor, error) {
	if n.Name == f.bad {
		deadline := time.Now().Add(5 * time.Second)
		for f.holding.Load() < f.holdFor && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		return nil, errSource
	}
	if n.Name == "T" {
		return f.fakeSource.Neighbors(ctx, n, dir, kinds)
	}
	f.holding.Add(1)
	select {
	case <-ctx.Done():
		f.cancelled.Add(1)
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		return nil, errors.New("not cancelled")
	}
}

func TestParallelCancelsTheRestOnError(t *testing.T) {
	// C00 and C01, in and out, take the four slots: C01 fails once both
	// calls for C00 are holding.
	src := &failing{fakeSource: fan(), bad: "C01", holdFor: 2}
	ego := src.nodes["T"]
	start := time.Now()
	_, err := graph.Build(context.Background(), src, ego.URI, ego.Selection.StartLine-1, ego.Selection.StartCol-1,
		graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirOut, Parallel: 4})
	if !errors.Is(err, errSource) {
		t.Fatalf("error = %v, want the source's error, not a cancellation", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, should not be a cancellation", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Build took %v; the other calls were not cancelled", elapsed)
	}
	if got := src.cancelled.Load(); got != 2 {
		t.Errorf("%d running calls saw their context cancelled, want both calls for C00", got)
	}
}

func TestParallelHonoursTheCallersContext(t *testing.T) {
	src := &failing{fakeSource: fan(), bad: "none"}
	ego := src.nodes["T"]
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := graph.Build(ctx, src, ego.URI, ego.Selection.StartLine-1, ego.Selection.StartCol-1,
		graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirOut})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestParallelExpandsEachNodeOnce(t *testing.T) {
	src := &gauge{fakeSource: fan()}
	buildFrom(t, src, src.nodes["T"], graph.Options{Depth: 3, Kinds: allKinds, Direction: graph.DirBoth, Parallel: 8})
	for key, n := range src.expanded {
		if n != 1 {
			t.Errorf("%s expanded %d times, want once", key, n)
		}
	}
}

func TestNegativeParallelIsInvalid(t *testing.T) {
	src := chain()
	n := src.nodes["T"]
	if _, err := graph.Build(context.Background(), src, n.URI, 9, 5, graph.Options{Depth: 1, Kinds: allKinds, Parallel: -1}); err == nil {
		t.Error("Build with Parallel -1 succeeded, want an error")
	}
}

func BenchmarkBuild(b *testing.B) {
	for _, depth := range []int{1, 2} {
		for _, p := range []int{1, graph.DefaultParallel} {
			b.Run(fmt.Sprintf("depth=%d/parallel=%d", depth, p), func(b *testing.B) {
				src := &gauge{fakeSource: fan(), delay: time.Millisecond}
				ego := src.nodes["T"]
				opts := graph.Options{Depth: depth, Kinds: allKinds, Direction: graph.DirBoth, Parallel: p}
				for b.Loop() {
					if _, err := graph.Build(context.Background(), src, ego.URI, ego.Selection.StartLine-1, ego.Selection.StartCol-1, opts); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
