package store

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	biz "github.com/ongridio/ongrid/internal/manager/biz/topology"
	model "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

// TestMigrateSeedsBuiltinRelationTypes asserts the six seed rows land
// on a clean DB and the upsert on second migration is idempotent.
func TestMigrateSeedsBuiltinRelationTypes(t *testing.T) {
	db := newTestDB(t)
	rtRepo := NewRelationTypeRepo(db)
	ctx := context.Background()

	rows, err := rtRepo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 6 {
		t.Fatalf("expected 6 builtin rows, got %d", len(rows))
	}
	wantNames := map[string]bool{
		model.RelMemberOf: true, model.RelDependsOn: true,
		model.RelDeployedOn: true, model.RelReplicatesTo: true,
		model.RelMonitors: true, model.RelRoutesTo: true,
	}
	for _, r := range rows {
		if !r.Builtin {
			t.Errorf("%s: expected builtin=true", r.Name)
		}
		delete(wantNames, r.Name)
	}
	if len(wantNames) != 0 {
		t.Errorf("missing builtin rows: %v", wantNames)
	}

	// Second migration should not error and should not duplicate.
	if err := Migrate(db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	rows, _ = rtRepo.List(ctx)
	if len(rows) != 6 {
		t.Fatalf("after second Migrate, expected 6 rows still, got %d", len(rows))
	}
}

func TestNodeCRUD(t *testing.T) {
	db := newTestDB(t)
	nodes := NewNodeRepo(db)
	ctx := context.Background()

	n := &model.Node{Type: "service", Name: "order-api"}
	if err := nodes.Create(ctx, n); err != nil {
		t.Fatalf("create: %v", err)
	}
	if n.ID == 0 {
		t.Fatalf("expected autoincrement id")
	}
	got, err := nodes.Get(ctx, n.ID)
	if err != nil || got.Name != "order-api" {
		t.Fatalf("get: %v %+v", err, got)
	}

	// Update name + props.
	if err := nodes.Update(ctx, n.ID, "order-svc", `{"owner":"pay"}`); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = nodes.Get(ctx, n.ID)
	if got.Name != "order-svc" || got.PropsJSON != `{"owner":"pay"}` {
		t.Fatalf("update did not persist: %+v", got)
	}

	// List with filter.
	if err := nodes.Create(ctx, &model.Node{Type: "device", Name: "vm-001"}); err != nil {
		t.Fatalf("create device: %v", err)
	}
	rows, err := nodes.List(ctx, biz.NodeListFilter{Type: "service"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 service, got %d", len(rows))
	}

	// Count.
	cnt, _ := nodes.Count(ctx, biz.NodeListFilter{})
	if cnt != 2 {
		t.Fatalf("expected count 2, got %d", cnt)
	}

	// Delete + NotFound.
	if err := nodes.Delete(ctx, n.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := nodes.Get(ctx, n.ID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestRelationCRUD(t *testing.T) {
	db := newTestDB(t)
	nodes := NewNodeRepo(db)
	rels := NewRelationRepo(db)
	ctx := context.Background()

	// Two nodes to connect.
	a := &model.Node{Type: "service", Name: "a"}
	b := &model.Node{Type: "service", Name: "b"}
	_ = nodes.Create(ctx, a)
	_ = nodes.Create(ctx, b)

	r := &model.Relation{SrcID: a.ID, DstID: b.ID, Type: model.RelDependsOn}
	if err := rels.Create(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Listing by src.
	got, err := rels.List(ctx, biz.RelationListFilter{SrcID: a.ID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].DstID != b.ID {
		t.Fatalf("list mismatch: %+v", got)
	}

	// src_or_dst overlaps both endpoints.
	got, _ = rels.List(ctx, biz.RelationListFilter{SrcOrDstID: b.ID})
	if len(got) != 1 {
		t.Fatalf("src_or_dst mismatch: %+v", got)
	}

	// Unique on (src, dst, type) — recreating same triple should error.
	dup := &model.Relation{SrcID: a.ID, DstID: b.ID, Type: model.RelDependsOn}
	if err := rels.Create(ctx, dup); err == nil {
		t.Fatalf("expected unique constraint violation on duplicate triple")
	}

	// Delete.
	if err := rels.Delete(ctx, r.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := rels.Get(ctx, r.ID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	recreated := &model.Relation{SrcID: a.ID, DstID: b.ID, Type: model.RelDependsOn}
	if err := rels.Create(ctx, recreated); err != nil {
		t.Fatalf("recreate relation after soft delete: %v", err)
	}
	if recreated.ID == r.ID {
		t.Fatalf("recreated relation reused soft-deleted id %d", r.ID)
	}
}

func TestCountRelationsByTypeGuard(t *testing.T) {
	db := newTestDB(t)
	nodes := NewNodeRepo(db)
	rels := NewRelationRepo(db)
	rtRepo := NewRelationTypeRepo(db)
	ctx := context.Background()

	a := &model.Node{Type: "service", Name: "a"}
	b := &model.Node{Type: "service", Name: "b"}
	_ = nodes.Create(ctx, a)
	_ = nodes.Create(ctx, b)
	_ = rels.Create(ctx, &model.Relation{SrcID: a.ID, DstID: b.ID, Type: model.RelDependsOn})

	n, err := rtRepo.CountRelationsByType(ctx, model.RelDependsOn)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1, got %d", n)
	}
}
