package llm_wiki

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	store "github.com/ongridio/ongrid/internal/manager/data/knowledge/llm_wiki/store"
	"gorm.io/gorm"
)

func TestOpen_ProvidesRepositoryAndSearchIndexOnSharedDatabase(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(context.Background(), db, nil, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Repository() == nil {
		t.Fatal("repository is nil")
	}
	if opened.SearchIndex == nil {
		t.Fatal("search index is nil")
	}
	_, total, err := opened.Repository().ListSources(context.Background(), 1, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("source total = %d", total)
	}
}

func TestOpen_RejectsMissingDatabase(t *testing.T) {
	if _, err := Open(context.Background(), nil, nil, nil, 0, nil); err == nil {
		t.Fatal("nil database was accepted")
	}
}
