package chatruntime

import (
	"context"
	"errors"
	"fmt"
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

func TestRuntimeDailyBudgetErrorPersistsAndEmits(t *testing.T) {
	sess := &aiopsmodel.Session{ID: "budget-daily", UserID: 7}
	store := newMemSessions(sess)
	inner := newScriptedChatModel()
	cfg := Config{Sessions: store, ChatModel: inner}
	want := "每日限额"
	cfg.CallbackDeps = callbacks.Deps{BudgetChecker: llm.NewInMemoryBudget(1)}
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
	if !strings.Contains(content, want) {
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
}
