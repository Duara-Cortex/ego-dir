# ego-dir

Ego-dir is a tool that combines LSP (Language Server Protocol) capabilities with an ego-graph retrieval engine, enabling agents to inspect code to find a method's meaning or what calls it, without flooding the context window with raw repository text or repetitive codebase searches.

Point it at a symbol position `(file, line, col)` and it returns a compact, deterministic
Markdown (or JSON) report of that symbol, the **ego**, and its neighbours, the **alters**, up to
`k` hops away. Alters are callers, callees, implementations and type dependencies.

## Features

- **Headless LSP client.** JSON-RPC 2.0 over stdio, written with only the Go standard library.
  It starts the right language server for the file on demand.
- **Ego-graph engine.** Breadth-first expansion from a symbol to depth `k` (default 1, max 5),
  with edge-kind filtering, stable ordering and de-duplication.
- **Agent-ready output.** Prompt-optimised Markdown with a fixed section layout, plus an
  equivalent JSON schema. The same input always produces byte-identical output.
- **Read-only.** Ego-dir never edits files. It only asks the language server questions.
- **Single static binary.** No runtime dependencies besides the language server for your language.

## Supported language servers

The server is chosen by file extension. Use `--server "<cmd> <args>"` to override it.

| Extension            | Default command                        |
|----------------------|----------------------------------------|
| `.go`                | `gopls`                                |
| `.py`                | `pyright-langserver --stdio`           |
| `.rs`                | `rust-analyzer`                        |
| `.ts .tsx .js .jsx`  | `typescript-language-server --stdio`   |
| `.php`               | `intelephense --stdio`                 |

The language server must be installed. Ego-dir does not install it.

## Usage 

```sh
# Ego-graph around the symbol at main.go line 42, column 7, two hops out
ego-dir inspect_ego --file internal/graph/builder.go --line 42 --col 7 --depth 2

# Only follow call edges, emit JSON
ego-dir inspect_ego --file main.go --line 10 --col 6 --edges calls --format json

# Single-purpose tools
ego-dir resolve_definition --file main.go --line 10 --col 6
ego-dir find_references    --file main.go --line 10 --col 6
```

| Flag          | Default    | Meaning                                                                      |
|---------------|------------|------------------------------------------------------------------------------|
| `--file`      | (required) | Path to the source file. Relative paths resolve against `--root`.            |
| `--line`      | (required) | **1-based** line, as shown in editors.                                       |
| `--col`       | (required) | **1-based** column in characters (runes). Converted to LSP UTF-16 internally. |
| `--depth`     | `1`        | Hop radius `k` (1–5).                                                        |
| `--format`    | `md`       | `md` or `json` (all three commands).                                         |
| `--edges`     | see note   | Comma list of `calls,implements,instantiates,imports,references`, or `all`. Default: all except `references`. |
| `--direction` | `both`     | `in` (callers/upstream), `out` (callees/downstream) or `both`.               |
| `--body`      | `ego`      | Include bodies for `ego`, `all` or `none`.                                   |
| `--root`      | cwd        | Workspace root passed to the language server.                                |
| `--server`    | by ext     | Override the language server command.                                        |
| `--timeout`   | `30s`      | Overall deadline.                                                            |

Results go to stdout. Diagnostics go to stderr. The exit code is `0` on success, `1` on a
usage error (bad flags, missing file, position outside the file) and `2` on an LSP or runtime
error.

## Architecture

There are three decoupled layers. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for
diagrams, data structures and the edge derivation table.

```
cmd/ego-dir         Agent interface (CLI, flags, exit codes)
internal/graph      Ego-graph engine (Node, Edge, EgoGraph, BFS Builder)
internal/render     Markdown + JSON renderers (deterministic)
internal/lsp        LSP harness (JSON-RPC 2.0 framing, client, server registry)
```

## Build and test

```sh
go build ./...
go vet ./...
go test ./...                       # unit tests, offline, using an in-process fake LSP server
go test -tags integration ./...     # also runs real-server tests (skipped if gopls is missing)

make test                           # go vet + go test -race (what CI runs)
make pending                        # acceptance tests for code not written yet (expected to fail)
VERSION=v0.1.0 make package         # cross-compile to dist/*.tar.gz + dist/checksums.txt
```

## Release

Pushing a tag that matches `v*` runs [`.github/workflows/release.yml`](.github/workflows/release.yml).
It runs `make test`, then `make package` for linux, darwin and windows (amd64 and arm64), then
publishes a GitHub Release with the archives and `checksums.txt`.

## License

See [LICENSE](LICENSE).
