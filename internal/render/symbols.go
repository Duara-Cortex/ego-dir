package render

import (
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
)

// Reference is one use of a symbol, in editor coordinates (1-based line,
// 1-based character column). Text is the trimmed source line.
type Reference struct {
	URI       string
	Line, Col int
	Text      string
}

// Definition writes the declaration of n: where it is, its signature and
// its doc.
func Definition(w io.Writer, n graph.Node, root string) error {
	v := symbolView(n, root)
	blocks := []string{
		"# Definition: " + inlineCode(n.Name),
		"- kind: " + string(n.Kind) + "\n- location: " + v.id(n.ID) + "\n- declaration: " + v.location(n),
		"## Signature", orNone(n.Signature, func(s string) string { return fence(fenceLanguage(n.URI), s, "") }),
		"## Doc", orNone(n.Doc, func(s string) string { return s }),
	}
	_, err := io.WriteString(w, strings.Join(blocks, "\n\n")+"\n")
	return err
}

// DefinitionJSON writes the declaration of n as JSON.
func DefinitionJSON(w io.Writer, n graph.Node, root string) error {
	v := symbolView(n, root)
	return encode(w, struct {
		Schema string     `json:"schema"`
		Symbol jsonSymbol `json:"symbol"`
	}{Schema, v.symbol(n)})
}

// References writes the uses of target, sorted by file, line and column.
func References(w io.Writer, target graph.Node, refs []Reference, root string) error {
	v := symbolView(target, root)
	refs = sortedReferences(refs)
	blocks := []string{
		"# References: " + inlineCode(target.Name),
		"- definition: " + v.id(target.ID) + "\n- count: " + strconv.Itoa(len(refs)),
		"## References",
	}
	if len(refs) == 0 {
		blocks = append(blocks, none)
	} else {
		items := make([]string, len(refs))
		for i, r := range refs {
			items[i] = "- " + v.refID(r)
			if r.Text != "" {
				items[i] += " " + inlineCode(r.Text)
			}
		}
		blocks = append(blocks, strings.Join(items, "\n"))
	}
	_, err := io.WriteString(w, strings.Join(blocks, "\n\n")+"\n")
	return err
}

// ReferencesJSON writes the uses of target as JSON.
func ReferencesJSON(w io.Writer, target graph.Node, refs []Reference, root string) error {
	v := symbolView(target, root)
	type jsonRef struct {
		ID   string `json:"id"`
		File string `json:"file"`
		Line int    `json:"line"`
		Col  int    `json:"col"`
		Text string `json:"text,omitempty"`
	}
	out := struct {
		Schema     string     `json:"schema"`
		Symbol     jsonSymbol `json:"symbol"`
		References []jsonRef  `json:"references"`
	}{Schema: Schema, Symbol: v.symbol(target), References: []jsonRef{}}
	for _, r := range sortedReferences(refs) {
		out.References = append(out.References, jsonRef{v.refID(r), v.file(graph.Node{URI: r.URI}), r.Line, r.Col, r.Text})
	}
	return encode(w, out)
}

type jsonSymbol struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Qualified string     `json:"qualified,omitempty"`
	Kind      string     `json:"kind"`
	File      string     `json:"file,omitempty"`
	Range     *jsonRange `json:"range,omitempty"`
	Signature string     `json:"signature,omitempty"`
	Doc       string     `json:"doc,omitempty"`
}

func symbolView(n graph.Node, root string) *view {
	return &view{root: root, byID: map[graph.NodeID]graph.Node{n.ID: n}}
}

func (v *view) symbol(n graph.Node) jsonSymbol {
	return jsonSymbol{
		ID: v.id(n.ID), Name: n.Name, Qualified: n.Qualified, Kind: string(n.Kind), File: v.file(n),
		Range:     rangeOf(n),
		Signature: n.Signature, Doc: n.Doc,
	}
}

func (v *view) refID(r Reference) string {
	return v.file(graph.Node{URI: r.URI}) + ":" + strconv.Itoa(r.Line) + ":" + strconv.Itoa(r.Col)
}

func sortedReferences(refs []Reference) []Reference {
	out := append([]Reference(nil), refs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.URI != b.URI {
			return a.URI < b.URI
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
	return out
}

func encode(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
