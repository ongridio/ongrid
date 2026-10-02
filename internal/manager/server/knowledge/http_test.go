package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/qdrantx"
)

// ---- in-memory qdrant ----
//
// memVec is a faithful-enough stand-in for *qdrantx.Client: it stores
// points by id and, crucially, JSON round-trips every payload on Upsert so
// numbers come back as float64 and arrays as []any — exactly what the real
// HTTP client decodes. That makes the biz dedupe/filter helpers
// (chunkIndexFromPayload, docIDAlias, MustMatch matching) run the same code
// paths they do against live qdrant. Without the round-trip the e2e would
// silently exercise types that never occur in production.
type memVec struct {
	order  []uint64
	points map[uint64]qdrantx.SearchHit
}

func newMemVec() *memVec { return &memVec{points: map[uint64]qdrantx.SearchHit{}} }

func (m *memVec) count() int { return len(m.points) }

func (m *memVec) EnsureCollection(context.Context, string, int) error              { return nil }
func (m *memVec) EnsurePayloadIndex(context.Context, string, string, string) error { return nil }

func (m *memVec) Upsert(_ context.Context, _ string, pts []qdrantx.Point) error {
	for _, p := range pts {
		// Mimic the JSON encode/decode the real client does, so payload
		// value types match production (float64 / []any).
		raw, err := json.Marshal(p.Payload)
		if err != nil {
			return err
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
		if _, exists := m.points[p.ID]; !exists {
			m.order = append(m.order, p.ID)
		}
		m.points[p.ID] = qdrantx.SearchHit{ID: p.ID, Payload: payload}
	}
	return nil
}

func (m *memVec) DeleteByFilter(_ context.Context, _ string, must map[string]any) error {
	if len(must) == 0 {
		return fmt.Errorf("memVec: refusing empty DeleteByFilter")
	}
	for id, p := range m.points {
		if payloadMatches(p.Payload, must) {
			delete(m.points, id)
			m.dropOrder(id)
		}
	}
	return nil
}

func (m *memVec) DeleteByID(_ context.Context, _ string, id uint64) error {
	delete(m.points, id)
	m.dropOrder(id)
	return nil
}

func (m *memVec) GetPoints(_ context.Context, _ string, ids []uint64) ([]qdrantx.SearchHit, error) {
	out := make([]qdrantx.SearchHit, 0, len(ids))
	for _, id := range ids {
		if p, ok := m.points[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memVec) Search(_ context.Context, _ string, _ []float32, opts qdrantx.SearchOpts) ([]qdrantx.SearchHit, error) {
	out := m.matching(opts.MustMatch)
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func (m *memVec) Scroll(_ context.Context, _ string, opts qdrantx.ScrollOpts) (*qdrantx.ScrollResult, error) {
	out := m.matching(opts.MustMatch)
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return &qdrantx.ScrollResult{Points: out}, nil
}

// matching returns stored points (insertion order) whose payload satisfies
// every MustMatch clause. Empty filter returns everything.
func (m *memVec) matching(must map[string]any) []qdrantx.SearchHit {
	out := make([]qdrantx.SearchHit, 0, len(m.order))
	for _, id := range m.order {
		p := m.points[id]
		if payloadMatches(p.Payload, must) {
			out = append(out, p)
		}
	}
	return out
}

func (m *memVec) dropOrder(id uint64) {
	for i, v := range m.order {
		if v == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

// payloadMatches mirrors qdrantx.buildFilter: scalar want = exact (or
// array-contains for array fields like tags / path_prefixes); slice want =
// match.any.
func payloadMatches(payload, must map[string]any) bool {
	for k, want := range must {
		if !fieldMatches(payload[k], want) {
			return false
		}
	}
	return true
}

func fieldMatches(have, want any) bool {
	switch w := want.(type) {
	case []string:
		for _, x := range w {
			if scalarOrContains(have, x) {
				return true
			}
		}
		return false
	case []any:
		for _, x := range w {
			if scalarOrContains(have, x) {
				return true
			}
		}
		return false
	default:
		return scalarOrContains(have, want)
	}
}

// scalarOrContains compares via fmt.Sprint so uint64 want matches a float64
// payload value (post-JSON), and a scalar want matches an array payload if
// the array contains it.
func scalarOrContains(have, want any) bool {
	ws := fmt.Sprint(want)
	switch h := have.(type) {
	case nil:
		return false
	case []any:
		for _, e := range h {
			if fmt.Sprint(e) == ws {
				return true
			}
		}
		return false
	default:
		return fmt.Sprint(h) == ws
	}
}

// ---- deterministic embedder ----

type idEmbed struct{}

func (idEmbed) Dim() int { return 8 }
func (idEmbed) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, 8) // ranking is irrelevant for these e2e checks
	}
	return out, nil
}

// ---- harness ----

func newE2E(t *testing.T) (http.Handler, *memVec) {
	t.Helper()
	store := newMemVec()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	uc, err := biz.New(context.Background(), nil, store, idEmbed{}, t.TempDir(), log)
	if err != nil {
		t.Fatalf("biz.New: %v", err)
	}
	r := chi.NewRouter()
	NewHandler(uc).Register(r)
	return r, store
}

type mixedSearchStub struct {
	got biz.SearchOptions
}

func (s *mixedSearchStub) Search(_ context.Context, _ string, opts biz.SearchOptions) ([]biz.SearchHit, error) {
	s.got = opts
	return []biz.SearchHit{
		{Doc: &model.Doc{ID: 1, SourceType: "wiki", Title: "RAG Wiki", Content: "wiki preview"}, Score: 0.9, Layer: "wiki", PageType: "topic", PageID: "topic-rag", SourceVersionID: "7", MatchedNode: "HNSW"},
		{Doc: &model.Doc{ID: 2, SourceType: "upload", Title: "RAG Source", Content: "raw preview"}, Score: 0.8, Layer: "raw"},
	}, nil
}

func TestSearch_ReturnsMixedHitMetadata(t *testing.T) {
	store := newMemVec()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	uc, err := biz.New(context.Background(), nil, store, idEmbed{}, t.TempDir(), log)
	if err != nil {
		t.Fatalf("biz.New: %v", err)
	}
	searcher := &mixedSearchStub{}
	handler := NewHandler(uc)
	handler.SetSearchService(searcher)
	router := chi.NewRouter()
	handler.Register(router)

	rec := req(t, router, http.MethodGet, "/v1/knowledge/search?q=rag&limit=2&mode=hybrid", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Doc             docDTO `json:"doc"`
			Layer           string `json:"layer"`
			PageID          string `json:"page_id"`
			SourceVersionID string `json:"source_version_id"`
			MatchedNode     string `json:"matched_node"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Items[0].Layer != "wiki" || body.Items[0].PageID != "topic-rag" || body.Items[0].SourceVersionID != "7" || body.Items[0].MatchedNode != "HNSW" || body.Items[1].Layer != "raw" {
		t.Fatalf("mixed search response = %+v", body.Items)
	}
	if searcher.got.Mode != "hybrid" || searcher.got.Limit != 2 {
		t.Fatalf("search options = %+v", searcher.got)
	}
}

// req fires one request and returns the recorder. body==nil sends no body;
// a string body is sent verbatim with the given content type.
func req(t *testing.T, router http.Handler, method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	return rec
}

func jsonReq(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	return req(t, router, method, path, "application/json", rdr)
}

func decodeDoc(t *testing.T, rec *httptest.ResponseRecorder) docDTO {
	t.Helper()
	var d docDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode doc (%s): %v", rec.Body.String(), err)
	}
	return d
}

func decodeDocs(t *testing.T, rec *httptest.ResponseRecorder) []docDTO {
	t.Helper()
	var env struct {
		Items []docDTO `json:"items"`
		Total int      `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode docs (%s): %v", rec.Body.String(), err)
	}
	return env.Items
}

func idStr(id uint64) string { return strconv.FormatUint(id, 10) }

// seedPoint inserts a raw point (used to plant vault/repo docs the public
// API can't create, so we can assert they stay read-only).
func (m *memVec) seedPoint(id uint64, payload map[string]any) {
	_ = m.Upsert(context.Background(), "", []qdrantx.Point{{ID: id, Payload: payload}})
}

var _ = model.SourceManual // keep model import referenced for source-type tests
