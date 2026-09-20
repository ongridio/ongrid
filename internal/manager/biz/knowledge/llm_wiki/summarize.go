package llm_wiki

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// summarizeTokenBudget is the corpus size (in estimated tokens) above which the
// Map phase summarizes every chunk instead of passing the text through.
const summarizeTokenBudget = 32_000

// digestItem is the plain-text digest of one chunk plus the sources it came
// from. The planner consumes the reduced digest, the evidence resolver maps the
// referenced indices back to source documents.
type digestItem struct {
	ChunkIndex int
	Text       string
	SourceIDs  []uint64
	// DocumentTitle is the document the chunk came from. The planner never sees
	// the corpus, so without it a digest of an organization's documents reaches
	// the model as anonymous text: it cannot tell a runbook from a contract, and
	// it cannot name a page after the document it was compiled from.
	DocumentTitle string
}

// summarizer performs the Map phase of Wiki compilation: it turns every corpus
// chunk into one digest item, summarizing chunks only when the corpus is large
// enough to need it.
type summarizer struct {
	llm CompilerLLM
	log *slog.Logger
}

const summarizerSystemPrompt = `You are a knowledge base summarizer. Output plain text only. Be concise but preserve the details the material treats as important, whatever kind of material it is.
` + sourceLanguageRule

// newSummarizer creates a corpus summarizer.
func newSummarizer(llm CompilerLLM, log *slog.Logger) *summarizer {
	return &summarizer{llm: llm, log: log}
}

// Summarize turns the corpus into digest items. Short corpora are returned
// verbatim so small installs need no extra LLM round trips.
func (s *summarizer) Summarize(ctx context.Context, corpus *corpus) ([]digestItem, error) {
	// Estimate total tokens
	totalTokens := 0
	for _, chunk := range corpus.Chunks {
		totalTokens += estimateTokens(chunk.Text)
	}

	// Short corpus: skip summarization, use chunks directly
	if totalTokens < summarizeTokenBudget {
		s.log.InfoContext(ctx, "summarizer: short corpus, skipping summarization",
			slog.Int("chunks", len(corpus.Chunks)),
			slog.Int("estimated_tokens", totalTokens))

		items := make([]digestItem, len(corpus.Chunks))
		for i, chunk := range corpus.Chunks {
			document := corpus.Documents[chunk.DocumentIndex]
			items[i] = digestItem{
				ChunkIndex:    i,
				Text:          chunk.Text,
				SourceIDs:     []uint64{document.SourceID},
				DocumentTitle: digestTitle(document.Title),
			}
		}
		return items, nil
	}

	// Long corpus: perform Map summarization
	s.log.InfoContext(ctx, "summarizer: performing map summarization",
		slog.Int("chunks", len(corpus.Chunks)),
		slog.Int("estimated_tokens", totalTokens))

	items := make([]digestItem, len(corpus.Chunks))
	for i, chunk := range corpus.Chunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		summary, err := s.summarizeChunk(ctx, chunk.Text, i)
		if err != nil {
			return nil, fmt.Errorf("summarize chunk %d: %w", i, err)
		}

		document := corpus.Documents[chunk.DocumentIndex]
		items[i] = digestItem{
			ChunkIndex:    i,
			Text:          summary,
			SourceIDs:     []uint64{document.SourceID},
			DocumentTitle: digestTitle(document.Title),
		}
	}

	return items, nil
}

// summarizeChunk generates a plain-text summary of a chunk.
func (s *summarizer) summarizeChunk(ctx context.Context, text string, index int) (string, error) {
	prompt := summarizeChunkPrompt(text)

	resp, err := s.llm.Complete(ctx, llm.ChatReq{
		Messages: []llm.Message{
			{Role: "system", Content: summarizerSystemPrompt},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("llm summarize: %w", err)
	}

	summary := strings.TrimSpace(resp.Assistant.Content)
	if summary == "" {
		return "", fmt.Errorf("empty summary returned for chunk %d", index)
	}

	return summary, nil
}

// summarizeChunkPrompt renders the summary request for one source chunk.
func summarizeChunkPrompt(text string) string {
	return fmt.Sprintf(`Summarize the following document in plain language.

Requirements:
- Focus on the key facts, concepts, procedures, decisions, and figures the document carries
- Keep names, roles, dates, amounts, and values exact, and keep any code, command, path, or configuration value exact
- Describe the material as what it is; do not reframe a policy, a report, or a note as a technical manual
- Keep the summary concise but complete
- Output plain text only, no JSON or markdown formatting
- %s

Text to summarize:
%s`, sourceLanguageRule, text)
}

// maxDigestTitleRunes caps the document title carried into the digest. The
// title is prompt scaffolding, not content, and must not crowd out the passage
// it labels.
const maxDigestTitleRunes = 200

// digestTitle normalizes a document title for the digest's one-line header. A
// title carrying a newline would otherwise break the per-item framing and make
// the rest of the title read as part of the passage.
func digestTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > maxDigestTitleRunes {
		title = string(runes[:maxDigestTitleRunes])
	}
	return title
}

// buildDigest joins digest items into the single digest string the planner reads.
func buildDigest(items []digestItem) string {
	var digest strings.Builder
	for i, item := range items {
		// Only expose the stable, zero-based digest index. Exposing database
		// source IDs here made the planner confuse those IDs with item indices.
		digest.WriteString(fmt.Sprintf("--- Digest source_id: %d ---\n", i))
		// The planner never sees the corpus itself, so the title is what tells it
		// whether a passage came from a runbook, a contract or a meeting note —
		// and what to name a page compiled from that document.
		if item.DocumentTitle != "" {
			digest.WriteString("Document: " + item.DocumentTitle + "\n")
		}
		digest.WriteString(item.Text)
		digest.WriteString("\n\n")
	}
	return digest.String()
}

// estimateTokens provides a rough token estimate (charsPerToken characters per token).
func estimateTokens(text string) int {
	return len(text) / charsPerToken
}
