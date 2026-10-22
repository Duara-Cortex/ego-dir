//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsppool"
)

// BenchmarkGoplsMCPCall times an inspect_ego tool call on graph.Build in
// this repository with real gopls: a fresh server per call (cold), and a
// warm one. Run with
//
//	go test -tags integration -run '^$' -bench GoplsMCPCall -benchtime 5x ./cmd/ego-dir
func BenchmarkGoplsMCPCall(b *testing.B) {
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		b.Skip("gopls not on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		b.Fatal(err)
	}
	file := filepath.Join("internal", "graph", "builder.go")
	src, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		b.Fatal(err)
	}
	line := 0
	for i, l := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(l, "func Build(") {
			line = i + 1
		}
	}
	cfg := mcpConfig{root: root, server: gopls, timeout: time.Minute}
	for _, depth := range []int{1, 2} {
		args, _ := json.Marshal(map[string]any{"file": filepath.ToSlash(file), "line": line, "col": 6, "depth": depth})
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
