package flow

import (
	"context"
	"errors"
	"testing"
	"time"

	bizflow "github.com/ongridio/ongrid/internal/manager/biz/flow"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

type llmClientFunc func(context.Context, llm.ChatReq) (*llm.ChatResp, error)

func (f llmClientFunc) Chat(ctx context.Context, req llm.ChatReq) (*llm.ChatResp, error) {
	return f(ctx, req)
}

func TestLLMNodeUsesLiveConfiguredTimeout(t *testing.T) {
	var timeout time.Duration
	var callCtx context.Context
	client := llm.WithDefaultTimeout(llmClientFunc(func(ctx context.Context, _ llm.ChatReq) (*llm.ChatResp, error) {
		callCtx = ctx
		return &llm.ChatResp{Assistant: llm.Message{Content: "report"}}, nil
	}), func(context.Context) time.Duration { return timeout })
	engine := bizflow.NewEngine(bizflow.Executors{LLM: LLMRunner{Client: client}}, nil, nil)
	node := bizflow.GraphNode{ID: "report", Type: bizflow.NodeLLM, Config: []byte(`{"prompt":"Generate a report"}`)}
	for _, duration := range []time.Duration{2 * time.Minute, 10 * time.Minute, 5 * time.Minute} {
		t.Run(duration.String(), func(t *testing.T) {
			timeout = duration
			before := time.Now()
			result, err := engine.RunSingle(context.Background(), node, &bizflow.RunContext{})
			after := time.Now()
			if err != nil {
				t.Fatal(err)
			}
			deadline, ok := callCtx.Deadline()
			if !ok || deadline.Before(before.Add(duration)) || deadline.After(after.Add(duration)) {
				t.Fatalf("deadline = %v (present=%v), want configured timeout %s", deadline, ok, duration)
			}
			if result.Output.(map[string]any)["answer"] != "report" {
				t.Fatalf("output = %#v", result.Output)
			}
			if !errors.Is(callCtx.Err(), context.Canceled) {
				t.Fatalf("request context not released after completion: %v", callCtx.Err())
			}
		})
	}
}

func TestLLMNodePreservesParentDeadline(t *testing.T) {
	for _, duration := range []time.Duration{time.Minute, 10 * time.Minute} {
		t.Run(duration.String(), func(t *testing.T) {
			want := time.Now().Add(duration)
			ctx, cancel := context.WithDeadline(context.Background(), want)
			defer cancel()
			client := llm.WithDefaultTimeout(llmClientFunc(func(ctx context.Context, _ llm.ChatReq) (*llm.ChatResp, error) {
				if got, ok := ctx.Deadline(); !ok || !got.Equal(want) {
					t.Fatalf("deadline = %v (present=%v), want parent deadline %v", got, ok, want)
				}
				return &llm.ChatResp{}, nil
			}), func(context.Context) time.Duration { return 5 * time.Minute })
			engine := bizflow.NewEngine(bizflow.Executors{LLM: LLMRunner{Client: client}}, nil, nil)
			_, err := engine.RunSingle(ctx, bizflow.GraphNode{Type: bizflow.NodeLLM, Config: []byte(`{"prompt":"Generate a report"}`)}, &bizflow.RunContext{})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLLMNodePropagatesCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		name, wantErr := "timeout", context.DeadlineExceeded
		if cancelParent {
			name, wantErr = "parent cancellation", context.Canceled
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := llm.WithDefaultTimeout(llmClientFunc(func(ctx context.Context, _ llm.ChatReq) (*llm.ChatResp, error) {
				if cancelParent {
					cancel()
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Second):
					t.Fatal("LLM request did not observe cancellation")
					return nil, nil
				}
			}), func(context.Context) time.Duration { return 10 * time.Millisecond })
			engine := bizflow.NewEngine(bizflow.Executors{LLM: LLMRunner{Client: client}}, nil, nil)
			_, err := engine.RunSingle(ctx, bizflow.GraphNode{Type: bizflow.NodeLLM, Config: []byte(`{"prompt":"Generate a report"}`)}, &bizflow.RunContext{})
			if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want %v", err, wantErr)
			}
		})
	}
}
