package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
)

func testRepo(t *testing.T) (*Repo, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	return repo, db
}

func TestClaimJob_WhenLeaseExpires_RecoversAfterRestart(t *testing.T) {
	repo, db := testRepo(t)
	activeKey := "tenant-0-full-corpus"
	job := &model.CompileJob{TenantID: 0, ActiveKey: &activeKey, Status: model.JobPending, Stage: "queued"}
	if err := repo.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimJob(context.Background(), 0, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.LeaseOwner != "worker-a" {
		t.Fatalf("owner = %q", claimed.LeaseOwner)
	}
	if _, err := repo.ClaimJob(context.Background(), 0, "worker-b", time.Minute); !errors.Is(err, biz.ErrNoPendingJob) {
		t.Fatalf("active lease claim error = %v", err)
	}
	past := time.Now().Add(-time.Minute)
	if err := db.Model(&model.CompileJob{}).Where("id = ?", job.ID).Update("lease_expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	recovered, err := repo.ClaimJob(context.Background(), 0, "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.LeaseOwner != "worker-b" || recovered.Attempt != 2 {
		t.Fatalf("recovered = %+v", recovered)
	}
}

func TestCreateJob_RejectsSecondActiveTenantJob(t *testing.T) {
	repo, _ := testRepo(t)
	firstKey, secondKey := "first", "second"
	if err := repo.CreateJob(context.Background(), &model.CompileJob{TenantID: 7, ActiveKey: &firstKey, Status: model.JobPending, Stage: "queued"}); err != nil {
		t.Fatal(err)
	}
	err := repo.CreateJob(context.Background(), &model.CompileJob{TenantID: 7, ActiveKey: &secondKey, Status: model.JobPending, Stage: "queued"})
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("second job error = %v", err)
	}
}

func TestCancelJob_AllowsFailedJob(t *testing.T) {
	repo, db := testRepo(t)
	activeKey := "failed-cancel-key"
	job := &model.CompileJob{TenantID: 0, ActiveKey: &activeKey, Status: model.JobFailed, Stage: "compile", ErrorMessage: "compile failed"}
	if err := db.Create(job).Error; err != nil {
		t.Fatal(err)
	}
	cancelled, err := repo.CancelJob(context.Background(), 0, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != model.JobCancelled || !cancelled.CancelRequested || cancelled.ActiveKey != nil {
		t.Fatalf("cancelled job = %+v", cancelled)
	}
}

func TestDeleteSource_RemovesOwnedVersions(t *testing.T) {
	repo, db := testRepo(t)
	source := &model.Source{TenantID: 0, SourceKey: "source:delete", SourceType: "organization", RawPath: "team/delete.md", Status: model.SourceSucceeded}
	if err := db.Create(source).Error; err != nil {
		t.Fatal(err)
	}
	version := &model.SourceVersion{TenantID: 0, SourceID: source.ID, SHA256: "delete-v1", SnapshotPath: "delete-v1.md", SchemaVersion: biz.SchemaVersion}
	if err := db.Create(version).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteSource(context.Background(), 0, source.ID); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]any{"sources": &model.Source{}, "versions": &model.SourceVersion{}} {
		var count int64
		if err := db.Model(target).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d", name, count)
		}
	}
}

func TestMigrate_ReplacesLegacyWikiBuildsSchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE wiki_builds (id INTEGER PRIMARY KEY, status TEXT NOT NULL DEFAULT 'building', corpus_fingerprint TEXT NOT NULL DEFAULT '', published_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable("wiki_builds") || !db.Migrator().HasColumn("wiki_builds", "tenant_id") {
		t.Fatal("wiki_builds was not recreated with the current schema")
	}
	if db.Migrator().HasColumn("wiki_builds", "corpus_fingerprint") {
		t.Fatal("retired wiki_builds columns still exist")
	}
}

