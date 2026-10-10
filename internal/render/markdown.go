package render

import (
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
)

const none = "_none_"

// Markdown writes the prompt-oriented report of g. Every section is always
// present (empty ones read _none_), so the layout never changes. root is
// the workspace root that file paths are shown relative to.
func Markdown(w io.Writer, g *graph.EgoGraph, root string) error {
	v := newView(g, root)
	ego := v.ego
	lang := fenceLanguage(ego.URI)

	blocks := []string{
		"# Ego: " + inlineCode(ego.Name),
		"- kind: " + string(ego.Kind) + "\n- location: " + v.location(ego) + "\n- depth: " + strconv.Itoa(g.Depth),
		"## Signature", orNone(ego.Signature, func(s string) string { return fence(lang, s, "") }),
		"## Doc", orNone(ego.Doc, func(s string) string { return s }),
		"## Body", orNone(ego.Body, func(s string) string { return fence(lang, s, "") }),
		"## Upstream (callers)",
	}
	blocks = append(blocks, v.byHop(v.up)...)
	blocks = append(blocks, "## Downstream (callees)")
	blocks = append(blocks, v.byHop(v.down)...)
	blocks = append(blocks, "## Implementations", v.egoEdges(graph.EdgeImplements))
	blocks = append(blocks, "## Type dependencies", v.egoEdges(graph.EdgeInstantiates, graph.EdgeImports))
	blocks = append(blocks, "## Edges", v.edgeTable())

	_, err := io.WriteString(w, strings.Join(blocks, "\n\n")+"\n")
	return err
}

// byHop renders the nodes of one side grouped under "### Hop N".
func (v *view) byHop(dist map[graph.NodeID]int) []string {
	hops := map[int][]string{}
	for _, n := range v.g.Nodes { // sorted, so entries are in a stable order
		if d, ok := dist[n.ID]; ok {
			hops[d] = append(hops[d], v.entry(n))
		}
	}
	if len(hops) == 0 {
		return []string{none}
	}
	var levels []int
	for d := range hops {
		levels = append(levels, d)
	}
	sort.Ints(levels)
	var out []string
	for _, d := range levels {
		out = append(out, "### Hop "+strconv.Itoa(d), strings.Join(hops[d], "\n"))
	}
	return out
}

// entry is one alter as a list item with its signature, doc and, when
// present, body.
func (v *view) entry(n graph.Node) string {
	var b strings.Builder
	b.WriteString("- " + inlineCode(n.Name) + " (" + string(n.Kind) + ") " + v.location(n))
	if n.Signature != "" {
		b.WriteString("\n  - signature: " + inlineCode(collapse(n.Signature)))
	}
	if n.Doc != "" {
		b.WriteString("\n  - doc: " + collapse(n.Doc))
	}
	if n.Body != "" {
		b.WriteString("\n\n" + fence(fenceLanguage(n.URI), n.Body, "  "))
	}
	return b.String()
}

// egoEdges lists the nodes joined to the ego by edges of the given kinds.
func (v *view) egoEdges(kinds ...graph.EdgeKind) string {
	want := map[graph.EdgeKind]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	var items []string
	for _, e := range v.g.Edges {
		if !want[e.Kind] {
			continue
		}
		var other graph.NodeID
		switch v.g.Ego {
		case e.From:
			other = e.To
		case e.To:
			other = e.From
		default:
			continue
		}
		if n, ok := v.byID[other]; ok {
			items = append(items, v.entry(n))
		}
	}
	if len(items) == 0 {
		return none
	}
	return strings.Join(items, "\n")
}

func (v *view) edgeTable() string {
	if len(v.g.Edges) == 0 {
		return none
	}
	rows := []string{"| from | kind | to | weight |", "|------|------|----|--------|"}
	for _, e := range v.g.Edges {
		rows = append(rows, "| "+v.label(e.From)+" | "+string(e.Kind)+" | "+v.label(e.To)+" | "+strconv.Itoa(e.Weight)+" |")
	}
	return strings.Join(rows, "\n")
}

// label is a node's name and file:line for the edge table.
func (v *view) label(id graph.NodeID) string {
	n, ok := v.byID[id]
	if !ok {
		return inlineCode(string(id))
	}
	name := strings.ReplaceAll(inlineCode(n.Name), "|", `\|`)
	return name + " " + v.file(n) + ":" + strconv.Itoa(n.Selection.StartLine)
}

func orNone(s string, f func(string) string) string {
	if strings.TrimSpace(s) == "" {
		return none
	}
	return f(s)
}

// fence wraps code in a fenced block, indenting every line by indent. The
// fence is longer than any backtick run inside the code.
func fence(lang, code, indent string) string {
	marker := "```"
	for strings.Contains(code, marker) {
		marker += "`"
	}
	lines := append([]string{marker + lang}, strings.Split(code, "\n")...)
	lines = append(lines, marker)
	for i := range lines {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

// inlineCode wraps s in backticks, using a longer delimiter when s itself
// contains backticks.
func inlineCode(s string) string {
	delim := "`"
	for strings.Contains(s, delim) {
		delim += "`"
	}
	if len(delim) > 1 || strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		return delim + " " + s + " " + delim
	}
	return delim + s + delim
}

// collapse joins a multi-line text onto one line.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
