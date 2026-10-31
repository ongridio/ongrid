package citype

import (
	"context"
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// countingRepo is an in-memory port that also records how many times each
// batch method was called, so the "bounded queries regardless of row count"
// claim is testable rather than asserted in a comment.
type countingRepo struct {
	// The attribute-management half is a separate double; see attrfake_test.go.
	noAttrRepo

	types   []citypemodel.CIType
	attrs   map[model.ID]citypemodel.Attribute
	parents map[model.ID][]model.ID
	icons   map[model.ID]string
	failOn  string

	listCalls   int
	attrCalls   int
	parentCalls int
	iconCalls   int

	// Recorded by the write methods, which live in usecase_write_test.go.
	createdBinding model.ID
	createdParents []model.ID
	updatedUnique  model.ID
	deletedActor   model.ID
}

func (c *countingRepo) ListTypes(_ context.Context, f ListFilter) ([]citypemodel.CIType, error) {
	c.listCalls++
	if c.failOn == "ListTypes" {
		return nil, errors.New("boom")
	}
	out := make([]citypemodel.CIType, 0, len(c.types))
	for _, ct := range c.types {
		if f.NameLike != "" {
			// The fake mirrors the repo's substring semantics loosely; the
			// real matching is the store's job and is tested there.
			continue
		}
		if len(f.IDs) > 0 {
			found := false
			for _, id := range f.IDs {
				if id == ct.ID {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, ct)
	}
	return out, nil
}

func (c *countingRepo) GetType(_ context.Context, id model.ID) (*citypemodel.CIType, error) {
	if c.failOn == "GetType" {
		return nil, errors.New("boom")
	}
	for i := range c.types {
		if c.types[i].ID == id {
			ct := c.types[i]
			return &ct, nil
		}
	}
	return nil, nil
}

func (c *countingRepo) GetTypeByName(_ context.Context, name string) (*citypemodel.CIType, error) {
	for i := range c.types {
		if c.types[i].Name == name {
			ct := c.types[i]
			return &ct, nil
		}
	}
	return nil, nil
}

func (c *countingRepo) AttributesByID(_ context.Context, ids []model.ID) (map[model.ID]citypemodel.Attribute, error) {
	c.attrCalls++
	out := map[model.ID]citypemodel.Attribute{}
	for _, id := range ids {
		if a, ok := c.attrs[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func (c *countingRepo) ParentIDsByChild(_ context.Context, childIDs []model.ID) (map[model.ID][]model.ID, error) {
	c.parentCalls++
	out := map[model.ID][]model.ID{}
	for _, id := range childIDs {
		if p, ok := c.parents[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

func (c *countingRepo) TypeIcons(context.Context) (map[model.ID]string, error) {
	c.iconCalls++
	return c.icons, nil
}

// newCIType builds a fixture. The ID lives on the embedded Base, and setting a
// promoted field in a composite literal requires Go 1.27 (the module targets
// 1.25), so fixtures go through helpers.
func newCIType(id model.ID, name, alias string) citypemodel.CIType {
	ct := citypemodel.CIType{Name: name, Alias: alias, Enabled: true}
	ct.ID = id
	return ct
}

func newAttr(id model.ID, name string) citypemodel.Attribute {
	a := citypemodel.Attribute{Name: name, Alias: name, ValueType: "2"}
	a.ID = id
	return a
}

func idPtr(id model.ID) *model.ID { return &id }

func TestListEnrichesDerivedFields(t *testing.T) {
	unique, show := model.ID(10), model.ID(11)
	ct := newCIType(1, "host", "主机")
	ct.UniqueID = unique
	ct.ShowID = idPtr(show)

	repo := &countingRepo{
		types: []citypemodel.CIType{ct},
		attrs: map[model.ID]citypemodel.Attribute{
			unique: newAttr(unique, "hostname"),
			show:   newAttr(show, "display_name"),
		},
		parents: map[model.ID][]model.ID{1: {7, 3}},
	}

	got, err := NewUsecase(repo, nil).List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List() = %d views, want 1", len(got))
	}
	if got[0].UniqueKey != "hostname" {
		t.Fatalf("UniqueKey = %q, want the attribute NAME hostname", got[0].UniqueKey)
	}
	if got[0].ShowName != "display_name" {
		t.Fatalf("ShowName = %q, want display_name", got[0].ShowName)
	}
	// Parents must be sorted so the console renders a stable list.
	if len(got[0].ParentIDs) != 2 || got[0].ParentIDs[0] != 3 || got[0].ParentIDs[1] != 7 {
		t.Fatalf("ParentIDs = %v, want [3 7] (sorted)", got[0].ParentIDs)
	}
}

// The core structural claim: enrichment costs a BOUNDED number of queries no
// matter how many CI types are returned. cantor did a cache lookup per row and
// per reference; without Redis that would become an N+1, which this asserts
// against.
func TestListUsesBoundedQueriesRegardlessOfRowCount(t *testing.T) {
	repo := &countingRepo{attrs: map[model.ID]citypemodel.Attribute{}}
	for i := 1; i <= 500; i++ {
		ct := newCIType(model.ID(i), "type", "t")
		ct.UniqueID = model.ID(1000 + i)
		repo.types = append(repo.types, ct)
		repo.attrs[model.ID(1000+i)] = newAttr(model.ID(1000+i), "attr")
	}

	got, err := NewUsecase(repo, nil).List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 500 {
		t.Fatalf("List() = %d views, want 500", len(got))
	}
	if repo.listCalls != 1 || repo.attrCalls != 1 || repo.parentCalls != 1 {
		t.Fatalf("query counts = list:%d attrs:%d parents:%d, want exactly 1 each for 500 rows",
			repo.listCalls, repo.attrCalls, repo.parentCalls)
	}
}

// Deduplication: several types sharing one unique attribute must not cause the
// attribute to be requested repeatedly.
func TestListDeduplicatesAttributeLookups(t *testing.T) {
	repo := &countingRepo{attrs: map[model.ID]citypemodel.Attribute{42: newAttr(42, "hostname")}}
	for i := 1; i <= 20; i++ {
		ct := newCIType(model.ID(i), "t", "t")
		ct.UniqueID = 42
		repo.types = append(repo.types, ct)
	}

	if _, err := NewUsecase(repo, nil).List(context.Background(), ListFilter{}); err != nil {
		t.Fatal(err)
	}
	if repo.attrCalls != 1 {
		t.Fatalf("attrCalls = %d, want 1 batch call for 20 rows sharing one attribute", repo.attrCalls)
	}
}

// No types means no enrichment queries at all.
func TestListWithNoRowsSkipsEnrichment(t *testing.T) {
	repo := &countingRepo{}
	got, err := NewUsecase(repo, nil).List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got != nil {
		t.Fatalf("List() = %v, want nil", got)
	}
	if repo.attrCalls != 0 || repo.parentCalls != 0 {
		t.Fatalf("enrichment ran for an empty result: attrs:%d parents:%d", repo.attrCalls, repo.parentCalls)
	}
}

// A CI type whose unique attribute was deleted must still list, with an empty
// name rather than an error — cantor tolerated the dangling reference.
func TestListToleratesDanglingAttributeReference(t *testing.T) {
	ct := newCIType(1, "host", "主机")
	ct.UniqueID = 999 // no such attribute

	repo := &countingRepo{
		types: []citypemodel.CIType{ct},
		attrs: map[model.ID]citypemodel.Attribute{},
	}
	got, err := NewUsecase(repo, nil).List(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v, want the dangling reference tolerated", err)
	}
	if got[0].UniqueKey != "" {
		t.Fatalf("UniqueKey = %q, want empty", got[0].UniqueKey)
	}
}

func TestGetByIDAndName(t *testing.T) {
	repo := &countingRepo{
		types: []citypemodel.CIType{newCIType(5, "kvm", "虚机")},
		attrs: map[model.ID]citypemodel.Attribute{},
	}
	uc := NewUsecase(repo, nil)
	ctx := context.Background()

	byID, err := uc.Get(ctx, 5)
	if err != nil || byID.Type.Name != "kvm" {
		t.Fatalf("Get(5) = %+v, %v", byID, err)
	}
	byName, err := uc.GetByName(ctx, "kvm")
	if err != nil || byName.Type.ID != 5 {
		t.Fatalf("GetByName(kvm) = %+v, %v", byName, err)
	}
}

func TestGetMissingOrInvalid(t *testing.T) {
	uc := NewUsecase(&countingRepo{attrs: map[model.ID]citypemodel.Attribute{}}, nil)
	ctx := context.Background()

	if _, err := uc.Get(ctx, 404); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrNotFound", err)
	}
	if _, err := uc.Get(ctx, 0); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("Get(0) error = %v, want ErrInvalid", err)
	}
	if _, err := uc.GetByName(ctx, ""); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("GetByName(\"\") error = %v, want ErrInvalid", err)
	}
	if _, err := uc.GetByName(ctx, "nope"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("GetByName(missing) error = %v, want ErrNotFound", err)
	}
}

// Icons fall back to the type name, which is what cantor did so the console
// never had to special-case a model with no icon.
func TestIconsFallsBackToTypeName(t *testing.T) {
	withIcon := newCIType(1, "host", "主机")
	icon := "caise-jiaohuanji$$"
	withIcon.Icon = &icon
	without := newCIType(2, "kvm", "虚机")

	repo := &countingRepo{
		types: []citypemodel.CIType{withIcon, without},
		icons: map[model.ID]string{1: icon},
		attrs: map[model.ID]citypemodel.Attribute{},
	}

	got, err := NewUsecase(repo, nil).Icons(context.Background())
	if err != nil {
		t.Fatalf("Icons() error = %v", err)
	}
	if got[1] != icon {
		t.Fatalf("icon for 1 = %q, want %q", got[1], icon)
	}
	if got[2] != "kvm" {
		t.Fatalf("icon for 2 = %q, want the type name as fallback", got[2])
	}
}

// An empty icon string must also fall back, not yield an empty icon.
func TestIconsEmptyStringFallsBack(t *testing.T) {
	ct := newCIType(3, "switch", "交换机")
	empty := ""
	ct.Icon = &empty

	repo := &countingRepo{
		types: []citypemodel.CIType{ct},
		icons: map[model.ID]string{3: ""},
		attrs: map[model.ID]citypemodel.Attribute{},
	}
	got, err := NewUsecase(repo, nil).Icons(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[3] != "switch" {
		t.Fatalf("icon = %q, want the fallback name", got[3])
	}
}

func TestUnwiredRepoReportsNotWiredYet(t *testing.T) {
	uc := NewUsecase(nil, nil)
	ctx := context.Background()

	if _, err := uc.List(ctx, ListFilter{}); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("List() error = %v, want ErrNotWiredYet", err)
	}
	if _, err := uc.Get(ctx, 1); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("Get() error = %v, want ErrNotWiredYet", err)
	}
	if _, err := uc.Icons(ctx); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("Icons() error = %v, want ErrNotWiredYet", err)
	}
}

func TestRepoErrorsPropagate(t *testing.T) {
	repo := &countingRepo{attrs: map[model.ID]citypemodel.Attribute{}, failOn: "ListTypes"}

	if _, err := NewUsecase(repo, nil).List(context.Background(), ListFilter{}); err == nil {
		t.Fatal("List() = nil error, want the repo failure to surface")
	}
}
