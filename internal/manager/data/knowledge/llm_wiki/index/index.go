// Package index implements the rebuildable Wiki lexical and vector indexes.
// Lexical matching lives in the application database (portable LIKE over
// wiki_lexical); page vectors live in a dedicated Qdrant collection.
package index

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/embedding"
	"github.com/ongridio/ongrid/internal/pkg/qdrantx"
	"gorm.io/gorm"
)

// CollectionName is the dedicated Qdrant collection for LLM Wiki page vectors.
const CollectionName = "ongrid_llm_wiki"

// Payload keys written to Qdrant. tenant_id is stored as a decimal string so
// server-side filters never hit qdrant's int64 JSON number limit.
const (
	payloadTenantID = "tenant_id"
	payloadPageID   = "page_id"
	payloadPageType = "page_type"
	payloadTitle    = "title"
	payloadBodyHash = "body_hash"
)

// VectorStore is the narrow qdrant surface the Wiki index consumes.
// *qdrantx.Client satisfies it; tests can inject a fake.
type VectorStore interface {
	EnsureCollection(ctx context.Context, name string, dim int) error
	EnsurePayloadIndex(ctx context.Context, collection, field, schema string) error
	Upsert(ctx context.Context, collection string, points []qdrantx.Point) error
	DeleteByID(ctx context.Context, collection string, id uint64) error
	DeleteByFilter(ctx context.Context, collection string, mustMatch map[string]any) error
	GetPoints(ctx context.Context, collection string, ids []uint64) ([]qdrantx.SearchHit, error)
	Search(ctx context.Context, collection string, vector []float32, opts qdrantx.SearchOpts) ([]qdrantx.SearchHit, error)
	Scroll(ctx context.Context, collection string, opts qdrantx.ScrollOpts) (*qdrantx.ScrollResult, error)
}

type Index struct {
	db    *gorm.DB
	vec   VectorStore
	embed embedding.Embedder
	dim   int
}

