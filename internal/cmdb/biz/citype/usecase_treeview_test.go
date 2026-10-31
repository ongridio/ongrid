package citype

import (
	"context"
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// --- the service-tree half of the attribute double ---

func (a *attrRepo) ServiceTrees(_ context.Context, visibleTo model.ID, includeAll bool) ([]citypemodel.PreferenceRelationView, error) {
	if a.treeErr != nil {
		return nil, a.treeErr
	}
	var out []citypemodel.PreferenceRelationView
	for _, v := range a.serviceTrees {
		if includeAll || v.UID == visibleTo || v.IsPublic {
			out = append(out, v)
		}
	}
	return out, nil
}

func (a *attrRepo) ServiceTreeByID(_ context.Context, id model.ID) (*citypemodel.PreferenceRelationView, error) {
	for _, v := range a.serviceTrees {
		if v.ID == id {
			copied := v
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) ServiceTreeByName(_ context.Context, name string) (*citypemodel.PreferenceRelationView, error) {
	for _, v := range a.serviceTrees {
		if v.Name == name {
			copied := v
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) SaveServiceTree(_ context.Context, in ServiceTreeSaveSpec) (*citypemodel.PreferenceRelationView, error) {
	if a.treeErr != nil {
		return nil, a.treeErr
	}
	a.savedTree = &in
	row := citypemodel.PreferenceRelationView{
		UID: in.UID, Name: in.Name, CRIDs: citypemodel.TypePairList(in.Pairs),
		IsPublic: in.IsPublic, Option: in.Option,
	}
	if in.ID > 0 {
		row.ID = in.ID
	} else {
		row.ID = model.ID(len(a.serviceTrees) + 400)
	}
	a.serviceTrees = append(a.serviceTrees, row)
	return &row, nil
}

func (a *attrRepo) DeleteServiceTree(_ context.Context, in ServiceTreeDeleteSpec) error {
	if a.treeErr != nil {
		return a.treeErr
	}
	a.deletedTree = &in
	var kept []citypemodel.PreferenceRelationView
	for _, v := range a.serviceTrees {
		if v.ID != in.ID {
			kept = append(kept, v)
		}
	}
	a.serviceTrees = kept
	return nil
}

func (a *attrRepo) ServiceTreesReferencing(_ context.Context, typeID model.ID) ([]citypemodel.PreferenceRelationView, error) {
	var out []citypemodel.PreferenceRelationView
	for _, v := range a.serviceTrees {
		for _, p := range v.CRIDs {
			if p.ParentID == typeID || p.ChildID == typeID {
				out = append(out, v)
				break
			}
		}
	}
	return out, nil
}

func (a *attrRepo) SearchOptions(_ context.Context, f SearchOptionFilter) ([]citypemodel.PreferenceSearchOption, error) {
	if a.searchErr != nil {
		return nil, a.searchErr
	}
	var out []citypemodel.PreferenceSearchOption
	for _, o := range a.searchOptions {
		if o.UID == nil || *o.UID != f.UID {
			continue
		}
		if f.Name != "" && (o.Name == nil || *o.Name != f.Name) {
			continue
		}
		if f.PRVID > 0 && (o.PRVID == nil || *o.PRVID != f.PRVID) {
			continue
		}
		if f.PTVID > 0 && (o.PTVID == nil || *o.PTVID != f.PTVID) {
			continue
		}
		if f.TypeID > 0 && (o.TypeID == nil || *o.TypeID != f.TypeID) {
			continue
		}
		out = append(out, o)
	}
	return out, nil
}

func (a *attrRepo) SearchOptionByID(_ context.Context, id model.ID) (*citypemodel.PreferenceSearchOption, error) {
	for _, o := range a.searchOptions {
		if o.ID == id {
			copied := o
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) CreateSearchOption(_ context.Context, in SearchOptionCreateSpec) (*citypemodel.PreferenceSearchOption, error) {
	if a.searchErr != nil {
		return nil, a.searchErr
	}
	a.createdSearch = &in
	uid, name := in.UID, in.Name
	row := citypemodel.PreferenceSearchOption{
		UID: &uid, Name: &name, Option: in.Option,
		PRVID: in.PRVID, PTVID: in.PTVID, TypeID: in.TypeID,
	}
	row.ID = model.ID(len(a.searchOptions) + 600)
	a.searchOptions = append(a.searchOptions, row)
	return &row, nil
}

func (a *attrRepo) UpdateSearchOption(_ context.Context, in SearchOptionUpdateSpec) error {
	if a.searchErr != nil {
		return a.searchErr
	}
	a.updatedSearch = &in
	for i := range a.searchOptions {
		if a.searchOptions[i].ID == in.ID {
			name := in.Name
			a.searchOptions[i].Name = &name
			a.searchOptions[i].Option = in.Option
		}
	}
	return nil
}

func (a *attrRepo) DeleteSearchOption(_ context.Context, id model.ID) error {
	if a.searchErr != nil {
		return a.searchErr
	}
	a.deletedSearch = &id
	return nil
}

// --- fixtures ---

// treeRepo seeds a chain of models 1 -> 2 -> 3 with the relations between them,
// plus the model relation rows the graph is built from.
func treeRepo() *attrRepo {
	r, _ := prefRepo()
	for _, n := range []struct {
		id   int
		name string
	}{{20, "idc"}, {30, "rack"}, {40, "switch"}} {
		ct := newCIType(model.ID(n.id), n.name, n.name)
		ct.UniqueID = 1
		r.types = append(r.types, ct)
		r.aliases[model.ID(n.id)] = n.name
	}
	r.relations = []citypemodel.CITypeRelation{
		relationRow(20, 30, string(model.ConstraintOne2Many)),
		relationRow(30, 40, string(model.ConstraintMany2Many)),
	}
	return r
}

func relationRow(parent, child int, constraint string) citypemodel.CITypeRelation {
	row := citypemodel.CITypeRelation{
		ParentID: model.ID(parent), ChildID: model.ID(child), RelationTypeID: 5,
	}
	c := constraint
	row.Constraint = &c
	row.ID = model.ID(parent*100 + child)
	return row
}

func treePairs(ns ...int) []citypemodel.TypePair { return chain(ns...) }

// --- the can_edit rule ---

// THE RULE, reproduced from cantor because the console depends on it: a
// relation cannot be added when it would conflict with a service tree that
// already contains a many-to-many step.
func TestCanEditRelationRefusesAPairAlreadyInAManyToManyTree(t *testing.T) {
	repo := treeRepo()
	view := citypemodel.PreferenceRelationView{UID: 7, Name: "t", CRIDs: citypemodel.TypePairList(treePairs(20, 30, 40))}
	view.ID = 400
	repo.serviceTrees = []citypemodel.PreferenceRelationView{view}

	got, err := attrUsecase(repo).CanEditRelation(context.Background(), 30, 40)
	if err != nil {
		t.Fatalf("CanEditRelation() error = %v", err)
	}
	if got {
		t.Fatal("CanEditRelation() = true, want the many-to-many pair refused")
	}

	// The other end of the same tree is a plain one-to-many step, so it is
	// still editable: the rule is about many-to-many, not about being in a tree.
	ok, err := attrUsecase(repo).CanEditRelation(context.Background(), 20, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("CanEditRelation() = false for a one-to-many pair, want true")
	}
}

// The second half of the rule: nothing may hang off the BOTTOM of a tree that
// contains a many-to-many step, because the tree has no single instance to hang
// it from.
func TestCanEditRelationRefusesAppendingToAManyToManyTree(t *testing.T) {
	repo := treeRepo()
	// 40 is the child of the last resolvable pair, and 45 is a model related to
	// it that is not part of the definition.
	fifth := newCIType(45, "port", "端口")
	fifth.UniqueID = 1
	repo.types = append(repo.types, fifth)
	repo.relations = append(repo.relations, relationRow(40, 45, string(model.ConstraintOne2Many)))

	view := citypemodel.PreferenceRelationView{UID: 7, Name: "t", CRIDs: citypemodel.TypePairList(treePairs(20, 30, 40))}
	view.ID = 400
	repo.serviceTrees = []citypemodel.PreferenceRelationView{view}

	got, err := attrUsecase(repo).CanEditRelation(context.Background(), 40, 45)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("CanEditRelation() = true, want appending to a many-to-many tree refused")
	}
}

func TestCanEditRelationAllowsUnrelatedPairs(t *testing.T) {
	repo := treeRepo()
	view := citypemodel.PreferenceRelationView{UID: 7, Name: "t", CRIDs: citypemodel.TypePairList(treePairs(20, 30, 40))}
	view.ID = 400
	repo.serviceTrees = []citypemodel.PreferenceRelationView{view}

	got, err := attrUsecase(repo).CanEditRelation(context.Background(), 30, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("CanEditRelation() = false for a pair no tree mentions")
	}
}

// A definition naming a model that is gone must not break the check. cantor
// indexed each stored pair as a mapping and looked every model up without a
// guard, so a stale definition raised through this read too.
func TestCanEditRelationToleratesAStaleDefinition(t *testing.T) {
	repo := treeRepo()
	view := citypemodel.PreferenceRelationView{
		UID: 7, Name: "stale",
		CRIDs: citypemodel.TypePairList{{ParentID: 9999, ChildID: 9998}},
	}
	view.ID = 400
	repo.serviceTrees = []citypemodel.PreferenceRelationView{view}

	got, err := attrUsecase(repo).CanEditRelation(context.Background(), 20, 30)
	if err != nil {
		t.Fatalf("CanEditRelation() error = %v, want the stale definition skipped", err)
	}
	if !got {
		t.Fatal("CanEditRelation() = false, want true: the stale definition constrains nothing")
	}
}

// --- service tree validation ---

// cantor validated nothing, so a definition naming a model nobody has was
// stored — and its reader then crashed on it, permanently, for every user.
func TestSaveServiceTreeValidatesThePairs(t *testing.T) {
	repo := treeRepo()
	uc := attrUsecase(repo)
	ctx := context.Background()

	// A model that does not exist.
	_, err := uc.SaveServiceTree(ctx, 7, ServiceTreeInput{
		Name: "t", Pairs: []citypemodel.TypePair{{ParentID: 20, ChildID: 9999}},
	})
	if !errors.Is(err, ErrServiceTreeUnknownModel) {
		t.Fatalf("unknown model error = %v", err)
	}

	// A pair that is not a model relation.
	_, err = uc.SaveServiceTree(ctx, 7, ServiceTreeInput{
		Name: "t", Pairs: []citypemodel.TypePair{{ParentID: 20, ChildID: 40}},
	})
	if !errors.Is(err, ErrServiceTreeUnknownModel) {
		t.Fatalf("non-relation pair error = %v, want it refused", err)
	}

	// A model related to itself.
	_, err = uc.SaveServiceTree(ctx, 7, ServiceTreeInput{
		Name: "t", Pairs: []citypemodel.TypePair{{ParentID: 20, ChildID: 20}},
	})
	if !errors.Is(err, ErrServiceTreeCycle) {
		t.Fatalf("self pair error = %v", err)
	}

	// And the valid one goes through.
	if _, err := uc.SaveServiceTree(ctx, 7, ServiceTreeInput{
		Name: "t", Pairs: treePairs(20, 30),
	}); err != nil {
		t.Fatalf("valid definition error = %v", err)
	}
}

func TestSaveServiceTreeRequiresAName(t *testing.T) {
	repo := treeRepo()
	_, err := attrUsecase(repo).SaveServiceTree(context.Background(), 7, ServiceTreeInput{
		Pairs: treePairs(20, 30),
	})
	if !errors.Is(err, ErrServiceTreeNameRequired) {
		t.Fatalf("error = %v", err)
	}
}

// A public definition is readable by all and writable by its owner.
func TestSaveServiceTreeRefusesAnotherUsersDefinition(t *testing.T) {
	repo := treeRepo()
	view := citypemodel.PreferenceRelationView{UID: 8, Name: "theirs", CRIDs: citypemodel.TypePairList(treePairs(20, 30))}
	view.ID = 400
	repo.serviceTrees = []citypemodel.PreferenceRelationView{view}

	_, err := attrUsecase(repo).SaveServiceTree(context.Background(), 7, ServiceTreeInput{
		ID: 400, Name: "theirs", Pairs: treePairs(20, 30),
	})
	if !errors.Is(err, ErrServiceTreeNotOwner) {
		t.Fatalf("error = %v, want ErrServiceTreeNotOwner", err)
	}
}

// cantor's delete took a NAME, ignored the id in the URL, and applied no user
// filter — so one user could delete every definition sharing a name.
func TestDeleteServiceTreeIsByIDAndOwned(t *testing.T) {
	repo := treeRepo()
	mine := citypemodel.PreferenceRelationView{UID: 7, Name: "shared", CRIDs: citypemodel.TypePairList(treePairs(20, 30))}
	mine.ID = 400
	theirs := citypemodel.PreferenceRelationView{UID: 8, Name: "shared", CRIDs: citypemodel.TypePairList(treePairs(20, 30))}
	theirs.ID = 401
	repo.serviceTrees = []citypemodel.PreferenceRelationView{mine, theirs}

	uc := attrUsecase(repo)
	ctx := context.Background()

	// Deleting the OTHER user's row by id is refused.
	if err := uc.DeleteServiceTree(ctx, 7, 401); !errors.Is(err, ErrServiceTreeNotOwner) {
		t.Fatalf("cross-user delete error = %v, want it refused", err)
	}
	if len(repo.serviceTrees) != 2 {
		t.Fatal("the other user's definition was removed")
	}

	// Deleting one's own leaves the other alone.
	if err := uc.DeleteServiceTree(ctx, 7, 400); err != nil {
		t.Fatalf("DeleteServiceTree() error = %v", err)
	}
	if len(repo.serviceTrees) != 1 || repo.serviceTrees[0].ID != 401 {
		t.Fatalf("remaining = %+v, want only the other user's", repo.serviceTrees)
	}
}

// A definition naming a model that is gone is REPORTED without its rendering,
// rather than taking the list down with it.
func TestListServiceTreesSkipsTheRenderingOfAStaleDefinition(t *testing.T) {
	repo := treeRepo()
	good := citypemodel.PreferenceRelationView{UID: 7, Name: "good", CRIDs: citypemodel.TypePairList(treePairs(20, 30))}
	good.ID = 400
	stale := citypemodel.PreferenceRelationView{
		UID: 7, Name: "stale", CRIDs: citypemodel.TypePairList{{ParentID: 20, ChildID: 9999}},
	}
	stale.ID = 401
	repo.serviceTrees = []citypemodel.PreferenceRelationView{good, stale}

	got, err := attrUsecase(repo).ListServiceTrees(context.Background(), 7, true)
	if err != nil {
		t.Fatalf("ListServiceTrees() error = %v, want the stale row tolerated", err)
	}
	if len(got) != 2 {
		t.Fatalf("definitions = %d, want both listed", len(got))
	}
	if got[0].Tree == nil || len(got[0].Tree.Order) == 0 {
		t.Fatalf("good definition = %+v, want it rendered", got[0].Tree)
	}
	if got[1].Tree == nil || len(got[1].Tree.Order) != 0 {
		t.Fatalf("stale definition = %+v, want an empty rendering rather than an error", got[1].Tree)
	}
	// The stored pairs are still returned, so the console can show and fix it.
	if len(got[1].Pairs) != 1 {
		t.Fatalf("pairs = %+v, want the stored chain returned for editing", got[1].Pairs)
	}
}

// Visibility: the resource menu must not be handed the shape of a page the
// caller cannot open.
func TestListServiceTreesRespectsVisibility(t *testing.T) {
	repo := treeRepo()
	mine := citypemodel.PreferenceRelationView{UID: 7, Name: "mine", CRIDs: citypemodel.TypePairList(treePairs(20, 30))}
	mine.ID = 400
	theirs := citypemodel.PreferenceRelationView{UID: 8, Name: "theirs", CRIDs: citypemodel.TypePairList(treePairs(20, 30))}
	theirs.ID = 401
	public := citypemodel.PreferenceRelationView{
		UID: 9, Name: "public", IsPublic: true, CRIDs: citypemodel.TypePairList(treePairs(20, 30)),
	}
	public.ID = 402
	repo.serviceTrees = []citypemodel.PreferenceRelationView{mine, theirs, public}

	uc := attrUsecase(repo)
	ctx := context.Background()

	visible, err := uc.ListServiceTrees(ctx, 7, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 {
		t.Fatalf("visible = %d, want the caller's own and the public one", len(visible))
	}

	all, err := uc.ListServiceTrees(ctx, 7, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all = %d, want everything for the management page", len(all))
	}
}

// --- saved searches ---

func TestCreateSearchOptionRejectsADuplicate(t *testing.T) {
	repo := treeRepo()
	uc := attrUsecase(repo)
	ctx := context.Background()

	if _, err := uc.CreateSearchOption(ctx, 7, SearchOptionInput{
		Name: "my host query", Option: map[string]any{"q": "hostname:x"},
	}); err != nil {
		t.Fatalf("first CreateSearchOption() error = %v", err)
	}
	if _, err := uc.CreateSearchOption(ctx, 7, SearchOptionInput{
		Name: "my host query", Option: map[string]any{"q": "hostname:y"},
	}); !errors.Is(err, ErrSearchOptionExists) {
		t.Fatalf("duplicate error = %v, want ErrSearchOptionExists", err)
	}
}

// The three reserved names are the console's own lists and may repeat.
func TestCreateSearchOptionAllowsRepeatedReservedNames(t *testing.T) {
	repo := treeRepo()
	uc := attrUsecase(repo)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := uc.CreateSearchOption(ctx, 7, SearchOptionInput{
			Name: citypemodel.SearchOptionRecent, Option: map[string]any{"q": "x"},
		}); err != nil {
			t.Fatalf("CreateSearchOption(%s) error = %v, want the rolling list writable repeatedly",
				citypemodel.SearchOptionRecent, err)
		}
	}
}

// cantor's update crashed on EVERY call: it looked the duplicate up without
// asking for a single row, got a list back, and read `.id` off the list.
func TestUpdateSearchOptionWorks(t *testing.T) {
	repo := treeRepo()
	uc := attrUsecase(repo)
	ctx := context.Background()

	created, err := uc.CreateSearchOption(ctx, 7, SearchOptionInput{
		Name: "first", Option: map[string]any{"q": "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := uc.UpdateSearchOption(ctx, 7, created.ID, SearchOptionInput{
		Name: "second", Option: map[string]any{"q": "b"},
	})
	if err != nil {
		t.Fatalf("UpdateSearchOption() error = %v, want it to work at all", err)
	}
	if updated.Name != "second" {
		t.Fatalf("name = %q", updated.Name)
	}
}

func TestUpdateSearchOptionRefusesAnotherUsersSearch(t *testing.T) {
	repo := treeRepo()
	uid := model.ID(8)
	row := citypemodel.PreferenceSearchOption{UID: &uid, Name: strptr("theirs")}
	row.ID = 600
	repo.searchOptions = []citypemodel.PreferenceSearchOption{row}

	_, err := attrUsecase(repo).UpdateSearchOption(context.Background(), 7, 600, SearchOptionInput{Name: "mine"})
	if !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("error = %v, want forbidden rather than a 400 about the request", err)
	}
}

func TestDeleteSearchOptionRefusesAnotherUsersSearch(t *testing.T) {
	repo := treeRepo()
	uid := model.ID(8)
	row := citypemodel.PreferenceSearchOption{UID: &uid, Name: strptr("theirs")}
	row.ID = 600
	repo.searchOptions = []citypemodel.PreferenceSearchOption{row}

	if err := attrUsecase(repo).DeleteSearchOption(context.Background(), 7, 600); !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("error = %v", err)
	}
	if repo.deletedSearch != nil {
		t.Fatal("the other user's search was deleted")
	}
}

func TestSearchOptionNameIsRequired(t *testing.T) {
	repo := treeRepo()
	_, err := attrUsecase(repo).CreateSearchOption(context.Background(), 7, SearchOptionInput{Name: "  "})
	if !errors.Is(err, ErrSearchOptionNameRequired) {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceTreeUsecaseWithoutRepoReportsNotWired(t *testing.T) {
	uc := NewUsecase(nil, nil)
	ctx := context.Background()
	if _, err := uc.ListServiceTrees(ctx, 7, true); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("ListServiceTrees() error = %v", err)
	}
	if _, err := uc.CanEditRelation(ctx, 1, 2); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("CanEditRelation() error = %v", err)
	}
}
