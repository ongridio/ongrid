// Package imformat converts the CommonMark/GFM emitted by the agent into
// the native text dialects supported by IM providers. Markdown remains an
// input/fallback format; provider payload JSON is built by trusted code.
package imformat

import (
	"bytes"
	"html"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	xhtml "golang.org/x/net/html"
)

const telegramMaxRunes = 4096

const (
	slackSectionMaxRunes = 2900
	slackMaxSections     = 45
)

type dialect uint8

const (
	dialectPlain dialect = iota
	dialectSlack
	dialectTelegramHTML
)

// Plain strips presentation syntax while retaining readable structure. It is
// used as the notification/accessibility fallback for native IM payloads.
func Plain(markdown string) string {
	return render(markdown, dialectPlain, 0)
}

// PlainExcerpt returns a bounded plain-text fallback suitable for push
// notifications and accessibility surfaces.
func PlainExcerpt(markdown string, maxRunes int) string {
	plain := Plain(markdown)
	if maxRunes <= 0 || utf8.RuneCountInString(plain) <= maxRunes {
		return plain
	}
	return truncateRunes(plain, maxRunes-1) + "…"
}

// Slack converts CommonMark/GFM into Slack mrkdwn. Slack's syntax resembles
// Markdown but differs for bold text and links, so forwarding GFM verbatim is
// not reliable.
func Slack(markdown string) string {
	return render(markdown, dialectSlack, 0)
}

// SlackSections returns Block Kit section bodies within Slack's per-section
// limit. Oversized indivisible blocks fall back to plain text before splitting
// so a cut can never leave malformed mrkdwn links or emphasis markers.
func SlackSections(markdown string) []string {
	body := Slack(markdown)
	if body == "" {
		return nil
	}
	paragraphs := strings.Split(body, "\n\n")
	sections := make([]string, 0, len(paragraphs))
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 || len(sections) >= slackMaxSections {
			return
		}
		sections = append(sections, current.String())
		current.Reset()
	}
	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		if utf8.RuneCountInString(paragraph) > slackSectionMaxRunes {
			flush()
			plain := Plain(paragraph)
			for utf8.RuneCountInString(plain) > slackSectionMaxRunes && len(sections) < slackMaxSections {
				sections = append(sections, truncateRunes(plain, slackSectionMaxRunes))
				plain = string([]rune(plain)[slackSectionMaxRunes:])
			}
			if plain != "" && len(sections) < slackMaxSections {
				sections = append(sections, plain)
			}
			continue
		}
		separator := ""
		if current.Len() > 0 {
			separator = "\n\n"
		}
		if utf8.RuneCountInString(current.String()+separator+paragraph) > slackSectionMaxRunes {
			flush()
			separator = ""
		}
		if len(sections) >= slackMaxSections {
			break
		}
		current.WriteString(separator)
		current.WriteString(paragraph)
	}
	flush()
	return sections
}

// TelegramHTML converts CommonMark/GFM into Telegram's HTML parse mode. HTML
// has a smaller escaping surface than MarkdownV2 and is stable across edits.
// The result is bounded to Telegram's 4096-character message limit while
// preserving balanced tags.
func TelegramHTML(markdown string) string {
	return render(markdown, dialectTelegramHTML, telegramMaxRunes)
}

func render(markdown string, d dialect, maxRunes int) string {
	source := []byte(strings.TrimSpace(markdown))
	if len(source) == 0 {
		return ""
	}
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	doc := md.Parser().Parse(text.NewReader(source))
	r := renderer{source: source, dialect: d, maxRunes: maxRunes}
	r.renderChildren(doc, 0)
	return strings.TrimSpace(r.out.String())
}

type renderer struct {
	source   []byte
	dialect  dialect
	maxRunes int
	out      strings.Builder
	stopped  bool
}

func (r *renderer) renderChildren(parent ast.Node, quoteDepth int) {
	for child := parent.FirstChild(); child != nil && !r.stopped; child = child.NextSibling() {
		r.renderBlock(child, quoteDepth)
	}
}

