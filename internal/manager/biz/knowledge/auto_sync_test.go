package knowledge

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	store "github.com/ongridio/ongrid/internal/manager/data/knowledge/store"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
)

func newSyncDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestFailedInitialSyncRespectsDisabledSchedule(t *testing.T) {
	repos := store.New(newSyncDB(t))
	ctx := context.Background()
	row := &model.Repository{URL: "https://example.test/docs.git", Branch: "main"}
	if err := repos.CreateRepo(ctx, row); err != nil {
		t.Fatal(err)
	}
	cloneDir := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(cloneDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	u := &Usecase{repo: repos, cloneDir: cloneDir, log: slog.Default()}
	u.syncDueRepos(ctx)
	failed, err := repos.GetRepo(ctx, row.ID)
	if err != nil || failed.LastSyncError == "" || repoSyncDue(failed) {
		t.Fatalf("failed initial sync still due: %+v %v", failed, err)
	}
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	_, err = u.recordSyncFailure(expired, row, context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost sync timeout: %v", err)
	}
	failed, err = repos.GetRepo(ctx, row.ID)
	if err != nil || failed.LastSyncError != context.DeadlineExceeded.Error() || repoSyncDue(failed) {
		t.Fatalf("sync timeout was not recorded: %+v %v", failed, err)
	}
}

func TestAutoSyncCreatesSnapshotAndSkipsUnchangedIndex(t *testing.T) {
	fixture, _ := newCodeBrowseUC(t)
	remote := fixture.repoDir(1)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", remote}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	git("tag", "indexed")
	git("update-server-info")
	remoteHTTP := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(remote, ".git"))))
	defer remoteHTTP.Close()
	db := newSyncDB(t)
	repoStore := store.New(db)
	vec := &fakeVec{}
	u, err := New(context.Background(), repoStore, vec, fakeEmbed{}, t.TempDir(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	// Invalid URL/ref and denied access must never create a registration.
	for _, input := range []CreateRepoInput{
		{URL: remoteHTTP.URL + "/", Branch: "missing"},
		{URL: remoteHTTP.URL + "/", Branch: "--upload-pack=bad"},
		{URL: "file://" + remote, Branch: "indexed"},
		{URL: "https://user:password@example.com/repo.git", Branch: "main"},
	} {
		if _, err := u.CreateRepo(context.Background(), input); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("validation: %v", err)
		}
	}
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer denied.Close()
	if _, err := u.CreateRepo(context.Background(), CreateRepoInput{URL: denied.URL + "/", Branch: "main"}); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("access rejection: %v", err)
	}
	rows, err := repoStore.ListRepos(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("invalid registration saved: %v %v", rows, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); u.RunAutoSync(ctx) }()
	defer func() { cancel(); <-done }()
	row, err := u.CreateRepo(ctx, CreateRepoInput{URL: remoteHTTP.URL + "/", Branch: "indexed"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, err = repoStore.GetRepo(ctx, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.LastSyncedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("automatic creation sync did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if row.LastSyncError != "" || !row.HistoryComplete || row.CommitCount != 1 || row.TagCount != 1 || row.BranchCount != 1 || row.IndexedCommit == "" {
		t.Fatalf("snapshot: %+v", row)
	}
	writes := len(vec.upserts)
	if writes == 0 {
		t.Fatal("first sync did not index")
	}
	git("-c", "user.name=test", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "new release")
	git("tag", "new-release")
	git("update-server-info")
	u.syncDueRepos(context.Background())
	unchanged, _ := repoStore.GetRepo(context.Background(), row.ID)
	if unchanged.CommitCount != 1 {
		t.Fatal("synced before interval elapsed")
	}
	if row.AutoSyncEnabled || row.AutoSyncIntervalMinutes != 30 {
		t.Fatalf("unexpected default schedule: %+v", row)
	}
	lastSync := time.Now().Add(-24 * time.Hour).UTC()
	if err := db.Model(&model.Repository{}).Where("id = ?", row.ID).Update("last_synced_at", lastSync).Error; err != nil {
		t.Fatal(err)
	}
	u.syncDueRepos(context.Background())
	disabled, err := repoStore.GetRepo(context.Background(), row.ID)
	if err != nil || disabled.CommitCount != 1 || !disabled.LastSyncedAt.Equal(lastSync) {
		t.Fatalf("disabled schedule fetched: %+v %v", disabled, err)
	}
	enabled := true
	minutes := int32(1500)
	edit := UpdateRepoInput{Branch: row.Branch, AutoSyncEnabled: &enabled, AutoSyncIntervalMinutes: &minutes}
	if _, err := u.UpdateRepo(context.Background(), row.ID, edit); err != nil {
		t.Fatal(err)
	}
	u.syncDueRepos(context.Background())
	waiting, err := repoStore.GetRepo(context.Background(), row.ID)
	if err != nil || !waiting.LastSyncedAt.Equal(lastSync) || waiting.CommitCount != 1 {
		t.Fatalf("configured interval ignored: %+v %v", waiting, err)
	}
	minutes = 0
	if _, err := u.UpdateRepo(context.Background(), row.ID, edit); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("invalid interval accepted: %v", err)
	}
	minutes = 45
	if _, err := u.UpdateRepo(context.Background(), row.ID, edit); err != nil {
		t.Fatal(err)
	}
	u.syncDueRepos(context.Background())
	updated, err := repoStore.GetRepo(context.Background(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CommitCount != 2 || updated.TagCount != 2 || updated.IndexedCommit != row.IndexedCommit || len(vec.upserts) != writes {
		t.Fatalf("tag-only sync rebuilt index or wrong stats: %+v", updated)
	}

	if _, err := u.UpdateRepo(context.Background(), row.ID, UpdateRepoInput{Branch: "missing", Description: "bad edit"}); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("invalid edit: %v", err)
	}
	preserved, err := repoStore.GetRepo(context.Background(), row.ID)
	if err != nil || preserved.Branch != row.Branch || preserved.Description != row.Description || preserved.IndexedCommit != row.IndexedCommit || preserved.LastSyncedAt == nil || len(vec.upserts) != writes {
		t.Fatalf("invalid edit changed data: %+v %v", preserved, err)
	}
	changed, err := u.UpdateRepo(context.Background(), row.ID, UpdateRepoInput{Branch: "new-release", Description: "new index"})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ID != row.ID || changed.URL != row.URL || changed.LastSyncedAt != nil || changed.CommitCount != 2 || !changed.AutoSyncEnabled || changed.AutoSyncIntervalMinutes != 45 {
		t.Fatalf("update lost identity/history or was not queued: %+v", changed)
	}
	u.syncDueRepos(context.Background())
	changed, err = repoStore.GetRepo(context.Background(), row.ID)
	if err != nil || changed.Branch != "new-release" || changed.LastSyncedAt == nil || changed.IndexedCommit == row.IndexedCommit || len(vec.upserts) <= writes {
		t.Fatalf("new indexing ref not applied: %+v %v", changed, err)
	}
	u.active.Store(row.ID, true)
	if _, err := u.UpdateRepo(context.Background(), row.ID, UpdateRepoInput{Branch: "indexed"}); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("parallel edit: %v", err)
	}
	if _, err := u.Sync(context.Background(), row.ID); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("parallel sync: %v", err)
	}
	if err := u.DeleteRepo(context.Background(), row.ID); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("parallel delete: %v", err)
	}
	listed, err := u.ListRepos(context.Background())
	if err != nil || !listed[0].Syncing {
		t.Fatalf("active flag: %v %v", listed, err)
	}
	u.active.Delete(row.ID)
	// Disabling the schedule must work offline and keep the last attempt.
	t.Run("disable without git", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		enabled := false
		disabled, err := u.UpdateRepo(context.Background(), row.ID, UpdateRepoInput{Branch: changed.Branch, AutoSyncEnabled: &enabled})
		if err != nil || disabled.AutoSyncEnabled || disabled.AutoSyncIntervalMinutes != minutes || !disabled.LastSyncedAt.Equal(*changed.LastSyncedAt) {
			t.Fatalf("disable changed sync state: %+v %v", disabled, err)
		}
		if synced, err := u.syncRepo(context.Background(), row.ID, true); synced != nil || err != nil {
			t.Fatalf("queued worker ignored disabled schedule: %+v %v", synced, err)
		}
	})
	// Failed indexing must retry even if the checkout was already updated.
	if err := db.Model(&model.Repository{}).Where("id = ?", row.ID).Update("indexed_commit", "").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := u.Sync(context.Background(), row.ID); err != nil {
		t.Fatal(err)
	}
	if len(vec.upserts) <= writes {
		t.Fatal("missing index marker was not rebuilt")
	}
}
