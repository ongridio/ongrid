package ci

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

// The search endpoint's wire contract, which is what this layer owns: how a
// request's parameters are read, and the shape of the body that comes back.
// What the query MEANS is settled three packages down and tested there.

func TestReadSearchParams(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want searchParams
	}{
		{
			name: "cantor's parameter names",
			url:  "/v1/cmdb/ci/s?q=hostname:web-01&page=2&count=25&ret_key=alias&fl=hostname,note&facet=tags&sort=-cpu",
			want: searchParams{
				query: "hostname:web-01", sort: "-cpu",
				fields: []string{"hostname", "note"}, facets: []string{"tags"},
				page: 2, count: 25, key: cibiz.RetKeyAlias,
			},
		},
		{
			name: "count may be spelled page_size, as cantor accepted",
			url:  "/v1/cmdb/ci/search?q=_type:host&page_size=10",
			want: searchParams{query: "_type:host", page: 1, count: 10, key: cibiz.RetKeyName},
		},
		{
			name: "an absent page size takes the default, not zero rows",
			url:  "/v1/cmdb/ci/s",
			want: searchParams{page: 1, count: 0, key: cibiz.RetKeyName},
		},
		{
			name: "an unrecognised ret_key falls back to name",
			url:  "/v1/cmdb/ci/s?ret_key=nonsense",
			want: searchParams{page: 1, key: cibiz.RetKeyName},
		},
		{
			name: "a field list ignores empty entries",
			url:  "/v1/cmdb/ci/s?fl=hostname,,note,",
			want: searchParams{page: 1, key: cibiz.RetKeyName, fields: []string{"hostname", "note"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			got, err := readSearchParams(req)
			if err != nil {
				t.Fatalf("readSearchParams() error = %v", err)
			}
			assertParams(t, got, tc.want)
		})
	}
}

// count=all is refused rather than ignored. cantor accepted the value and then
// formatted it into a LIMIT clause, which is a syntax error on its own MySQL
// backend — it was never usable, so answering "no" is not a regression, and
// quietly substituting the default would answer a different question.
func TestReadSearchParamsRefusesCountAll(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/cmdb/ci/s?count=all", nil)
	if _, err := readSearchParams(req); err == nil {
		t.Fatal("count=all was accepted")
	}
}

