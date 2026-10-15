package llm_wiki

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSummarize_CarriesTheDocumentTitleIntoTheDigest — the planner only ever
// sees the digest, so a digest without titles reached the model as anonymous
// text: it could not tell a runbook from a contract, and it could not name a
// page after the document it was compiled from.
func TestSummarize_CarriesTheDocumentTitleIntoTheDigest(t *testing.T) {
	corpus := &corpus{
		Documents: []corpusDocument{
			{SourceID: 4, SourceVersionID: 7, Title: "2026 年度采购合同.md"},
			{SourceID: 5, SourceVersionID: 8, Title: "on-call 排障手册.md"},
		},
		Chunks: []corpusChunk{
			{DocumentIndex: 0, Ordinal: 0, Text: "合同期自 2026 年 1 月起。"},
			{DocumentIndex: 1, Ordinal: 0, Text: "解析失败时先看 resolver。"},
		},
	}
	summarizer := newSummarizer(mustNotCallLLM(), testWikiLogger())

	items, err := summarizer.Summarize(context.Background(), corpus)

	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "2026 年度采购合同.md", items[0].DocumentTitle)
	assert.Equal(t, "on-call 排障手册.md", items[1].DocumentTitle)
	digest := buildDigest(items)
	assert.Contains(t, digest, "Document: 2026 年度采购合同.md")
	assert.Contains(t, digest, "Document: on-call 排障手册.md")
}

// TestDigestTitle_KeepsTheDigestHeaderToOneLine — the title is rendered into the
// digest's per-item header, so a title carrying a newline would push part of
// itself into the passage the planner reads.
func TestDigestTitle_KeepsTheDigestHeaderToOneLine(t *testing.T) {
	assert.Equal(t, "危险 变更", digestTitle("  危险\n变更\t"))
	assert.Equal(t, "", digestTitle("\n\n"))

	long := strings.Repeat("标", maxDigestTitleRunes+50)
	got := digestTitle(long)
	assert.Equal(t, maxDigestTitleRunes, len([]rune(got)))
	assert.True(t, strings.HasPrefix(long, got), "truncation must not corrupt the runes it keeps")
}

// TestBuildDigestOmitsTheTitleLineWhenTheDocumentHasNone — an empty title must
// not render a stray label the planner could read as content.
func TestBuildDigestOmitsTheTitleLineWhenTheDocumentHasNone(t *testing.T) {
	digest := buildDigest([]digestItem{{ChunkIndex: 0, Text: "no title here", SourceIDs: []uint64{1}}})

	require.Equal(t, "--- Digest source_id: 0 ---\nno title here\n\n", digest)
	assert.NotContains(t, digest, "Document:")
}
