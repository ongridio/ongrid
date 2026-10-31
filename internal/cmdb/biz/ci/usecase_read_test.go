package ci

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// --- a read-only double ---

type readRepo struct {
	ci   *cimodel.CI
	cis  map[model.ID]*cimodel.CI
	rows []ValueRow

	listed  []cimodel.CI
	total   int64
	lastLim int
	lastOff int

	singleLoads int
	batchLoads  int
}

func (r *readRepo) LoadCI(context.Context, model.ID) (*cimodel.CI, error) {
	r.singleLoads++
	return r.ci, nil
}

func (r *readRepo) LoadCIs(_ context.Context, ids []model.ID) (map[model.ID]*cimodel.CI, error) {
	r.batchLoads++
	out := map[model.ID]*cimodel.CI{}
	for _, id := range ids {
		if r.cis != nil && r.cis[id] != nil {
			out[id] = r.cis[id]
		}
	}
	return out, nil
}

func (r *readRepo) ListCIsByType(_ context.Context, _ model.ID, limit, offset int) ([]cimodel.CI, int64, error) {
	r.lastLim, r.lastOff = limit, offset
	return r.listed, r.total, nil
}

func (r *readRepo) ValueRowsForCIs(context.Context, []model.ID) (map[model.ID][]ValueRow, error) {
	out := map[model.ID][]ValueRow{}
	if r.ci != nil {
		out[r.ci.ID] = r.rows
	}
	return out, nil
}

func (r *readRepo) ValuesForAttribute(context.Context, model.ID, []model.ID) (map[model.ID][]ValueRow, error) {
	return map[model.ID][]ValueRow{}, nil
}

func (r *readRepo) CountCIsByType(context.Context, model.ID) (int64, error) { return r.total, nil }

// The write and query halves are unused by these tests; embedding the shared
// stubs keeps the double from growing a method per change to either.
type readRepoStub struct {
	readRepo
	queryRepoStub
	valueRepoStub
	relationRepoStub
	dcimRepoStub
}

// resolverStub is a TypeResolver over one model.
type resolverStub struct {
	ct     citypemodel.CIType
	specs  map[model.ID]AttributeSpec
	show   AttributeSpec
	unique AttributeSpec
}

func (s *resolverStub) ResolveForCI(_ context.Context, ref string) (*AttributeSet, error) {
	if ref != s.ct.Name && ref != "1" {
		return nil, nil
	}
	set := &AttributeSet{
		Type: s.ct, Specs: s.specs, ShowAttr: s.show, UniqueAttr: s.unique,
		ByName:  map[string]model.ID{},
		ByAlias: map[string]model.ID{},
	}
	for id, spec := range s.specs {
		if _, seen := set.ByName[spec.Name]; !seen {
			set.ByName[spec.Name] = id
		}
		if spec.Alias != "" {
			if _, seen := set.ByAlias[spec.Alias]; !seen {
				set.ByAlias[spec.Alias] = id
			}
		}
	}
	return set, nil
}

// readFixture is a model with a name, a secret and a note, and one instance.
func readFixture(t *testing.T) (*readRepoStub, *resolverStub, *cimodel.CI) {
	t.Helper()
	ct := citypemodel.CIType{Name: "host", Alias: "主机", UniqueID: 1, Enabled: true}
	ct.ID = 1
	specs := map[model.ID]AttributeSpec{
		1: {ID: 1, Name: "hostname", Alias: "主机名", ValueType: value.TypeText, IsIndex: true},
		2: {ID: 2, Name: "root_password", Alias: "密码", ValueType: value.TypeText, IsPassword: true},
		3: {ID: 3, Name: "note", Alias: "备注", ValueType: value.TypeText},
	}

	// The snapshot is rendered by the write path, which omits secrets. The
	// value rows, below, DO hold the secret — that is what makes the read-path
	// filter the thing under test.
	doc, err := json.Marshal(map[string]any{"hostname": "web-01"})
	if err != nil {
		t.Fatal(err)
	}
	ci := &cimodel.CI{TypeID: 1, PropsJSON: string(doc)}
	ci.ID = 42
	ci.CreatedAt = time.Unix(1700000000, 0)
	ci.UpdatedAt = time.Unix(1700000001, 0)

	repo := &readRepoStub{readRepo: readRepo{
		ci:     ci,
		listed: []cimodel.CI{*ci},
		total:  1,
		rows: []ValueRow{
			{RowID: 1, AttrID: 1, Table: value.TableIndexTexts, Text: "web-01"},
			{RowID: 2, AttrID: 2, Table: value.TableTexts, Text: "hunter2"},
			{RowID: 3, AttrID: 3, Table: value.TableIndexTexts, Text: "hello"},
		},
	}}
	return repo, &resolverStub{ct: ct, specs: specs}, ci
}

