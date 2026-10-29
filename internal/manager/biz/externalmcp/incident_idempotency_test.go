package externalmcp_test

import (
	"context"
	"testing"
	"time"

	alertbiz "github.com/ongridio/ongrid/internal/manager/biz/alert"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	alertdata "github.com/ongridio/ongrid/internal/manager/data/alert/store"
	operation "github.com/ongridio/ongrid/internal/manager/model/aiops"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	alertservice "github.com/ongridio/ongrid/internal/manager/service/alert"
	service "github.com/ongridio/ongrid/internal/manager/service/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/stretchr/testify/require"
)

func TestSilenceRetryAfterExpiryReturnsOriginalOperation(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.db.AutoMigrate(&alertmodel.Incident{}, &alertmodel.Event{}, &alertmodel.Silence{}))
	repo := alertdata.NewRepo(f.db)
	uc := alertbiz.NewUsecase(repo, f.e.Log)
	alerts := alertservice.New(uc, repo, nil, f.e.Log)
	resources := service.Resources{Alerts: alerts}
	var err error
	f.e.Catalog, err = biz.NewCatalog(append(service.ProjectTools(resources.Tools()), f.e.ControlTools()...))
	require.NoError(t, err)
	row := &alertmodel.Incident{Title: "review fixture", DedupeKey: "review-fixture", Status: "open", Severity: "warning", ScopeType: "global", Rule: "fixture-rule"}
	require.NoError(t, f.db.Create(row).Error)
	detail, err := f.call(t, "get_incident_detail", map[string]any{"incident_ids": []uint64{row.ID}})
	require.NoError(t, err)
	revision := detail.(biz.Result).Data.(map[string]any)["results"].([]any)[0].(map[string]any)["revision"]
	until := time.Now().UTC().Add(5 * time.Second).Truncate(time.Second)
	args := map[string]any{"action": "silence", "incident_id": row.ID, "until": until.Format(time.RFC3339), "reason": "isolated review", "expected_revision": revision, "idempotency_key": "review-repeat-silence"}
	first, err := f.callAndConfirm(t, "update_incident", args)
	require.NoError(t, err)
	op := first.(biz.OperationResult)
	require.Equal(t, operation.OperationStateSucceeded, op.State)
	detail, err = f.call(t, "get_incident_detail", map[string]any{"incident_ids": []uint64{row.ID}})
	require.NoError(t, err)
	revision = detail.(biz.Result).Data.(map[string]any)["results"].([]any)[0].(map[string]any)["revision"]
	pendingArgs := map[string]any{"action": "silence", "incident_id": row.ID, "until": until.Format(time.RFC3339), "reason": "isolated review", "expected_revision": revision, "idempotency_key": "pending-repeat-silence"}
	pending, err := f.call(t, "update_incident", pendingArgs)
	require.NoError(t, err)
	require.Equal(t, operation.OperationStateAwaitingApproval, pending.(biz.OperationResult).State)
	// Simulate retry of a lost response once the requested silence has elapsed.
	<-time.After(time.Until(until.Add(10 * time.Millisecond)))
	retry, retryErr := f.call(t, "update_incident", args)
	stored, err := f.store.Get(context.Background(), op.OperationID)
	require.NoError(t, err)
	var silences int64
	require.NoError(t, f.db.Model(&alertmodel.Silence{}).Count(&silences).Error)
	require.NoError(t, retryErr, "a completed operation must remain retrievable by its original idempotency key")
	require.Equal(t, op, retry)
	require.Equal(t, operation.OperationStateSucceeded, stored.State)
	require.EqualValues(t, 1, silences)
	retry, err = f.call(t, "update_incident", pendingArgs)
	require.NoError(t, err)
	require.Equal(t, pending, retry)
	args["reason"] = "different request"
	_, err = f.call(t, "update_incident", args)
	require.ErrorIs(t, err, biz.ErrIdempotencyConflict)
	args["idempotency_key"] = "invalid-fresh-silence"
	for _, deadline := range []string{until.Format(time.RFC3339), time.Now().Add(31 * 24 * time.Hour).Format(time.RFC3339), "not-a-time"} {
		args["until"] = deadline
		_, err = f.call(t, "update_incident", args)
		require.ErrorIs(t, err, errs.ErrInvalid)
	}
	args["until"] = until.Format(time.RFC3339)
	args["dry_run"] = true
	_, err = f.call(t, "update_incident", args)
	require.ErrorIs(t, err, errs.ErrInvalid)
	var operations int64
	require.NoError(t, f.db.Model(&operation.Operation{}).Count(&operations).Error)
	require.EqualValues(t, 2, operations, "invalid fresh requests must roll back the idempotency record")
	_, err = f.e.Approvals.Approve(context.Background(), f.id.Owner.UserID, pending.(biz.OperationResult).ApprovalID)
	require.NoError(t, err)
	failed, err := f.e.GetOperation(context.Background(), f.id, pending.(biz.OperationResult).OperationID)
	require.NoError(t, err)
	require.Equal(t, operation.OperationStateFailed, failed.(biz.OperationResult).State)
	require.Equal(t, "INVALID_ARGUMENT", failed.(biz.OperationResult).ErrorCode)
	require.NoError(t, f.db.Model(&alertmodel.Silence{}).Count(&silences).Error)
	require.EqualValues(t, 1, silences, "expired confirmation must not create a silence")
	delete(args, "dry_run")
	args["until"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	_, err = f.call(t, "update_incident", args)
	require.NoError(t, err, "rejected requests must not reserve the idempotency key")
}
