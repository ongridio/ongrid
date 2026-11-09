package ci

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

// The relation routes' wire contract: how the hierarchy parameters are read and
// that the routes exist. What a hierarchy MEANS is settled three packages down
// and tested there against cantor's own answers.

// `level` is a LIST OF LEVELS TO RETURN, not a depth. cantor split it on commas
// through its argument handler, so `level=1,3` means levels 1 and 3 — it walks
// to 3 and returns 1 and 3.
func TestLevelParams(t *testing.T) {
	cases := []struct {
		raw  string
		want []int
	}{
		{"", nil},
		{"1", []int{1}},
		{"1,2,3", []int{1, 2, 3}},
		{"1,3", []int{1, 3}},
		{" 2 , 1 ", []int{2, 1}},
		{"1;3", []int{1, 3}},
		{"1,,2", []int{1, 2}},
	}
	for _, tc := range cases {
		got, err := levelParams(tc.raw)
		if err != nil {
			t.Fatalf("levelParams(%q) error = %v", tc.raw, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("levelParams(%q) = %v, want %v", tc.raw, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("levelParams(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		}
	}

	// Only the SYNTAX is checked here. Whether a level is in range is the
	// walk's decision — the bound is the walk's to enforce, and duplicating it
	// at the edge would be two places to change and one of them to forget.
	// `-1` parses, and Usecase.WalkHierarchy refuses it.
	for _, bad := range []string{"x", "1,x"} {
		if _, err := levelParams(bad); err == nil {
			t.Fatalf("levelParams(%q) was accepted", bad)
		}
	}
	if got, err := levelParams("-1"); err != nil || len(got) != 1 || got[0] != -1 {
		t.Fatalf("levelParams(-1) = %v, %v; the bound belongs to the walk", got, err)
	}
}

// root_id and root_ids are the same parameter: cantor spelled it singular on the
// hierarchy route and the DCIM page still sends it that way.
func TestIDListParam(t *testing.T) {
	got, err := idListParam("1, 2 ,3")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("ids = %v", got)
	}

	empty, err := idListParam("  ")
	if err != nil || empty != nil {
		t.Fatalf("an empty list = %v, %v", empty, err)
	}

	for _, bad := range []string{"x", "0", "-1", "1,x"} {
		if _, err := idListParam(bad); err == nil {
			t.Fatalf("ids %q were accepted", bad)
		}
	}
}

// cantor's boolean convention, which the console still sends as `1`.
func TestBoolParam(t *testing.T) {
	for _, yes := range []string{"1", "true", "TRUE", "yes", "Y", "on"} {
		if !boolParam(yes) {
			t.Fatalf("%q read as false", yes)
		}
	}
	for _, no := range []string{"", "0", "false", "no", "nonsense"} {
		if boolParam(no) {
			t.Fatalf("%q read as true", no)
		}
	}
}

// An empty body is the common case for the pair endpoints: the pair is in the
// path and the body carries only the optional ancestor path.
func TestDecodeRelationToleratesAnEmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/cmdb/ci-relations/1/2", nil)
	got, err := decodeRelation(req)
	if err != nil {
		t.Fatalf("an empty body error = %v", err)
	}
	if got.AncestorIDs != "" || got.RelationTypeID != 0 {
		t.Fatalf("decoded %+v from an empty body", got)
	}

	withBody := httptest.NewRequest(http.MethodPost, "/x",
		strings.NewReader(`{"ancestor_ids":" 7,8 ","relation_type_id":5}`))
	got, err = decodeRelation(withBody)
	if err != nil {
		t.Fatal(err)
	}
	if got.AncestorIDs != "7,8" || got.RelationTypeID != model.ID(5) {
		t.Fatalf("decoded %+v", got)
	}

	broken := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{nope"))
	if _, err := decodeRelation(broken); err == nil {
		t.Fatal("a malformed body was accepted")
	}
}

// Both the hierarchy search and the two neighbour routes are registered, and the
// delete-by-id route does not shadow them.
//
// THE THREE STATIC PATHS ARE THE INTERESTING ONES. `/search/full` sits below
// `/search`, and `/path/s` occupies BOTH segments of the `{first}/{second}`
// route — chi resolves a static segment ahead of a parameter, and this asserts
// that it does, because a shadowed route would answer a path search with a
// relation write.
func TestRelationRoutesAreRegistered(t *testing.T) {
	h := NewHandler(nil)
	r := chi.NewRouter()
	h.Register(r)

	var got []string
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got = append(got, method+" "+route)
		return nil
	})
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"GET /v1/cmdb/ci-relations/search",
		"GET /v1/cmdb/ci-relations/search/full",
		"GET /v1/cmdb/ci-relations/statistics",
		"POST /v1/cmdb/ci-relations/path/s",
		"GET /v1/cmdb/ci-relations/{id}/children",
		"GET /v1/cmdb/ci-relations/{id}/parents",
		"POST /v1/cmdb/ci-relations/batch",
		"POST /v1/cmdb/ci-relations/{first}/{second}",
		"DELETE /v1/cmdb/ci-relations/{first}/{second}",
		"DELETE /v1/cmdb/ci-relations/{id}",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("routes:\n%s\nmissing %s", joined, want)
		}
	}
}

