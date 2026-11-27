package toolresult

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	dom "golang.org/x/net/html"
)

type layoutField struct {
	path string
	text *dom.Node
}

type tableLayout struct {
	doc, row *dom.Node
	fields   []layoutField
	counts   map[string][]*dom.Node
}

// PageNeedsSource checks actual HTML elements and attributes. A standalone
// document can mention the binding syntax in prose or CSS.
func PageNeedsSource(page string, knownCollection bool) bool {
	if !knownCollection && !strings.Contains(strings.ToLower(page), "data-ongrid-") {
		return false
	}
	z := dom.NewTokenizer(strings.NewReader(page))
	for {
		switch z.Next() {
		case dom.ErrorToken:
			return false
		case dom.StartTagToken, dom.SelfClosingTagToken:
			token := z.Token()
			if knownCollection && slices.Contains([]string{"table", "article", "ul", "ol", "dl"}, token.Data) {
				return true
			}
			for _, a := range token.Attr {
				if strings.HasPrefix(a.Key, "data-ongrid-") {
					return true
				}
			}
		}
	}
}

func parseTableLayout(layout string) (*tableLayout, error) {
	if len(layout) > maxTableBytes {
		return nil, errors.New("HTML layout exceeds 16 MiB")
	}
	doc, err := dom.Parse(strings.NewReader(layout))
	if err != nil {
		return nil, err
	}
	l := &tableLayout{doc: doc, counts: make(map[string][]*dom.Node)}
	for n := range doc.Descendants() {
		if n.Type != dom.ElementNode {
			continue
		}
		var marker, value string
		for _, attr := range n.Attr {
			if !strings.HasPrefix(attr.Key, "data-ongrid-") {
				continue
			}
			if marker != "" {
				return nil, errors.New("use one data binding per element")
			}
			marker, value = attr.Key, attr.Val
		}
		if marker == "" {
			continue
		}
		inRow, inBody := false, false
		for a := range n.Ancestors() {
			if a.Namespace != "" || a.Data == "template" || a.Data == "noscript" {
				return nil, errors.New("place data bindings in visible HTML content")
			}
			inRow = inRow || a == l.row
			inBody = inBody || a.Data == "body"
		}
		if !inBody || n.Namespace != "" {
			return nil, errors.New("place data bindings in the HTML body")
		}
		switch marker {
		case "data-ongrid-row":
			if l.row != nil || !slices.Contains([]string{"tr", "article", "li", "div"}, n.Data) {
				return nil, errors.New("use one data-ongrid-row on a tr, article, li or div")
			}
			for sibling := range n.Parent.ChildNodes() {
				if sibling != n && sibling.Type != dom.CommentNode && !(sibling.Type == dom.TextNode && strings.TrimSpace(sibling.Data) == "") {
					return nil, errors.New("put the repeatable row alone in its container; put headings and totals outside")
				}
			}
			if n.Data == "tr" {
				if n.Parent.Data != "tbody" {
					return nil, errors.New("place the repeatable table row in tbody")
				}
				for sibling := range n.Parent.Parent.ChildNodes() {
					if sibling != n.Parent && sibling.Data == "tbody" {
						return nil, errors.New("keep all inventory rows in the bound tbody")
					}
				}
			}
			l.row = n
		case "data-ongrid-field", "data-ongrid-count":
			// These elements serialize source values only as escaped text.
			if !slices.Contains([]string{"td", "th", "span", "strong", "code", "b", "em", "small", "p", "div", "dd", "dt", "h1", "h2", "h3", "h4", "h5", "h6"}, n.Data) {
				return nil, fmt.Errorf("%s requires a text container such as a td or span", marker)
			}
			for child := n.FirstChild; child != nil; child = n.FirstChild {
				if child.Type != dom.TextNode || strings.TrimSpace(child.Data) != "" {
					return nil, fmt.Errorf("leave %s empty for the verified source value", marker)
				}
				n.RemoveChild(child)
			}
			text := &dom.Node{Type: dom.TextNode}
			n.AppendChild(text)
			if marker == "data-ongrid-field" {
				if !inRow || !resultfile.ValidPointer(value) || len(l.fields) >= 32 {
					return nil, errors.New("use 1–32 field pointers inside data-ongrid-row")
				}
				l.fields = append(l.fields, layoutField{path: value, text: text})
			} else {
				if inRow || !slices.Contains([]string{"source_rows", "excluded_rows", "exported_rows"}, value) {
					return nil, errors.New("use source_rows, excluded_rows or exported_rows outside the repeatable row")
				}
				l.counts[value] = append(l.counts[value], text)
			}
		default:
			return nil, fmt.Errorf("unknown HTML data binding %q", marker)
		}
	}
	if l.row == nil || len(l.fields) == 0 {
		return nil, errors.New("saved-result HTML requires one data-ongrid-row with empty data-ongrid-field elements")
	}
	return l, nil
}

// The bound is applied while serializing, including repeated model markup and HTML escaping.
type pageBuffer struct{ content strings.Builder }

func (b *pageBuffer) Write(p []byte) (int, error) {
	if len(p) > maxTableBytes-b.content.Len() {
		return 0, errors.New("rendered page exceeds 16 MiB")
	}
	return b.content.Write(p)
}

// RenderTablePage repeats the model's own markup, binding data only as text.
func (u *Usecase) RenderTablePage(ctx context.Context, scope Scope, layout string, spec TableSpec) (string, TableCounts, error) {
	var counts TableCounts
	l, err := parseTableLayout(layout)
	if err != nil {
		return "", counts, err
	}
	fields := make([]string, len(l.fields))
	for i, f := range l.fields {
		fields[i] = f.path
	}
	if len(spec.Columns) > 0 {
		if len(spec.Columns) != len(fields) {
			return "", counts, errors.New("table.columns must match the bound fields or be omitted")
		}
		for i, col := range spec.Columns {
			if col.Pointer != fields[i] {
				return "", counts, errors.New("table.columns must match the bound field order or be omitted")
			}
		}
	}
	var rows pageBuffer
	w := bufio.NewWriter(&rows)
	counts, err = u.visitTable(ctx, scope, spec, fields, func(values []string) error {
		for i, v := range values {
			l.fields[i].text.Data = v
		}
		return dom.Render(w, l.row)
	})
	if err != nil {
		return "", counts, err
	}
	if err = w.Flush(); err != nil {
		return "", counts, err
	}
	for key, value := range map[string]int{"source_rows": counts.Source, "excluded_rows": counts.Excluded, "exported_rows": counts.Exported} {
		for _, node := range l.counts[key] {
			node.Data = strconv.Itoa(value)
		}
	}
	// Only markup already serialized by dom.Render is inserted here. Source
	// values have passed through ordinary HTML text nodes, never raw HTML.
	l.row.Parent.InsertBefore(&dom.Node{Type: dom.RawNode, Data: rows.content.String()}, l.row)
	l.row.Parent.RemoveChild(l.row)
	var page pageBuffer
	if err = dom.Render(&page, l.doc); err != nil {
		return "", counts, err
	}
	return page.content.String(), counts, nil
}
