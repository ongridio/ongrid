package store

import (
	"context"
	"testing"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// The accept write: one transaction that creates or updates the instance, marks
// it as discovered, and lays down the edges its fields established.
//
// THE MARKER IS cantor's `c_cis.a` column. A discovered instance is one an
// operator did not type in, and the console uses the flag to say so; the write
// path is the only place it can be set.

func TestWriteCICanMarkADiscoveredInstanceAndRelateIt(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)
	ctx := context.Background()

	switchType := citypemodel.CIType{Name: "switch", Alias: "交换机", UniqueID: attrHostname, Enabled: true}
	if err := db.Create(&switchType).Error; err != nil {
		t.Fatalf("create peer model: %v", err)
	}
	peer, err := repo.WriteCI(ctx, cibiz.WriteSpec{
		TypeRef: switchType.Name, TypeID: switchType.ID,
		UniqueAttrID: attrHostname, UniqueValue: "sw-01",
		Policy: cibiz.ExistReject, Specs: f.specs,
		Values: map[model.ID]any{attrHostname: "sw-01"},
		Audit:  cibiz.AuditInfo{Actor: 7, Origin: "test"},
	})
	if err != nil {
		t.Fatalf("write peer: %v", err)
	}

	in := spec(f, "web-01")
	in.Policy = cibiz.ExistReplace
	in.MarkAutoDiscovery = true
	in.Relations = []cibiz.RelationSpec{{
		PeerCIID: peer.CIID, PeerTypeID: switchType.ID,
		DiscoveredIsFirst: true, DiscoveredTypeID: f.ct.ID,
		RelationTypeID: 3, Constraint: model.ConstraintMany2Many,
	}}
	res, err := repo.WriteCI(ctx, in)
	if err != nil {
		t.Fatalf("WriteCI: %v", err)
	}

	var ci cimodel.CI
	if err := db.First(&ci, res.CIID).Error; err != nil {
		t.Fatal(err)
	}
	if !ci.IsAutoDiscovery {
		t.Fatal("the discovered marker was not written")
	}

	var rel cimodel.Relation
	if err := db.Where("first_ci_id = ? AND second_ci_id = ?", res.CIID, peer.CIID).First(&rel).Error; err != nil {
		t.Fatalf("the relation was not written in the same transaction: %v", err)
	}
	if rel.RelationTypeID != 3 {
		t.Fatalf("relation type = %d, want 3", rel.RelationTypeID)
	}
	if rel.Source == nil || *rel.Source != string(model.RelationFromAutoDiscovery) {
		t.Fatalf("relation source = %v, want auto-discovery", rel.Source)
	}
}

// An UPDATE takes the marker and the edges too: re-accepting a changed sighting
// is the same operation on an instance that already exists.
func TestWriteCIUpdateMarksAndRelates(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)
	ctx := context.Background()

	switchType := citypemodel.CIType{Name: "switch", Alias: "交换机", UniqueID: attrHostname, Enabled: true}
	if err := db.Create(&switchType).Error; err != nil {
		t.Fatalf("create peer model: %v", err)
	}
	peer, err := repo.WriteCI(ctx, cibiz.WriteSpec{
		TypeRef: switchType.Name, TypeID: switchType.ID,
		UniqueAttrID: attrHostname, UniqueValue: "sw-01",
		Policy: cibiz.ExistReject, Specs: f.specs,
		Values: map[model.ID]any{attrHostname: "sw-01"},
		Audit:  cibiz.AuditInfo{Actor: 7, Origin: "test"},
	})
	if err != nil {
		t.Fatalf("write peer: %v", err)
	}
	created, err := repo.WriteCI(ctx, spec(f, "web-02"))
	if err != nil {
		t.Fatalf("write CI: %v", err)
	}

	upd := spec(f, "web-02")
	upd.Policy = cibiz.ExistReplace
	upd.MarkAutoDiscovery = true
	upd.Relations = []cibiz.RelationSpec{{
		PeerCIID: peer.CIID, PeerTypeID: switchType.ID,
		// The discovered instance is the CHILD of this pair, so the edge has
		// to be written the other way round.
		DiscoveredIsFirst: false, DiscoveredTypeID: f.ct.ID,
		RelationTypeID: 4, Constraint: model.ConstraintMany2Many,
	}}
	if _, err := repo.WriteCI(ctx, upd); err != nil {
		t.Fatalf("update CI: %v", err)
	}

	var ci cimodel.CI
	if err := db.First(&ci, created.CIID).Error; err != nil {
		t.Fatal(err)
	}
	if !ci.IsAutoDiscovery {
		t.Fatal("the update did not mark the instance as discovered")
	}
	var rel cimodel.Relation
	if err := db.Where("first_ci_id = ? AND second_ci_id = ?", peer.CIID, created.CIID).First(&rel).Error; err != nil {
		t.Fatalf("the reversed relation was not written: %v", err)
	}
}
