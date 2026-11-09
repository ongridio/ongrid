package store

import (
	"context"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

// The topology walk's batch reads: both endpoints and both models in one query
// per direction, with deleted endpoints dropped.

func TestGraphChildrenAndParentsCarryBothModels(t *testing.T) {
	repo, _ := newRelationRepo(t, treeEdges)
	ctx := context.Background()

	children, err := repo.GraphChildren(ctx, []model.ID{1})
	if err != nil {
		t.Fatalf("GraphChildren: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("children = %+v, want 1→2 and 1→3", children)
	}
	for _, edge := range children {
		if edge.From != 1 || edge.FromTypeID != hostTypeID || edge.ToTypeID != hostTypeID {
			t.Fatalf("edge = %+v", edge)
		}
	}

	parents, err := repo.GraphParents(ctx, []model.ID{2})
	if err != nil {
		t.Fatalf("GraphParents: %v", err)
	}
	if len(parents) != 1 || parents[0].From != 1 || parents[0].To != 2 {
		t.Fatalf("parents = %+v, want 1→2", parents)
	}
}
