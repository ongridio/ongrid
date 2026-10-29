package externalmcp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	operation "github.com/ongridio/ongrid/internal/manager/model/aiops"
	approvalmodel "github.com/ongridio/ongrid/internal/manager/model/approval"
	auditmodel "github.com/ongridio/ongrid/internal/manager/model/audit"
	approvalserver "github.com/ongridio/ongrid/internal/manager/server/approval"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCancellationRetiresPendingApproval(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	out, err := f.call(t, "manage_monitor_panel", panelInput("cancelled-approval"))
	require.NoError(t, err)
	op := out.(biz.OperationResult)
	require.Equal(t, operation.OperationStateAwaitingApproval, op.State)
	cancelled, err := f.e.CancelOperation(ctx, f.id, op.OperationID)
	require.NoError(t, err)
	require.Equal(t, operation.OperationStateCancelled, cancelled.(biz.OperationResult).State)
	pending, err := f.e.Approvals.CountPending(ctx)
	require.NoError(t, err)
	approval, err := f.e.Approvals.Get(ctx, op.ApprovalID)
	require.NoError(t, err)
	work, err := f.store.Pending(ctx)
	require.NoError(t, err)
	require.Empty(t, work, "cancelled operation is never reconciled again")
	require.Zero(t, pending)
	require.Equal(t, approvalmodel.StatusFailed, approval.Status)
	repeated, err := f.e.CancelOperation(ctx, f.id, op.OperationID)
	require.NoError(t, err)
	require.Equal(t, cancelled, repeated)
	require.NoError(t, f.store.Sweep(ctx, time.Now().Add(31*24*time.Hour)))
	_, err = f.store.Get(ctx, op.OperationID)
	require.ErrorIs(t, err, errs.ErrNotFound)
	handler := approvalserver.NewHandler(f.e.Approvals)
	handler.SetExternalOperations(f.e)
	router := chi.NewRouter()
	handler.Register(router)
	req := httptest.NewRequest(http.MethodGet, "/v1/approvals/"+op.ApprovalID, nil)
	req = req.WithContext(tenantctx.With(ctx, f.id.Owner))
	result := httptest.NewRecorder()
	router.ServeHTTP(result, req)
	require.Equal(t, http.StatusOK, result.Code, "retained approval must remain viewable after operation cleanup")
}

func TestCancellationRollsBackWhenApprovalInvalidationFails(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	out, err := f.call(t, "manage_monitor_panel", panelInput("cancel-rollback"))
	require.NoError(t, err)
	op := out.(biz.OperationResult)
	var auditBefore int64
	require.NoError(t, f.db.Model(&auditmodel.Log{}).Count(&auditBefore).Error)
	failure := errors.New("approval update unavailable")
	require.NoError(t, f.db.Callback().Update().Before("gorm:update").Register("test:fail_approval", func(tx *gorm.DB) {
		if tx.Statement.Table == "approvals" {
			tx.AddError(failure)
		}
	}))
	_, err = f.e.CancelOperation(ctx, f.id, op.OperationID)
	require.ErrorIs(t, err, failure)
	row, err := f.store.Get(ctx, op.OperationID)
	require.NoError(t, err)
	require.Equal(t, operation.OperationStateAwaitingApproval, row.State)
	require.Nil(t, row.TerminalAt)
	approval, err := f.e.Approvals.Get(ctx, op.ApprovalID)
	require.NoError(t, err)
	require.Equal(t, approvalmodel.StatusPending, approval.Status)
	var auditAfter int64
	require.NoError(t, f.db.Model(&auditmodel.Log{}).Count(&auditAfter).Error)
	require.Equal(t, auditBefore, auditAfter)
}
