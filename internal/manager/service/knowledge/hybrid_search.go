// Package knowledge contains application services that compose the
// operator knowledge base with the compiled LLM Wiki.
package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"

	knowledgebiz "github.com/ongridio/ongrid/internal/manager/biz/knowledge"
	llmwikibiz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	knowledgemodel "github.com/ongridio/ongrid/internal/manager/model/knowledge"
)

// RawSearcher is the part of the operator knowledge base needed by the
// hybrid search service.
type RawSearcher interface {
	Search(ctx context.Context, query string, opts knowledgebiz.SearchOptions) ([]knowledgebiz.SearchHit, error)
}

// HybridSearcher merges compiled Wiki hits with the existing Raw knowledge
// base. It is shared by the Agent tool and the Knowledge HTTP API.
type HybridSearcher struct {
	raw  RawSearcher
	wiki *llmwikibiz.Usecase
}

// NewHybridSearcher creates a searcher. wiki may be nil when the LLM Wiki
// feature is disabled; Raw search remains available in that case.
func NewHybridSearcher(raw RawSearcher, wiki *llmwikibiz.Usecase) *HybridSearcher {
	return &HybridSearcher{raw: raw, wiki: wiki}
}

// hasSearchFilters reports whether the caller restricted the search to a
// knowledge-base path or to a set of tags.
func hasSearchFilters(opts knowledgebiz.SearchOptions) bool {
	return strings.TrimSpace(opts.Path) != "" ||
		strings.TrimSpace(opts.PathPrefix) != "" ||
		len(opts.Tags) > 0
}

// Search implements the application-level hybrid retrieval policy.
func (s *HybridSearcher) Search(ctx context.Context, query string, opts knowledgebiz.SearchOptions) ([]knowledgebiz.SearchHit, error) {
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	if mode == "" {
		mode = "hybrid"
	}
	if mode == "rag" || s.wiki == nil {
		opts.Mode = ""
		return s.raw.Search(ctx, query, opts)
	}

	// A Wiki page is generated from sources, not stored under a knowledge-base
	// path, and carries no tags — so a Wiki hit cannot be checked against Path,
	// PathPrefix or Tags. Returning one anyway would smuggle in a page the caller
	// explicitly excluded, so a filtered query never merges Wiki hits: hybrid and
	// rag answer from the raw knowledge base, and a wiki-only request reports no
	// hits rather than unfiltered ones.
	if hasSearchFilters(opts) {
		if mode == "wiki" {
			return nil, nil
		}
		opts.Mode = ""
		return s.raw.Search(ctx, query, opts)
	}

	wikiHits, wikiErr := s.wiki.Search(ctx, llmwikibiz.DefaultTenantID, query, opts.Limit)
	converted := make([]knowledgebiz.SearchHit, 0, len(wikiHits))
	for _, hit := range wikiHits {
		digest := sha256.Sum256([]byte(hit.PageID))
		converted = append(converted, knowledgebiz.SearchHit{
			Doc: &knowledgemodel.Doc{
				ID:         binary.BigEndian.Uint64(digest[:8]),
				SourceType: "wiki",
				Title:      hit.Title,
				Content:    hit.Preview,
			},
			Score:           hit.Score,
			Layer:           hit.Layer,
			PageType:        hit.PageType,
			PageID:          hit.PageID,
			SourceVersionID: hit.SourceVersionID,
			MatchedNode:     hit.MatchedNode,
		})
	}
	if mode == "wiki" {
		return converted, wikiErr
	}

	opts.Mode = ""
	rawHits, rawErr := s.raw.Search(ctx, query, opts)
	if wikiErr != nil && rawErr != nil {
		return nil, errors.Join(wikiErr, rawErr)
	}
	if wikiErr != nil {
		return rawHits, nil
	}
	if rawErr != nil {
		return converted, nil
	}
	for rank := range rawHits {
		if rawHits[rank].Layer == "" {
			rawHits[rank].Layer = "raw"
		}
	}

	// The two layers interleave by rank, Wiki first. A weighted rank merge cannot
	// do that: any Wiki weight above 1.0 puts every Wiki hit ahead of every raw
	// hit (1.5/61 > 1.0/61), so a limit of ten Wiki hits crowded the operator
	// playbooks out of every result — the raw layer is the evidence the Wiki is
	// compiled from and the one the knowledge prologue looks for, so it must stay
	// reachable. Whichever layer runs out of hits first hands its remaining slots
	// to the other, and each hit keeps the relevance its own layer measured.
	merged := make([]knowledgebiz.SearchHit, 0, len(converted)+len(rawHits))
	for rank := 0; len(merged) < opts.Limit && (rank < len(converted) || rank < len(rawHits)); rank++ {
		if rank < len(converted) {
			merged = append(merged, converted[rank])
		}
		if len(merged) < opts.Limit && rank < len(rawHits) {
			merged = append(merged, rawHits[rank])
		}
	}
	return merged, nil
}
