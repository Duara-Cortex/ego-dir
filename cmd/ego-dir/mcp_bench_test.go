package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
	"github.com/Duara-Cortex/ego-dir/internal/lsppool"
)

// BenchmarkMCPCall times one inspect_ego tool call with a fresh server per
// call (cold) and with a warm one, against the fake server slowed to
// resemble a real one: 200ms to load the workspace, 1ms per request.
func BenchmarkMCPCall(b *testing.B) {
	b.Setenv(lspfake.EnvHelper, "sample")
	b.Setenv(lspfake.EnvInitialiseDelay, "200ms")
	b.Setenv(lspfake.EnvLatency, "1ms")
	root, err := lspfake.SampleRoot()
	if err != nil {
		b.Fatal(err)
	}
	cfg := mcpConfig{root: root, server: os.Args[0], timeout: 30 * time.Second}
	for _, depth := range []int{1, 2} {
		args, _ := json.Marshal(map[string]any{"file": "shapes.go", "line": 18, "col": 6, "depth": depth})
		run := func(b *testing.B, srv servers) {
			if _, err := callTool(context.Background(), cfg, srv, "inspect_ego", args); err != nil {
				b.Fatal(err) // warms the pool; not timed
			}
			for b.Loop() {
				if _, err := callTool(context.Background(), cfg, srv, "inspect_ego", args); err != nil {
					b.Fatal(err)
				}
			}
		}
		b.Run(fmt.Sprintf("cold/depth=%d", depth), func(b *testing.B) { run(b, oneShot{}) })
		b.Run(fmt.Sprintf("warm/depth=%d", depth), func(b *testing.B) {
			pool := lsppool.New(lsppool.Options{Idle: time.Minute})
			defer pool.Close(context.Background())
			run(b, pooled{pool})
		})
	}
}
