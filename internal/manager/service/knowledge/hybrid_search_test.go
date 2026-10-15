package knowledge

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/glebarez/sqlite"
	knowledgebiz "github.com/ongridio/ongrid/internal/manager/biz/knowledge"
	llmwikibiz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	llmwikistore "github.com/ongridio/ongrid/internal/manager/data/knowledge/llm_wiki/store"
	knowledgemodel "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// countingIndex stands in for the Wiki search index and records whether a query
// reached it at all. hits overrides the single default result when set.
type countingIndex struct {
	queries int
	hits    []llmwikibiz.SearchHit
}

func (i *countingIndex) IndexPage(context.Context, llmwikibiz.IndexDocument) error { return nil }
func (i *countingIndex) Clear(context.Context, uint64) error                       { return nil }

func (i *countingIndex) Search(context.Context, uint64, string, int) ([]llmwikibiz.SearchHit, error) {
	i.queries++
	if i.hits != nil {
		return i.hits, nil
	}
	return []llmwikibiz.SearchHit{{Layer: "wiki", PageID: "dns-overview", Title: "DNS", Preview: "body", Score: 1}}, nil
}

// recordingRaw records the raw search call it answered. hits overrides the
// single default result when set.
type recordingRaw struct {
	calls int
	opts  knowledgebiz.SearchOptions
	hits  []knowledgebiz.SearchHit
}

func (r *recordingRaw) Search(_ context.Context, _ string, opts knowledgebiz.SearchOptions) ([]knowledgebiz.SearchHit, error) {
	r.calls++
	r.opts = opts
	if r.hits != nil {
		return r.hits, nil
	}
	return []knowledgebiz.SearchHit{{Doc: &knowledgemodel.Doc{ID: 1, Title: "raw hit"}, Layer: "raw", Score: 1}}, nil
}

// wikiHitList builds n Wiki hits, already in the order the index ranked them.
func wikiHitList(n int) []llmwikibiz.SearchHit {
	hits := make([]llmwikibiz.SearchHit, 0, n)
	for i := 0; i < n; i++ {
		hits = append(hits, llmwikibiz.SearchHit{
			Layer:   "wiki",
			PageID:  fmt.Sprintf("wiki-%d", i),
			Title:   fmt.Sprintf("Wiki %d", i),
			Preview: "body",
			Score:   0.9,
		})
	}
	return hits
}

// rawHitList builds n raw hits, already in the order the vector search ranked them.
func rawHitList(n int) []knowledgebiz.SearchHit {
	hits := make([]knowledgebiz.SearchHit, 0, n)
	for i := 0; i < n; i++ {
		hits = append(hits, knowledgebiz.SearchHit{
			Doc:   &knowledgemodel.Doc{ID: uint64(i + 1), Title: fmt.Sprintf("Raw %d", i)},
			Layer: "raw",
			Score: 0.8,
		})
	}
	return hits
}

// newHybridSearcher wires a hybrid searcher over a real Wiki usecase backed by an
// in-memory store, so the Wiki branch behaves as it does in production.
func newHybridSearcher(t *testing.T, index *countingIndex) (*HybridSearcher, *recordingRaw) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	repo := llmwikistore.New(db)
	require.NoError(t, llmwikistore.Migrate(db))
	files, err := llmwikibiz.NewFileStore(t.TempDir())
	require.NoError(t, err)
	wiki, err := llmwikibiz.NewWithUsageRecorder(context.Background(), repo, files, stubCompilerLLM{}, index, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, err)

	raw := &recordingRaw{}
	return NewHybridSearcher(raw, wiki), raw
}

type stubCompilerLLM struct{}

func (stubCompilerLLM) Complete(context.Context, llm.ChatReq) (*llm.ChatResp, error) {
	return nil, nil
}
func (stubCompilerLLM) ModelVersion() string { return "stub" }

// TestHybridSearch_WithoutFiltersMergesWikiHits is the baseline the filtered case
// is contrasted against.
func TestHybridSearch_WithoutFiltersMergesWikiHits(t *testing.T) {
	index := &countingIndex{}
	searcher, raw := newHybridSearcher(t, index)

	hits, err := searcher.Search(context.Background(), "dns", knowledgebiz.SearchOptions{Limit: 10})

	require.NoError(t, err)
	assert.Equal(t, 1, index.queries)
	assert.Equal(t, 1, raw.calls)
	layers := make([]string, 0, len(hits))
	for _, hit := range hits {
		layers = append(layers, hit.Layer)
	}
	assert.Contains(t, layers, "wiki")
	assert.Contains(t, layers, "raw")
}

// TestHybridSearch_KeepsLayerScoresAndOrdersByFusion — the searcher orders the
// merged list by RRF, but it must not publish the rank value as the score. Rank
// values span 1.5/(60+rank) down to 1.0/(60+rank), so overwriting the layers'
// own relevance with one put every result inside a hundredth of the others: the
// Knowledge page rendered the whole top ten as "0.02", and the agent's knowledge
// prologue, which injects a playbook only above 0.6, stopped firing entirely.
func TestHybridSearch_KeepsLayerScoresAndOrdersByFusion(t *testing.T) {
	index := &countingIndex{}
	searcher, _ := newHybridSearcher(t, index)

	hits, err := searcher.Search(context.Background(), "dns", knowledgebiz.SearchOptions{Limit: 10})

	require.NoError(t, err)
	require.Len(t, hits, 2)
	// The interleave puts Wiki first at equal rank...
	assert.Equal(t, "wiki", hits[0].Layer)
	assert.Equal(t, "raw", hits[1].Layer)
	// ...and each hit still reports the relevance its own layer measured.
	for _, hit := range hits {
		assert.Equal(t, 1.0, hit.Score, "hybrid must not replace the layer's own score")
	}
}

