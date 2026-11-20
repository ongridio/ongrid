package chatruntime

import (
	"context"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

type contextStore interface {
	ContextHistory(context.Context, *model.Session, *model.Message) ([]*model.Message, error)
	BeginContextTurn(context.Context, string, string) error
	SaveContextUsage(context.Context, string, string, model.ContextUsage) error
	SaveContextCheckpoint(context.Context, string, string, int64, model.ContextCheckpoint) (bool, error)
}

func (rt *Runtime) contextWindow(ctx context.Context, p, m string) *graph.ContextWindow {
	var cap llm.WindowCapability
	if rt.cfg.ResolveWindow != nil {
		p, m, cap = rt.cfg.ResolveWindow(ctx, p, m)
	}
	return &graph.ContextWindow{Provider: p, Model: m, Capability: cap, Checker: rt.cfg.CallbackDeps.BudgetChecker, BudgetUser: rt.cfg.CallbackDeps.BudgetUserID}
}
