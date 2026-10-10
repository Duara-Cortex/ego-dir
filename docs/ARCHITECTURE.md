# Architecture

## Layers

```mermaid
flowchart TB
    Agent["AI agent / human"] -->|"argv: inspect_ego --file --line --col --depth --format"| CLI

    subgraph Interface["Agent interface · cmd/ego-dir"]
        CLI["flag parsing, validation,<br/>1-based to LSP position conversion"]
    end

    subgraph Engine["Ego-graph engine · internal/graph"]
        Builder["Builder (BFS, depth k,<br/>edge filter, dedupe)"]
        Model["Node / Edge / EgoGraph"]
        Builder --> Model
    end

    subgraph Render["internal/render"]
        MD["Markdown renderer"]
        JSON["JSON renderer"]
    end

    subgraph Harness["LSP harness · internal/lsp"]
        Registry["Server registry<br/>(extension to command)"]
        Client["Client<br/>(typed LSP requests)"]
        Conn["JSON-RPC 2.0 Conn<br/>(Content-Length framing,<br/>id to response routing)"]
        Registry --> Client --> Conn
    end

    CLI --> Builder
    Builder -->|"SymbolSource interface"| Client
    Model --> MD & JSON
    MD & JSON -->|stdout| Agent
    Conn <-->|"stdio pipes"| LS["Language server process<br/>gopls, pyright, rust-analyzer, tsserver"]
```

The builder depends only on a narrow `SymbolSource` interface, not on the concrete LSP
client. That lets the graph logic be unit-tested against an in-memory fake graph, and lets
the LSP client be tested against an in-process fake server.

## Request flow for `inspect_ego`

```mermaid
sequenceDiagram
    participant CLI
    participant B as Builder
    participant C as LSP Client
    participant S as Language server

    CLI->>C: Start(root, cmd)
    C->>S: initialise
    S-->>C: InitialiseResult (capabilities)
    C->>S: initialised (notification)
    CLI->>B: Build(file, pos, depth k)
    B->>C: didOpen + prepareCallHierarchy(pos)
    C->>S: textDocument/prepareCallHierarchy
    S-->>C: CallHierarchyItem (the ego)
    loop BFS frontier, hop 1..k
        B->>C: incomingCalls / outgoingCalls
        B->>C: implementation / typeDefinition / references
        C->>S: requests (concurrent, bounded)
        S-->>C: results
        B->>B: dedupe by (uri, range), enqueue unseen nodes
    end
    B->>C: hover (signature + doc) for each node
    B-->>CLI: EgoGraph (sorted)
    CLI->>C: Close()
    C->>S: shutdown
    C->>S: exit (notification)
    C->>C: wait for process
```

## Data structures

```go
type NodeID string // "<uri>#L<startLine>:<startChar>", stable and unique

type Range struct{ StartLine, StartCol, EndLine, EndCol int } // 1-based for output

type Node struct {
    ID        NodeID
    Name      string     // e.g. "Builder.Build"
    Qualified string     // e.g. "graph.Builder.Build"
    Kind      SymbolKind // function, method, struct, interface, ...
    URI       string
    Range     Range      // full declaration range
    Selection Range      // identifier range
    Signature string
    Doc       string
    Body      string     // only filled for the ego, unless --body=all
    Hop       int        // 0 = ego
}

type EdgeKind string // calls | implements | instantiates | imports | references

type Edge struct {
    From, To NodeID
    Kind     EdgeKind
    Weight   int // number of call sites / references collapsed into this edge
}

type EgoGraph struct {
    Ego   NodeID
    Depth int
    Nodes []Node // sorted by (Hop, URI, Range)
    Edges []Edge // sorted by (From, To, Kind)
}
```

## Edge derivation

| Edge kind      | Direction   | LSP source                                                       | Notes                                   |
|----------------|-------------|------------------------------------------------------------------|-----------------------------------------|
| `calls`        | caller to callee | `textDocument/prepareCallHierarchy`, then `callHierarchy/incomingCalls` and `callHierarchy/outgoingCalls` | Weight = number of `fromRanges`. |
| `implements`   | concrete to interface | `textDocument/implementation` on the type or method | Orientation from the outline: an interface, or a method inside one, is the target. |
| `instantiates` | user to type | `textDocument/typeDefinition` on the symbol's name (e.g. a constructor's result type) | Best-effort; depends on server support. |
| `references`   | referrer to symbol | `textDocument/references` (excluding the declaration), grouped by enclosing symbol | Fallback when call hierarchy is unsupported. |
| `imports`      | file to package | Not derived yet. | The builder supports the kind; the LSP-backed source returns no `imports` edges. |

