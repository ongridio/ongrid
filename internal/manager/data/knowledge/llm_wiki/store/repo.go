// Package store implements LLM Wiki persistence on the application database
// selected by ONGRID_DB_DIALECT (MySQL or SQLite): sources and versions,
// compile jobs, immutable build pages and search indexes.
package store

import (
	"errors"
	"fmt"
	"strings"

	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
)

// Repo is the LLM Wiki repository.
type Repo struct{ db *gorm.DB }

func New(db *gorm.DB) *Repo { return &Repo{db: db} }

// Migrate creates the current source/build state and the derived search
// tables on MySQL or SQLite.
func Migrate(db *gorm.DB) error {
	if db == nil {
		return errors.New("migrate llm wiki: database is required")
	}
	dialect := db.Dialector.Name()
	if dialect != "mysql" && dialect != "sqlite" {
		return fmt.Errorf("migrate llm wiki: unsupported dialect %s", dialect)
	}
	if dialect == "sqlite" {
		for _, statement := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000", "PRAGMA journal_mode = WAL"} {
			if err := db.Exec(statement).Error; err != nil {
				return fmt.Errorf("migrate llm wiki: configure sqlite: %w", err)
			}
		}
	}
	// The pre-RFC-005 wiki_builds carried corpus_fingerprint/published_at
	// columns. Drop that retired leftover before AutoMigrate so the current
	// build schema can own the table name.
	if db.Migrator().HasTable("wiki_builds") && db.Migrator().HasColumn("wiki_builds", "corpus_fingerprint") {
		if err := db.Migrator().DropTable("wiki_builds"); err != nil {
			return fmt.Errorf("migrate llm wiki: drop legacy wiki_builds: %w", err)
		}
	}
	// Page vectors moved to Qdrant (ADR-036). MySQL drops the retired table
	// through db/migrations; SQLite has no SQL migration runner, so the
	// single-instance database is cleaned here.
	if dialect == "sqlite" && db.Migrator().HasTable("wiki_vectors") {
		if err := db.Migrator().DropTable("wiki_vectors"); err != nil {
			return fmt.Errorf("migrate llm wiki: drop legacy wiki_vectors: %w", err)
		}
	}
	// Nothing ever wrote wiki_source_chunks, and the wiki_index_meta schema
	// version had no reader, so both were retired. MySQL drops them through
	// db/migrations; SQLite has no SQL migration runner, so the single-instance
	// database is cleaned here.
	if dialect == "sqlite" {
		for _, table := range []string{"wiki_source_chunks", "wiki_index_meta"} {
			if db.Migrator().HasTable(table) {
				if err := db.Migrator().DropTable(table); err != nil {
					return fmt.Errorf("migrate llm wiki: drop retired %s: %w", table, err)
				}
			}
		}
	}
	if err := db.AutoMigrate(
		&model.Source{}, &model.SourceVersion{}, &model.CompileJob{},
		&model.WikiBuild{}, &model.WikiBuildPage{},
		&model.WikiLexical{},
	); err != nil {
		return fmt.Errorf("migrate llm wiki: source and build tables: %w", err)
	}
	return nil
}

// mapNotFound turns GORM's record-not-found into the shared not-found error.
func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.ErrNotFound
	}
	return err
}

// isDuplicateKey reports whether err is a unique-constraint violation.
func isDuplicateKey(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate entry") || strings.Contains(message, "unique constraint failed")
}

var _ biz.Repository = (*Repo)(nil)
