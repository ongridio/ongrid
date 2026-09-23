package llm_wiki

import (
	"context"
	"os"
	"testing"

	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	store "github.com/ongridio/ongrid/internal/manager/data/knowledge/llm_wiki/store"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const mysqlTestTenant = 9

// TestMySQLBackendIntegration exercises the MySQL dialect on a real server.
// Set WIKI_MYSQL_TEST_DSN to a scratch database DSN, for example:
//
//	WIKI_MYSQL_TEST_DSN='user:pass@tcp(127.0.0.1:3306)/ongrid_wiki_test?parseTime=true' go test ./...
func TestMySQLBackendIntegration(t *testing.T) {
	dsn := os.Getenv("WIKI_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("WIKI_MYSQL_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cleanupMySQLTestTenant(t, db)
	opened, err := Open(ctx, db, nil, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := opened.Repository()

	source, version, changed, err := repo.UpsertSourceVersion(ctx,
		&model.Source{TenantID: mysqlTestTenant, SourceKey: "manual:mysql", SourceType: "manual", RawPath: "mysql.md", Status: model.SourcePending},
		&model.SourceVersion{TenantID: mysqlTestTenant, SHA256: "hash-mysql", SnapshotPath: "mysql.md", SchemaVersion: "v1"},
	)
	if err != nil || !changed || source == nil || version == nil {
		t.Fatalf("upsert source: changed=%v err=%v", changed, err)
	}

	build, err := repo.CreateBuild(ctx, mysqlTestTenant)
	if err != nil {
		t.Fatal(err)
	}
	page := &model.WikiBuildPage{BuildID: build.ID, TenantID: mysqlTestTenant, PageID: "manual-page", PageType: "generated", Title: "磁盘容量", BodyPath: "manual-page.md", BodySHA256: "hash-page", SourceRefsJSON: "[]"}
	if err := repo.CreatePagesBatch(ctx, []*model.WikiBuildPage{page}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateBuildStatus(ctx, mysqlTestTenant, build.ID, model.BuildValidated, ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.ActivateBuild(ctx, mysqlTestTenant, build.ID); err != nil {
		t.Fatal(err)
	}

	document := biz.IndexDocument{TenantID: mysqlTestTenant, PageID: page.PageID, PageType: page.PageType, Title: page.Title, Content: "磁盘容量 50% 使用率，请检查 disk usage"}
	if err := opened.SearchIndex.IndexPage(ctx, document); err != nil {
		t.Fatalf("index page: %v", err)
	}
	hits, err := opened.SearchIndex.Search(ctx, mysqlTestTenant, "50%", 10)
	if err != nil {
		t.Fatalf("search literal wildcard: %v", err)
	}
	if len(hits) != 1 || hits[0].PageID != page.PageID {
		t.Fatalf("literal wildcard hits = %+v", hits)
	}
	hits, err = opened.SearchIndex.Search(ctx, mysqlTestTenant, "磁盘", 10)
	if err != nil {
		t.Fatalf("search cjk: %v", err)
	}
	if len(hits) != 1 || hits[0].Title != page.Title {
		t.Fatalf("cjk hits = %+v", hits)
	}
	active, err := repo.GetActiveBuild(ctx, mysqlTestTenant)
	if err != nil || active.ID != build.ID {
		t.Fatalf("active build = %+v err=%v", active, err)
	}
	_, total, err := repo.ListSources(ctx, mysqlTestTenant, "", 10)
	if err != nil || total != 1 {
		t.Fatalf("list sources total=%d err=%v", total, err)
	}
}

func cleanupMySQLTestTenant(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range []string{"wiki_build_pages", "wiki_builds", "wiki_compile_jobs", "wiki_source_versions", "wiki_sources", "wiki_lexical"} {
		if err := db.Exec("DELETE FROM "+table+" WHERE tenant_id = ?", mysqlTestTenant).Error; err != nil {
			t.Fatalf("cleanup %s: %v", table, err)
		}
	}
}