// New wires the search index onto the migrated Wiki schema and the Qdrant
// collection. Callers run store.Migrate first (startup migrations do this for
// every backend). When an embedder is configured, Qdrant is a hard dependency:
// a missing client or unreachable collection fails startup.
func New(ctx context.Context, db *gorm.DB, vec VectorStore, embed embedding.Embedder, dim int, log *slog.Logger) (*Index, error) {
	if db == nil {
		return nil, errors.New("llmwiki index: database is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	if embed != nil {
		// The embedder is the source of truth for the vector size; the
		// caller-supplied dim only matters when no embedder is wired.
		if embedDim := embed.Dim(); embedDim > 0 {
			dim = embedDim
		}
	}
	if embed != nil && vec == nil {
		return nil, errors.New("llmwiki index: qdrant client is required when embedding is configured")
	}
	if embed != nil {
		if err := vec.EnsureCollection(ctx, CollectionName, dim); err != nil {
			return nil, fmt.Errorf("llmwiki index: ensure qdrant collection: %w", err)
		}
		if err := vec.EnsurePayloadIndex(ctx, CollectionName, payloadTenantID, "keyword"); err != nil {
			return nil, fmt.Errorf("llmwiki index: ensure qdrant payload index: %w", err)
		}
	}
	log.Info("llm wiki search index ready",
		slog.String("collection", CollectionName),
		slog.Bool("vector_search", embed != nil),
		slog.Int("dim", dim))
	return &Index{db: db, vec: vec, embed: embed, dim: dim}, nil
}

func (i *Index) IndexPage(ctx context.Context, document biz.IndexDocument) error {
	if strings.TrimSpace(document.PageID) == "" {
		return errors.New("llmwiki index: page id is required")
	}
	pageKey := fmt.Sprintf("%d:%s", document.TenantID, document.PageID)
	if err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("page_key = ?", pageKey).Delete(&model.WikiLexical{}).Error; err != nil {
			return fmt.Errorf("delete old lexical page: %w", err)
		}
		row := model.WikiLexical{
			PageKey:  pageKey,
			TenantID: document.TenantID,
			PageID:   document.PageID,
			PageType: document.PageType,
			Title:    document.Title,
			Content:  document.Content,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("insert lexical page: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("llmwiki index: update lexical page: %w", err)
	}

	if i.embed == nil {
		return nil
	}
	bodyHash := contentHash(document.Content)
	id := pointID(document.TenantID, document.PageID)
	existing, err := i.vec.GetPoints(ctx, CollectionName, []uint64{id})
	if err != nil {
		return fmt.Errorf("llmwiki index: load existing vector: %w", err)
	}
	if len(existing) > 0 {
		if hash, _ := existing[0].Payload[payloadBodyHash].(string); hash == bodyHash {
			return nil
		}
	}
	vectors, err := i.embed.Embed(ctx, []string{document.Title + "\n\n" + document.Content})
	if err != nil {
		return errors.Join(fmt.Errorf("llmwiki index: embed page: %w", err), i.deleteVector(ctx, document.TenantID, document.PageID))
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		cause := errors.New("llmwiki index: embedder returned an unexpected vector count")
		return errors.Join(cause, i.deleteVector(ctx, document.TenantID, document.PageID))
	}
	if i.dim > 0 && len(vectors[0]) != i.dim {
		cause := fmt.Errorf("llmwiki index: vector dimension %d does not match %d", len(vectors[0]), i.dim)
		return errors.Join(cause, i.deleteVector(ctx, document.TenantID, document.PageID))
	}
	point := qdrantx.Point{
		ID:     id,
		Vector: vectors[0],
		Payload: map[string]any{
			payloadTenantID: strconv.FormatUint(document.TenantID, 10),
			payloadPageID:   document.PageID,
			payloadPageType: document.PageType,
			payloadTitle:    document.Title,
			payloadBodyHash: bodyHash,
		},
	}
	if err := i.vec.Upsert(ctx, CollectionName, []qdrantx.Point{point}); err != nil {
		return fmt.Errorf("llmwiki index: save vector: %w", err)
	}
	return nil
}

// Clear removes every derived search entry for one tenant: first the Qdrant
// points, then the lexical rows. Qdrant and SQL cannot share a transaction, so
// the derived state is briefly inconsistent after a partial failure; the next
// compile rebuilds both sides. Without an embedder the collection may not
// exist, so only the lexical side is cleared.
func (i *Index) Clear(ctx context.Context, tenantID uint64) error {
	if i.embed != nil && i.vec != nil {
		if err := i.vec.DeleteByFilter(ctx, CollectionName, map[string]any{payloadTenantID: tenantIDKey(tenantID)}); err != nil {
			return fmt.Errorf("llmwiki index: clear vectors: %w", err)
		}
	}
	return i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ?", tenantID).Delete(&model.WikiLexical{}).Error; err != nil {
			return fmt.Errorf("llmwiki index: clear lexical pages: %w", err)
		}
		return nil
	})
}

// HasVectors reports whether the tenant has at least one page vector. Disabled
// vector search reports true so callers skip backfilling.
func (i *Index) HasVectors(ctx context.Context, tenantID uint64) (bool, error) {
	if i.embed == nil || i.vec == nil {
		return true, nil
	}
	result, err := i.vec.Scroll(ctx, CollectionName, qdrantx.ScrollOpts{
		MustMatch: map[string]any{payloadTenantID: tenantIDKey(tenantID)},
		Limit:     1,
	})
	if err != nil {
		return false, fmt.Errorf("llmwiki index: probe vectors: %w", err)
	}
	return len(result.Points) > 0, nil
}

func (i *Index) deleteVector(ctx context.Context, tenantID uint64, pageID string) error {
	if i.vec == nil {
		return nil
	}
	if err := i.vec.DeleteByID(ctx, CollectionName, pointID(tenantID, pageID)); err != nil {
		return fmt.Errorf("llmwiki index: delete stale vector: %w", err)
	}
	return nil
}

