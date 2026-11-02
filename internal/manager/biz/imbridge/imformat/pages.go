package imformat

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	"strings"
)

// MarkdownPages preserves the original rich text when it fits. Oversized
// blocks become escaped plain text; the supplied predicate checks the actual
// provider payload or a conservative provider-specific source budget.
func MarkdownPages(markdown string, fits func(string) bool) []string {
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return nil
	}
	if fits(markdown) {
		return []string{markdown}
	}
	source := []byte(markdown)
	doc := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(source))
	var blocks []string
	// Render the oversized answer as readable text before partitioning. This
	// deliberately avoids splitting fences, links or emphasis across messages.
	// Short answers retain the provider's full native rich-text representation.
	r := renderer{source: source, dialect: dialectPlain, preserveLinks: true}
	r.renderChildren(doc, 0)
	plain := r.out.String()
	atoms := make([]string, 0, len(plain))
	for _, ch := range plain {
		part := string(ch)
		if strings.ContainsRune("\\`*_{}[]<>()#+-.!|~", ch) {
			part = "\\" + part
		}
		atoms = append(atoms, part)
	}
	for len(atoms) > 0 {
		// Bound predicate work even for very large answers. Binary search avoids
		// reparsing every growing prefix once per character.
		upper := len(atoms)
		if upper > 4000 {
			upper = 4000
		}
		lo, hi := 1, upper
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if fits(strings.Join(atoms[:mid], "")) {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		blocks = append(blocks, strings.Join(atoms[:lo], ""))
		atoms = atoms[lo:]
	}
	return blocks
}
