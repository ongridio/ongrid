package llm_wiki

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWikiPromptsPreserveSourceLanguage(t *testing.T) {
	prompts := map[string]string{
		"summarizer system": summarizerSystemPrompt,
		"summarizer user":   summarizeChunkPrompt("示例文档"),
		"planner system":    plannerSystemPrompt,
		"planner user":      plannerPrompt("示例文档"),
		"writer system":     sectionWriterSystemPrompt,
		"writer user":       sectionWriterPrompt("示例页面", "概述", "示例内容"),
	}

	for name, prompt := range prompts {
		t.Run(name, func(t *testing.T) {
			assert.Contains(t, prompt, "same language as the source material")
			assert.Contains(t, prompt, "Do not translate the source")
		})
	}
}
