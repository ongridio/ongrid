package citype

import (
	"context"
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// --- the relation half of the attribute double ---

func (a *attrRepo) ListTypeRelations(_ context.Context, f TypeRelationFilter) ([]citypemodel.CITypeRelation, error) {
	if a.relationErr != nil {
		return nil, a.relationErr
	}
	want := map[model.ID]bool{}
	for _, id := range f.TypeIDs {
		want[id] = true
	}
	out := make([]citypemodel.CITypeRelation, 0, len(a.relations))
	for _, r := range a.relations {
		if len(want) > 0 && !want[r.ParentID] && !want[r.ChildID] {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (a *attrRepo) TypeRelationByID(_ context.Context, id model.ID) (*citypemodel.CITypeRelation, error) {
	for _, r := range a.relations {
		if r.ID == id {
			copied := r
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) TypeRelationByPair(_ context.Context, parentID, childID model.ID) (*citypemodel.CITypeRelation, error) {
	for _, r := range a.relations {
		if r.ParentID == parentID && r.ChildID == childID {
			copied := r
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) SaveTypeRelation(_ context.Context, in TypeRelationSaveSpec) (*citypemodel.CITypeRelation, error) {
	if a.relationErr != nil {
		return nil, a.relationErr
	}
	a.savedRelation = &in
	row := citypemodel.CITypeRelation{
		ParentID: in.ParentID, ChildID: in.ChildID,
		RelationTypeID: in.RelationTypeID,
		ParentAttrIDs:  citypemodel.IDList(in.ParentAttrIDs),
		ChildAttrIDs:   citypemodel.IDList(in.ChildAttrIDs),
	}
	constraint := in.Constraint
	row.Constraint = &constraint
	row.ID = model.ID(len(a.relations) + 700)
	a.relations = append(a.relations, row)
	return &row, nil
}

func (a *attrRepo) DeleteTypeRelation(_ context.Context, in TypeRelationDeleteSpec) error {
	if a.relationErr != nil {
		return a.relationErr
	}
	a.deletedRelation = &in
	return nil
}

func (a *attrRepo) RelationTypesByID(_ context.Context, ids []model.ID) (map[model.ID]citypemodel.RelationType, error) {
	out := map[model.ID]citypemodel.RelationType{}
	for _, id := range ids {
		if rt, ok := a.relationTypes[id]; ok {
			out[id] = rt
		}
	}
	return out, nil
}

// --- fixtures ---

// relRepo builds two models plus a relation type, all of which the relation use
// cases need.
func relRepo() *attrRepo {
	r := newAttrRepo()
	host := newCIType(10, "host", "主机")
	host.UniqueID = 1
	host.ShowID = &[]model.ID{2}[0]
	idc := newCIType(20, "idc", "机房")
	idc.UniqueID = 3
	r.types = []citypemodel.CIType{host, idc}
	r.aliases[10] = "主机"
	r.aliases[20] = "机房"
	r.withAttr(textAttr(1, "hostname"))
	r.withAttr(textAttr(2, "display_name"))
	r.withAttr(textAttr(3, "idc_name"))
	r.relationTypes[5] = citypemodel.RelationType{Name: "runs_on", Color: "#abc"}
	r.relationTypes[5] = func() citypemodel.RelationType {
		rt := citypemodel.RelationType{Name: "runs_on", Color: "#abc"}
		rt.ID = 5
		return rt
	}()
	return r
}

func relInput() TypeRelationInput {
	return TypeRelationInput{
		ParentRef: "10", ChildRef: "20", RelationTypeID: 5, Actor: 7,
	}
}

// --- save ---

func TestSaveTypeRelationValidatesItsInputs(t *testing.T) {
	ctx := context.Background()

	t.Run("self loop", func(t *testing.T) {
		in := relInput()
		in.ChildRef = "10"
		_, err := attrUsecase(relRepo()).SaveTypeRelation(ctx, in)
		if !errors.Is(err, ErrRelationSelfLoop) {
			t.Fatalf("error = %v, want ErrRelationSelfLoop", err)
		}
	})

	t.Run("unknown relation type", func(t *testing.T) {
		in := relInput()
		in.RelationTypeID = 999
		_, err := attrUsecase(relRepo()).SaveTypeRelation(ctx, in)
		if !errors.Is(err, ErrRelationTypeNotFound) {
			t.Fatalf("error = %v, want ErrRelationTypeNotFound", err)
		}
	})

	t.Run("unknown constraint", func(t *testing.T) {
		in := relInput()
		in.Constraint = "9"
		_, err := attrUsecase(relRepo()).SaveTypeRelation(ctx, in)
		if !errors.Is(err, ErrRelationConstraintInvalid) {
			t.Fatalf("error = %v, want ErrRelationConstraintInvalid", err)
		}
	})

	t.Run("missing child model", func(t *testing.T) {
		in := relInput()
		in.ChildRef = "nope"
		_, err := attrUsecase(relRepo()).SaveTypeRelation(ctx, in)
		if !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("error = %v, want NotFound", err)
		}
	})
}

// An omitted cardinality takes cantor's default rather than being refused: the
// console's "add a relation" form does not ask for it.
func TestSaveTypeRelationDefaultsTheConstraint(t *testing.T) {
	repo := relRepo()
	if _, err := attrUsecase(repo).SaveTypeRelation(context.Background(), relInput()); err != nil {
		t.Fatalf("SaveTypeRelation() error = %v", err)
	}
	if repo.savedRelation.Constraint != string(model.ConstraintOne2Many) {
		t.Fatalf("constraint = %q, want the one-to-many default", repo.savedRelation.Constraint)
	}
}

func TestSaveTypeRelationReturnsTheRenderedEdge(t *testing.T) {
	repo := relRepo()
	got, err := attrUsecase(repo).SaveTypeRelation(context.Background(), relInput())
	if err != nil {
		t.Fatalf("SaveTypeRelation() error = %v", err)
	}
	if got.Parent.Type.Type.Name != "host" || got.Child.Type.Type.Name != "idc" {
		t.Fatalf("edge = %+v", got)
	}
	if got.Parent.RelationType.Name != "runs_on" || got.Parent.RelationType.Color != "#abc" {
		t.Fatalf("relation type = %+v, want the dictionary entry resolved", got.Parent.RelationType)
	}
	if got.Parent.Constraint != string(model.ConstraintOne2Many) {
		t.Fatalf("constraint = %q", got.Parent.Constraint)
	}
}

// The cycle refusal surfaces as a conflict, because the request is well formed
// and the graph's current state is what refuses it.
func TestRelationCycleMapsToConflict(t *testing.T) {
	repo := relRepo()
	repo.relationErr = &RelationCycleError{Path: []model.ID{10, 20, 10}}

	_, err := attrUsecase(repo).SaveTypeRelation(context.Background(), relInput())
	if !errors.Is(err, ErrRelationCycle) {
		t.Fatalf("error = %v, want ErrRelationCycle", err)
	}
	if errs.HTTPStatus(err) != 409 {
		t.Fatalf("status = %d, want 409 — the graph refuses this, not the request", errs.HTTPStatus(err))
	}
}

// --- reads ---

func TestListRelatedTypesFiltersBySide(t *testing.T) {
	repo := relRepo()
	// host -> idc, and idc -> host would be a cycle, so use a third model.
	third := newCIType(30, "switch", "交换机")
	third.UniqueID = 1
	repo.types = append(repo.types, third)
	repo.aliases[30] = "交换机"

	rt := repo.relationTypes[5]
	rowA := citypemodel.CITypeRelation{ParentID: 10, ChildID: 20, RelationTypeID: rt.ID}
	rowA.ID = 700
	rowB := citypemodel.CITypeRelation{ParentID: 10, ChildID: 30, RelationTypeID: rt.ID}
	rowB.ID = 701
	repo.relations = []citypemodel.CITypeRelation{rowA, rowB}

	uc := attrUsecase(repo)
	ctx := context.Background()

	children, err := uc.ListRelatedTypes(ctx, "10", true)
	if err != nil {
		t.Fatalf("ListRelatedTypes(children) error = %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("children = %d, want both", len(children))
	}
	if children[0].CTRID != 700 || children[1].CTRID != 701 {
		t.Fatalf("ctr ids = %d/%d, want the relation rows' own ids", children[0].CTRID, children[1].CTRID)
	}

	parents, err := uc.ListRelatedTypes(ctx, "20", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 1 || parents[0].Type.Type.ID != 10 {
		t.Fatalf("parents = %+v, want host", parents)
	}
	// host itself has no parents.
	none, err := uc.ListRelatedTypes(ctx, "10", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("parents = %+v, want none", none)
	}
}

// The attribute set carries the model's effective attributes, one shape for
// every endpoint.
func TestRelatedTypeCarriesTheEffectiveAttributeSet(t *testing.T) {
	repo := relRepo()
	// host inherits from a parent, whose attribute must show up as inherited.
	parentType := newCIType(40, "base", "基础")
	parentType.UniqueID = 1
	repo.types = append(repo.types, parentType)
	repo.aliases[40] = "基础"
	repo.ancestors = []model.ID{40}
	repo.bindings = []citypemodel.CITypeAttribute{
		bindingFixture(40, 1, 0, false),
		bindingFixture(10, 2, 1, true),
	}
	row := citypemodel.CITypeRelation{ParentID: 10, ChildID: 20, RelationTypeID: 5}
	row.ID = 700
	repo.relations = []citypemodel.CITypeRelation{row}

	// The PARENT side of idc's relations is host, which is the model whose
	// attribute set the bindings above describe.
	got, err := attrUsecase(repo).ListRelatedTypes(context.Background(), "20", false)
	if err != nil {
		t.Fatalf("ListRelatedTypes() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("related = %d", len(got))
	}
	if got[0].Type.Type.ID != 10 {
		t.Fatalf("related model = %d, want host", got[0].Type.Type.ID)
	}
	attrs := got[0].Attributes
	if len(attrs) != 2 {
		t.Fatalf("attributes = %+v, want both the inherited and the own one", attrs)
	}
	if !attrs[0].Inherited || attrs[0].InheritedFrom != "基础" {
		t.Fatalf("first attribute = %+v, want it labelled inherited", attrs[0])
	}
	if attrs[1].Inherited {
		t.Fatalf("second attribute = %+v, want the model's own", attrs[1])
	}
}

// show_key falls back to the unique attribute, so the console never has to
// special-case a model with no display attribute.
func TestRelatedTypeShowKeyFallsBackToTheUniqueAttribute(t *testing.T) {
	repo := relRepo()
	row := citypemodel.CITypeRelation{ParentID: 10, ChildID: 20, RelationTypeID: 5}
	row.ID = 700
	repo.relations = []citypemodel.CITypeRelation{row}

	got, err := attrUsecase(repo).ListRelatedTypes(context.Background(), "10", true)
	if err != nil {
		t.Fatal(err)
	}
	// host has show_id 2, so it reports display_name; idc has none, so it
	// reports its unique attribute.
	byID := map[model.ID]string{}
	for _, r := range got {
		byID[r.Type.Type.ID] = r.ShowKey
	}
	if byID[10] != "" {
		t.Fatalf("host show_key = %q, want empty here: the far end is idc", byID[10])
	}
	if got[0].ShowKey != "idc_name" {
		t.Fatalf("idc show_key = %q, want the unique attribute's name", got[0].ShowKey)
	}
}

func TestListTypeRelationsRendersBothEnds(t *testing.T) {
	repo := relRepo()
	row := citypemodel.CITypeRelation{ParentID: 10, ChildID: 20, RelationTypeID: 5}
	row.ID = 700
	constraint := string(model.ConstraintOne2One)
	row.Constraint = &constraint
	row.ParentAttrIDs = citypemodel.IDList{1}
	repo.relations = []citypemodel.CITypeRelation{row}

	rows, err := attrUsecase(repo).ListTypeRelations(context.Background(), TypeRelationFilter{})
	if err != nil {
		t.Fatalf("ListTypeRelations() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("relations = %d", len(rows))
	}
	if rows[0].Parent.Type.Type.ID != 10 || rows[0].Child.Type.Type.ID != 20 {
		t.Fatalf("ends = %+v", rows[0])
	}
	// Both ends carry the relation's configuration, because the console renders
	// an edge as one object with two sides.
	if rows[0].Child.Constraint != constraint {
		t.Fatalf("child constraint = %q, want %q", rows[0].Child.Constraint, constraint)
	}
	if len(rows[0].Child.ParentAttrIDs) != 1 {
		t.Fatalf("child attr ids = %v, want the edge's pair on both sides", rows[0].Child.ParentAttrIDs)
	}
}

// A relation whose model was deleted still renders, so the console can show and
// remove it rather than erroring on a page it cannot load.
func TestRelationWithADeletedModelStillRenders(t *testing.T) {
	repo := relRepo()
	row := citypemodel.CITypeRelation{ParentID: 10, ChildID: 999, RelationTypeID: 5}
	row.ID = 700
	repo.relations = []citypemodel.CITypeRelation{row}

	rows, err := attrUsecase(repo).ListTypeRelations(context.Background(), TypeRelationFilter{})
	if err != nil {
		t.Fatalf("ListTypeRelations() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("relations = %d, want the dangling edge rendered", len(rows))
	}
	if rows[0].Child.Type.Type.ID != 999 {
		t.Fatalf("child = %+v", rows[0].Child.Type)
	}
}

func TestFindTypePathsValidatesItsInputs(t *testing.T) {
	repo := relRepo()
	uc := attrUsecase(repo)
	ctx := context.Background()

	if _, err := uc.FindTypePaths(ctx, "10", nil); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("empty targets error = %v, want ErrInvalid", err)
	}
	if _, err := uc.FindTypePaths(ctx, "nope", []string{"20"}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("bad source error = %v, want NotFound", err)
	}
	if _, err := uc.FindTypePaths(ctx, "10", []string{"nope"}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("bad target error = %v, want NotFound", err)
	}
}

func TestListTypeDescendantsWalksTheGraph(t *testing.T) {
	repo := relRepo()
	third := newCIType(30, "switch", "交换机")
	third.UniqueID = 1
	repo.types = append(repo.types, third)
	a := citypemodel.CITypeRelation{ParentID: 10, ChildID: 20, RelationTypeID: 5}
	a.ID = 700
	b := citypemodel.CITypeRelation{ParentID: 20, ChildID: 30, RelationTypeID: 5}
	b.ID = 701
	repo.relations = []citypemodel.CITypeRelation{a, b}

	levels, err := attrUsecase(repo).ListTypeDescendants(context.Background(), "10")
	if err != nil {
		t.Fatalf("ListTypeDescendants() error = %v", err)
	}
	if len(levels) != 2 {
		t.Fatalf("levels = %+v, want two", levels)
	}
	if levels[0].Types[0] != 20 || levels[1].Types[0] != 30 {
		t.Fatalf("levels = %+v, want 20 then 30", levels)
	}
}

func TestTypesByIDPreservesTheRequestedOrder(t *testing.T) {
	repo := relRepo()
	got, err := attrUsecase(repo).TypesByID(context.Background(), []model.ID{20, 10})
	if err != nil {
		t.Fatalf("TypesByID() error = %v", err)
	}
	if len(got) != 2 || got[0].Type.ID != 20 || got[1].Type.ID != 10 {
		t.Fatalf("order = %+v, want the requested order so the console does not flicker", got)
	}
}

// --- delete ---

// cantor dereferenced a missing pair's row without checking, so deleting a pair
// with no relation answered 500 with a Python attribute error.
func TestDeleteTypeRelationByPairMissingIsNotFound(t *testing.T) {
	repo := relRepo()
	err := attrUsecase(repo).DeleteTypeRelationByPair(context.Background(), "10", "20", 7)
	if !errors.Is(err, ErrRelationNotFound) {
		t.Fatalf("error = %v, want ErrRelationNotFound", err)
	}
}

func TestDeleteTypeRelationByIDMissingIsNotFound(t *testing.T) {
	repo := relRepo()
	err := attrUsecase(repo).DeleteTypeRelation(context.Background(), 999, 7)
	if !errors.Is(err, ErrRelationNotFound) {
		t.Fatalf("error = %v, want ErrRelationNotFound", err)
	}
}

func TestDeleteTypeRelationByPairResolvesTheRow(t *testing.T) {
	repo := relRepo()
	row := citypemodel.CITypeRelation{ParentID: 10, ChildID: 20, RelationTypeID: 5}
	row.ID = 700
	repo.relations = []citypemodel.CITypeRelation{row}

	if err := attrUsecase(repo).DeleteTypeRelationByPair(context.Background(), "10", "20", 7); err != nil {
		t.Fatalf("DeleteTypeRelationByPair() error = %v", err)
	}
	if repo.deletedRelation == nil || repo.deletedRelation.ID != 700 {
		t.Fatalf("deleted = %+v, want the resolved row", repo.deletedRelation)
	}
	if repo.deletedRelation.Actor != 7 {
		t.Fatal("the caller must be recorded on the history row")
	}
}

func TestRelationUsecaseWithoutRepoReportsNotWired(t *testing.T) {
	uc := NewUsecase(nil, nil)
	ctx := context.Background()
	if _, err := uc.ListTypeRelations(ctx, TypeRelationFilter{}); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("ListTypeRelations() error = %v", err)
	}
	if _, err := uc.SaveTypeRelation(ctx, relInput()); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("SaveTypeRelation() error = %v", err)
	}
	if _, err := uc.ListTypeDescendants(ctx, "10"); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("ListTypeDescendants() error = %v", err)
	}
}
