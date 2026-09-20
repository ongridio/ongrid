package llm_wiki

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

const sectionWriterSystemPrompt = `You are a documentation writer. Output clean Markdown content only.
` + sourceLanguageRule

// pageWriter renders resolved pages as Markdown. Sections that already carry
// Markdown structure are kept verbatim; the rest is polished by the LLM.
type pageWriter struct {
	llm CompilerLLM
	log *slog.Logger
}

// newPageWriter creates a Markdown page writer.
func newPageWriter(llm CompilerLLM, log *slog.Logger) *pageWriter {
	return &pageWriter{llm: llm, log: log}
}

// WritePage renders one page: title, every section, then the source footer.
func (w *pageWriter) WritePage(ctx context.Context, page *resolvedPage) (string, error) {
	w.log.InfoContext(ctx, "writer: generating page content",
		slog.String("page_id", page.PageID),
		slog.Int("sections", len(page.Sections)))

	var content strings.Builder
	content.WriteString(fmt.Sprintf("# %s\n\n", page.Title))

	for _, section := range page.Sections {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		sectionContent, err := w.writeSection(ctx, page.Title, section)
		if err != nil {
			return "", fmt.Errorf("write section %s: %w", section.Heading, err)
		}

		content.WriteString(sectionContent)
		content.WriteString("\n\n")
	}

	if len(page.Sources) > 0 {
		// The footer is written here rather than by the model, so it cannot follow
		// the source language the way the page body does, and the pipeline does no
		// language detection by design (HLD-002 §6.4). A hardcoded "## Sources"
		// heading was therefore English prose on every page in every language, so
		// the footer carries none: a rule and the cited titles, which are already
		// in the source language because they are the documents' own.
		content.WriteString("---\n\n")
		seenSources := make(map[uint64]bool)
		for _, source := range page.Sources {
			if seenSources[source.SourceID] {
				continue
			}
			seenSources[source.SourceID] = true
			content.WriteString(fmt.Sprintf("- %s (ID: %d)\n", source.DocumentTitle, source.SourceID))
		}
	}

	return content.String(), nil
}

// writeSection renders one section. The raw section content is used whenever it
// is already Markdown and as the fallback when the LLM call fails, so a build
// never loses text to a provider error.
func (w *pageWriter) writeSection(ctx context.Context, pageTitle string, section resolvedSection) (string, error) {
	if hasMarkdownFormatting(section.Content) {
		return renderSection(section.Heading, section.Content), nil
	}

	resp, err := w.llm.Complete(ctx, llm.ChatReq{
		Messages: []llm.Message{
			{Role: "system", Content: sectionWriterSystemPrompt},
			{Role: "user", Content: sectionWriterPrompt(pageTitle, section.Heading, section.Content)},
		},
	})
	if err != nil {
		w.log.WarnContext(ctx, "writer: LLM failed, using raw content",
			slog.String("section", section.Heading),
			slog.String("error", err.Error()))
		return renderSection(section.Heading, section.Content), nil
	}

	return renderSection(section.Heading, strings.TrimSpace(resp.Assistant.Content)), nil
}

func renderSection(heading, content string) string {
	return fmt.Sprintf("## %s\n\n%s", heading, content)
}

func sectionWriterPrompt(pageTitle, heading, content string) string {
	return fmt.Sprintf(`You are writing one section of a Wiki page titled "%s".

Write the content for the section "%s" based on the following source material.

Requirements:
- Write clear, concise documentation for the readers of this Wiki
- Use Markdown formatting (headers, lists, tables, code blocks where appropriate)
- Preserve the details the source treats as important — names, dates, amounts, roles, steps — and keep any code or configuration exact
- Keep the source material's own register and point of view; do not turn a policy, a report, or a note into a technical tutorial
- Make the content self-contained and easy to understand
- Add nothing the source material does not contain: never introduce a command, parameter, value, date, name, role or step of your own, and never state a guess as a fact
- %s
- Do not include the section heading (it will be added separately)

Source material:
%s`, pageTitle, heading, sourceLanguageRule, content)
}

// hasMarkdownFormatting reports whether content already carries Markdown
// structure worth keeping instead of rewriting it through the LLM.
func hasMarkdownFormatting(content string) bool {
	for _, indicator := range []string{"```", "- ", "1. ", "> ", "**", "__", "~~"} {
		if strings.Contains(content, indicator) {
			return true
		}
	}
	return len(strings.Split(content, "\n")) > 3
}