// The static routes WIN over the parameterised ones they share a shape with.
func TestStaticRoutesAreNotShadowedByParameters(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Register(r)

	for _, tc := range []struct {
		method, path string
		wantPattern  string
	}{
		{http.MethodGet, "/v1/cmdb/ci-relations/search/full", "/v1/cmdb/ci-relations/search/full"},
		{http.MethodGet, "/v1/cmdb/ci-relations/statistics", "/v1/cmdb/ci-relations/statistics"},
		{http.MethodPost, "/v1/cmdb/ci-relations/path/s", "/v1/cmdb/ci-relations/path/s"},
	} {
		routeCtx := chi.NewRouteContext()
		if !r.Match(routeCtx, tc.method, tc.path) {
			t.Fatalf("%s %s did not match any route", tc.method, tc.path)
		}
		if got := routeCtx.RoutePattern(); got != tc.wantPattern {
			t.Fatalf("%s %s matched %s, want %s", tc.method, tc.path, got, tc.wantPattern)
		}
	}
}

// The statistics response is FLAT, with the roots' counts as siblings of
// `detail` — which is what the console's `stats[String(id)]` read requires.
func TestStatisticsBodyIsFlat(t *testing.T) {
	got := statisticsBody(&cibiz.Statistics{
		Counts: map[string]int64{"9": 3, "12": 0},
		Detail: map[string]map[string]int64{"9": {"7": 3}, "12": {}},
	})

	if got["9"] != int64(3) || got["12"] != int64(0) {
		t.Fatalf("top level = %v; the counts must not be nested", got)
	}
	detail, ok := got["detail"].(map[string]any)
	if !ok {
		t.Fatalf("detail = %T", got["detail"])
	}
	perRoot, ok := detail["9"].(map[string]any)
	if !ok || perRoot["7"] != int64(3) {
		t.Fatalf("detail[9] = %v", detail["9"])
	}
	if _, has := got["counts"]; has {
		t.Fatalf("a nested counts key leaked: %v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"9":3`) {
		t.Fatalf("marshalled = %s", raw)
	}
}

// The path search's body tolerates the console's `page_size` spelling as well
// as `count`, and its numbers may arrive as JSON numbers.
func TestPathSearchBodyReadsBothPageSizeNames(t *testing.T) {
	for _, body := range []string{
		`{"source":{"type_id":8},"path":[[8,9,11]],"count":25}`,
		`{"source":{"type_id":8},"path":[[8,9,11]],"page_size":25}`,
	} {
		var req pathSearchBody
		dec := json.NewDecoder(strings.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&req); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
		if req.Source.TypeID != 8 || len(req.Path) != 1 || len(req.Path[0]) != 3 {
			t.Fatalf("decoded %+v from %s", req, body)
		}
	}
}
