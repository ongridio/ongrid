package externalmcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	topologybiz "github.com/ongridio/ongrid/internal/manager/biz/topology"
	topologydata "github.com/ongridio/ongrid/internal/manager/data/topology/store"
	operation "github.com/ongridio/ongrid/internal/manager/model/aiops"
	approvalmodel "github.com/ongridio/ongrid/internal/manager/model/approval"
	topologymodel "github.com/ongridio/ongrid/internal/manager/model/topology"
	server "github.com/ongridio/ongrid/internal/manager/server/externalmcp"
	topologyserver "github.com/ongridio/ongrid/internal/manager/server/topology"
	"github.com/stretchr/testify/require"
)

func TestExistingTopologyTypeRequiresConfirmation(t *testing.T) {
	t.Setenv("ONGRID_SECRET_KEY", "isolated-review-only-encryption-key")
	f := newFixture(t)
	require.NoError(t, f.db.AutoMigrate(&topologymodel.RelationType{}, &topologymodel.NodeType{}))
	existing := topologymodel.RelationType{Name: "custom-dependency", Direction: "src_to_dst", SemanticsTag: "hard_dep", PropagatesFailure: true}
	require.NoError(t, f.db.Create(&existing).Error)
	uc := topologybiz.NewUsecase(nil, nil, topologydata.NewRelationTypeRepo(f.db), topologydata.NewNodeTypeRepo(f.db), nil)
	router := chi.NewRouter()
	topologyserver.NewHandler(uc).Register(router)
	catalog, err := server.APITools(router, true)
	require.NoError(t, err)
	f.e.Catalog, err = biz.NewCatalog(append(catalog, f.e.ControlTools()...))
	require.NoError(t, err)
	result, err := f.call(t, "call_api_write", map[string]any{
		"action": "post_topology_relation_types", "idempotency_key": "review-overwrite-relation-type", "reason": "review fixture",
		"body": map[string]any{"name": existing.Name, "direction": "dst_to_src", "semantics_tag": "annotation", "propagates_failure": false},
	})
	require.NoError(t, err)
	op := result.(biz.OperationResult)
	require.Equal(t, operation.OperationStateAwaitingApproval, op.State)
	require.NotEmpty(t, op.ApprovalID)
	before, err := uc.GetRelationType(context.Background(), existing.Name)
	require.NoError(t, err)
	require.Equal(t, existing.Direction, before.Direction)
	require.Equal(t, existing.SemanticsTag, before.SemanticsTag)
	require.Equal(t, existing.PropagatesFailure, before.PropagatesFailure)
	_, err = f.e.Approvals.Approve(context.Background(), f.id.Owner.UserID, op.ApprovalID)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.e.Run(ctx) }()
	defer func() { cancel(); require.NoError(t, <-done) }()
	require.Eventually(t, func() bool {
		row, err := f.store.Get(ctx, op.OperationID)
		return err == nil && row.State == operation.OperationStateSucceeded
	}, 4*time.Second, 20*time.Millisecond)
	after, err := uc.GetRelationType(ctx, existing.Name)
	require.NoError(t, err)
	var approvals int64
	require.NoError(t, f.db.Model(&approvalmodel.Approval{}).Count(&approvals).Error)
	require.EqualValues(t, 1, approvals)
	require.Equal(t, "dst_to_src", after.Direction)
	require.Equal(t, "annotation", after.SemanticsTag)
	require.False(t, after.PropagatesFailure)
}
