package aiops

import (
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/agent"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

func TestResolveSessionRunOptions(t *testing.T) {
	provider, selectedModel := "deepseek", "deepseek-chat"
	sess := &model.Session{Provider: &provider, Model: &selectedModel}

	got := resolveSessionRunOptions(sess, agent.RunOptions{})
	if got.Provider != provider || got.Model != selectedModel {
		t.Fatalf("session fallback = %q/%q", got.Provider, got.Model)
	}

	got = resolveSessionRunOptions(sess, agent.RunOptions{Provider: "custom", Model: "qwen"})
	if got.Provider != "custom" || got.Model != "qwen" {
		t.Fatalf("request override = %q/%q", got.Provider, got.Model)
	}

	got = resolveSessionRunOptions(&model.Session{}, agent.RunOptions{})
	if got.Provider != "" || got.Model != "" {
		t.Fatalf("legacy session should fall through: %+v", got)
	}
}
