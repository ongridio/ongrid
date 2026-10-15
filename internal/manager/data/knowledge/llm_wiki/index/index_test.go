package index

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	store "github.com/ongridio/ongrid/internal/manager/data/knowledge/llm_wiki/store"
	"github.com/ongridio/ongrid/internal/pkg/embedding"
	"github.com/ongridio/ongrid/internal/pkg/qdrantx"
	"gorm.io/gorm"
)

type indexEmbedder struct {
	calls int
	err   error
}

func (*indexEmbedder) Dim() int { return 2 }

func (e *indexEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	return [][]float32{{1, 0}}, nil
}

// fakeVectorStore is a minimal in-memory stand-in for *qdrantx.Client.
type fakeVectorStore struct {
	points        map[uint64]qdrantx.SearchHit
	order         []uint64
	ensureErr     error
	searchErr     error
	upserts       int
	deletesByID   int
	deletesByFilt int
	lastSearch    qdrantx.SearchOpts
	lastFilter    map[string]any
}

func newFakeVectorStore() *fakeVectorStore {
	return &fakeVectorStore{points: map[uint64]qdrantx.SearchHit{}}
}

func (f *fakeVectorStore) EnsureCollection(context.Context, string, int) error { return f.ensureErr }

func (f *fakeVectorStore) EnsurePayloadIndex(context.Context, string, string, string) error {
	return nil
}

func (f *fakeVectorStore) Upsert(_ context.Context, _ string, points []qdrantx.Point) error {
	f.upserts++
	for _, point := range points {
		if _, exists := f.points[point.ID]; !exists {
			f.order = append(f.order, point.ID)
		}
		f.points[point.ID] = qdrantx.SearchHit{ID: point.ID, Payload: point.Payload}
	}
	return nil
}

func (f *fakeVectorStore) DeleteByID(_ context.Context, _ string, id uint64) error {
	f.deletesByID++
	delete(f.points, id)
	f.dropOrder(id)
	return nil
}

func (f *fakeVectorStore) DeleteByFilter(_ context.Context, _ string, must map[string]any) error {
	if len(must) == 0 {
		return errors.New("fake qdrant: empty filter")
	}
	f.deletesByFilt++
	f.lastFilter = must
	for id, point := range f.points {
		if testPayloadMatches(point.Payload, must) {
			delete(f.points, id)
			f.dropOrder(id)
		}
	}
	return nil
}

func (f *fakeVectorStore) GetPoints(_ context.Context, _ string, ids []uint64) ([]qdrantx.SearchHit, error) {
	out := make([]qdrantx.SearchHit, 0, len(ids))
	for _, id := range ids {
		if point, ok := f.points[id]; ok {
			out = append(out, point)
		}
	}
	return out, nil
}

