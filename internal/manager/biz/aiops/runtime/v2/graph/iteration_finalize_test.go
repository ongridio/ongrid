package graph

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"strings"
	"testing"
)

func TestIterationFinalizationReservesSummary(t *testing.T) {
	for _, stream := range []bool{false, true} {
		inner := newScriptedChatModel(makeAssistantNoTools("partial evidence summary"))
		wrapped := wrapBudgetStopModel(inner, 3)
		input := []*schema.Message{schema.SystemMessage("policy"), schema.UserMessage("inspect"), makeAssistantToolCall("", "a", "query_devices", `{}`), schema.ToolMessage(`{}`, "a"), makeAssistantToolCall("", "b", "query_incidents", `{}`), schema.ToolMessage(`{}`, "b")}
		var got *schema.Message
		var err error
		if stream {
			reader, e := wrapped.Stream(context.Background(), input)
			if e != nil {
				t.Fatal(e)
			}
			got, err = reader.Recv()
			reader.Close()
		} else {
			got, err = wrapped.Generate(context.Background(), input)
		}
		if err != nil || got.Content != "partial evidence summary" || len(got.ToolCalls) != 0 {
			t.Fatalf("final=%+v err=%v", got, err)
		}
		if !strings.Contains(inner.st.inputsLog[0][0].Content, "final response") {
			t.Fatal("missing finalization instruction")
		}
		if input[0].Content != "policy" {
			t.Fatal("mutated history")
		}
	}
}
func TestIterationFinalizationCountsOnlyCurrentTurn(t *testing.T) {
	m := &budgetStopModel{maxIterations: 2}
	old := []*schema.Message{schema.UserMessage("old"), makeAssistantToolCall("", "a", "read", `{}`)}
	if !m.shouldFinalize(old) {
		t.Fatal("expected summary reserve")
	}
	if m.shouldFinalize(append(old, schema.UserMessage("new"))) {
		t.Fatal("old turn consumed current budget")
	}
	if !m.shouldFinalize(append(old, schema.UserMessage("<system-reminder>continue</system-reminder>"))) {
		t.Fatal("reminder reset budget")
	}
}
func TestIterationFinalizationRejectsAdditionalTools(t *testing.T) {
	inner := newScriptedChatModel(makeAssistantToolCall("", "bad", "query_devices", `{}`))
	m := wrapBudgetStopModel(inner, 1)
	got, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("inspect")})
	if err != nil || got == nil || len(got.ToolCalls) > 0 || got.Content == "" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Generate(ctx, nil); err == nil {
		t.Fatal("cancellation swallowed")
	}
}

func TestGraphIterationLimitEndsWithoutMaxSteps(t *testing.T) {
	for _, stream := range []bool{false, true} {
		inner := newScriptedChatModel(makeAssistantToolCall("", "a", "alpha", `{}`), makeAssistantToolCall("", "b", "beta", `{}`), makeAssistantNoTools("collected evidence"))
		a := &fakeBaseTool{name: "alpha", class: "read", parameters: `{"type":"object"}`, runResp: `{}`}
		b := &fakeBaseTool{name: "beta", class: "read", parameters: `{"type":"object"}`, runResp: `{}`}
		g, err := BuildReActGraph(inner, []basetool.BaseTool{a, b}, Config{MaxIterations: 3})
		if err != nil {
			t.Fatal(err)
		}
		input := &Input{UserText: "inspect"}
		var out *Output
		if stream {
			sr, e := g.Stream(context.Background(), input)
			if e != nil {
				t.Fatal(e)
			}
			out, err = sr.Recv()
			sr.Close()
		} else {
			out, err = g.Invoke(context.Background(), input)
		}
		if err != nil || out == nil || out.AssistantMessage.Content != "collected evidence" {
			t.Fatalf("stream=%v output=%+v err=%v", stream, out, err)
		}
		if a.calls.Load() != 1 || b.calls.Load() != 1 || inner.generateCalls() != 3 {
			t.Fatal("iteration reserve executed wrong number of calls")
		}
		if !strings.Contains(inner.st.inputsLog[2][0].Content, "final response") {
			t.Fatal("last model turn was not summary-only")
		}
	}
}

func TestFinalizationDoesNotExposeToolProtocol(t *testing.T) {
	inner := newScriptedChatModel(makeAssistantNoTools("next query <｜｜DSML｜｜ calls>"))
	m := wrapBudgetStopModel(inner, 1)
	got, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("inspect")})
	if err != nil || strings.Contains(got.Content, "DSML") || len(got.ToolCalls) != 0 {
		t.Fatalf("unsafe summary=%+v err=%v", got, err)
	}
	prompt := inner.st.inputsLog[0]
	if !strings.Contains(prompt[len(prompt)-1].Content, "investigation phase has ended") {
		t.Fatal("missing final phase transition")
	}
}
