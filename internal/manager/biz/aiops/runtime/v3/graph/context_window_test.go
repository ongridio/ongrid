package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	model "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	openai "github.com/sashabaranov/go-openai"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func currentContextMessage() *schema.Message {
	m := schema.UserMessage("current question")
	m.Extra = map[string]any{"current_context_turn": true}
	return m
}

func TestContextCompactsOnceAcrossModelSteps(t *testing.T) {
	raw := newScriptedChatModel(schema.AssistantMessage("User wants to diagnose disk errors.", nil))
	state := &ContextWindow{Capability: llm.WindowCapability{Total: 6000}}
	saves := 0
	state.Save = func(_ context.Context, id, summary string) error {
		saves++
		if id == "" || !strings.Contains(summary, "disk") {
			t.Fatal(id, summary)
		}
		return nil
	}
	m := &windowModel{raw: raw, inner: raw, window: state}
	var in []*schema.Message
	in = append(in, schema.SystemMessage("policy"))
	for i := 0; i < 60; i++ {
		u := schema.UserMessage(fmt.Sprintf("investigation %02d: %s", i, strings.Repeat("log ", 60)))
		u.Extra = map[string]any{"context_row": fmt.Sprint(i)}
		a := schema.AssistantMessage("completed", nil)
		a.ReasoningContent = "PRIVATE_REASONING"
		in = append(in, u, a)
	}
	in = append(in, schema.UserMessage("<system-reminder>constraints</system-reminder>"), currentContextMessage())
	view, opts, u, err := m.prepare(context.Background(), in, nil)
	if err != nil || saves != 1 || u.Estimated > u.InputBudget || u.Before <= u.Estimated || len(view) >= len(in) {
		t.Fatalf("prepare: usage=%+v saves=%d err=%v", u, saves, err)
	}
	if model.GetCommonOptions(&model.Options{}, opts...).MaxTokens == nil || view[0].Role != schema.System || view[len(view)-1] != in[len(in)-1] {
		t.Fatal("lost current turn or output reserve")
	}
	for _, batch := range raw.st.inputsLog {
		for _, v := range batch {
			if strings.Contains(v.Content, "PRIVATE_REASONING") {
				t.Fatal("private reasoning in summary request")
			}
		}
	}
	calls := raw.st.generateCalls.Load()
	_, _, _, err = m.prepare(context.Background(), in, nil)
	if err != nil || raw.st.generateCalls.Load() != calls || saves != 1 {
		t.Fatalf("second step repeated compaction: %v", err)
	}
	if len(in) != 123 || !strings.Contains(in[1].Content, "00") {
		t.Fatal("source history changed")
	}
}

func TestContextCompactsCompleteStepsOnly(t *testing.T) {
	raw := newScriptedChatModel(schema.AssistantMessage("Earlier tool results reviewed.", nil))
	m := &windowModel{raw: raw, inner: raw, window: &ContextWindow{Capability: llm.WindowCapability{Total: 5000}}}
	in := []*schema.Message{schema.SystemMessage("policy"), currentContextMessage()}
	for i := 0; i < 10; i++ {
		id := fmt.Sprint(i)
		in = append(in, schema.AssistantMessage("", []schema.ToolCall{{ID: id, Function: schema.FunctionCall{Name: "read"}}}), schema.ToolMessage(strings.Repeat("hello ", 230), id))
	}
	got, _, u, e := m.prepare(context.Background(), in, nil)
	if e != nil || m.window.stepCut == 0 || u.Before <= u.Estimated || got[1] != in[1] || got[len(got)-1] != in[len(in)-1] {
		t.Fatalf("steps: %+v %v", u, e)
	}
	for j := 3; j < len(got); j += 2 {
		if len(got[j].ToolCalls) != 1 || got[j].ToolCalls[0].ID != got[j+1].ToolCallID {
			t.Fatalf("detached tool at %d", j)
		}
	}
}

func TestContextToolReferenceAndOversizedCurrentInput(t *testing.T) {
	s := strings.Repeat("sample ", 900)
	text := fmt.Sprintf(`{"kind":"tool_result","result_id":"e659aa59-7b05-4e83-9bf4-d214996de221","storage_status":"ready","capture_status":"partial","preview":%q}`, s)
	msg := schema.ToolMessage(text, "call")
	ref := savedView(msg)
	if ref == msg || strings.Contains(ref.Content, s) || !strings.Contains(ref.Content, "partial") || msg.Content != text {
		t.Fatal("evidence projection lost semantics")
	}
	raw := newScriptedChatModel(schema.AssistantMessage("unused", nil))
	m := &windowModel{inner: raw, raw: raw, window: &ContextWindow{Capability: llm.WindowCapability{Total: 1024}}}
	user := currentContextMessage()
	user.Content = s
	_, _, u, e := m.prepare(context.Background(), []*schema.Message{user}, nil)
	if e == nil || raw.st.generateCalls.Load() != 0 || u.Status != "over_limit" {
		t.Fatalf("oversized user request: %+v %v", u, e)
	}
}

