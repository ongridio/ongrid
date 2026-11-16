package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// ciFixture is a model with a text unique attribute plus a couple of others.
type ciFixture struct {
	ct    citypemodel.CIType
	specs map[model.ID]cibiz.AttributeSpec
}

const (
	attrHostname model.ID = 1
	attrCPU      model.ID = 2
	attrSecret   model.ID = 3
	attrNote     model.ID = 4
)

// newCIFixture creates the model row the store's lock reads.
func newCIFixture(t *testing.T, repo *Repo, db *gorm.DB) ciFixture {
	t.Helper()
	ct := citypemodel.CIType{Name: "host", Alias: "主机", UniqueID: attrHostname, Enabled: true}
	if err := db.Create(&ct).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}
	return ciFixture{
		ct: ct,
		specs: map[model.ID]cibiz.AttributeSpec{
			attrHostname: {ID: attrHostname, Name: "hostname", Alias: "主机名", ValueType: value.TypeText, IsIndex: true, IsRequired: true},
			attrCPU:      {ID: attrCPU, Name: "cpu", Alias: "核数", ValueType: value.TypeInt, IsIndex: true},
			attrSecret:   {ID: attrSecret, Name: "root_password", Alias: "密码", ValueType: value.TypeText, IsPassword: true},
			attrNote:     {ID: attrNote, Name: "note", Alias: "备注", ValueType: value.TypeText},
		},
	}
}

func spec(f ciFixture, unique string) cibiz.WriteSpec {
	return cibiz.WriteSpec{
		TypeRef: f.ct.Name, TypeID: f.ct.ID,
		UniqueAttrID: attrHostname, UniqueValue: unique,
		Policy: cibiz.ExistReject,
		Specs:  f.specs,
		Values: map[model.ID]any{attrHostname: unique},
		Audit:  cibiz.AuditInfo{Actor: 7, Origin: "test"},
	}
}

// newCIStoreRepo builds a repo over a migrated in-memory database, plus the
// model table the lock reads.
func newCIStoreRepo(t *testing.T) (*Repo, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.AutoMigrate(&citypemodel.CIType{}); err != nil {
		t.Fatalf("migrate model table: %v", err)
	}
	return NewRepo(db), db
}

func countCIs(t *testing.T, db *gorm.DB, typeID model.ID) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&cimodel.CI{}).Where("type_id = ?", typeID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWriteCICreates(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)

	res, err := repo.WriteCI(context.Background(), spec(f, "web-01"))
	if err != nil {
		t.Fatalf("WriteCI() error = %v", err)
	}
	if !res.Created || res.CIID == 0 {
		t.Fatalf("result = %+v, want a create", res)
	}

	var ci cimodel.CI
	if err := db.First(&ci, res.CIID).Error; err != nil {
		t.Fatal(err)
	}
	if ci.PropsJSON == "" {
		t.Fatal("the snapshot was not written")
	}
	doc, err := cibiz.ParsePropsDocument(ci.PropsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if doc["hostname"] != "web-01" {
		t.Fatalf("snapshot = %v", doc)
	}
}

// THE POLICY. cantor's `exist_policy`, whose four values mean what they say.
func TestWriteCIAppliesTheExistPolicy(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)
	ctx := context.Background()

	if _, err := repo.WriteCI(ctx, spec(f, "web-01")); err != nil {
		t.Fatal(err)
	}

	// reject: the default for a create, and the one that must not silently
	// overwrite.
	reject := spec(f, "web-01")
	reject.Values[attrCPU] = 99
	if _, err := repo.WriteCI(ctx, reject); !errors.Is(err, cibiz.ErrAlreadyExists) {
		t.Fatalf("reject error = %v, want ErrAlreadyExists", err)
	}
	if n := countCIs(t, db, f.ct.ID); n != 1 {
		t.Fatalf("instances = %d, want the refused write to have changed nothing", n)
	}

	// need: "this must be an update".
	need := spec(f, "web-404")
	need.Policy = cibiz.ExistNeed
	if _, err := repo.WriteCI(ctx, need); !errors.Is(err, cibiz.ErrNotFound) {
		t.Fatalf("need error = %v, want ErrNotFound", err)
	}
	if n := countCIs(t, db, f.ct.ID); n != 1 {
		t.Fatalf("instances = %d, want the refused write not to have created one", n)
	}

	// replace: updates what is there.
	replace := spec(f, "web-01")
	replace.Policy = cibiz.ExistReplace
	replace.Values[attrCPU] = 8
	res, err := repo.WriteCI(ctx, replace)
	if err != nil {
		t.Fatalf("replace error = %v", err)
	}
	if res.Created {
		t.Fatal("Created = true, want the existing instance updated")
	}
	if n := countCIs(t, db, f.ct.ID); n != 1 {
		t.Fatalf("instances = %d, want one", n)
	}
}

// THE UNIQUE LOOKUP IS CASE-INSENSITIVE ON BOTH DIALECTS.
//
// sqlite's default `=` is case-SENSITIVE, so a bare `value = ?` here would make
// "WEB-01" and "web-01" two different instances on the optional backend and one
// on MySQL. The comparison carries COLLATE NOCASE for exactly this reason.
func TestWriteCIFindsTheUniqueValueCaseInsensitively(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)
	ctx := context.Background()

	if _, err := repo.WriteCI(ctx, spec(f, "web-01")); err != nil {
		t.Fatal(err)
	}

	differentCase := spec(f, "WEB-01")
	if _, err := repo.WriteCI(ctx, differentCase); !errors.Is(err, cibiz.ErrAlreadyExists) {
		t.Fatalf("error = %v, want the differently-cased value to collide — otherwise the same "+
			"instance exists twice on SQLite and once on MySQL", err)
	}
	if n := countCIs(t, db, f.ct.ID); n != 1 {
		t.Fatalf("instances = %d, want one", n)
	}
}