// THE HARD REQUIREMENT. A password attribute must not appear in a list or a
// detail response — not as a value, not as an empty key, not anywhere in the
// encoded body.
//
// cantor served password values only from `GET /ci/{id}/attributes/{attr}/password`,
// which the console calls when a person asks to see one. A list that carried
// them would be a credential dump behind a single page load, and the read path
// is the only place that can prevent it: the value IS stored, in the unindexed
// text table, and the fallback read fetches it.
func TestReadsNeverReturnAPasswordValue(t *testing.T) {
	repo, res, _ := readFixture(t)
	uc := NewUsecase(repo, res, nil)
	ctx := context.Background()

	// `note` is NOT in the snapshot, so asking for it forces the fallback read
	// — and the fallback reads the value tables, which is where the password
	// actually lives. Without this the filter under test never runs, and the
	// test would pass with the filter deleted.
	page, err := uc.ListByType(ctx, "host", 1, 0, []string{"hostname", "note"}, RetKeyName)
	if err != nil {
		t.Fatalf("ListByType() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %d", len(page.Items))
	}

	// Non-vacuity: the fallback ran, so the values came partly from the value
	// tables. If `note` were missing the read would not have taken that path.
	if page.Items[0].Values["note"] != "hello" {
		t.Fatalf("values = %+v, want the fallback field returned", page.Items[0].Values)
	}
	if page.Items[0].Values["hostname"] != "web-01" {
		t.Fatalf("values = %+v, want the snapshot field returned", page.Items[0].Values)
	}
	assertNoSecret(t, "list", page.Items[0].Values)

	got, err := uc.Get(ctx, 42, []string{"hostname", "note"}, RetKeyName)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Values["note"] != "hello" {
		t.Fatalf("values = %+v, want the fallback field returned", got.Values)
	}
	assertNoSecret(t, "detail", got.Values)

	// And with no projection at all: no fallback is taken, and the password is
	// still absent because the snapshot never held it.
	all, err := uc.Get(ctx, 42, nil, RetKeyName)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "detail without fields", all.Values)
}

// The secret is absent even when the caller asks for it by name and by alias —
// a `fields` filter is a projection, not an authorisation.
//
// THIS IS THE TEST THAT ACTUALLY EXERCISES THE SECRET FILTER, and it is worth
// saying why so it is not deleted as a duplicate of the one above. With a field
// projection in force, the NAME filter alone hides anything not asked for — so
// asking for `note` and `hostname` leaves the password out even with the secret
// filter removed. Only asking for the password by its ALIAS survives the name
// filter and reaches the secret filter. Verified by mutation: deleting the
// secret filter from renderInstance leaves the test above green and turns this
// one red with the leak visible in the message.
func TestReadsRefuseToReturnAPasswordEvenWhenAsked(t *testing.T) {
	repo, res, _ := readFixture(t)
	uc := NewUsecase(repo, res, nil)
	ctx := context.Background()

	for _, asked := range [][]string{{"root_password"}, {"密码"}, {"hostname", "root_password"}, {"note", "密码"}} {
		got, err := uc.Get(ctx, 42, asked, RetKeyName)
		if err != nil {
			t.Fatalf("Get(%v) error = %v", asked, err)
		}
		assertNoSecret(t, "detail with fields", got.Values)
	}
}

