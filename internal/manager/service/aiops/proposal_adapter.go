package aiops

import (
	"context"
	"fmt"
	"strings"
	"time"

	aiopstoolsdec "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	manageraiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type MutatingProposalRepo interface {
	Insert(ctx context.Context, p *manageraiopsmodel.MutatingProposal) error
	UpdateDecision(ctx context.Context, id, decision string, reason *string) error
	MarkExecuted(ctx context.Context, id string, t time.Time) error
}

// MutatingProposalSink adapts the aiops data repo to ReviewGate's
// decorator-local audit seam. It consumes a repository interface so the
// service does not depend on a data implementation.
type MutatingProposalSink struct {
	repo MutatingProposalRepo
}

func NewMutatingProposalSink(repo MutatingProposalRepo) *MutatingProposalSink {
	if repo == nil {
		return nil
	}
	return &MutatingProposalSink{repo: repo}
}

func (s *MutatingProposalSink) Insert(ctx context.Context, ev aiopstoolsdec.MutatingProposalEvent) (string, error) {
	if s == nil || s.repo == nil {
		return "", errs.ErrInvalid
	}
	argsJSON := ev.ArgsJSON
	if strings.TrimSpace(argsJSON) == "" {
		argsJSON = "{}"
	}
	p := &manageraiopsmodel.MutatingProposal{
		SessionID:      ev.SessionID,
		ToolName:       ev.ToolName,
		ArgsJSON:       argsJSON,
		ToolClass:      ev.ToolClass,
		ReviewerAgent:  ev.ReviewerAgent,
		ReviewerTaskID: ev.ReviewerTaskID,
		OperatorUserID: ev.OperatorUserID,
		CreatedAt:      ev.CreatedAt,
	}
	if err := s.repo.Insert(ctx, p); err != nil {
		return "", fmt.Errorf("insert mutating proposal: %w", err)
	}
	return p.ID, nil
}

func (s *MutatingProposalSink) UpdateDecision(ctx context.Context, id, decision string, reason string) error {
	if s == nil || s.repo == nil {
		return errs.ErrInvalid
	}
	return s.repo.UpdateDecision(ctx, id, decision, &reason)
}

func (s *MutatingProposalSink) MarkExecuted(ctx context.Context, id string, t time.Time) error {
	if s == nil || s.repo == nil {
		return errs.ErrInvalid
	}
	return s.repo.MarkExecuted(ctx, id, t)
}
