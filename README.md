# ego-dir

Ego-dir is a tool that combines LSP (Language Server Protocol) capabilities with an ego-graph retrieval engine, enabling agents to inspect code to find a method's meaning or what calls it, without flooding the context window with raw repository text or repetitive codebase searches.

Point it at a symbol position `(file, line, col)` and it returns a compact, deterministic
Markdown (or JSON) report of that symbol, the **ego**, and its neighbours, the **alters**, up to
`k` hops away. Alters are callers, callees, implementations, type dependencies and,
on request, members and parents. Pointed at a module's name (`package report`), it reports
the module and the modules it imports.

## Features

- **Headless LSP client.** JSON-RPC 2.0 over stdio, written with only the Go standard library.
  It starts the right language server for the file on demand.
- **Ego-graph engine.** Breadth-first expansion from a symbol to depth `k` (default 1, max 5),
  with edge-kind filtering, stable ordering and de-duplication.
- **Agent-ready output.** Prompt-optimised Markdown with a fixed section layout, plus an
  equivalent JSON schema. The same input always produces byte-identical output.
- **Read-only.** Ego-dir never edits files. It only asks the language server questions.
- **Single static binary.** No runtime dependencies besides the language server for your language.

## Install

**From a release** (no Go needed). Download the archive for your platform and
`checksums.txt` from the [releases page](https://github.com/Duara-Cortex/ego-dir/releases), then:

```sh
shasum -a 256 -c checksums.txt --ignore-missing     # verify the download
tar -xzf ego-dir_v0.0.1_darwin_arm64.tar.gz
install -m 0755 ego-dir_v0.0.1_darwin_arm64/ego-dir ~/.local/bin/   # or any directory on your PATH
```

**With Go** (1.26 or later):

```sh
go install github.com/Duara-Cortex/ego-dir/cmd/ego-dir@latest
```

`ego-dir version` reports `dev` for this route; release archives and `make install` carry the version.

**From source:**

```sh
VERSION=v0.0.1 make install                         # installs into ~/.local/bin (override with INSTALL_DIR=...)
```

If `~/.local/bin` is not on your PATH (macOS does not add it by default), add it to your
shell profile (`~/.zshrc` or `~/.bashrc`):

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Then install the language server for your language (see below).

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

# A package and the packages it imports (the cursor on its name in "package graph")
ego-dir inspect_ego --file internal/graph/model.go --line 3 --col 9

# What a type contains (fields, methods) and what contains it (its package)
ego-dir inspect_ego --file shapes.go --line 9 --col 6 --edges contains

# Single-purpose tools
ego-dir resolve_definition --file main.go --line 10 --col 6
ego-dir find_references    --file main.go --line 10 --col 6
```

| Flag          | Default    | Meaning                                                                                                                             |
|---------------|------------|-------------------------------------------------------------------------------------------------------------------------------------|
| `--file`      | (required) | Path to the source file. Relative paths resolve against `--root`.                                                                   |
| `--line`      | (required) | **1-based** line, as shown in editors.                                                                                              |
| `--col`       | (required) | **1-based** column in characters (runes). Converted to LSP UTF-16 internally.                                                       |
| `--depth`     | `1`        | Hop radius `k` (1–5).                                                                                                               |
| `--format`    | `md`       | `md` or `json` (all three commands).                                                                                                |
| `--edges`     | see note   | Comma list of `calls,implements,instantiates,imports,contains,references`, or `all`. Default: all except `contains` and `references`. |
| `--direction` | `both`     | `in` (callers/upstream), `out` (callees/downstream) or `both`.                                                                      |
| `--body`      | `ego`      | Include bodies for `ego`, `all` or `none`.                                                                                          |
| `--root`      | cwd        | Workspace root passed to the language server. Alters outside it (standard library, module cache, sibling directories) are left out. |
| `--server`    | by ext     | Override the language server command.                                                                                               |
| `--timeout`   | `30s`      | Overall deadline.                                                                                                                   |

Results go to stdout. Diagnostics go to stderr. The exit code is `0` on success, `1` on a
usage error (bad flags, missing file, position outside the file) and `2` on an LSP or runtime
error.

## Use with an agent

`ego-dir mcp` serves the three commands as [Model Context Protocol](https://modelcontextprotocol.io)
tools over stdio, so an agent can call them directly instead of running shell commands:

```sh
ego-dir mcp [--root DIR] [--server "CMD ARGS"] [--timeout DURATION]
```

Register it with Claude Code (once per repository). Use absolute paths for both the binary
and `--root`: Claude Code starts the server with its own environment, which may not have
`~/.local/bin` on its PATH.

```sh
claude mcp add ego-dir -- /path/to/ego-dir mcp --root /path/to/repo
```

Then run `/mcp` in Claude Code: `ego-dir` should be listed as connected, with the tools
`inspect_ego`, `resolve_definition` and `find_references`. Ask something like "use inspect_ego
to show what calls `TotalArea` in shapes.go", and the agent calls the tool. The result is
the same Markdown (or JSON) the CLI prints.

| Tool                 | Arguments                                                                  |
|----------------------|----------------------------------------------------------------------------|
| `inspect_ego`        | `file`, `line`, `col` (required); `format`, `depth`, `edges`, `direction`, `body` |
| `resolve_definition` | `file`, `line`, `col` (required); `format`                                  |
| `find_references`    | `file`, `line`, `col` (required); `format`                                  |

The arguments mean what the flags of the same name mean. `file` is relative to the root.

- **Fixed configuration.** `--root`, `--server` and `--timeout` are set when the server
  starts. A tool call cannot change them: an argument the schema does not list is an error.
- **Confined to the root.** A `file` that resolves outside `--root`, whether by `..`, an
  absolute path or a symbolic link, is refused.
- **Read-only.** Every tool only asks the language server questions, and says so
  (`readOnlyHint`).
- **Errors the agent can read.** A bad argument or a failed lookup is a tool result marked
  `isError`, with the same message the CLI prints, so the agent can correct itself.
- Each call starts its own language server and stops it afterwards, within `--timeout`
  (counted from when the call starts running). Up to four calls run at once; more wait their
  turn. A call the client cancels is stopped.
- Only the `file` argument is confined. A position on a use site resolves to the declaration
  it refers to, which may be outside the root (the standard library, for example), and is
  then reported like any other ego.
- Protocol messages go to stdout, one JSON object per line; diagnostics go to stderr. The
  server exits with `0` when stdin closes.
- It speaks MCP `2026-07-28` (`server/discover`, the version in each request's `_meta`) and,
  for clients that start with `initialize`, `2025-11-25`, `2025-06-18` and `2024-11-05`.

## Architecture

There are three decoupled layers, with an MCP server in front of the agent interface. See
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for diagrams, data structures and the edge derivation table.

```
cmd/ego-dir         Agent interface (CLI, flags, exit codes, MCP tools)
internal/mcp        Model Context Protocol server (JSON-RPC 2.0 over stdio)
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

make build                          # build dist/ego-dir for this machine (VERSION=v0.0.1 make build to stamp a version)
make install                        # install ego-dir into ~/.local/bin (override with INSTALL_DIR=...)
make test                           # go vet + go test -race (what CI runs)
make integration                    # tests against real language servers (skipped when not installed)
make pending                        # acceptance tests for code not written yet (expected to fail)
VERSION=v0.0.1 make package         # cross-compile to dist/*.tar.gz + dist/checksums.txt
```

## Release

Pushing a tag that matches `v*` runs [`.github/workflows/release.yml`](.github/workflows/release.yml).
It runs `make test`, then `make package` for linux, darwin and windows (amd64 and arm64), then
publishes a GitHub Release with the archives and `checksums.txt`.

## License

See [LICENSE](LICENSE).
