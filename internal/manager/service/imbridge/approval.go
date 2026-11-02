package imbridge

import (
	"context"
	"fmt"
	model "github.com/ongridio/ongrid/internal/manager/model/approval"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// ApprovalRepository is read-only; approval decisions continue through the
// existing administrator-authenticated approval usecase.
type ApprovalRepository interface {
	Get(ctx context.Context, id string) (*model.Approval, error)
}

// WithApprovals attaches status lookup during construction, before any runs.
func WithApprovals(repo ApprovalRepository) func(*AiopsServiceAdapter) {
	return func(a *AiopsServiceAdapter) { a.approvals = repo }
}

func (a *AiopsServiceAdapter) ApprovalStatus(ctx context.Context, sessionID, id string) (string, error) {
	if a.approvals == nil {
		return "", fmt.Errorf("approval status unavailable")
	}
	row, err := a.approvals.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get IM approval: %w", err)
	}
	if row == nil || sessionID == "" || row.SessionID != sessionID || row.ProposedBy != a.serviceUserID {
		return "", errs.ErrNotFound
	}
	return row.Status, nil
}