// A POST may carry the expression in its body and the paging in the query
// string, and an empty body is not an error: cantor's POST route forwarded
// straight to its GET handler.
func TestReadSearchParamsFromAPostBody(t *testing.T) {
	body := `{"q":"_type:host,hostname:web-01","count":5,"fl":["hostname","note"],"facet":["tags"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/cmdb/ci/s?page=3", strings.NewReader(body))
	got, err := readSearchParams(req)
	if err != nil {
		t.Fatalf("readSearchParams() error = %v", err)
	}
	assertParams(t, got, searchParams{
		query: "_type:host,hostname:web-01", page: 3, count: 5,
		fields: []string{"hostname", "note"}, facets: []string{"tags"}, key: cibiz.RetKeyName,
	})

	empty := httptest.NewRequest(http.MethodPost, "/v1/cmdb/ci/s?q=_type:host", nil)
	got, err = readSearchParams(empty)
	if err != nil {
		t.Fatalf("an empty POST body was rejected: %v", err)
	}
	if got.query != "_type:host" {
		t.Fatalf("query = %q, want the query string to be used", got.query)
	}

	broken := httptest.NewRequest(http.MethodPost, "/v1/cmdb/ci/s", strings.NewReader("{not json"))
	if _, err := readSearchParams(broken); err == nil {
		t.Fatal("a malformed body was accepted")
	}
}

func assertParams(t *testing.T, got, want searchParams) {
	t.Helper()
	if got.query != want.query || got.sort != want.sort || got.page != want.page ||
		got.count != want.count || got.key != want.key {
		t.Fatalf("params = %+v, want %+v", got, want)
	}
	if strings.Join(got.fields, ",") != strings.Join(want.fields, ",") ||
		strings.Join(got.facets, ",") != strings.Join(want.facets, ",") {
		t.Fatalf("lists = %v / %v, want %v / %v", got.fields, got.facets, want.fields, want.facets)
	}
}

// --- the body ---

// THE ROW SHAPE IS CANTOR'S: the identifying fields first, then every attribute
// value flat alongside them. The console reads `row.hostname` directly, so
// nesting the values under a `values` key would mean rewriting every consumer.
func TestSearchResponseShape(t *testing.T) {
	page := &cibiz.SearchPage{
		Hits: []cibiz.SearchHit{
			{
				Instance: cibiz.Instance{
					ID: 42, TypeID: 1, UpdatedAt: time.Date(2024, 3, 1, 10, 30, 0, 0, time.UTC),
					Values: map[string]any{"hostname": "web-01"},
				},
				TypeName: "host", TypeAlias: "主机",
				UniqueAttr: "hostname", UniqueAlias: "主机名",
				UpdatedBy: "alice",
			},
		},
		NumFound: 7,
		Page:     2,
		Counter:  map[string]int{"host": 1},
		Facets: map[string][]cibiz.FacetBucket{
			"hostname": {{Value: "web-01", Count: 3}},
		},
	}

	body := searchResponse(page)
	if body["numfound"] != int64(7) || body["total"] != 1 || body["page"] != 2 {
		t.Fatalf("counts = %v / %v / %v", body["numfound"], body["total"], body["page"])
	}

	result, ok := body["result"].([]map[string]any)
	if !ok || len(result) != 1 {
		t.Fatalf("result = %#v", body["result"])
	}
	row := result[0]
	for key, want := range map[string]any{
		"_id": model.ID(42), "_type": model.ID(1), "ci_id": model.ID(42),
		"ci_type": "host", "ci_type_alias": "主机",
		"unique": "hostname", "unique_alias": "主机名",
		"_updated_at": "2024-03-01 10:30:00", "_updated_by": "alice",
		"hostname": "web-01",
	} {
		if row[key] != want {
			t.Fatalf("row[%q] = %#v, want %#v (row = %#v)", key, row[key], want, row)
		}
	}

	// A facet bucket is a triple: the value, its count, and the field it came
	// from — the console reads the third element as the column key.
	facets, ok := body["facet"].(map[string]any)
	if !ok {
		t.Fatalf("facet = %#v", body["facet"])
	}
	triples, ok := facets["hostname"].([][3]any)
	if !ok || len(triples) != 1 {
		t.Fatalf("facet[hostname] = %#v", facets["hostname"])
	}
	if triples[0] != [3]any{"web-01", int64(3), "hostname"} {
		t.Fatalf("triple = %#v", triples[0])
	}

	// It marshals: the body is what goes on the wire, so a value the encoder
	// refuses would be a 500 rather than a shape bug.
	if _, err := json.Marshal(body); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}

// An attribute value overwrites a metadata key rather than being dropped, which
// is what cantor did. The collision is not reachable through the model
// configuration — every name the response uses for itself is in cantor's
// BUILTIN_KEYWORDS, which the attribute dictionary refuses — but a response
// that silently dropped a value would be worse than one where the value wins.
func TestSearchResponseLetsAValueWin(t *testing.T) {
	page := &cibiz.SearchPage{
		Hits: []cibiz.SearchHit{{
			Instance:   cibiz.Instance{ID: 1, TypeID: 1, Values: map[string]any{"unique": "from-an-attribute"}},
			TypeName:   "host",
			UniqueAttr: "hostname",
		}},
	}
	body := searchResponse(page)
	row := body["result"].([]map[string]any)[0]
	if row["unique"] != "from-an-attribute" {
		t.Fatalf("row[unique] = %v", row["unique"])
	}
}

// An empty page still carries `result` as a list rather than null, so a client
// can iterate it without a null check.
func TestSearchResponseEmptyPage(t *testing.T) {
	body := searchResponse(&cibiz.SearchPage{Hits: []cibiz.SearchHit{}, Counter: map[string]int{}})
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"result":[]`) {
		t.Fatalf("encoded body = %s, want an empty result list", raw)
	}
	if strings.Contains(string(raw), `"facet"`) {
		t.Fatalf("encoded body = %s, want no facet key when none was asked for", raw)
	}
}

// --- routes ---

// Both of cantor's spellings are served, under GET and POST, because a stored
// expression or a bookmark may use either and the POST form is the one that can
// carry an expression too long for a request line.
func TestSearchRoutesAreRegistered(t *testing.T) {
	h := NewHandler(nil)
	r := chi.NewRouter()
	h.Register(r)

	var got []string
	_ = chi.Walk(r, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got = append(got, route)
		return nil
	})
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"/v1/cmdb/ci/s", "/v1/cmdb/ci/search",
		"/v1/cmdb/ci-types/{ref}/instances", "/v1/cmdb/instances/{id}",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("routes = %s\nmissing %s", joined, want)
		}
	}
}
