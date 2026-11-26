package citype

import (
	"context"
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// --- the level-view half of the double ---

type levelRepo struct {
	*attrRepo

	views []citypemodel.PreferenceTreeView

	saved     *LevelViewSaveSpec
	deleteFor [][2]model.ID
}

func (l *levelRepo) LevelViews(_ context.Context, uid model.ID) ([]citypemodel.PreferenceTreeView, error) {
	if l.prefErr != nil {
		return nil, l.prefErr
	}
	var out []citypemodel.PreferenceTreeView
	for _, v := range l.views {
		if v.UID == uid {
			out = append(out, v)
		}
	}
	return out, nil
}

func (l *levelRepo) LevelView(_ context.Context, uid, typeID model.ID) (*citypemodel.PreferenceTreeView, error) {
	for i, v := range l.views {
		if v.UID == uid && v.TypeID == typeID {
			return &l.views[i], nil
		}
	}
	return nil, nil
}

func (l *levelRepo) SaveLevelView(_ context.Context, in LevelViewSaveSpec) (*citypemodel.PreferenceTreeView, error) {
	if l.prefErr != nil {
		return nil, l.prefErr
	}
	l.saved = &in
	if len(in.Levels) == 0 {
		return nil, nil
	}
	return &citypemodel.PreferenceTreeView{
		UID: in.UID, TypeID: in.TypeID, Levels: citypemodel.IDList(in.Levels),
	}, nil
}

func (l *levelRepo) DeleteLevelView(_ context.Context, uid, typeID model.ID) error {
	l.deleteFor = append(l.deleteFor, [2]model.ID{uid, typeID})
	return nil
}

// levelFixture is two models with two bound attributes each, plus a saved view
// for each so the ordering rules can be exercised.
func levelFixture() *levelRepo {
	base := newAttrRepo()
	host := newCIType(10, "host", "主机")
	host.UniqueID = 1
	sw := newCIType(20, "switch", "交换机")
	sw.UniqueID = 1
	base.types = []citypemodel.CIType{host, sw}
	base.aliases[10] = "主机"
	base.aliases[20] = "交换机"
	// Built by the shared helpers rather than as literals: Go 1.25 cannot set a
	// promoted embedded field in a composite literal, which the Attribute's
	// Base.ID is. The alias is overwritten afterwards so the fixture exercises
	// alias resolution with something other than the name.
	hostname := textAttr(1, "hostname")
	hostname.Alias = "主机名"
	base.attrs[1] = hostname
	cpu := intAttr(2, "cpu")
	cpu.Alias = "核数"
	base.attrs[2] = cpu
	base.bindings = []citypemodel.CITypeAttribute{
		{TypeID: 10, AttrID: 1, Order: 0},
		{TypeID: 10, AttrID: 2, Order: 1},
		{TypeID: 20, AttrID: 1, Order: 0},
	}
	return &levelRepo{attrRepo: base}
}

// --- reading ---

// The view carries the model's identity FLATTENED IN, because that is the shape
// cantor returned and the console reads `item.name` off the same object as
// `item.levels`.
func TestListTreeLevelsRendersTheModelAndItsLevels(t *testing.T) {
	repo := levelFixture()
	repo.views = []citypemodel.PreferenceTreeView{
		{UID: 7, TypeID: 10, Levels: citypemodel.IDList{1, 2}},
	}
	repo.views[0].ID = 99

	got, err := NewUsecase(repo, nil).ListTreeLevels(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListTreeLevels() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("items = %d, want 1", len(got))
	}
	item := got[0]
	if item.ID != 99 || item.TypeID != 10 || item.Name != "host" || item.Alias != "主机" {
		t.Fatalf("item = %+v", item)
	}
	if len(item.Levels) != 2 || item.Levels[0].Name != "hostname" || item.Levels[1].Name != "cpu" {
		t.Fatalf("levels = %+v, want the saved order", item.Levels)
	}
}

// ORDER COMES FROM THE TREE ORDERING, with a model that has no entry sorting
// last. A constant fallback would place it alongside the first ordered model
// instead of after all of them.
func TestListTreeLevelsFollowsTheTreeOrder(t *testing.T) {
	repo := levelFixture()
	// Saved in the opposite order to the tree ordering, so the assertion
	// cannot pass by accident of insertion.
	repo.views = []citypemodel.PreferenceTreeView{
		{UID: 7, TypeID: 20, Levels: citypemodel.IDList{1}},
		{UID: 7, TypeID: 10, Levels: citypemodel.IDList{1}},
	}
	repo.typeOrder = []citypemodel.PreferenceCITypeOrder{
		{UID: 7, TypeID: idPtr(10), Order: 1, IsTree: true},
		{UID: 7, TypeID: idPtr(20), Order: 2, IsTree: true},
		// The RESOURCE ordering on the same table must not affect this one.
		{UID: 7, TypeID: idPtr(20), Order: 0, IsTree: false},
	}

	got, err := NewUsecase(repo, nil).ListTreeLevels(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TypeID != 10 || got[1].TypeID != 20 {
		t.Fatalf("order = %d,%d, want 10 then 20", got[0].TypeID, got[1].TypeID)
	}

	// Drop the ordering entry for the first model: it must sort AFTER the one
	// that still has a position.
	repo.typeOrder = repo.typeOrder[1:]
	got, err = NewUsecase(repo, nil).ListTreeLevels(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].TypeID != 20 || got[1].TypeID != 10 {
		t.Fatalf("order = %d,%d, want the unordered model last", got[0].TypeID, got[1].TypeID)
	}
}

// A level whose attribute has been removed from the model is SKIPPED. cantor
// looked the id up in a cache and dropped the miss, which is why removing an
// attribute can leave a stale id in this column; a page that renders nine of
// ten columns is still a page.
func TestListTreeLevelsSkipsLevelsWhoseAttributeIsGone(t *testing.T) {
	repo := levelFixture()
	repo.views = []citypemodel.PreferenceTreeView{
		{UID: 7, TypeID: 10, Levels: citypemodel.IDList{1, 999, 2}},
	}

	got, err := NewUsecase(repo, nil).ListTreeLevels(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Levels) != 2 || got[0].Levels[0].Name != "hostname" || got[0].Levels[1].Name != "cpu" {
		t.Fatalf("levels = %+v, want the dangling id dropped and the order kept", got[0].Levels)
	}
}

// A view whose model no longer exists is skipped rather than rendered as a
// nameless entry. The cascade removes these rows with the model, so reaching
// here means the cascade has not run.
func TestListTreeLevelsSkipsAViewWithNoModel(t *testing.T) {
	repo := levelFixture()
	repo.views = []citypemodel.PreferenceTreeView{
		{UID: 7, TypeID: 404, Levels: citypemodel.IDList{1}},
		{UID: 7, TypeID: 10, Levels: citypemodel.IDList{1}},
	}

	got, err := NewUsecase(repo, nil).ListTreeLevels(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TypeID != 10 {
		t.Fatalf("items = %+v, want only the resolvable view", got)
	}
}

func TestListTreeLevelsWithNoViews(t *testing.T) {
	got, err := NewUsecase(levelFixture(), nil).ListTreeLevels(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("items = %+v, want none", got)
	}
	// A list, not nil: the console iterates the response.
	if got == nil {
		t.Fatal("items is nil, want an empty list")
	}
}

// --- writing ---

// A level reference may be a NAME, an ALIAS or a numeric id, in cantor's order.
func TestSetTreeLevelsResolvesEveryReferenceForm(t *testing.T) {
	repo := levelFixture()
	uc := NewUsecase(repo, nil)

	got, err := uc.SetTreeLevels(context.Background(), 7, "host", []string{"hostname", "核数", "1"})
	if err != nil {
		t.Fatalf("SetTreeLevels() error = %v", err)
	}
	if repo.saved == nil {
		t.Fatal("nothing was saved")
	}
	if repo.saved.TypeID != 10 || repo.saved.UID != 7 {
		t.Fatalf("saved = %+v", repo.saved)
	}
	if len(repo.saved.Levels) != 3 || repo.saved.Levels[0] != 1 || repo.saved.Levels[1] != 2 {
		t.Fatalf("levels = %v, want the resolved ids in order", repo.saved.Levels)
	}
	// A NEW view is appended to the user's tree ordering, which is what makes
	// the model appear on the tree page at all.
	if !repo.saved.AppendOrder {
		t.Fatal("AppendOrder = false, want a new view to be ordered")
	}
	if len(got.Levels) != 3 {
		t.Fatalf("returned levels = %+v", got.Levels)
	}
}

// Editing an EXISTING view must not move the model in the sidebar: the position
// belongs to the user and has its own endpoint.
func TestSetTreeLevelsDoesNotReorderAnExistingView(t *testing.T) {
	repo := levelFixture()
	repo.views = []citypemodel.PreferenceTreeView{{UID: 7, TypeID: 10, Levels: citypemodel.IDList{1}}}

	if _, err := NewUsecase(repo, nil).SetTreeLevels(context.Background(), 7, "10", []string{"cpu"}); err != nil {
		t.Fatal(err)
	}
	if repo.saved.AppendOrder {
		t.Fatal("AppendOrder = true, want an edit to leave the ordering alone")
	}
}

// AN EMPTY LIST REMOVES THE VIEW. It is how the console expresses "stop
// customising this model", and an empty string is one of the ways it says so.
func TestSetTreeLevelsWithNoLevelsClearsTheView(t *testing.T) {
	repo := levelFixture()
	uc := NewUsecase(repo, nil)

	for _, levels := range [][]string{nil, {}, {""}} {
		repo.saved = nil
		if _, err := uc.SetTreeLevels(context.Background(), 7, "host", levels); err != nil {
			t.Fatalf("SetTreeLevels(%v) error = %v", levels, err)
		}
		if repo.saved == nil || len(repo.saved.Levels) != 0 {
			t.Fatalf("levels %v saved %+v, want an empty write", levels, repo.saved)
		}
	}
}

// AN UNRESOLVABLE LEVEL IS REFUSED AND NOTHING IS WRITTEN. cantor rewrote the
// list in place and kept whatever did not match, so a typo was stored verbatim
// and silently dropped at read time — the page then showed fewer levels than
// the user had chosen, with nothing to say why.
func TestSetTreeLevelsRefusesAnUnknownLevel(t *testing.T) {
	repo := levelFixture()

	_, err := NewUsecase(repo, nil).SetTreeLevels(context.Background(), 7, "host", []string{"hostname", "nope"})
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if repo.saved != nil {
		t.Fatalf("saved %+v, want nothing written", repo.saved)
	}

	// And a model that does not exist is a 404, not a 400.
	_, err = NewUsecase(repo, nil).SetTreeLevels(context.Background(), 7, "nope", []string{"hostname"})
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

// A level count is bounded, because the column is a JSON array with no width of
// its own and the endpoint is reachable by any authenticated caller.
func TestSetTreeLevelsRefusesTooManyLevels(t *testing.T) {
	repo := levelFixture()
	many := make([]string, MaxTreeLevels+1)
	for i := range many {
		many[i] = "1"
	}

	_, err := NewUsecase(repo, nil).SetTreeLevels(context.Background(), 7, "host", many)
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if repo.saved != nil {
		t.Fatal("a refused write reached the repository")
	}
}

// AN INHERITED ATTRIBUTE RESOLVES. cantor resolved against the model's own
// bindings only, so a hierarchy could not display a column the model
// demonstrably has — an arbitrary restriction, not a rule.
func TestSetTreeLevelsAcceptsAnInheritedAttribute(t *testing.T) {
	repo := levelFixture()
	base := newCIType(30, "base", "基础")
	base.UniqueID = 3
	repo.types = append(repo.types, base)
	owner := textAttr(3, "owner")
	owner.Alias = "负责人"
	repo.attrs[3] = owner
	repo.bindings = append(repo.bindings, citypemodel.CITypeAttribute{TypeID: 30, AttrID: 3, Order: 0})
	repo.ancestors = []model.ID{30}

	if _, err := NewUsecase(repo, nil).SetTreeLevels(context.Background(), 7, "host", []string{"owner"}); err != nil {
		t.Fatalf("SetTreeLevels() with an inherited attribute error = %v", err)
	}
	if repo.saved == nil || len(repo.saved.Levels) != 1 || repo.saved.Levels[0] != 3 {
		t.Fatalf("saved = %+v, want the inherited attribute's id", repo.saved)
	}
}

func TestSetTreeLevelsWithoutAWiring(t *testing.T) {
	uc := NewUsecase(nil, nil)
	if _, err := uc.SetTreeLevels(context.Background(), 7, "host", nil); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("error = %v, want ErrNotWiredYet", err)
	}
	if _, err := uc.ListTreeLevels(context.Background(), 7); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("error = %v, want ErrNotWiredYet", err)
	}
}