func (r *renderer) renderBlock(node ast.Node, quoteDepth int) {
	if r.stopped {
		return
	}
	switch n := node.(type) {
	case *ast.Heading:
		body := r.renderInlines(n)
		if body == "" {
			return
		}
		switch r.dialect {
		case dialectSlack:
			r.writeBlock("*" + body + "*")
		case dialectTelegramHTML:
			r.writeBlock("<b>" + body + "</b>")
		default:
			r.writeBlock(body)
		}
	case *ast.Paragraph, *ast.TextBlock:
		body := r.renderInlines(node)
		if quoteDepth > 0 {
			body = r.quote(body)
		}
		r.writeBlock(body)
	case *ast.Blockquote:
		r.renderChildren(n, quoteDepth+1)
	case *ast.List:
		r.renderList(n, quoteDepth)
	case *ast.FencedCodeBlock:
		r.renderCode(linesValue(n.Lines(), r.source), string(n.Language(r.source)))
	case *ast.CodeBlock:
		r.renderCode(linesValue(n.Lines(), r.source), "")
	case *ast.ThematicBreak:
		switch r.dialect {
		case dialectSlack:
			r.writeBlock("────────")
		case dialectTelegramHTML:
			r.writeBlock("────────")
		default:
			r.writeBlock("---")
		}
	case *extast.Table:
		r.renderTable(n)
	case *ast.HTMLBlock:
		// Raw HTML from an LLM must not become provider markup. Keep only
		// its readable text and escape it for the destination dialect.
		r.writeBlock(escapeText(stripTags(string(n.Text(r.source))), r.dialect))
	default:
		if node.HasChildren() {
			r.renderChildren(node, quoteDepth)
		}
	}
}

func (r *renderer) renderList(list *ast.List, quoteDepth int) {
	index := list.Start
	for item := list.FirstChild(); item != nil && !r.stopped; item = item.NextSibling() {
		body := strings.TrimSpace(r.renderInlines(item))
		if body == "" {
			continue
		}
		prefix := "• "
		if list.IsOrdered() {
			prefix = itoa(index) + ". "
			index++
		}
		line := prefix + strings.ReplaceAll(body, "\n", "\n  ")
		if quoteDepth > 0 {
			line = r.quote(line)
		}
		r.writeBlock(line)
	}
}

func (r *renderer) renderCode(code, language string) {
	code = strings.TrimSuffix(code, "\n")
	if code == "" {
		return
	}
	switch r.dialect {
	case dialectSlack:
		code = strings.ReplaceAll(code, "```", "'''")
		r.writeBlock("```" + code + "```")
	case dialectTelegramHTML:
		lang := safeLanguage(language)
		open := "<pre><code>"
		if lang != "" {
			open = `<pre><code class="language-` + html.EscapeString(lang) + `">`
		}
		r.writeBlock(open + html.EscapeString(code) + "</code></pre>")
	default:
		r.writeBlock(code)
	}
}

func (r *renderer) renderTable(table *extast.Table) {
	rows := make([]string, 0, table.ChildCount())
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		cells := make([]string, 0, row.ChildCount())
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, strings.TrimSpace(plainInline(cell, r.source)))
		}
		if len(cells) > 0 {
			rows = append(rows, strings.Join(cells, " | "))
		}
	}
	if len(rows) == 0 {
		return
	}
	tableText := strings.Join(rows, "\n")
	switch r.dialect {
	case dialectSlack:
		r.writeBlock("```" + escapeSlack(tableText) + "```")
	case dialectTelegramHTML:
		r.writeBlock("<pre>" + html.EscapeString(tableText) + "</pre>")
	default:
		r.writeBlock(tableText)
	}
}

func (r *renderer) renderInlines(parent ast.Node) string {
	var b strings.Builder
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		r.renderInline(&b, child, inlineStyle{})
	}
	return strings.TrimSpace(b.String())
}

type inlineStyle struct {
	bold   bool
	italic bool
	strike bool
	code   bool
	link   string
}

func (r *renderer) renderInline(b *strings.Builder, node ast.Node, style inlineStyle) {
	switch n := node.(type) {
	case *ast.Text:
		value := string(n.Value(r.source))
		if n.SoftLineBreak() || n.HardLineBreak() {
			value += "\n"
		}
		r.writeStyled(b, value, style)
	case *ast.String:
		r.writeStyled(b, string(n.Value), style)
	case *ast.Emphasis:
		if n.Level == 2 {
			style.bold = true
		} else {
			style.italic = true
		}
		r.renderInlineChildren(b, n, style)
	case *extast.Strikethrough:
		style.strike = true
		r.renderInlineChildren(b, n, style)
	case *extast.TaskCheckBox:
		if n.IsChecked {
			r.writeStyled(b, "☑ ", style)
		} else {
			r.writeStyled(b, "☐ ", style)
		}
	case *ast.CodeSpan:
		style.code = true
		r.writeStyled(b, string(n.Text(r.source)), style)
	case *ast.Link:
		style.link = safeLink(string(n.Destination))
		r.renderInlineChildren(b, n, style)
	case *ast.AutoLink:
		label := string(n.Label(r.source))
		style.link = safeLink(string(n.URL(r.source)))
		r.writeStyled(b, label, style)
	case *ast.Image:
		label := strings.TrimSpace(plainInline(n, r.source))
		if label == "" {
			label = "image"
		}
		style.link = safeLink(string(n.Destination))
		r.writeStyled(b, label, style)
	case *ast.RawHTML:
		// Do not pass model-authored HTML through to an IM parse mode.
		return
	default:
		r.renderInlineChildren(b, node, style)
	}
}

