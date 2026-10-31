package store

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

func newTestRepo(t *testing.T) (*Repo, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(testDSN(t)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewRepo(db), db
}

// seedType inserts a CI type and returns its assigned id.
func seedType(t *testing.T, db *gorm.DB, name, alias string, uniqueID model.ID, icon *string) citypemodel.CIType {
	t.Helper()
	ct := citypemodel.CIType{Name: name, Alias: alias, UniqueID: uniqueID, Enabled: true, Icon: icon}
	if err := db.Create(&ct).Error; err != nil {
		t.Fatalf("create ci type %s: %v", name, err)
	}
	return ct
}

func seedAttr(t *testing.T, db *gorm.DB, name string) citypemodel.Attribute {
	t.Helper()
	a := citypemodel.Attribute{Name: name, Alias: name, ValueType: "2"}
	if err := db.Create(&a).Error; err != nil {
		t.Fatalf("create attribute %s: %v", name, err)
	}
	return a
}

func TestListTypesOrdersByID(t *testing.T) {
	repo, db := newTestRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")

	for _, name := range []string{"c_third", "a_first", "b_second"} {
		seedType(t, db, name, name, attr.ID, nil)
	}

	got, err := repo.ListTypes(ctx, citypebiz.ListFilter{})
	if err != nil {
		t.Fatalf("ListTypes() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListTypes() = %d rows, want 3", len(got))
	}
	if got[0].Name != "c_third" {
		t.Fatalf("first row = %q, want insertion order (c_third)", got[0].Name)
	}
}

// The name filter is a SUBSTRING match, matching cantor's get_by_like.
func TestListTypesNameSubstring(t *testing.T) {
	repo, db := newTestRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")
	seedType(t, db, "kvm_host", "k", attr.ID, nil)
	seedType(t, db, "switch", "s", attr.ID, nil)
	seedType(t, db, "host_container", "h", attr.ID, nil)

	got, err := repo.ListTypes(ctx, citypebiz.ListFilter{NameLike: "host"})
	if err != nil {
		t.Fatalf("ListTypes() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListTypes(name~host) = %d rows, want 2", len(got))
	}
}

// A user-supplied `%` must match literally rather than widening the search.
func TestListTypesEscapesLikeMetacharacters(t *testing.T) {
	repo, db := newTestRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")
	seedType(t, db, "web_1", "w", attr.ID, nil)
	seedType(t, db, "webX1", "x", attr.ID, nil)
	seedType(t, db, "100%cpu", "p", attr.ID, nil)
	seedType(t, db, "100Xcpu", "q", attr.ID, nil)

	cases := []struct {
		filter    string
		wantNames []string
	}{
		// `_` is a single-character wildcard in LIKE; it must not be treated as one.
		{"web_1", []string{"web_1"}},
		// `%` is a multi-character wildcard; it must not be treated as one.
		{"100%cpu", []string{"100%cpu"}},
		// A literal backslash must not become an escape prefix.
		{`\`, nil},
	}
	for _, tc := range cases {
		got, err := repo.ListTypes(ctx, citypebiz.ListFilter{NameLike: tc.filter})
		if err != nil {
			t.Fatalf("ListTypes(%q) error = %v", tc.filter, err)
		}
		var names []string
		for _, ct := range got {
			names = append(names, ct.Name)
		}
		if len(names) != len(tc.wantNames) {
			t.Fatalf("ListTypes(%q) = %v, want %v", tc.filter, names, tc.wantNames)
		}
		for i := range names {
			if names[i] != tc.wantNames[i] {
				t.Fatalf("ListTypes(%q) = %v, want %v", tc.filter, names, tc.wantNames)
			}
		}
	}
}

func TestListTypesByIDList(t *testing.T) {
	repo, db := newTestRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")
	a := seedType(t, db, "host", "h", attr.ID, nil)
	b := seedType(t, db, "kvm", "k", attr.ID, nil)
	seedType(t, db, "switch", "s", attr.ID, nil)

	got, err := repo.ListTypes(ctx, citypebiz.ListFilter{IDs: []model.ID{a.ID, b.ID}})
	if err != nil {
		t.Fatalf("ListTypes() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListTypes(ids) = %d rows, want 2", len(got))
	}
}

func TestGetTypeByNameIsExact(t *testing.T) {
	repo, db := newTestRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")
	seedType(t, db, "kvm_host", "k", attr.ID, nil)

	if got, err := repo.GetTypeByName(ctx, "kvm"); err != nil || got != nil {
		t.Fatalf("GetTypeByName(prefix) = %+v, %v; want nil", got, err)
	}
	if got, err := repo.GetTypeByName(ctx, "KVM_HOST"); err != nil || got != nil {
		t.Fatalf("GetTypeByName(uppercase) = %+v, %v; want nil (exact match)", got, err)
	}
	if got, err := repo.GetTypeByName(ctx, "kvm_host"); err != nil || got == nil {
		t.Fatalf("GetTypeByName(exact) = %+v, %v; want the row", got, err)
	}
}

func TestGetTypeMissingReturnsNil(t *testing.T) {
	repo, _ := newTestRepo(t)

	got, err := repo.GetType(context.Background(), 999)
	if err != nil || got != nil {
		t.Fatalf("GetType(999) = %+v, %v; want nil, nil", got, err)
	}
}

// Batch attribute loading must return only the ids that exist — a CI type may
// reference an attribute that was deleted afterwards.
func TestAttributesByIDReturnsOnlyExisting(t *testing.T) {
	repo, db := newTestRepo(t)
	a := seedAttr(t, db, "hostname")
	b := seedAttr(t, db, "cpu_count")

	got, err := repo.AttributesByID(context.Background(), []model.ID{a.ID, b.ID, 9999})
	if err != nil {
		t.Fatalf("AttributesByID() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("AttributesByID() returned %d rows, want 2 (missing id omitted)", len(got))
	}
	if got[a.ID].Name != "hostname" || got[b.ID].Name != "cpu_count" {
		t.Fatalf("AttributesByID() = %+v", got)
	}
}

func TestAttributesByIDWithNoIDsSkipsQuery(t *testing.T) {
	repo, _ := newTestRepo(t)

	got, err := repo.AttributesByID(context.Background(), nil)
	if err != nil {
		t.Fatalf("AttributesByID(nil) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("AttributesByID(nil) = %+v, want empty", got)
	}
}

func TestParentIDsByChildGroupsEdges(t *testing.T) {
	repo, db := newTestRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")
	parent1 := seedType(t, db, "p1", "p1", attr.ID, nil)
	parent2 := seedType(t, db, "p2", "p2", attr.ID, nil)
	child := seedType(t, db, "c", "c", attr.ID, nil)
	lonely := seedType(t, db, "l", "l", attr.ID, nil)

	for _, pid := range []model.ID{parent2.ID, parent1.ID} {
		if err := db.Create(&citypemodel.CITypeInheritance{ParentID: pid, ChildID: child.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.ParentIDsByChild(ctx, []model.ID{child.ID, lonely.ID})
	if err != nil {
		t.Fatalf("ParentIDsByChild() error = %v", err)
	}
	// Ordered by parent_id, so the usecase's sort is belt-and-braces.
	if len(got[child.ID]) != 2 || got[child.ID][0] != parent1.ID || got[child.ID][1] != parent2.ID {
		t.Fatalf("parents of child = %v, want [%d %d]", got[child.ID], parent1.ID, parent2.ID)
	}
	if _, ok := got[lonely.ID]; ok {
		t.Fatalf("lonely type should be absent from the map, got %v", got[lonely.ID])
	}
}

func TestTypeIconsOnlyReturnsNonEmpty(t *testing.T) {
	repo, db := newTestRepo(t)
	attr := seedAttr(t, db, "hostname")
	icon := "caise-jiaohuanji$$"
	empty := ""
	withIcon := seedType(t, db, "host", "h", attr.ID, &icon)
	withEmpty := seedType(t, db, "kvm", "k", attr.ID, &empty)
	seedType(t, db, "switch", "s", attr.ID, nil)

	got, err := repo.TypeIcons(context.Background())
	if err != nil {
		t.Fatalf("TypeIcons() error = %v", err)
	}
	if got[withIcon.ID] != icon {
		t.Fatalf("icon for host = %q, want %q", got[withIcon.ID], icon)
	}
	if _, ok := got[withEmpty.ID]; ok {
		t.Fatalf("an empty icon should be absent so the caller can fall back")
	}
	if len(got) != 1 {
		t.Fatalf("TypeIcons() = %d entries, want 1", len(got))
	}
}

// Soft-deleted CI types must disappear from every read, including the icon map.
func TestSoftDeletedTypesAreHidden(t *testing.T) {
	repo, db := newTestRepo(t)
	ctx := context.Background()
	attr := seedAttr(t, db, "hostname")
	icon := "x$$"
	ct := seedType(t, db, "host", "h", attr.ID, &icon)

	if err := db.Delete(&citypemodel.CIType{}, ct.ID).Error; err != nil {
		t.Fatal(err)
	}

	if rows, err := repo.ListTypes(ctx, citypebiz.ListFilter{}); err != nil || len(rows) != 0 {
		t.Fatalf("ListTypes() after delete = %+v, %v; want empty", rows, err)
	}
	if got, err := repo.GetType(ctx, ct.ID); err != nil || got != nil {
		t.Fatalf("GetType() after delete = %+v, %v; want nil", got, err)
	}
	if got, err := repo.GetTypeByName(ctx, "host"); err != nil || got != nil {
		t.Fatalf("GetTypeByName() after delete = %+v, %v; want nil", got, err)
	}
	if icons, err := repo.TypeIcons(ctx); err != nil || len(icons) != 0 {
		t.Fatalf("TypeIcons() after delete = %+v, %v; want empty", icons, err)
	}

	// Soft delete, so the row survives unscoped.
	var unscoped int64
	if err := db.Unscoped().Model(&citypemodel.CIType{}).Count(&unscoped).Error; err != nil {
		t.Fatal(err)
	}
	if unscoped != 1 {
		t.Fatalf("unscoped rows = %d, want 1", unscoped)
	}
}

func TestEscapeLike(t *testing.T) {
	cases := map[string]string{
		"plain": "plain",
		"web_1": `web\_1`,
		"100%":  `100\%`,
		`a\b`:   `a\\b`,
		`%_\`:   `\%\_\\`,
	}
	for in, want := range cases {
		if got := escapeLike(in); got != want {
			t.Errorf("escapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}

// testDSN names an in-memory database that EVERY connection in this test
// shares.
//
// A BARE `:memory:` IS NOT ENOUGH, and the difference is not academic. With
// `:memory:` each CONNECTION gets its own private, empty database, and
// database/sql may open a second connection at any moment — for instance while
// a transaction holds the first one. A test that then asserts "nothing was
// written" reads through whichever connection it is handed and can pass because
// it is looking at an empty database rather than because the write was rolled
// back. Measured on glebarez/sqlite: a CREATE on one connection followed by a
// read from inside a transaction reported "no such table: probe", and the pool
// had grown from one connection to two.
//
// The unique name keeps this test's database to itself, so tests cannot bleed
// into each other through a shared cache. It is dropped when the last
// connection closes.
func testDSN(t *testing.T) string {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_", "?", "_", "#", "_").Replace(t.Name())
	return "file:cmdb-" + name + "?mode=memory&cache=shared"
}
