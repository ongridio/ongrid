package store

import (
	"context"
	"testing"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
)

// The discovery accept path's bridge to the instance write.
//
// IT RESOLVES THE PEERS AND THE MODEL PAIR'S RULE, then hands one WriteSpec to
// the store so the instance and its edges share a transaction. The fixture's
// resolver has no relation rules, so this wraps it with the one rule the test
// needs.

type relationResolver struct{ fixtureResolver }

func (relationResolver) RelationBetween(_ context.Context, parent, child model.ID) (cibiz.TypeRelationRule, bool, error) {
	if parent == 1 && child == 2 {
		return cibiz.TypeRelationRule{RelationTypeID: 3, Constraint: model.ConstraintMany2Many}, true, nil
	}
	return cibiz.TypeRelationRule{}, false, nil
}

func TestDiscoveryWriterMapsValuesAndBuildsTheEdge(t *testing.T) {
	repo, db := newSearchRepo(t)
	uc := cibiz.NewUsecase(repo, relationResolver{}, nil)
	writer := cibiz.NewDiscoveryWriter(uc)
	ctx := context.Background()

	got, err := writer.WriteDiscovered(ctx, discoverybiz.DiscoveredInstance{
		TypeID: 1,
		Values: map[string]any{"hostname": "disc-01", "cpu": 4},
		Relations: []discoverybiz.DiscoveredRelation{{
			PeerTypeID: 2, PeerAttrID: saHostname, Values: []any{"web-99"},
		}},
		Actor: 7,
	})
	if err != nil {
		t.Fatalf("WriteDiscovered: %v", err)
	}
	if got.CIID == 0 || !got.Created {
		t.Fatalf("result = %+v, want a create", got)
	}

	var ci cimodel.CI
	if err := db.First(&ci, got.CIID).Error; err != nil {
		t.Fatal(err)
	}
	if !ci.IsAutoDiscovery {
		t.Fatal("the accepted instance was not marked as discovered")
	}

	// The switch with hostname web-99 is instance 18 in the fixture.
	var rel cimodel.Relation
	if err := db.Where("first_ci_id = ? AND second_ci_id = ?", got.CIID, 18).First(&rel).Error; err != nil {
		t.Fatalf("the discovered edge was not written: %v", err)
	}
	if rel.RelationTypeID != 3 {
		t.Fatalf("relation type = %d, want 3", rel.RelationTypeID)
	}
	if rel.Source == nil || *rel.Source != string(model.RelationFromAutoDiscovery) {
		t.Fatalf("relation source = %v, want auto-discovery", rel.Source)
	}

	// A PEER VALUE THAT MATCHES NOTHING still writes the instance: the relation
	// is a hint, not a precondition, and refusing the accept would make a
	// missing switch block every host that mentions it.
	second, err := writer.WriteDiscovered(ctx, discoverybiz.DiscoveredInstance{
		TypeID: 1,
		Values: map[string]any{"hostname": "disc-02"},
		Relations: []discoverybiz.DiscoveredRelation{{
			PeerTypeID: 2, PeerAttrID: saHostname, Values: []any{"does-not-exist"},
		}},
		Actor: 7,
	})
	if err != nil {
		t.Fatalf("WriteDiscovered without a peer: %v", err)
	}
	if second.CIID == 0 {
		t.Fatal("the instance was not written when its peer was missing")
	}
	var count int64
	if err := db.Model(&cimodel.Relation{}).Where("first_ci_id = ?", second.CIID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("a relation was invented for a missing peer: %d", count)
	}
}

// A MODEL PAIR WITH NO DECLARED RULE WRITES NO EDGE, and still accepts the
// instance: the graph is the administrator's decision, not a reason to lose a
// discovered machine.
func TestDiscoveryWriterSkipsAnUndeclaredPair(t *testing.T) {
	repo, db := newSearchRepo(t)
	uc := cibiz.NewUsecase(repo, fixtureResolver{}, nil)
	writer := cibiz.NewDiscoveryWriter(uc)

	got, err := writer.WriteDiscovered(context.Background(), discoverybiz.DiscoveredInstance{
		TypeID: 1,
		Values: map[string]any{"hostname": "disc-03"},
		Relations: []discoverybiz.DiscoveredRelation{{
			PeerTypeID: 2, PeerAttrID: saHostname, Values: []any{"web-99"},
		}},
		Actor: 7,
	})
	if err != nil {
		t.Fatalf("WriteDiscovered: %v", err)
	}
	var count int64
	if err := db.Model(&cimodel.Relation{}).Where("first_ci_id = ? OR second_ci_id = ?", got.CIID, got.CIID).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("an edge was written for a model pair with no rule: %d", count)
	}
}
