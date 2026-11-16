package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	topologybiz "github.com/ongridio/ongrid/internal/cmdb/biz/topology"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_", "?", "_", "#", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:topology-"+name+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newRepo(t *testing.T) *Repo { return NewRepo(newTestDB(t)) }

func TestGroupAndViewRoundTrip(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	group, err := repo.CreateGroup(ctx, "网络")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if group.ID == 0 || group.Order != 1 {
		t.Fatalf("group = %+v", group)
	}
	// NAME LOOKUP FOLDS CASE, like the other named CMDB tables.
	found, err := repo.FindGroupByName(ctx, "网络")
	if err != nil || found == nil || found.ID != group.ID {
		t.Fatalf("find group = %+v, %v", found, err)
	}

	view, err := repo.CreateView(ctx, topologybiz.ViewWrite{
		Name:                 "机房拓扑",
		GroupID:              &group.ID,
		CentralNodeType:      5,
		CentralNodeInstances: "_type:5",
		Path:                 map[string][]model.ID{"1": {2}, "-1": {4}},
		Option:               map[string]any{"layout": "LR"},
	})
	if err != nil {
		t.Fatalf("create view: %v", err)
	}
	if view.CentralNodeType == nil || *view.CentralNodeType != 5 {
		t.Fatalf("view = %+v", view)
	}
	if len(view.Path["1"]) != 1 || view.Path["1"][0] != 2 {
		t.Fatalf("path = %+v", view.Path)
	}

	// A MAP UPDATE BYPASSES THE JSON SERIALIZER; the reload proves the store
	// encoded it rather than handing the driver a map.
	updated, err := repo.UpdateView(ctx, view.ID, topologybiz.ViewWrite{
		Name: "机房拓扑 v2", GroupID: &group.ID, CentralNodeType: 5,
		CentralNodeInstances: "_type:5", Path: map[string][]model.ID{"2": {3}},
	})
	if err != nil {
		t.Fatalf("update view: %v", err)
	}
	if updated.Name != "机房拓扑 v2" || len(updated.Path["2"]) != 1 || updated.Path["2"][0] != 3 {
		t.Fatalf("updated view = %+v path=%+v", updated, updated.Path)
	}
	if len(updated.Path["1"]) != 0 {
		t.Fatalf("the update kept a level it replaced: %+v", updated.Path)
	}

	if err := repo.DeleteGroup(ctx, group.ID); !errors.Is(err, topologybiz.ErrGroupNotEmpty) {
		t.Fatalf("delete non-empty group = %v, want ErrGroupNotEmpty", err)
	}
	if err := repo.DeleteView(ctx, view.ID); err != nil {
		t.Fatalf("delete view: %v", err)
	}
	if err := repo.DeleteGroup(ctx, group.ID); err != nil {
		t.Fatalf("delete empty group: %v", err)
	}
}

func TestOrderingAndAssignment(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	a, _ := repo.CreateGroup(ctx, "A")
	b, _ := repo.CreateGroup(ctx, "B")

	if err := repo.ReorderGroups(ctx, []model.ID{b.ID, a.ID}); err != nil {
		t.Fatalf("reorder groups: %v", err)
	}
	groups, err := repo.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if groups[0].ID != b.ID || groups[1].ID != a.ID {
		t.Fatalf("group order = %+v", groups)
	}

	v1, _ := repo.CreateView(ctx, topologybiz.ViewWrite{Name: "v1", CentralNodeType: 1})
	v2, _ := repo.CreateView(ctx, topologybiz.ViewWrite{Name: "v2", CentralNodeType: 1})
	if v1.Order != 1 || v2.Order != 2 {
		t.Fatalf("view orders = %d,%d", v1.Order, v2.Order)
	}
	if err := repo.ReorderViews(ctx, []model.ID{v2.ID, v1.ID}); err != nil {
		t.Fatalf("reorder views: %v", err)
	}
	views, _ := repo.ListViews(ctx)
	if views[0].ID != v2.ID || views[1].ID != v1.ID {
		t.Fatalf("view order = %+v", views)
	}

	if err := repo.AssignViewsToGroup(ctx, a.ID, []model.ID{v2.ID, v1.ID}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	views, _ = repo.ListViews(ctx)
	for _, view := range views {
		if view.GroupID == nil || *view.GroupID != a.ID {
			t.Fatalf("view %d was not assigned: %+v", view.ID, view.GroupID)
		}
	}
}
