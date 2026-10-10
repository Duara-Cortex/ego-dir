package render

import (
	"io"

	"github.com/rasJacobCharles/ego-dir/internal/graph"
)

// Schema identifies the JSON output format.
const Schema = "ego-dir/v1"

type jsonGraph struct {
	Schema string     `json:"schema"`
	Ego    string     `json:"ego"`
	Depth  int        `json:"depth"`
	Nodes  []jsonNode `json:"nodes"`
	Edges  []jsonEdge `json:"edges"`
}

type jsonNode struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Qualified string    `json:"qualified,omitempty"`
	Kind      string    `json:"kind"`
	File      string    `json:"file"`
	Range     jsonRange `json:"range"`
	Hop       int       `json:"hop"`
	Role      string    `json:"role"`
	Signature string    `json:"signature,omitempty"`
	Doc       string    `json:"doc,omitempty"`
	Body      string    `json:"body,omitempty"`
}

type jsonRange struct {
	StartLine int `json:"start_line"`
	StartCol  int `json:"start_col"`
	EndLine   int `json:"end_line"`
	EndCol    int `json:"end_col"`
}

type jsonEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Kind   string `json:"kind"`
	Weight int    `json:"weight"`
}

// JSON writes g in the ego-dir/v1 schema: ids are "file:line:col" of the
// symbol's name relative to root, and every node has a role (ego,
// upstream, downstream, both or related).
func JSON(w io.Writer, g *graph.EgoGraph, root string) error {
	v := newView(g, root)
	out := jsonGraph{
		Schema: Schema,
		Ego:    v.id(v.g.Ego),
		Depth:  v.g.Depth,
		Nodes:  []jsonNode{},
		Edges:  []jsonEdge{},
	}
	for _, n := range v.g.Nodes {
		out.Nodes = append(out.Nodes, jsonNode{
			ID:        v.id(n.ID),
			Name:      n.Name,
			Qualified: n.Qualified,
			Kind:      string(n.Kind),
			File:      v.file(n),
			Range:     jsonRange{n.Range.StartLine, n.Range.StartCol, n.Range.EndLine, n.Range.EndCol},
			Hop:       n.Hop,
			Role:      v.role(n.ID),
			Signature: n.Signature,
			Doc:       n.Doc,
			Body:      n.Body,
		})
	}
	for _, e := range v.g.Edges {
		out.Edges = append(out.Edges, jsonEdge{From: v.id(e.From), To: v.id(e.To), Kind: string(e.Kind), Weight: e.Weight})
	}
	return encode(w, out)
}