func TestContextRejectionRetriesOnlyBeforeGeneration(t *testing.T) {
	ctx := context.Background()
	raw := newScriptedChatModel(schema.AssistantMessage("Previous findings.", nil))
	history := []*schema.Message{schema.SystemMessage("policy")}
	for i := 0; i < 12; i++ {
		history = append(history, schema.UserMessage(strings.Repeat("old ", 160)), schema.AssistantMessage("done", nil))
	}
	history = append(history, currentContextMessage())
	for _, test := range []struct {
		name  string
		err   error
		calls int
	}{
		{"explicit limit", &openai.APIError{HTTPStatusCode: 400, Code: "context_length_exceeded", Message: "maximum context length is 2048 tokens"}, 2},
		{"unknown failure", errors.New("connection lost"), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := &windowModel{inner: raw, raw: raw, window: &ContextWindow{Capability: llm.WindowCapability{Total: 16000}}}
			calls := 0
			_, _, err := windowCall(ctx, m, history, nil, func(_ context.Context, msgs []*schema.Message, _ ...model.Option) (string, error) {
				calls++
				if calls == 1 {
					return "", test.err
				}
				if len(msgs) >= len(history) {
					t.Fatal("same request retried")
				}
				return "done", nil
			})
			if calls != test.calls || (calls == 2 && err != nil) || (calls == 1 && !errors.Is(err, test.err)) {
				t.Fatalf("calls=%d, error=%v", calls, err)
			}
		})
	}
}

func TestRejectedSummaryDoesNotAdvanceCursor(t *testing.T) {
	raw := newScriptedChatModel(schema.AssistantMessage("invented d533978a-3423-447c-99b0-9b696977ecb2", nil))
	state := &ContextWindow{Capability: llm.WindowCapability{Total: 4096}, Save: func(context.Context, string, string) error { t.Fatal("invalid summary persisted"); return nil }}
	m := &windowModel{inner: raw, raw: raw, window: state}
	in := []*schema.Message{schema.SystemMessage("policy"), schema.UserMessage(strings.Repeat("log ", 2100)), currentContextMessage()}
	_, _, u, e := m.prepare(context.Background(), in, nil)
	if e != nil || u.Status != "deferred" || state.historyCut != 0 || state.Summary != "" {
		t.Fatalf("failed summary: %+v, %v", u, e)
	}
}

func TestDurableContextCompactionAt256K(t *testing.T) {
	const turnID = "dd2960a7-739d-4017-87e6-f74a79b07284"
	const resultID = "0958a974-44f8-4e1d-99cf-e1c060ddebba"
	const invented = "71fb1a41-8fa0-4140-9ed0-cad30df46ea7"
	const prior = "Approval is pending."
	for _, tc := range []struct {
		name     string
		reply    *schema.Message
		modelErr error
		fallback bool
	}{
		{name: "complete", reply: schema.AssistantMessage("Host online; inspect the saved incident list.", nil)},
		{name: "provider incomplete", modelErr: fmt.Errorf("provider: %w", llm.ErrIncompleteResponse), fallback: true},
		{name: "exhausted output", reply: &schema.Message{Content: "truncated " + invented, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{CompletionTokens: 8192}}}, fallback: true},
		{name: "unverified evidence", reply: schema.AssistantMessage(invented, nil), fallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := newScriptedChatModel(tc.reply)
			raw.st.genErr = tc.modelErr
			state := &ContextWindow{Capability: llm.WindowCapability{Total: 1050000}, Summary: prior}
			var saved string
			state.Save = func(_ context.Context, boundary, notes string) error {
				saved = notes
				return nil
			}
			model := &windowModel{raw: raw, inner: raw, window: state}
			source := schema.UserMessage("Investigate CPU. " + strings.Repeat("采样", 66000) + " Keep the node online.")
			source.Extra = map[string]any{"context_row": turnID}
			call := schema.AssistantMessage("", []schema.ToolCall{{ID: "call_1", Function: schema.FunctionCall{Name: "query_incidents"}}})
			call.ReasoningContent = "private reasoning"
			result := schema.ToolMessage(`{"open":0,"acknowledged":4}`, "call_1")
			result.ToolName = "query_incidents"
			result.Extra = map[string]any{"result_id": resultID}
			current := currentContextMessage()
			view, _, usage, err := model.prepare(context.Background(), []*schema.Message{source, call, result, current}, nil)
			if limit := raw.st.lastMaxTokens; limit == nil || *limit != usage.OutputReserve {
				t.Fatalf("summary did not receive the reserved completion budget: %v", limit)
			}
			if err != nil || usage.Window != 256000 || usage.Status != "compacted" || usage.After >= usage.Before ||
				len(view) != 2 || view[1] != current || saved != state.Summary || !strings.Contains(saved, resultID) ||
				strings.Contains(saved, invented) || strings.Contains(saved, call.ReasoningContent) {
				t.Fatalf("compaction: %+v err=%v notes=%q", usage, err, saved)
			}
			if tc.fallback {
				for _, want := range []string{prior, turnID, "Keep the node online.", "Archived conversation excerpts"} {
					if !strings.Contains(saved, want) {
						t.Fatalf("missing %q in archived notes", want)
					}
				}
			}
		})
	}
}

func TestArchivedExcerptsRequireSuccessfulCheckpoint(t *testing.T) {
	const previous = "Keep previous decisions."
	source := schema.UserMessage(strings.Repeat("disk sample ", 750))
	source.Extra = map[string]any{"context_row": "0ac81d0d-f936-42c8-b9d2-cac19041d4db"}
	raw := newScriptedChatModel()
	raw.st.genErr = llm.ErrIncompleteResponse
	saves := 0
	state := &ContextWindow{Capability: llm.WindowCapability{Total: 4096}, Summary: previous}
	state.Save = func(context.Context, string, string) error {
		saves++
		return errors.New("checkpoint superseded")
	}
	m := &windowModel{raw: raw, window: state}
	in := []*schema.Message{source, currentContextMessage()}
	_, _, usage, err := m.prepare(context.Background(), in, nil)
	if err != nil || saves != 1 || usage.Status != "deferred" || state.historyCut != 0 || state.Summary != previous {
		t.Fatalf("lost history on failed save: %+v, err=%v, saves=%d", usage, err, saves)
	}
}