func (i *Index) Search(ctx context.Context, tenantID uint64, query string, limit int) ([]biz.SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	lexical, lexicalErr := i.searchLexical(ctx, tenantID, query, limit*2)
	vector, vectorErr := i.searchVector(ctx, tenantID, query, limit*2)
	// Qdrant is a hard dependency: a vector failure must not be masked by
	// lexical-only results.
	if vectorErr != nil {
		return nil, vectorErr
	}
	if lexicalErr != nil {
		return mergeRRF(query, nil, vector, limit), nil
	}
	return mergeRRF(query, lexical, vector, limit), nil
}

func (i *Index) searchLexical(ctx context.Context, tenantID uint64, query string, limit int) ([]biz.SearchHit, error) {
	type lexicalRow struct {
		PageID    string  `gorm:"column:page_id"`
		PageType  string  `gorm:"column:page_type"`
		Title     string  `gorm:"column:title"`
		Preview   string  `gorm:"column:preview"`
		MatchRank float64 `gorm:"column:match_rank"`
	}
	var rows []lexicalRow
	pattern := "%" + escapeLike(query) + "%"
	// SUBSTR, CASE and ESCAPE are portable across MySQL and SQLite. match_rank
	// prefers title matches, then aliases, then body-only matches. RANK is a
	// reserved word on MySQL 8, so the alias is not named rank.
	const statement = `SELECT page_id, page_type, title, SUBSTR(content, 1, 800) AS preview,
		CASE WHEN title LIKE ? ESCAPE '!' THEN 2 WHEN aliases LIKE ? ESCAPE '!' THEN 1 ELSE 0 END AS match_rank
		FROM wiki_lexical
		WHERE tenant_id = ? AND (title LIKE ? ESCAPE '!' OR aliases LIKE ? ESCAPE '!' OR content LIKE ? ESCAPE '!')
		ORDER BY match_rank DESC, page_id ASC LIMIT ?`
	err := i.db.WithContext(ctx).Raw(statement, pattern, pattern, tenantID, pattern, pattern, pattern, limit).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("llmwiki index: search lexical: %w", err)
	}
	hits := make([]biz.SearchHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, biz.SearchHit{Layer: "wiki", PageID: row.PageID, PageType: row.PageType, Title: row.Title, Preview: row.Preview, Score: (row.MatchRank + 1) / 3})
	}
	return hits, nil
}

