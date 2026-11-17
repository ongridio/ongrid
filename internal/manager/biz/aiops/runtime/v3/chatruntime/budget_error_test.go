package chatruntime

import (
	"context"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph/callbacks"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func TestGraphErrorBudgetClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"daily", llm.ErrBudgetExceeded, "今日 LLM"},
		{"context", llm.ErrContextBudgetExceeded, "上下文上限"},
		{"wrapped_context", fmt.Errorf("graph: %w", fmt.Errorf("model: %w", llm.ErrContextBudgetExceeded)), "上下文上限"},
		{"wrapped_daily", fmt.Errorf("graph: %w", fmt.Errorf("model: %w", llm.ErrBudgetExceeded)), "今日 LLM"},
		{"analysis", errors.New("analysis budget exhausted"), "analysis budget exhausted"},
		{"reads", errors.New("result reading budget exhausted"), "result reading budget exhausted"},
		{"unrelated", errors.New("budget exceeded"), "budget exceeded"},
		{"timeout", context.DeadlineExceeded, "超时或被取消"},
		{"provider_quota", errors.New("API error: insufficient_quota"), "LLM provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildGraphErrorApology(tc.err)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if !errors.Is(tc.err, llm.ErrBudgetExceeded) && strings.Contains(got, "今日 LLM") {
				t.Fatalf("unrelated error classified as daily budget: %q", got)
			}
		})
	}
}

func TestRuntimeBudgetErrorsPersistAndEmitDistinctMessages(t *testing.T) {
	for _, kind := range []string{"context", "daily"} {
		t.Run(kind, func(t *testing.T) {
			sess := &aiopsmodel.Session{ID: "budget-" + kind, UserID: 7}
			store := newMemSessions(sess)
			inner := newScriptedChatModel()
			cfg := Config{Sessions: store, ChatModel: inner}
			want := "每日限额"
			if kind == "context" {
				cfg.ChatModel = llm.WithContextBudget(inner, llm.ContextBudget{Window: 4096, Output: 2048})
				want = "上下文上限"
			} else {
				cfg.CallbackDeps = callbacks.Deps{BudgetChecker: llm.NewInMemoryBudget(1)}
			}
			rt, err := NewRuntime(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var events []Event
			reply, err := rt.Handle(context.Background(), &Request{SessionID: sess.ID, UserID: 7, UserText: "核对保存的数据", Emit: func(ev Event) { events = append(events, ev) }})
			if err != nil || reply == nil || reply.Message == nil || reply.Message.Content == nil {
				t.Fatalf("reply=%+v err=%v", reply, err)
			}
			content := *reply.Message.Content
			if !strings.Contains(content, want) || (kind == "context" && strings.Contains(content, "今日 LLM")) {
				t.Fatalf("wrong budget message: %s", content)
			}
			if inner.calls.Load() != 0 {
				t.Fatal("rejected request reached model")
			}
			last := store.messages[len(store.messages)-1]
			if last.Role != aiopsmodel.RoleAssistant || last.Content == nil || *last.Content != content {
				t.Fatal("wrong persisted error message")
			}
			emitted := false
			for _, ev := range events {
				if ev.Type == EventAssistant && ev.Assistant != nil && ev.Assistant.Content == content {
					emitted = true
				}
			}
			if !emitted || len(events) == 0 || events[len(events)-1].Type != EventDone {
				t.Fatal("missing error reply or done event")
			}
		})
	}
}

func TestRuntimeEmitsPreparedContextUsage(t *testing.T) {
	sess := &aiopsmodel.Session{ID: "usage", UserID: 7}
	rt, err := NewRuntime(Config{Sessions: newMemSessions(sess), ChatModel: newScriptedChatModel(schema.AssistantMessage("done", nil))})
	if err != nil {
		t.Fatal(err)
	}
	var usage *llm.ContextUsage
	_, err = rt.Handle(context.Background(), &Request{SessionID: sess.ID, UserID: 7, Provider: "custom", Model: "alias", UserText: "question", Emit: func(ev Event) {
		if ev.Type == contract.EventContextUsage {
			usage = ev.ContextUsage
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.Model != "alias" || usage.Provider != "custom" || usage.Window != 131072 || usage.InputTokens <= 4096 || usage.OutputTokens != 8192 {
		t.Fatalf("missing or invalid context estimate: %+v", usage)
	}
}