func assertNoSecret(t *testing.T, where string, values map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, leak := range []string{"hunter2", "root_password", "密码"} {
		if strings.Contains(body, leak) {
			t.Fatalf("%s response leaks %q: %s", where, leak, body)
		}
	}
}

// --- projection ---

func TestRetKeyChoosesTheValueKeys(t *testing.T) {
	repo, res, _ := readFixture(t)
	uc := NewUsecase(repo, res, nil)
	ctx := context.Background()

	byName, err := uc.Get(ctx, 42, nil, RetKeyName)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := byName.Values["hostname"]; !ok {
		t.Fatalf("values = %+v, want name keys", byName.Values)
	}

	byAlias, err := uc.Get(ctx, 42, nil, RetKeyAlias)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := byAlias.Values["主机名"]; !ok {
		t.Fatalf("values = %+v, want alias keys", byAlias.Values)
	}

	byID, err := uc.Get(ctx, 42, nil, RetKeyID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := byID.Values["1"]; !ok {
		t.Fatalf("values = %+v, want id keys", byID.Values)
	}

	// An unrecognised key falls back to name rather than failing the read: it
	// is a display preference, and a typo in it should not break a page.
	unknown, err := uc.Get(ctx, 42, nil, RetKey("nonsense"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := unknown.Values["hostname"]; !ok {
		t.Fatalf("values = %+v, want the name fallback", unknown.Values)
	}
}

func TestFieldsProjectTheValues(t *testing.T) {
	repo, res, _ := readFixture(t)
	uc := NewUsecase(repo, res, nil)
	ctx := context.Background()

	got, err := uc.Get(ctx, 42, []string{"hostname"}, RetKeyName)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Values) != 1 || got.Values["hostname"] != "web-01" {
		t.Fatalf("values = %+v, want only the requested field", got.Values)
	}

	// An alias projects the same as the name.
	byAlias, err := uc.Get(ctx, 42, []string{"备注"}, RetKeyName)
	if err != nil {
		t.Fatal(err)
	}
	if len(byAlias.Values) != 1 || byAlias.Values["note"] != "hello" {
		t.Fatalf("values = %+v, want the aliased field", byAlias.Values)
	}
}

// --- paging ---

// The page size is CLAMPED rather than trusted: every row carries a snapshot
// document, so an unbounded page is not a wide result set but an arbitrary
// amount of JSON.
func TestListByTypeClampsThePageSize(t *testing.T) {
	repo, res, _ := readFixture(t)
	uc := NewUsecase(repo, res, nil)

	if _, err := uc.ListByType(context.Background(), "host", 0, 100000, nil, RetKeyName); err != nil {
		t.Fatalf("ListByType() error = %v", err)
	}
	if repo.lastLim != MaxInstancePageSize {
		t.Fatalf("limit = %d, want it clamped to %d", repo.lastLim, MaxInstancePageSize)
	}
	if repo.lastOff != 0 {
		t.Fatalf("offset = %d, want 0 for the default page", repo.lastOff)
	}

	if _, err := uc.ListByType(context.Background(), "host", 3, 10, nil, RetKeyName); err != nil {
		t.Fatal(err)
	}
	if repo.lastLim != 10 || repo.lastOff != 20 {
		t.Fatalf("limit/offset = %d/%d, want 10 and 20 for page 3", repo.lastLim, repo.lastOff)
	}
}

func TestListByTypeUnknownModelIsNotFound(t *testing.T) {
	repo, res, _ := readFixture(t)
	_, err := NewUsecase(repo, res, nil).ListByType(context.Background(), "nope", 1, 0, nil, RetKeyName)
	if !errors.Is(err, ErrTypeNotFound) {
		t.Fatalf("error = %v, want ErrTypeNotFound", err)
	}
}

func TestUsecaseWithoutAWiringReportsNotWired(t *testing.T) {
	uc := NewUsecase(nil, nil, nil)
	ctx := context.Background()
	if _, err := uc.ListByType(ctx, "host", 1, 0, nil, RetKeyName); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("ListByType() error = %v", err)
	}
	if _, err := uc.Get(ctx, 42, nil, RetKeyName); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("Get() error = %v", err)
	}
}