func (r *renderer) renderInlineChildren(b *strings.Builder, parent ast.Node, style inlineStyle) {
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		r.renderInline(b, child, style)
	}
}

func (r *renderer) writeStyled(b *strings.Builder, value string, style inlineStyle) {
	if value == "" {
		return
	}
	value = escapeText(value, r.dialect)
	switch r.dialect {
	case dialectSlack:
		if style.code {
			value = "`" + strings.ReplaceAll(value, "`", "'") + "`"
		} else {
			if style.bold {
				value = "*" + value + "*"
			}
			if style.italic {
				value = "_" + value + "_"
			}
			if style.strike {
				value = "~" + value + "~"
			}
		}
		if style.link != "" {
			value = "<" + escapeSlackURL(style.link) + "|" + value + ">"
		}
	case dialectTelegramHTML:
		if style.code {
			value = "<code>" + value + "</code>"
		} else {
			if style.bold {
				value = "<b>" + value + "</b>"
			}
			if style.italic {
				value = "<i>" + value + "</i>"
			}
			if style.strike {
				value = "<s>" + value + "</s>"
			}
		}
		if style.link != "" {
			value = `<a href="` + html.EscapeString(style.link) + `">` + value + "</a>"
		}
	}
	b.WriteString(value)
}

func (r *renderer) quote(body string) string {
	switch r.dialect {
	case dialectTelegramHTML:
		return "<blockquote>" + body + "</blockquote>"
	default:
		return "> " + strings.ReplaceAll(body, "\n", "\n> ")
	}
}

func (r *renderer) writeBlock(block string) {
	block = strings.TrimSpace(block)
	if block == "" || r.stopped {
		return
	}
	separator := ""
	if r.out.Len() > 0 {
		separator = "\n\n"
	}
	if r.maxRunes <= 0 {
		r.out.WriteString(separator)
		r.out.WriteString(block)
		return
	}
	current := utf8.RuneCountInString(r.out.String())
	remaining := r.maxRunes - current - utf8.RuneCountInString(separator)
	if remaining <= 1 {
		r.stopped = true
		return
	}
	if utf8.RuneCountInString(block) <= remaining {
		r.out.WriteString(separator)
		r.out.WriteString(block)
		return
	}
	// A whole block does not fit. Avoid cutting HTML tags: fall back to a
	// plain, escaped excerpt and close the message with an explicit marker.
	marker := "…"
	excerpt := truncateRunes(stripTags(block), remaining-utf8.RuneCountInString(marker))
	r.out.WriteString(separator)
	if r.dialect == dialectTelegramHTML {
		r.out.WriteString(html.EscapeString(excerpt))
	} else {
		r.out.WriteString(excerpt)
	}
	r.out.WriteString(marker)
	r.stopped = true
}

func escapeText(s string, d dialect) string {
	switch d {
	case dialectSlack:
		return escapeSlack(s)
	case dialectTelegramHTML:
		return html.EscapeString(s)
	default:
		return s
	}
}

func escapeSlack(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(s)
}

func escapeSlackURL(s string) string {
	return strings.NewReplacer("&", "&amp;", "|", "%7C", ">", "%3E").Replace(s)
}

func safeLink(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto":
		return u.String()
	default:
		return ""
	}
}

func safeLanguage(s string) string {
	var b strings.Builder
	for _, ch := range strings.ToLower(strings.TrimSpace(s)) {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

func linesValue(lines *text.Segments, source []byte) string {
	var b bytes.Buffer
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		b.Write(segment.Value(source))
	}
	return b.String()
}

func plainInline(parent ast.Node, source []byte) string {
	var b strings.Builder
	_ = ast.Walk(parent, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.Text:
			b.Write(n.Value(source))
			if n.SoftLineBreak() || n.HardLineBreak() {
				b.WriteByte('\n')
			}
		case *ast.String:
			b.Write(n.Value)
		case *ast.CodeSpan:
			b.Write(n.Text(source))
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

func stripTags(s string) string {
	var b strings.Builder
	tokenizer := xhtml.NewTokenizer(strings.NewReader(s))
	for {
		switch tokenizer.Next() {
		case xhtml.TextToken:
			b.Write(tokenizer.Text())
		case xhtml.ErrorToken:
			return html.UnescapeString(b.String())
		}
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
