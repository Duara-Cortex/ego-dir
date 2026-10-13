// Package render turns an ego-graph into agent-ready output: prompt-oriented
// Markdown with a fixed section layout, or JSON. Output depends only on the
// graph's content (not its slice order) and contains no absolute paths:
// files are shown relative to the workspace root.
package render

import (
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// Node roles relative to the ego.
const (
	roleEgo        = "ego"
	roleUpstream   = "upstream"
	roleDownstream = "downstream"
	roleBoth       = "both"
	roleRelated    = "related" // joined to the graph, but on neither side
)

// view is a sorted copy of a graph with everything the renderers derive.
type view struct {
	g    *graph.EgoGraph
	root string
	ego  graph.Node
	byID map[graph.NodeID]graph.Node
	up   map[graph.NodeID]int // distance to the ego along edges
	down map[graph.NodeID]int // distance from the ego along edges
}

func newView(g *graph.EgoGraph, root string) *view {
	c := &graph.EgoGraph{
		Ego:   g.Ego,
		Depth: g.Depth,
		Nodes: append([]graph.Node(nil), g.Nodes...),
		Edges: append([]graph.Edge(nil), g.Edges...),
	}
	c.Sort()
	v := &view{g: c, root: root, byID: map[graph.NodeID]graph.Node{}}
	for _, n := range c.Nodes {
		v.byID[n.ID] = n
	}
	v.ego = v.byID[c.Ego]
	v.down = v.distances(func(e graph.Edge) (graph.NodeID, graph.NodeID) { return e.From, e.To })
	v.up = v.distances(func(e graph.Edge) (graph.NodeID, graph.NodeID) { return e.To, e.From })
	return v
}

// distances runs a BFS from the ego over edges oriented by dir.
// implements edges are walked both ways, as the builder does.
func (v *view) distances(dir func(graph.Edge) (from, to graph.NodeID)) map[graph.NodeID]int {
	dist := map[graph.NodeID]int{v.g.Ego: 0}
	frontier := []graph.NodeID{v.g.Ego}
	for len(frontier) > 0 {
		var next []graph.NodeID
		for _, id := range frontier {
			for _, e := range v.g.Edges {
				from, to := dir(e)
				var other graph.NodeID
				switch {
				case from == id:
					other = to
				case e.Kind == graph.EdgeImplements && to == id:
					other = from
				default:
					continue
				}
				if _, seen := dist[other]; !seen {
					dist[other] = dist[id] + 1
					next = append(next, other)
				}
			}
		}
		frontier = next
	}
	delete(dist, v.g.Ego)
	return dist
}

func (v *view) role(id graph.NodeID) string {
	if id == v.g.Ego {
		return roleEgo
	}
	_, up := v.up[id]
	_, down := v.down[id]
	switch {
	case up && down:
		return roleBoth
	case up:
		return roleUpstream
	case down:
		return roleDownstream
	default:
		return roleRelated
	}
}

// file returns the node's file (a module's directory) relative to the
// root, with / separators; "" when it has none.
func (v *view) file(n graph.Node) string {
	if n.URI == "" {
		return ""
	}
	p, err := lsp.URIToPath(n.URI)
	if err != nil {
		return n.URI
	}
	if v.root != "" {
		if rel, err := filepath.Rel(v.root, p); err == nil {
			p = rel
		}
	}
	return filepath.ToSlash(p)
}

// location is "file:line" or "file:start-end" for the whole declaration,
// and the qualified name (a module's path) for a node without a position.
func (v *view) location(n graph.Node) string {
	if !n.Located() {
		return n.Qualified
	}
	loc := v.file(n) + ":" + strconv.Itoa(n.Range.StartLine)
	if n.Range.EndLine > n.Range.StartLine {
		loc += "-" + strconv.Itoa(n.Range.EndLine)
	}
	return loc
}

// id is the node's stable, root-relative id: "file:line:col" of its name,
// or the graph ID ("module:<path>") of a node without a position.
func (v *view) id(id graph.NodeID) string {
	n, ok := v.byID[id]
	if !ok || !n.Located() {
		return string(id)
	}
	return v.file(n) + ":" + strconv.Itoa(n.Selection.StartLine) + ":" + strconv.Itoa(n.Selection.StartCol)
}

// fenceLanguage names the code-fence language for a node's file.
func fenceLanguage(uri string) string {
	switch strings.ToLower(path.Ext(uri)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".php":
		return "php"
	default:
		return ""
	}
}
