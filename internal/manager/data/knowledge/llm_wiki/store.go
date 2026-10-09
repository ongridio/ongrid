// Package llm_wiki wires the LLM Wiki repository and search index onto the
// application database selected by ONGRID_DB_DIALECT (MySQL or SQLite).
package llm_wiki

import (
	"context"
	"errors"
	"log/slog"

	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/manager/data/knowledge/llm_wiki/index"
	"github.com/ongridio/ongrid/internal/manager/data/knowledge/llm_wiki/store"
	"github.com/ongridio/ongrid/internal/pkg/embedding"
	"gorm.io/gorm"
)

// Store exposes the Wiki repository and search index over the shared
// application database. The pool is owned by the caller and is not closed
// here.
type Store struct {
	SearchIndex biz.SearchIndex

	repo *store.Repo
}

// Open wires the Wiki repository and search index onto db. Schema creation is
// performed by store.Migrate during application startup migrations. The search
// index keeps lexical rows in db and page vectors in the dedicated Qdrant
// collection; when an embedder is configured, vec must be reachable.
func Open(ctx context.Context, db *gorm.DB, vec index.VectorStore, embed embedding.Embedder, dim int, log *slog.Logger) (*Store, error) {
	if db == nil {
		return nil, errors.New("llmwiki store: database is required")
	}
	if ctx == nil {
		return nil, errors.New("llmwiki store: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	searchIndex, err := index.New(ctx, db, vec, embed, dim, log)
	if err != nil {
		return nil, err
	}
	if log != nil {
		log.Info("llm wiki store ready", slog.String("dialect", db.Dialector.Name()))
	}
	return &Store{SearchIndex: searchIndex, repo: store.New(db)}, nil
}

func (s *Store) Repository() biz.Repository {
	if s == nil {
		return nil
	}
	return s.repo
}