func (i *Index) searchVector(ctx context.Context, tenantID uint64, query string, limit int) ([]biz.SearchHit, error) {
	if i.embed == nil || i.vec == nil {
		return nil, nil
	}
	vectors, err := i.embed.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("llmwiki index: embed query: %w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil, errors.New("llmwiki index: embedder returned an unexpected query vector count")
	}
	points, err := i.vec.Search(ctx, CollectionName, vectors[0], qdrantx.SearchOpts{
		Limit:     limit,
		MustMatch: map[string]any{payloadTenantID: tenantIDKey(tenantID)},
	})
	if err != nil {
		return nil, fmt.Errorf("llmwiki index: search vectors: %w", err)
	}
	pageIDs := make([]string, 0, len(points))
	for _, point := range points {
		if pageID, _ := point.Payload[payloadPageID].(string); pageID != "" {
			pageIDs = append(pageIDs, pageID)
		}
	}
	previews, err := i.lexicalRows(ctx, tenantID, pageIDs)
	if err != nil {
		return nil, err
	}
	hits := make([]biz.SearchHit, 0, len(points))
	for _, point := range points {
		pageID, _ := point.Payload[payloadPageID].(string)
		if pageID == "" {
			continue
		}
		hit := biz.SearchHit{Layer: "wiki", PageID: pageID, Score: point.Score}
		if row, ok := previews[pageID]; ok {
			hit.PageType = row.PageType
			hit.Title = row.Title
			hit.Preview = row.Preview
		} else {
			// The point is authoritative enough to answer with payload
			// metadata when the lexical mirror is missing.
			hit.PageType, _ = point.Payload[payloadPageType].(string)
			hit.Title, _ = point.Payload[payloadTitle].(string)
		}
		hits = append(hits, hit)
	}
	sort.SliceStable(hits, func(left, right int) bool { return hits[left].Score > hits[right].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

type lexicalPreview struct {
	PageID   string `gorm:"column:page_id"`
	PageType string `gorm:"column:page_type"`
	Title    string `gorm:"column:title"`
	Preview  string `gorm:"column:preview"`
}

// lexicalRows loads the display metadata of vector hits in one query.
func (i *Index) lexicalRows(ctx context.Context, tenantID uint64, pageIDs []string) (map[string]lexicalPreview, error) {
	out := make(map[string]lexicalPreview, len(pageIDs))
	if len(pageIDs) == 0 {
		return out, nil
	}
	var rows []lexicalPreview
	err := i.db.WithContext(ctx).Raw(
		"SELECT page_id, page_type, title, SUBSTR(content, 1, 800) AS preview FROM wiki_lexical WHERE tenant_id = ? AND page_id IN ?",
		tenantID, pageIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("llmwiki index: load vector previews: %w", err)
	}
	for _, row := range rows {
		out[row.PageID] = row
	}
	return out, nil
}

// pointID derives the stable Qdrant point id of one page. Rewriting a page
// overwrites its point instead of duplicating it.
func pointID(tenantID uint64, pageID string) uint64 {
	sum := sha256.Sum256([]byte("wiki:" + strconv.FormatUint(tenantID, 10) + ":" + pageID))
	return binary.BigEndian.Uint64(sum[:8])
}

func tenantIDKey(tenantID uint64) string {
	return strconv.FormatUint(tenantID, 10)
}

// escapeLike neutralises LIKE wildcards so a query matches literal text. The
// caller pairs it with ESCAPE '!'.
func escapeLike(query string) string {
	replacer := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return replacer.Replace(query)
}

// mergeRRF fuses the lexical and vector legs by reciprocal rank fusion. RRF
// decides the order only: 1/(60+rank) is a rank signal that spans the single
// hundredth between 0.0246 and 0.0143 across the top ten, so it must never leave
// this function as a Score a caller thresholds on. Each hit keeps the relevance
// its own leg produced — the vector cosine, or the three-level lexical match
// rank — and a page found by both legs reports the cosine.
func mergeRRF(query string, lexical []biz.SearchHit, vector []biz.SearchHit, limit int) []biz.SearchHit {
	legs := []struct {
		hits       []biz.SearchHit
		fromVector bool
	}{{hits: lexical}, {hits: vector, fromVector: true}}

	byID := make(map[string]biz.SearchHit)
	fused := make(map[string]float64)
	for _, leg := range legs {
		for rank, hit := range leg.hits {
			existing, seen := byID[hit.PageID]
			switch {
			case !seen:
				existing = hit
			case leg.fromVector:
				existing.Score = hit.Score
			}
			byID[hit.PageID] = existing
			fused[hit.PageID] += 1 / float64(60+rank+1)
		}
	}

	// A title match outranks an equally ranked body match. The nudge belongs to
	// the fusion value alone: adding it to Score would inflate a number callers
	// read as relevance.
	needle := strings.ToLower(strings.TrimSpace(query))
	for pageID, hit := range byID {
		if strings.Contains(strings.ToLower(hit.Title), needle) {
			fused[pageID] += 0.005
		}
	}

	out := make([]biz.SearchHit, 0, len(byID))
	for _, hit := range byID {
		out = append(out, hit)
	}
	sort.Slice(out, func(left, right int) bool {
		leftFused, rightFused := fused[out[left].PageID], fused[out[right].PageID]
		if leftFused == rightFused {
			return out[left].PageID < out[right].PageID
		}
		return leftFused > rightFused
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
