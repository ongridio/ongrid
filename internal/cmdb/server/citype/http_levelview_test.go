package citype

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// --- the double ---

type levelStub struct {
	*attrStub

	views []citypemodel.PreferenceTreeView
	saved *citypebiz.LevelViewSaveSpec
}

func (s *levelStub) LevelViews(_ context.Context, uid model.ID) ([]citypemodel.PreferenceTreeView, error) {
	var out []citypemodel.PreferenceTreeView
	for _, v := range s.views {
		if v.UID == uid {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *levelStub) LevelView(_ context.Context, uid, typeID model.ID) (*citypemodel.PreferenceTreeView, error) {
	for i, v := range s.views {
		if v.UID == uid && v.TypeID == typeID {
			return &s.views[i], nil
		}
	}
	return nil, nil
}

func (s *levelStub) SaveLevelView(_ context.Context, in citypebiz.LevelViewSaveSpec) (*citypemodel.PreferenceTreeView, error) {
	s.saved = &in
	return &citypemodel.PreferenceTreeView{UID: in.UID, TypeID: in.TypeID, Levels: citypemodel.IDList(in.Levels)}, nil
}

func (s *levelStub) DeleteLevelView(context.Context, model.ID, model.ID) error { return nil }

// levelFixture is one model with two attributes, the second addressable only by
// its alias so the resolver is exercised.
func levelFixture(t *testing.T) *levelStub {
	t.Helper()
	stub := &levelStub{attrStub: newAttrStub()}

	ct := citypemodel.CIType{Name: "host", Alias: "主机", UniqueID: 1, Enabled: true}
	ct.ID = 10
	stub.types = []citypemodel.CIType{ct}
	stub.aliasOf[10] = "主机"

	hostname := citypemodel.Attribute{Name: "hostname", Alias: "主机名", ValueType: "2"}
	hostname.ID = 1
	cpu := citypemodel.Attribute{Name: "cpu", Alias: "核数", ValueType: "0"}
	cpu.ID = 2
	stub.attrs[1] = hostname
	stub.attrs[2] = cpu
	stub.byName["hostname"] = 1
	stub.byName["cpu"] = 2
	stub.aliases["主机名"] = 1
	stub.aliases["核数"] = 2
	stub.bindings = []citypemodel.CITypeAttribute{
		{TypeID: 10, AttrID: 1, Order: 0},
		{TypeID: 10, AttrID: 2, Order: 1},
	}
	return stub
}

// --- routes ---

// Levels may be sent as names, as ids, or MIXED, and a JSON body carries an id
// as a number while the console sends a name as a string. cantor read the same
// parameter through its form handler, where a number always arrived as text; a
// port that accepted only strings would reject half of what the console sends.
func TestSetTreeLevelsAcceptsMixedReferenceForms(t *testing.T) {
	stub := levelFixture(t)
	router := attrRouter(t, stub, roleAdmin, false, 7)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(http.MethodPut,
		"/v1/cmdb/preferences/ci-types/host/tree-levels",
		`{"levels":["hostname","核数",1]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if stub.saved == nil || stub.saved.TypeID != 10 {
		t.Fatalf("saved = %+v", stub.saved)
	}
	if len(stub.saved.Levels) != 3 || stub.saved.Levels[1] != 2 {
		t.Fatalf("levels = %v, want hostname, cpu, hostname", stub.saved.Levels)
	}
	if stub.saved.UID != 7 {
		t.Fatalf("uid = %d, want the caller's", stub.saved.UID)
	}

	var body citypebiz.TreeLevelView
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TypeID != 10 || len(body.Levels) != 3 || body.Levels[1].Name != "cpu" {
		t.Fatalf("body = %+v", body)
	}
}

// A body with no levels at all clears the view, which is how the console
// expresses "stop customising this model".
func TestSetTreeLevelsWithNoLevelsClears(t *testing.T) {
	stub := levelFixture(t)
	router := attrRouter(t, stub, roleAdmin, false, 7)

	for _, payload := range []string{`{}`, `{"levels":[]}`, `{"levels":[""]}`} {
		stub.saved = nil
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, jsonRequest(http.MethodPut,
			"/v1/cmdb/preferences/ci-types/host/tree-levels", payload))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %s", payload, rec.Code, rec.Body.String())
		}
		if stub.saved == nil || len(stub.saved.Levels) != 0 {
			t.Fatalf("%s: saved = %+v, want an empty write", payload, stub.saved)
		}
	}
}

// A level the model does not carry is the caller's mistake, so it is a 400 and
// not a 500 — cantor stored the typo verbatim and dropped it at read time.
func TestSetTreeLevelsRejectsAnUnknownLevel(t *testing.T) {
	stub := levelFixture(t)
	router := attrRouter(t, stub, roleAdmin, false, 7)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(http.MethodPut,
		"/v1/cmdb/preferences/ci-types/host/tree-levels", `{"levels":["nope"]}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if stub.saved != nil {
		t.Fatal("a refused write reached the repository")
	}
}

// A level entry that is neither text nor a number is refused rather than
// silently dropped.
func TestSetTreeLevelsRejectsAMalformedLevel(t *testing.T) {
	stub := levelFixture(t)
	router := attrRouter(t, stub, roleAdmin, false, 7)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, jsonRequest(http.MethodPut,
		"/v1/cmdb/preferences/ci-types/host/tree-levels", `{"levels":[{"name":"hostname"}]}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func TestListTreeLevelsReturnsTheCallersViews(t *testing.T) {
	stub := levelFixture(t)
	row := citypemodel.PreferenceTreeView{UID: 7, TypeID: 10, Levels: citypemodel.IDList{1}}
	row.ID = 55
	stub.views = []citypemodel.PreferenceTreeView{row}
	// Another user's row must not appear.
	other := citypemodel.PreferenceTreeView{UID: 8, TypeID: 10, Levels: citypemodel.IDList{1}}
	stub.views = append(stub.views, other)

	router := attrRouter(t, stub, roleAdmin, false, 7)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/cmdb/preferences/tree-levels", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body []citypebiz.TreeLevelView
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 1 || body[0].ID != 55 || body[0].Name != "host" {
		t.Fatalf("body = %+v", body)
	}
	if len(body[0].Levels) != 1 || body[0].Levels[0].Name != "hostname" {
		t.Fatalf("levels = %+v", body[0].Levels)
	}
}

// jsonRequest builds a JSON request with an empty body allowed.
func jsonRequest(method, path, payload string) *http.Request {
	return httptest.NewRequest(method, path, strings.NewReader(payload))
}

// The stub must satisfy the whole port; asserted here so a future addition to
// Repo fails this package's build rather than a call site's.
var _ citypebiz.Repo = (*levelStub)(nil)
