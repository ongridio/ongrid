package store

import (
	"context"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

func TestUpdateSessionModelPersistsAndIsolatesSessions(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	first := &model.Session{UserID: 1, Title: "first", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	second := &model.Session{UserID: 1, Title: "second", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := repo.CreateSession(ctx, first); err != nil {
		t.Fatalf("CreateSession first: %v", err)
	}
	if err := repo.CreateSession(ctx, second); err != nil {
		t.Fatalf("CreateSession second: %v", err)
	}

	if err := repo.UpdateSessionModel(ctx, first.ID, "custom", "qwen"); err != nil {
		t.Fatalf("UpdateSessionModel: %v", err)
	}
	gotFirst, err := repo.GetSession(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetSession first: %v", err)
	}
	gotSecond, err := repo.GetSession(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetSession second: %v", err)
	}
	if gotFirst.Provider == nil || *gotFirst.Provider != "custom" || gotFirst.Model == nil || *gotFirst.Model != "qwen" {
		t.Fatalf("first route = %v/%v", gotFirst.Provider, gotFirst.Model)
	}
	if gotSecond.Provider != nil || gotSecond.Model != nil {
		t.Fatalf("second route leaked = %v/%v", gotSecond.Provider, gotSecond.Model)
	}
}

func TestAPMSourceScopeSurvivesReload(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	scope := &model.APMSourceScope{TraceID: "trace", RepoID: "1", CommitSHA: "commit", Revision: "refs/tags/v1", SourceDirectory: "src", Error: "missing tag"}
	session := &model.Session{UserID: 1, Title: "APM", APMSource: scope}
	if err := repo.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetSession(ctx, session.ID)
	if err != nil || got.APMSource == nil || *got.APMSource != *scope {
		t.Fatalf("reload: %+v %v", got, err)
	}
}