// TestHasActiveJob_TracksPendingAndRunningJobs — callers that would change the
// source set use this signal to refuse while a build is in flight.
func TestHasActiveJob_TracksPendingAndRunningJobs(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()

	active, err := repo.HasActiveJob(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("tenant without jobs reported an active job")
	}

	activeKey := "tenant-0-full-corpus"
	job := &model.CompileJob{TenantID: 0, ActiveKey: &activeKey, Status: model.JobPending, Stage: "queued"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if active, err = repo.HasActiveJob(ctx, 0); err != nil || !active {
		t.Fatalf("pending job: active = %t, err = %v", active, err)
	}
	if active, err = repo.HasActiveJob(ctx, 1); err != nil || active {
		t.Fatalf("other tenant: active = %t, err = %v", active, err)
	}

	if _, err := repo.CancelJob(ctx, 0, job.ID); err != nil {
		t.Fatal(err)
	}
	if active, err = repo.HasActiveJob(ctx, 0); err != nil || active {
		t.Fatalf("cancelled job: active = %t, err = %v", active, err)
	}
}

// TestCreateJob_RejectsSecondJobWhenTheTenantKeyIsHeld — "one in-flight job per
// tenant" is enforced by the tenant-scoped active_key and uk_wiki_job_active, not
// by the pre-check: the pre-check only looks at pending/running rows, so two
// concurrent creates can both pass it, and only the unique index stops the
// second insert.
func TestCreateJob_RejectsSecondJobWhenTheTenantKeyIsHeld(t *testing.T) {
	repo, db := testRepo(t)
	ctx := context.Background()

	if model.ActiveJobKey(0) == model.ActiveJobKey(1) {
		t.Fatal("the active job key must be scoped to the tenant")
	}

	key := model.ActiveJobKey(0)
	holder := &model.CompileJob{TenantID: 0, ActiveKey: &key, Status: model.JobSucceeded, Stage: "completed"}
	if err := db.Create(holder).Error; err != nil {
		t.Fatal(err)
	}

	err := repo.CreateJob(ctx, &model.CompileJob{TenantID: 0, ActiveKey: &key, Status: model.JobPending, Stage: "queued"})

	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("CreateJob error = %v, want conflict", err)
	}
}

// TestListSourcesAfter_WalksEverySource — ListSources answers with a single capped
// page, so callers that must see every source (sync, delete, the compile corpus)
// have to page with ListSourcesAfter or they silently lose the tail.
func TestListSourcesAfter_WalksEverySource(t *testing.T) {
	repo, db := testRepo(t)
	ctx := context.Background()
	const total = 205
	for i := 1; i <= total; i++ {
		source := &model.Source{
			TenantID:   0,
			SourceKey:  fmt.Sprintf("key-%03d", i),
			SourceType: "organization",
			RawPath:    fmt.Sprintf("docs/%03d.md", i),
			Status:     model.SourcePending,
		}
		if err := db.Create(source).Error; err != nil {
			t.Fatal(err)
		}
	}

	page, count, err := repo.ListSources(ctx, 0, "", 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 200 || count != total {
		t.Fatalf("single page = %d rows of a reported %d, want 200 of %d", len(page), count, total)
	}

	seen := 0
	var afterID uint64
	for {
		rows, err := repo.ListSourcesAfter(ctx, 0, "", afterID, 200)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		if rows[0].ID <= afterID {
			t.Fatalf("page did not advance past id %d", afterID)
		}
		afterID = rows[len(rows)-1].ID
		seen += len(rows)
	}
	if seen != total {
		t.Fatalf("paged walk saw %d sources, want %d", seen, total)
	}
}

// TestUpsertSourceVersion_ReattachesWhenTheCurrentVersionRowIsGone — a source
// whose current_version_id points at a row that no longer exists made the next
// sync fail, because identical content then looked already stored. It must
// attach a fresh version instead.
func TestUpsertSourceVersion_ReattachesWhenTheCurrentVersionRowIsGone(t *testing.T) {
	repo, db := testRepo(t)
	ctx := context.Background()
	source := &model.Source{TenantID: 0, SourceKey: "organization:7", SourceType: "organization", RawPath: "PLAN.md"}
	newVersion := func() *model.SourceVersion {
		return &model.SourceVersion{SHA256: "abc123", SizeBytes: 3, SnapshotPath: "versions/x/abc123.md"}
	}

	stored, _, _, err := repo.UpsertSourceVersion(ctx, source, newVersion())
	if err != nil {
		t.Fatal(err)
	}
	if stored.CurrentVersionID == nil {
		t.Fatal("first mirror did not record a current version")
	}

	// The version row disappears while the source keeps pointing at it.
	if err := db.Exec("DELETE FROM wiki_source_versions WHERE id = ?", *stored.CurrentVersionID).Error; err != nil {
		t.Fatal(err)
	}

	again, version, changed, err := repo.UpsertSourceVersion(ctx, source, newVersion())
	if err != nil {
		t.Fatalf("re-mirror with a dangling current version: %v", err)
	}
	if !changed {
		t.Fatal("attaching a lost version must report a change")
	}
	if version == nil || version.ID == 0 {
		t.Fatalf("version = %+v", version)
	}
	if again.CurrentVersionID == nil || *again.CurrentVersionID != version.ID {
		t.Fatalf("current_version_id = %v, want %d", again.CurrentVersionID, version.ID)
	}
	var rows int64
	if err := db.Model(&model.SourceVersion{}).Where("id = ?", version.ID).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("version rows = %d, want 1", rows)
	}
}