func (f *fakeVectorStore) Search(_ context.Context, _ string, _ []float32, opts qdrantx.SearchOpts) ([]qdrantx.SearchHit, error) {
	f.lastSearch = opts
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	out := make([]qdrantx.SearchHit, 0, len(f.order))
	for rank, id := range f.order {
		point := f.points[id]
		if testPayloadMatches(point.Payload, opts.MustMatch) {
			point.Score = 1 / float64(rank+1)
			out = append(out, point)
		}
	}
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func (f *fakeVectorStore) Scroll(_ context.Context, _ string, opts qdrantx.ScrollOpts) (*qdrantx.ScrollResult, error) {
	out := make([]qdrantx.SearchHit, 0, len(f.order))
	for _, id := range f.order {
		point := f.points[id]
		if testPayloadMatches(point.Payload, opts.MustMatch) {
			out = append(out, point)
		}
	}
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return &qdrantx.ScrollResult{Points: out}, nil
}

func (f *fakeVectorStore) dropOrder(id uint64) {
	kept := f.order[:0]
	for _, existing := range f.order {
		if existing != id {
			kept = append(kept, existing)
		}
	}
	f.order = kept
}

func testPayloadMatches(payload map[string]any, must map[string]any) bool {
	for key, want := range must {
		got, ok := payload[key]
		if !ok || fmt.Sprint(got) != fmt.Sprint(want) {
			return false
		}
	}
	return true
}

func testIndex(t *testing.T, vec VectorStore, embedder embedding.Embedder, dimension int) (*Index, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	idx, err := New(context.Background(), db, vec, embedder, dimension, nil)
	if err != nil {
		t.Fatal(err)
	}
	return idx, db
}

func TestIndex_SearchesDocumentsAndIsolatesTenant(t *testing.T) {
	idx, _ := testIndex(t, nil, nil, 0)
	documents := []biz.IndexDocument{
		{TenantID: 1, PageID: "dns", PageType: "generated", Title: "DNS 排障", Content: "检查解析超时"},
		{TenantID: 2, PageID: "dns-other", PageType: "generated", Title: "DNS", Content: "另一个租户的解析文档"},
	}
	for _, document := range documents {
		if err := idx.IndexPage(context.Background(), document); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := idx.Search(context.Background(), 1, "解析", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].PageID != "dns" {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestIndex_ClearRemovesOnlyRequestedTenant(t *testing.T) {
	vec := newFakeVectorStore()
	idx, _ := testIndex(t, vec, &indexEmbedder{}, 2)
	for _, document := range []biz.IndexDocument{
		{TenantID: 1, PageID: "one", PageType: "generated", Title: "One", Content: "shared keyword"},
		{TenantID: 2, PageID: "two", PageType: "generated", Title: "Two", Content: "shared keyword"},
	} {
		if err := idx.IndexPage(context.Background(), document); err != nil {
			t.Fatal(err)
		}
	}
	if err := idx.Clear(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if vec.deletesByFilt != 1 {
		t.Fatalf("qdrant clear calls = %d", vec.deletesByFilt)
	}
	if vec.lastFilter[payloadTenantID] != "1" {
		t.Fatalf("clear filter = %+v", vec.lastFilter)
	}
	hits, err := idx.Search(context.Background(), 1, "keyword", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("cleared tenant hits = %+v", hits)
	}
	hits, err = idx.Search(context.Background(), 2, "keyword", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("other tenant lost its index entry")
	}
}

func TestIndex_TreatsLikeWildcardsLiterally(t *testing.T) {
	idx, _ := testIndex(t, nil, nil, 0)
	for _, document := range []biz.IndexDocument{
		{TenantID: 1, PageID: "percent", PageType: "generated", Title: "Disk", Content: "disk 50% full"},
		{TenantID: 1, PageID: "plain", PageType: "generated", Title: "Disk", Content: "disk cpu usage"},
	} {
		if err := idx.IndexPage(context.Background(), document); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := idx.Search(context.Background(), 1, "50%", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].PageID != "percent" {
		t.Fatalf("literal wildcard hits = %+v", hits)
	}
	hits, err = idx.Search(context.Background(), 1, "c_u", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("underscore wildcard hits = %+v", hits)
	}
}

func TestIndex_ReusesBodyHashAndDropsStaleVectorOnEmbeddingFailure(t *testing.T) {
	embedder := &indexEmbedder{}
	vec := newFakeVectorStore()
	idx, _ := testIndex(t, vec, embedder, 2)
	document := biz.IndexDocument{TenantID: 0, PageID: "hash-page", PageType: "generated", Title: "Hash", Content: "稳定正文"}
	if err := idx.IndexPage(context.Background(), document); err != nil {
		t.Fatal(err)
	}
	if err := idx.IndexPage(context.Background(), document); err != nil {
		t.Fatal(err)
	}
	if embedder.calls != 1 {
		t.Fatalf("embedding calls = %d; want body-hash reuse", embedder.calls)
	}
	if vec.upserts != 1 {
		t.Fatalf("qdrant upserts = %d; want 1", vec.upserts)
	}
	embedder.err = errors.New("embedding offline")
	document.Content = "更新后的正文"
	if err := idx.IndexPage(context.Background(), document); err == nil {
		t.Fatal("embedding failure was ignored")
	}
	if len(vec.points) != 0 {
		t.Fatalf("stale vector count = %d", len(vec.points))
	}
	if vec.deletesByID != 1 {
		t.Fatalf("qdrant delete calls = %d; want 1", vec.deletesByID)
	}
	hits, err := idx.searchLexical(context.Background(), 0, "更新", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].PageID != document.PageID {
		t.Fatalf("lexical fallback hits = %+v", hits)
	}
}

func TestIndex_VectorSearchFiltersTenantAndEnrichesFromLexical(t *testing.T) {
	vec := newFakeVectorStore()
	idx, _ := testIndex(t, vec, &indexEmbedder{}, 2)
	for _, document := range []biz.IndexDocument{
		{TenantID: 1, PageID: "dns", PageType: "generated", Title: "DNS 排障", Content: "检查解析超时"},
		{TenantID: 2, PageID: "dns-other", PageType: "generated", Title: "另一个租户", Content: "另一个租户的文档"},
	} {
		if err := idx.IndexPage(context.Background(), document); err != nil {
			t.Fatal(err)
		}
	}
	// "zzz" matches no lexical row, so every hit must come from the vector leg.
	hits, err := idx.Search(context.Background(), 1, "zzz", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].PageID != "dns" {
		t.Fatalf("vector hits = %+v", hits)
	}
	if hits[0].Title != "DNS 排障" || hits[0].Preview == "" || hits[0].PageType != "generated" {
		t.Fatalf("vector hit not enriched from lexical row: %+v", hits[0])
	}
	if vec.lastSearch.MustMatch[payloadTenantID] != "1" {
		t.Fatalf("search filter = %+v", vec.lastSearch.MustMatch)
	}
}

func TestIndex_VectorFailureIsNotMaskedByLexicalResults(t *testing.T) {
	vec := newFakeVectorStore()
	idx, _ := testIndex(t, vec, &indexEmbedder{}, 2)
	document := biz.IndexDocument{TenantID: 1, PageID: "dns", PageType: "generated", Title: "DNS", Content: "解析超时"}
	if err := idx.IndexPage(context.Background(), document); err != nil {
		t.Fatal(err)
	}
	vec.searchErr = errors.New("qdrant down")
	if _, err := idx.Search(context.Background(), 1, "DNS", 10); err == nil {
		t.Fatal("vector failure was masked by lexical results")
	}
}

func TestIndex_RequiresQdrantWhenEmbeddingConfigured(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), db, nil, &indexEmbedder{}, 2, nil); err == nil {
		t.Fatal("missing qdrant client was accepted with an embedder")
	}
}

func TestIndex_EnsureCollectionFailureIsFatal(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	vec := newFakeVectorStore()
	vec.ensureErr = errors.New("qdrant unreachable")
	if _, err := New(context.Background(), db, vec, &indexEmbedder{}, 2, nil); err == nil {
		t.Fatal("qdrant ensure failure was ignored")
	}
}

func TestIndex_PointIDIsStableAndTenantScoped(t *testing.T) {
	first := pointID(1, "dns")
	if first != pointID(1, "dns") {
		t.Fatal("point id is not stable")
	}
	if first == pointID(2, "dns") || first == pointID(1, "other") {
		t.Fatal("point id is not scoped to tenant and page")
	}
}

func TestIndex_HasVectorsReportsTenantState(t *testing.T) {
	vec := newFakeVectorStore()
	idx, _ := testIndex(t, vec, &indexEmbedder{}, 2)
	has, err := idx.HasVectors(context.Background(), 1)
	if err != nil || has {
		t.Fatalf("empty store has vectors=%v err=%v", has, err)
	}
	if err := idx.IndexPage(context.Background(), biz.IndexDocument{TenantID: 1, PageID: "dns", PageType: "generated", Title: "DNS", Content: "解析超时"}); err != nil {
		t.Fatal(err)
	}
	has, err = idx.HasVectors(context.Background(), 1)
	if err != nil || !has {
		t.Fatalf("populated store has vectors=%v err=%v", has, err)
	}
}

// sameScore compares a reported relevance against the value a leg produced.
func sameScore(got, want float64) bool {
	diff := got - want
	return diff < 1e-9 && diff > -1e-9
}

// TestMergeRRF_OrdersByFusionAndReportsRelevance pins the score contract of a
// Wiki search: RRF decides the order, but Score stays the relevance the leg
// produced. Publishing 1/(60+rank) instead made every hit report ~0.016 — a
// uniform "0.02" on the Knowledge page, and a silent fail against the 0.6 gate
// the agent's knowledge prologue applies.
func TestMergeRRF_OrdersByFusionAndReportsRelevance(t *testing.T) {
	lexical := []biz.SearchHit{
		{PageID: "body-only", Title: "unrelated", Score: 1.0 / 3},
		{PageID: "both", Title: "unrelated", Score: 1.0 / 3},
	}
	vector := []biz.SearchHit{
		{PageID: "both", Title: "unrelated", Score: 0.82},
		{PageID: "vector-only", Title: "unrelated", Score: 0.71},
	}

	hits := mergeRRF("needle", lexical, vector, 10)

	if len(hits) != 3 {
		t.Fatalf("hits = %+v", hits)
	}
	// The page both legs agree on wins the fusion, and the cosine the vector leg
	// measured is what callers get to see.
	if hits[0].PageID != "both" || !sameScore(hits[0].Score, 0.82) {
		t.Fatalf("top hit = %+v, want both at its vector cosine", hits[0])
	}
	byID := make(map[string]float64, len(hits))
	for _, hit := range hits {
		byID[hit.PageID] = hit.Score
	}
	if !sameScore(byID["body-only"], 1.0/3) {
		t.Fatalf("lexical-only score = %v, want the match rank tier", byID["body-only"])
	}
	if !sameScore(byID["vector-only"], 0.71) {
		t.Fatalf("vector-only score = %v, want the cosine", byID["vector-only"])
	}
}

// TestMergeRRF_TitleBonusOrdersButDoesNotInflateScore — the title match bonus is
// an ordering nudge. Adding it to Score, as the old code did, reported a
// relevance the search never measured.
func TestMergeRRF_TitleBonusOrdersButDoesNotInflateScore(t *testing.T) {
	lexical := []biz.SearchHit{{PageID: "titled", Title: "DNS 排障", Score: 1.0 / 3}}
	vector := []biz.SearchHit{{PageID: "other", Title: "别的东西", Score: 0.9}}

	hits := mergeRRF("DNS", lexical, vector, 10)

	if len(hits) != 2 || hits[0].PageID != "titled" {
		t.Fatalf("hits = %+v, want the title match first", hits)
	}
	if !sameScore(hits[0].Score, 1.0/3) {
		t.Fatalf("score = %v, want the lexical tier without the ordering bonus", hits[0].Score)
	}
}
