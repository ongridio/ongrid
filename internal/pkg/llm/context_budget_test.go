package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestContextBudgetRepeatedReadsPreservePairsAndHistory(t *testing.T) {
	stub := &einoStubClient{}
	base, err := NewClientChatModel(ClientChatModelConfig{Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	bounded := WithContextBudget(base.(model.ToolCallingChatModel), ContextBudget{Window: 16000, Output: 2048})
	msgs := []*schema.Message{schema.SystemMessage("Keep all safety rules."), schema.UserMessage("Find the failed build.")}
	for i := 0; i < 35; i++ {
		id := fmt.Sprint(i)
		msgs = append(msgs, &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: id, Function: schema.FunctionCall{Name: "read_tool_result", Arguments: `{"result_id":"saved"}`}}}}, &schema.Message{Role: schema.Tool, ToolCallID: id, Content: `{"result_id":"saved","items":"` + strings.Repeat("证据", 400) + `","scan_complete":false}`})
	}
	before, _ := json.Marshal(msgs)
	if _, err = bounded.Generate(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	if stub.lastReq.MaxTokens != 2048 {
		t.Fatalf("output limit=%d", stub.lastReq.MaxTokens)
	}
	seen := map[string]bool{}
	for _, m := range stub.lastReq.Messages {
		for _, c := range m.ToolCalls {
			seen[c.ID] = true
		}
		if m.Role == "tool" {
			if !seen[m.ToolCallID] {
				t.Fatalf("orphan result %s", m.ToolCallID)
			}
			delete(seen, m.ToolCallID)
		}
	}
	if len(seen) > 0 {
		t.Fatal("missing tool results")
	}
	if len(stub.lastReq.Messages) >= len(msgs) {
		t.Fatal("no old batches removed")
	}
	after, _ := json.Marshal(msgs)
	if string(before) != string(after) {
		t.Fatal("durable history was mutated")
	}
	stream, err := bounded.Stream(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
}

func TestContextBudgetIncludesSchemasAndRejectsPinnedOverflow(t *testing.T) {
	stub := &einoStubClient{}
	base, err := NewClientChatModel(ClientChatModelConfig{Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	bounded := WithContextBudget(base.(model.ToolCallingChatModel), ContextBudget{Window: 12000, Output: 2048})
	bound, err := bounded.WithTools([]*schema.ToolInfo{{Name: "large", Desc: strings.Repeat("schema", 3000)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bound.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")}); !errors.Is(err, ErrContextBudgetExceeded) {
		t.Fatalf("tool schemas: got %v, want context budget error", err)
	}
	if stub.callCnt.Load() != 0 {
		t.Fatal("over-budget request reached provider")
	}
	if _, err = bounded.Generate(context.Background(), []*schema.Message{schema.UserMessage(strings.Repeat("中文", 3000))}); !errors.Is(err, ErrContextBudgetExceeded) {
		t.Fatalf("oversized current user input: got %v, want context budget error", err)
	}
	for _, m := range []model.ToolCallingChatModel{bound, bounded} {
		stream, err := m.Stream(context.Background(), []*schema.Message{schema.UserMessage(strings.Repeat("中文", 3000))})
		if stream != nil {
			stream.Close()
		}
		if !errors.Is(err, ErrContextBudgetExceeded) || errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("stream: got %v, want only context budget error", err)
		}
	}
	if stub.callCnt.Load() != 0 {
		t.Fatal("context rejection reached provider")
	}
	if _, err = bound.Generate(context.Background(), []*schema.Message{schema.UserMessage("summarize")}, model.WithToolChoice(schema.ToolChoiceForbidden)); err != nil {
		t.Fatal(err)
	}
}

func TestContextBudgetKeepsDraftAcrossConfirmationTurn(t *testing.T) {
	stub := &einoStubClient{}
	base, err := NewClientChatModel(ClientChatModelConfig{Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	bounded := WithContextBudget(base.(model.ToolCallingChatModel), ContextBudget{Window: 12000, Output: 2048})
	const draft = `{"kind":"config_draft","draft_hash":"verified-hash"}`
	msgs := []*schema.Message{
		schema.UserMessage("create a draft"),
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "draft", Function: schema.FunctionCall{Name: "draft_config_change", Arguments: `{}`}}}},
		{Role: schema.Tool, ToolCallID: "draft", Content: draft},
		schema.UserMessage("unrelated old question"),
		schema.AssistantMessage(strings.Repeat("x", 10000), nil),
		schema.UserMessage("confirm the draft"),
	}
	if _, err := bounded.Generate(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range stub.lastReq.Messages {
		found = found || m.Content == draft
	}
	if !found {
		t.Fatal("confirmation lost its control result")
	}
}