`textDocument/definition` resolves the ego when the cursor is on a use site rather than on
the declaration. `textDocument/documentSymbol` supplies names, kinds and full ranges for
symbols that are not callable (types, interfaces), the enclosing symbol of a reference, and
whether a method belongs to an interface (needed to orient `implements` edges).
A request the server does not support counts as "no edges", not as an error.

## Traversal rules

- **Directions do not mix.** Upstream is a BFS over incoming edges, downstream a BFS over
  outgoing edges. `--direction both` is the union of the two. Siblings (other callees of a
  caller) and co-callers are therefore not alters.
- **`implements` is followed both ways** in both searches, so the concrete implementation of
  a called interface method is downstream, and callers of an interface method are upstream
  of its implementations. The edge itself keeps its concrete-to-interface orientation.
- A node's hop is its shortest distance from the ego. Nodes at hop `k` are not expanded.
- Each node is expanded at most once per direction (language-server calls are expensive).
- An edge seen from both of its ends is stored once. Its weight is the larger of the two
  reported weights, not their sum.
- Default edge kinds are `calls,implements,instantiates,imports`. `references` is opt-in,
  because with call hierarchy available it duplicates `calls`.

## Positions

- CLI input: 1-based line, 1-based column counted in characters (runes).
- LSP wire: 0-based line, 0-based column in **UTF-16 code units**.
- Conversion reads the line from disk and maps rune offset to UTF-16 offset, so multibyte
  characters and astral-plane characters (surrogate pairs) are handled. This is covered by unit tests.

## Determinism rules

1. Nodes are de-duplicated by `(URI, Selection range)`.
2. Nodes are sorted by `(Hop, URI, StartLine, StartCol)`. Edges are sorted by `(From, To, Kind)`.
3. Paths are rendered relative to `--root` using `/` separators.
4. No timestamps, durations or absolute paths appear in output.
5. Renderers are covered by golden-file tests (`testdata/*.golden`).

## Output schemas

The reference outputs are
[`internal/render/testdata/total_area.golden.md`](../internal/render/testdata/total_area.golden.md)
and [`total_area.golden.json`](../internal/render/testdata/total_area.golden.json): the ego-graph
of `TotalArea` in `testdata/sample`, at depth 2. In outline:

```markdown
# Ego: `graph.Builder.Build`
- kind: method
- location: internal/graph/builder.go:42-88
- depth: 2

## Signature
## Doc
## Body
## Upstream (callers)        <- incoming edges, grouped by hop
## Downstream (callees)      <- outgoing edges, grouped by hop
## Implementations
## Type dependencies
## Edges                     <- table: from | kind | to | weight
```

Empty sections are rendered as `_none_` and never omitted, so the layout stays fixed.
Upstream and downstream alters are listed per hop with signature and doc. Their bodies
appear only with `--body all`. *Implementations* lists `implements` edges touching the ego,
and *Type dependencies* lists its `instantiates`/`imports` edges. In JSON, every node has a
`role` (`ego`, `upstream`, `downstream`, `both`, or `related` for a node joined to the graph on neither side of the ego), and ids are `file:line:col` relative to
`--root`.

## Exit codes

| Code | When                                                                                |
|------|-------------------------------------------------------------------------------------|
| 0    | Success. Results on stdout; stderr is empty.                                        |
| 1    | Usage error, detected before a server starts: flags, missing file, unknown extension, position outside the file. |
| 2    | Runtime error: the server cannot start, a request fails, or no symbol is at the position. |

## Test strategy

| Layer        | Technique                                                                            |
|--------------|--------------------------------------------------------------------------------------|
| JSON-RPC     | Framing round-trips, malformed headers, concurrent ids, server-initiated requests     |
| LSP client   | In-process fake server over `net.Pipe` with canned responses; lifecycle ordering      |
| Graph        | Fake `SymbolSource` graph: depth limits, cycles, filters, dedupe, ordering            |
| Render       | Golden files, plus "render twice and compare bytes"                                    |
| CLI          | `run(args, stdout, stderr) int` tested in-process: flags, validation, exit codes      |
| Integration  | `//go:build integration`, real `gopls` on `testdata/sample`, skipped when gopls is missing |

### Pending acceptance tests

Acceptance tests are written before the code they test, behind the `pending` build tag, so
`make test` stays green. Once the code exists, delete the `//go:build pending` line from the
files it makes pass, and `make test` (and CI) runs them from then on. `make pending` runs the
tests still waiting for their code; none are pending at the moment.

Shared fixtures, which run today:

- `testdata/sample`: a tiny Go module with a known call graph (an interface, an
  implementation, a constructor and two callers).
- `internal/lsp/lspfake`: a scripted LSP server with its own framing code, independent of
  the client. Its `Sample` scenario answers like gopls for `testdata/sample`, computing every
  position from the file itself. Test binaries re-run themselves as this server (via
  `EGO_DIR_LSPFAKE=sample`), which tests process spawning and the CLI end to end without
  gopls.
