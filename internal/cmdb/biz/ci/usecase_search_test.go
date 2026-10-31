package ci

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// --- doubles ---

// searchRepoFake answers the query port from a fixed fixture and records what
// it was asked, so a test can assert the paging a use case decided.
type searchRepoFake struct {
	readRepo
	queryRepoStub
	valueRepoStub
	relationRepoStub
	dcimRepoStub

	rows    []cimodel.CI
	total   int64
	values  map[model.ID][]ValueRow
	attrs   []search.Attr
	types   []search.Type
	buckets map[model.ID][]FacetBucket

	lastLimit int
	lastPlan  search.Plan
}

func (f *searchRepoFake) QueryInstances(_ context.Context, plan search.Plan, limit, offset int) ([]cimodel.CI, int64, error) {
	f.lastPlan, f.lastLimit = plan, limit
	return f.rows, f.total, nil
}

// ValueRowsForCIs is the fallback read, served from the fixture rather than
// from the embedded readRepo, whose single-instance shape the search page
// never uses.
func (f *searchRepoFake) ValueRowsForCIs(_ context.Context, ids []model.ID) (map[model.ID][]ValueRow, error) {
	out := make(map[model.ID][]ValueRow, len(ids))
	for _, id := range ids {
		if rows, ok := f.values[id]; ok {
			out[id] = rows
		}
	}
	return out, nil
}

func (f *searchRepoFake) QueryAttributes(context.Context, []string) ([]search.Attr, error) {
	return f.attrs, nil
}

func (f *searchRepoFake) QueryTypes(context.Context, []string) ([]search.Type, error) {
	return f.types, nil
}

func (f *searchRepoFake) QueryFacets(context.Context, search.Plan, []search.Attr, int) (map[model.ID][]FacetBucket, error) {
	return f.buckets, nil
}

// multiResolver answers by type id for the two models the search fixture has.
type multiResolver struct {
	sets map[string]*AttributeSet
}

func (m *multiResolver) ResolveForCI(_ context.Context, ref string) (*AttributeSet, error) {
	if set, ok := m.sets[ref]; ok {
		return set, nil
	}
	return nil, nil
}

func (m *multiResolver) RelationBetween(context.Context, model.ID, model.ID) (TypeRelationRule, bool, error) {
	return TypeRelationRule{}, false, errors.New("the resolver double declares no model rules")
}

func (m *multiResolver) HierarchyLevels(context.Context, model.ID, int, bool) (HierarchyLevels, error) {
	return HierarchyLevels{}, errors.New("the resolver double declares no model graph")
}

// --- fixture ---

const (
	sfHostname model.ID = 1
	sfNote     model.ID = 2
	sfSecret   model.ID = 3
)

// searchFixture is two models, one of which has a password attribute, and one
// instance each. The snapshots deliberately omit every attribute but the
// hostname, so a test that asks for the note forces the fallback read — and the
// fallback is the path that reaches the value tables, which is where the
// password actually lives.
func searchFixture(t *testing.T) (*searchRepoFake, *multiResolver) {
	t.Helper()

	host := citypemodel.CIType{Name: "host", Alias: "主机", UniqueID: sfHostname, Enabled: true}
	host.ID = 1
	sw := citypemodel.CIType{Name: "switch", Alias: "交换机", UniqueID: sfHostname, Enabled: true}
	sw.ID = 2

	hostSpecs := map[model.ID]AttributeSpec{
		sfHostname: {ID: sfHostname, Name: "hostname", Alias: "主机名", ValueType: value.TypeText, IsIndex: true},
		sfNote:     {ID: sfNote, Name: "note", Alias: "备注", ValueType: value.TypeText},
		sfSecret:   {ID: sfSecret, Name: "root_password", Alias: "密码", ValueType: value.TypeText, IsPassword: true},
	}

	ci := cimodel.CI{TypeID: 1, PropsJSON: `{"hostname":"web-01"}`}
	ci.ID = 42
	ci.CreatedAt = time.Unix(1700000000, 0)
	ci.UpdatedAt = time.Unix(1700000001, 0)
	other := cimodel.CI{TypeID: 2, PropsJSON: `{"hostname":"sw-01"}`}
	other.ID = 43

	repo := &searchRepoFake{
		rows:  []cimodel.CI{ci, other},
		total: 2,
		values: map[model.ID][]ValueRow{
			42: {
				{RowID: 1, AttrID: sfHostname, Table: value.TableIndexTexts, Text: "web-01"},
				{RowID: 2, AttrID: sfNote, Table: value.TableIndexTexts, Text: "hello"},
				{RowID: 3, AttrID: sfSecret, Table: value.TableTexts, Text: "hunter2"},
			},
			43: {
				{RowID: 4, AttrID: sfHostname, Table: value.TableIndexTexts, Text: "sw-01"},
			},
		},
		attrs: []search.Attr{
			{ID: sfHostname, Name: "hostname", Alias: "主机名", ValueType: value.TypeText, IsIndex: true},
			{ID: sfNote, Name: "note", Alias: "备注", ValueType: value.TypeText},
			{ID: sfSecret, Name: "root_password", Alias: "密码", ValueType: value.TypeText, IsPassword: true, IsIndex: true},
		},
		types: []search.Type{
			{ID: 1, Name: "host", Alias: "主机"},
			{ID: 2, Name: "switch", Alias: "交换机"},
		},
		buckets: map[model.ID][]FacetBucket{
			sfHostname: {{Value: "web-01", Count: 1}, {Value: "sw-01", Count: 1}},
		},
	}

	resolver := &multiResolver{sets: map[string]*AttributeSet{
		"1": {Type: host, Specs: hostSpecs, UniqueAttrID: sfHostname,
			UniqueAttr: hostSpecs[sfHostname], ByName: map[string]model.ID{"hostname": sfHostname}},
		"2": {Type: sw, Specs: hostSpecs, UniqueAttrID: sfHostname,
			UniqueAttr: hostSpecs[sfHostname], ByName: map[string]model.ID{"hostname": sfHostname}},
	}}
	return repo, resolver
}

func mustSearchJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --- the hard requirement, on the search path ---

// THE SAME REQUIREMENT AS THE INSTANCE READ PATH, checked again here because
// this path builds its own rows: a password attribute must not appear in a
// search result — not as a value, not as a key, not anywhere in the body.
//
// cantor's search masked a password as "******" and returned the key; the port
// omits it entirely. Either way the value must not escape, and the search
// endpoint is the one where a mistake would be worst: it answers with many
// instances at once.
//
// The projection asks for `note`, which no snapshot holds, so the fallback read
// runs and the value tables — including the one holding the password — are
// loaded. Without that, the filter under test would never see a secret and the
// test would pass with it deleted.
//
// AND THE PROJECTION ALONE WOULD HIDE A MISSING FILTER, which is why the second
// half of this test asks for the password BY ITS ALIAS. A field list is a
// projection, not an authorisation: asking for `hostname` and `note` leaves the
// password out even with the secret filter removed, so that request cannot tell
// the two apart. Verified by mutation — with the filter deleted, the first half
// stays green and the aliased half fails with the leak in the message.
func TestSearchNeverReturnsAPassword(t *testing.T) {
	repo, resolver := searchFixture(t)
	uc := NewUsecase(repo, resolver, nil)

	page, err := uc.Search(context.Background(), SearchRequest{
		Query: "_type:(host;switch)", Fields: []string{"hostname", "note"}, RetKey: RetKeyName,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(page.Hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(page.Hits))
	}

	// Non-vacuity: the fallback ran, so values came from the value tables.
	if page.Hits[0].Values["note"] != "hello" {
		t.Fatalf("values = %+v, want the fallback field returned", page.Hits[0].Values)
	}

	body := mustSearchJSON(t, page.Hits)
	for _, leak := range []string{"hunter2", "root_password", "密码"} {
		if strings.Contains(body, leak) {
			t.Fatalf("search hits leak %q: %s", leak, body)
		}
	}

	// The discriminating case: ask for the secret by its alias, which the
	// projection lets through and only the secret filter can stop.
	for _, asked := range [][]string{
		{"root_password"}, {"密码"}, {"hostname", "root_password"}, {"note", "密码"},
	} {
		got, err := uc.Search(context.Background(), SearchRequest{
			Query: "_type:(host;switch)", Fields: asked, RetKey: RetKeyName,
		})
		if err != nil {
			t.Fatalf("Search(fields=%v) error = %v", asked, err)
		}
		body = mustSearchJSON(t, got.Hits)
		for _, leak := range []string{"hunter2", "root_password", "密码"} {
			if strings.Contains(body, leak) {
				t.Fatalf("search with fields=%v leaks %q: %s", asked, leak, body)
			}
		}
	}

	// And with no projection at all, where no fallback is taken.
	all, err := uc.Search(context.Background(), SearchRequest{Query: "_type:(host;switch)"})
	if err != nil {
		t.Fatal(err)
	}
	body = mustSearchJSON(t, all.Hits)
	for _, leak := range []string{"hunter2", "root_password", "密码"} {
		if strings.Contains(body, leak) {
			t.Fatalf("unprojected search hits leak %q: %s", leak, body)
		}
	}
}

// --- the row's identity ---

// A result set spans models, so each hit has to say which one it belongs to and
// which attribute identifies an instance of it — the console labels the rows
// with those.
func TestSearchHitsCarryTheirModel(t *testing.T) {
	repo, resolver := searchFixture(t)
	uc := NewUsecase(repo, resolver, nil)

	page, err := uc.Search(context.Background(), SearchRequest{Query: "_type:(host;switch)"})
	if err != nil {
		t.Fatal(err)
	}

	first := page.Hits[0]
	if first.TypeName != "host" || first.TypeAlias != "主机" {
		t.Fatalf("hit model = %q / %q", first.TypeName, first.TypeAlias)
	}
	if first.UniqueAttr != "hostname" || first.UniqueAlias != "主机名" {
		t.Fatalf("hit unique = %q / %q", first.UniqueAttr, first.UniqueAlias)
	}
	if page.Hits[1].TypeName != "switch" {
		t.Fatalf("second hit model = %q", page.Hits[1].TypeName)
	}

	// The counter is how many of THIS page belong to each model, keyed by name.
	if page.Counter["host"] != 1 || page.Counter["switch"] != 1 {
		t.Fatalf("counter = %+v", page.Counter)
	}
}

// A hit whose model has been deleted is reported, not rendered with empty
// labels: cantor dereferenced the missing type row and crashed on the result.
func TestSearchReportsAnInstanceWhoseModelIsGone(t *testing.T) {
	repo, resolver := searchFixture(t)
	repo.rows = []cimodel.CI{{TypeID: 99}}
	uc := NewUsecase(repo, resolver, nil)

	_, err := uc.Search(context.Background(), SearchRequest{Query: ""})
	if !errors.Is(err, ErrTypeNotFound) {
		t.Fatalf("Search() error = %v, want ErrTypeNotFound", err)
	}
}

// --- facets ---

// A facet is keyed the same way a value is, so the console can look a bucket
// list up by the column it asked for without knowing the ret_key.
func TestSearchKeysFacetsLikeValues(t *testing.T) {
	repo, resolver := searchFixture(t)
	uc := NewUsecase(repo, resolver, nil)

	for _, tc := range []struct {
		key  RetKey
		want string
	}{
		{RetKeyName, "hostname"},
		{RetKeyAlias, "主机名"},
		{RetKeyID, "1"},
	} {
		page, err := uc.Search(context.Background(), SearchRequest{
			Query: "_type:host", Facets: []string{"hostname"}, RetKey: tc.key,
		})
		if err != nil {
			t.Fatalf("Search(ret_key=%s) error = %v", tc.key, err)
		}
		if _, ok := page.Facets[tc.want]; !ok {
			t.Fatalf("facets = %+v, want the key %q", page.Facets, tc.want)
		}
	}
}

// --- paging ---

// The page size is CLAMPED rather than trusted. A page is assembled from whole
// rows, each carrying a snapshot document, so an unbounded page is not a wide
// result set but an arbitrary amount of JSON — and a facet over a
// high-cardinality attribute is an arbitrary amount more.
func TestSearchClampsThePageSize(t *testing.T) {
	repo, resolver := searchFixture(t)
	uc := NewUsecase(repo, resolver, nil)

	if _, err := uc.Search(context.Background(), SearchRequest{Query: "", Count: 100000}); err != nil {
		t.Fatal(err)
	}
	if repo.lastLimit != MaxInstancePageSize {
		t.Fatalf("limit = %d, want it clamped to %d", repo.lastLimit, MaxInstancePageSize)
	}

	if _, err := uc.Search(context.Background(), SearchRequest{Query: "", Count: 0, Page: 3}); err != nil {
		t.Fatal(err)
	}
	if repo.lastLimit != DefaultInstancePageSize {
		t.Fatalf("limit = %d, want the default %d", repo.lastLimit, DefaultInstancePageSize)
	}
}

// --- refusals ---

func TestSearchReportsAnUnknownAttribute(t *testing.T) {
	repo, resolver := searchFixture(t)
	uc := NewUsecase(repo, resolver, nil)

	_, err := uc.Search(context.Background(), SearchRequest{Query: "nope:x"})
	if !errors.Is(err, search.ErrUnknownAttribute) {
		t.Fatalf("Search() error = %v, want ErrUnknownAttribute", err)
	}
	// And it must be the caller's mistake, so the HTTP layer answers 400.
	if got := errs.HTTPStatus(err); got != 400 {
		t.Fatalf("HTTPStatus = %d, want 400", got)
	}
}

func TestSearchWithoutAWiringReportsNotWired(t *testing.T) {
	uc := NewUsecase(nil, nil, nil)
	if _, err := uc.Search(context.Background(), SearchRequest{Query: ""}); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("Search() error = %v", err)
	}
}