// TestHybridSearch_InterleavesSoNeitherLayerIsCrowdedOut — a page of Wiki hits
// used to hide the raw knowledge base completely. The rank weights put every
// Wiki hit above every raw hit (1.5/61 > 1.0/61), so the operator playbooks —
// the evidence the Wiki is compiled from — were unreachable whenever the Wiki
// had enough pages to fill the result.
func TestHybridSearch_InterleavesSoNeitherLayerIsCrowdedOut(t *testing.T) {
	index := &countingIndex{hits: wikiHitList(10)}
	searcher, raw := newHybridSearcher(t, index)
	raw.hits = rawHitList(10)

	hits, err := searcher.Search(context.Background(), "dns", knowledgebiz.SearchOptions{Limit: 10})

	require.NoError(t, err)
	require.Len(t, hits, 10)
	for position, want := range []string{"wiki", "raw", "wiki", "raw", "wiki", "raw"} {
		assert.Equal(t, want, hits[position].Layer, "position %d", position)
	}
	layers := map[string]int{}
	for _, hit := range hits {
		layers[hit.Layer]++
	}
	assert.Equal(t, 5, layers["wiki"])
	assert.Equal(t, 5, layers["raw"])
}

// TestHybridSearch_ShortLayerHandsItsSlotsToTheOther — the interleave must not
// waste a slot on a layer that has no hit left for that rank.
func TestHybridSearch_ShortLayerHandsItsSlotsToTheOther(t *testing.T) {
	index := &countingIndex{hits: wikiHitList(2)}
	searcher, raw := newHybridSearcher(t, index)
	raw.hits = rawHitList(10)

	hits, err := searcher.Search(context.Background(), "dns", knowledgebiz.SearchOptions{Limit: 10})

	require.NoError(t, err)
	require.Len(t, hits, 10)
	layers := map[string]int{}
	for _, hit := range hits {
		layers[hit.Layer]++
	}
	assert.Equal(t, 2, layers["wiki"])
	assert.Equal(t, 8, layers["raw"])
}

// TestHybridSearch_WikiOnlySearchIsUnchanged — the interleave only shapes the
// merged hybrid result, not a caller that asked for one layer.
func TestHybridSearch_WikiOnlySearchIsUnchanged(t *testing.T) {
	index := &countingIndex{hits: wikiHitList(4)}
	searcher, raw := newHybridSearcher(t, index)

	hits, err := searcher.Search(context.Background(), "dns", knowledgebiz.SearchOptions{Limit: 10, Mode: "wiki"})

	require.NoError(t, err)
	require.Len(t, hits, 4)
	assert.Zero(t, raw.calls)
	for _, hit := range hits {
		assert.Equal(t, "wiki", hit.Layer)
	}
}

// TestHybridSearch_WithFiltersDropsWikiHits — a Wiki page is generated from
// sources rather than stored under a knowledge-base path and carries no tags, so
// a filtered query cannot be answered from the Wiki without returning pages the
// caller excluded. The Knowledge page sends path_prefix when a directory is
// selected, which used to mix in Wiki pages from anywhere.
func TestHybridSearch_WithFiltersDropsWikiHits(t *testing.T) {
	filters := map[string]knowledgebiz.SearchOptions{
		"path":        {Path: "docs/runbook.md"},
		"path prefix": {PathPrefix: "docs"},
		"tags":        {Tags: []string{"ops"}},
	}
	for name, opts := range filters {
		t.Run(name, func(t *testing.T) {
			index := &countingIndex{}
			searcher, raw := newHybridSearcher(t, index)
			opts.Limit = 10
			opts.Mode = "hybrid"

			hits, err := searcher.Search(context.Background(), "dns", opts)

			require.NoError(t, err)
			assert.Zero(t, index.queries, "a filtered query must not consult the Wiki index")
			assert.Equal(t, 1, raw.calls)
			require.Len(t, hits, 1)
			require.NotNil(t, hits[0].Doc)
			assert.Equal(t, "raw hit", hits[0].Doc.Title)
		})
	}
}

// TestHybridSearch_WikiOnlyWithFiltersReportsNoHits — the caller asked for Wiki
// pages only, so unfiltered Wiki hits would be wrong and raw hits would ignore
// the mode: an empty result is the honest answer.
func TestHybridSearch_WikiOnlyWithFiltersReportsNoHits(t *testing.T) {
	index := &countingIndex{}
	searcher, raw := newHybridSearcher(t, index)

	hits, err := searcher.Search(context.Background(), "dns", knowledgebiz.SearchOptions{Limit: 10, Mode: "wiki", PathPrefix: "docs"})

	require.NoError(t, err)
	assert.Empty(t, hits)
	assert.Zero(t, index.queries)
	assert.Zero(t, raw.calls)
}
