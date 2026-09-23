package llm_wiki

import (
	"context"
	"errors"
	"log/slog"
	"strings"
)

// Search answers one Wiki query through the derived search index. The index is
// required: Qdrant vector search is a hard dependency, so an index failure is
// returned to the caller instead of being masked by a file scan.
func (u *Usecase) Search(ctx context.Context, tenantID uint64, query string, limit int) ([]SearchHit, error) {
	unlock := u.files.LockArtifacts()
	defer unlock()

	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	if u.indexer == nil {
		return nil, errors.New("llmwiki: search index is unavailable")
	}
	hits, err := u.indexer.Search(ctx, tenantID, query, limit)
	if err != nil {
		u.log.WarnContext(ctx, "wiki index search failed", slog.Any("err", err))
		return nil, err
	}
	return hits, nil
}
