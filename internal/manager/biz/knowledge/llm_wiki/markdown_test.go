package llm_wiki

import (
	"context"
	"errors"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWritePage_SourceFooterCarriesNoLanguageSpecificProse — the footer is
// written by this code, not by the model, so it cannot follow the source
// language the way the page body does, and the pipeline does no language
// detection by design. The hardcoded "## Sources" heading put English prose on
// every page in every language; the footer now carries a rule and the cited
// titles only, and those titles are already in the source language.
func TestWritePage_SourceFooterCarriesNoLanguageSpecificProse(t *testing.T) {
	page := &resolvedPage{
		PageID: "contracts",
		Title:  "采购合同",
		Sections: []resolvedSection{
			{Heading: "范围", Content: "本合同自 2026 年 1 月起生效。"},
		},
		Sources: []resolvedSource{
			{SourceID: 4, DocumentTitle: "采购合同.md"},
			{SourceID: 5, DocumentTitle: "排障手册.md"},
			{SourceID: 4, DocumentTitle: "采购合同.md"},
		},
	}
	writer := newPageWriter(writerLLM("本节说明采购范围。"), testWikiLogger())

	content, err := writer.WritePage(context.Background(), page)

	require.NoError(t, err)
	assert.Equal(t, "# 采购合同\n\n## 范围\n\n本节说明采购范围。\n\n---\n\n- 采购合同.md (ID: 4)\n- 排障手册.md (ID: 5)\n", content)
	assert.NotContains(t, content, "## Sources")
}

// TestWriteSection_AlreadyFormattedContentSkipsTheLLM — the verbatim path is the
// reason a section can reach a page untouched: it must not round-trip through
// the model, and it must not be able to lose text to one.
func TestWriteSection_AlreadyFormattedContentSkipsTheLLM(t *testing.T) {
	writer := newPageWriter(mustNotCallLLM(), testWikiLogger())

	got, err := writer.writeSection(context.Background(), "排障", resolvedSection{
		Heading: "步骤",
		Content: "- 先看 resolver\n- 再看上游 DNS",
	})

	require.NoError(t, err)
	assert.Equal(t, "## 步骤\n\n- 先看 resolver\n- 再看上游 DNS", got)
}

// TestWriteSection_FallsBackToRawContentWhenTheLLMFails — a build must never
// lose a section to a provider error.
func TestWriteSection_FallsBackToRawContentWhenTheLLMFails(t *testing.T) {
	failing := compilerLLMFunc{
		version: "test",
		call: func(context.Context, llm.ChatReq) (*llm.ChatResp, error) {
			return nil, errors.New("provider unavailable")
		},
	}
	writer := newPageWriter(failing, testWikiLogger())

	got, err := writer.writeSection(context.Background(), "排障", resolvedSection{
		Heading: "背景",
		Content: "一次解析故障的背景说明。",
	})

	require.NoError(t, err)
	assert.Equal(t, "## 背景\n\n一次解析故障的背景说明。", got)
}

// TestSectionWriterPrompt_ForbidsInventingMaterial — the writer is the last stage
// before publication, so an invented command or value lands in the Wiki with no
// reviewer: the prompt must forbid adding what the source material lacks.
func TestSectionWriterPrompt_ForbidsInventingMaterial(t *testing.T) {
	prompt := sectionWriterPrompt("排障", "步骤", "先看 resolver")

	assert.Contains(t, prompt, "Add nothing the source material does not contain")
}

// writerLLM answers every call with the same content.
func writerLLM(content string) CompilerLLM {
	return compilerLLMFunc{
		version: "test",
		call: func(context.Context, llm.ChatReq) (*llm.ChatResp, error) {
			return &llm.ChatResp{Assistant: llm.Message{Role: "assistant", Content: content}}, nil
		},
	}
}