// The unique lookup must not match an instance of a DIFFERENT model that
// happens to hold the same value.
func TestWriteCILookupIsScopedToTheModel(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)
	ctx := context.Background()

	other := citypemodel.CIType{Name: "switch", Alias: "交换机", UniqueID: attrHostname, Enabled: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.WriteCI(ctx, spec(f, "shared-name")); err != nil {
		t.Fatal(err)
	}

	sp := spec(f, "shared-name")
	sp.TypeRef = other.Name
	sp.TypeID = other.ID
	if _, err := repo.WriteCI(ctx, sp); err != nil {
		t.Fatalf("error = %v, want the other model's instance created: the lookup is per model", err)
	}
	if n := countCIs(t, db, other.ID); n != 1 {
		t.Fatalf("other model instances = %d, want 1", n)
	}
}

// A MODEL WITH NO SUCH UNIQUE ATTRIBUTE is refused rather than writing an
// unidentifiable instance.
func TestWriteCIRequiresTheUniqueAttributeInTheSpecs(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)
	sp := spec(f, "web-01")
	sp.UniqueAttrID = 999

	if _, err := repo.WriteCI(context.Background(), sp); err == nil {
		t.Fatal("WriteCI() = nil error, want the missing unique attribute refused")
	}
	if n := countCIs(t, db, f.ct.ID); n != 0 {
		t.Fatalf("instances = %d, want none", n)
	}
}

// A WRITE FOR A MODEL THAT DOES NOT EXIST fails on the lock, which is the first
// thing the transaction does.
func TestWriteCIRefusesAnUnknownModel(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)
	sp := spec(f, "web-01")
	sp.TypeID = 9999

	if _, err := repo.WriteCI(context.Background(), sp); !errors.Is(err, cibiz.ErrTypeNotFound) {
		t.Fatalf("error = %v, want ErrTypeNotFound", err)
	}
}

// THE WRITE IS ONE TRANSACTION. A failure while writing the value rows must
// leave no instance behind — cantor created the instance, then wrote values,
// then wrote the audit row, and a failure in either later step left a CI with
// no attributes.
func TestWriteCIIsAtomic(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)

	if err := db.Exec(`CREATE TRIGGER fail_value BEFORE INSERT ON c_value_index_texts
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`).Error; err != nil {
		t.Fatalf("create fault-injection trigger: %v", err)
	}

	sp := spec(f, "web-01")
	sp.Values[attrNote] = "hello"
	if _, err := repo.WriteCI(context.Background(), sp); err == nil {
		t.Fatal("WriteCI() with an injected failure = nil error, want error")
	}
	if n := countCIs(t, db, f.ct.ID); n != 0 {
		t.Fatalf("instances = %d, want the instance row rolled back with its values", n)
	}
}

// A PASSWORD VALUE IS NOT IN THE SNAPSHOT. The document column is read by list
// endpoints, so a secret copied into it is a credential dump behind one page
// load.
func TestWriteCIKeepsSecretsOutOfTheSnapshot(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)

	sp := spec(f, "web-01")
	sp.Values[attrSecret] = "hunter2"
	res, err := repo.WriteCI(context.Background(), sp)
	if err != nil {
		t.Fatalf("WriteCI() error = %v", err)
	}

	var ci cimodel.CI
	if err := db.First(&ci, res.CIID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ci.PropsJSON, "hunter2") {
		t.Fatalf("snapshot leaks the password: %s", ci.PropsJSON)
	}

	// The value itself is still stored — the dedicated endpoint reads it back.
	rows, err := repo.LoadValueRows(context.Background(), res.CIID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.AttrID == attrSecret && row.Text == "hunter2" {
			found = true
		}
	}
	if !found {
		t.Fatal("the password was not stored at all; it must be storable and merely not listable")
	}
}

// --- reads ---

func TestValueRowsForCIsIsOneQueryPerTable(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)
	ctx := context.Background()

	var ids []model.ID
	for i, name := range []string{"a-1", "a-2", "a-3"} {
		sp := spec(f, name)
		sp.Values[attrCPU] = i + 1
		res, err := repo.WriteCI(ctx, sp)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.CIID)
	}

	got, err := repo.ValueRowsForCIs(ctx, ids)
	if err != nil {
		t.Fatalf("ValueRowsForCIs() error = %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("returned %d instances, want %d", len(got), len(ids))
	}
	for _, id := range ids {
		if len(got[id]) == 0 {
			t.Fatalf("instance %d came back with no value rows", id)
		}
	}

	empty, err := repo.ValueRowsForCIs(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("got %d, want nothing for an empty request", len(empty))
	}
}

func TestListCIsByTypePages(t *testing.T) {
	repo, db := newCIStoreRepo(t)
	f := newCIFixture(t, repo, db)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		sp := spec(f, "web-0"+string(rune('a'+i)))
		if _, err := repo.WriteCI(ctx, sp); err != nil {
			t.Fatal(err)
		}
	}

	first, total, err := repo.ListCIsByType(ctx, f.ct.ID, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(first) != 2 {
		t.Fatalf("page = %d rows, total = %d, want 2 and 5", len(first), total)
	}
	// Newest first: the instance someone just created is the one they look for.
	if first[0].ID < first[1].ID {
		t.Fatalf("ids = %d,%d, want descending", first[0].ID, first[1].ID)
	}

	second, _, err := repo.ListCIsByType(ctx, f.ct.ID, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 || second[0].ID == first[0].ID {
		t.Fatalf("second page = %+v, want a different window", second)
	}

	n, err := repo.CountCIsByType(ctx, f.ct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("count = %d, want 5", n)
	}
}
