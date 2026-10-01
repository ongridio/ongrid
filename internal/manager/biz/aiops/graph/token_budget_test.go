package graph

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func TestBuildReActGraph_TokenBudgetStopsModelCalls(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"invoke", "stream"} {
		for _, tc := range []struct {
			name       string
			limit      int
			toolBudget bool
			wantReject bool
		}{
			{name: "exhausted", limit: 1, wantReject: true},
			{name: "exhausted_before_synthesis", limit: 1, toolBudget: true, wantReject: true},
			{name: "available", limit: 100000},
			{name: "available_for_synthesis", limit: 100000, toolBudget: true},
			{name: "unlimited", limit: 0},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				inner := newScriptedChatModel(makeAssistantNoTools("diagnosis complete"))
				g, err := BuildReActGraph(inner, nil, Config{})
				if err != nil {
					t.Fatal(err)
				}
				handler := llm.NewBudgetCallbackHandler(llm.NewInMemoryBudget(tc.limit), 0)
				input := &Input{UserText: "Check the service health and summarize the evidence."}
				if tc.toolBudget {
					input = &Input{History: []*schema.Message{
						schema.UserMessage(input.UserText),
						makeAssistantToolCall("", "call_1", "query_logql", `{}`),
						schema.ToolMessage(`{"status":"call_budget_exceeded","tool":"query_logql","final_answer_required":true}`, "call_1", schema.WithToolName("query_logql")),
					}}
				}
				err = runBudgetGraph(context.Background(), g, input, mode, compose.WithCallbacks(handler))
				wantCalls := int32(1)
				if tc.wantReject {
					wantCalls = 0
					if !errors.Is(err, llm.ErrBudgetExceeded) {
						t.Errorf("error = %v, want ErrBudgetExceeded", err)
					}
					if handler.Stats().Rejects != 1 {
						t.Errorf("budget stats = %+v, want one rejection", handler.Stats())
					}
				} else if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if calls := inner.generateCalls(); calls != wantCalls {
					t.Errorf("underlying model calls = %d, want %d", calls, wantCalls)
				}
			})
		}
	}
}

func runBudgetGraph(ctx context.Context, g compose.Runnable[*Input, *Output], input *Input, mode string, opts ...compose.Option) error {
	if mode == "invoke" {
		_, err := g.Invoke(ctx, input, opts...)
		return err
	}
	stream, err := g.Stream(ctx, input, opts...)
	if err != nil {
		return err
	}
	defer stream.Close()
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
