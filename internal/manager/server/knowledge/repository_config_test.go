package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
)

type repoUpdateRecorder struct {
	Service
	id     uint64
	branch string
}

func (s *repoUpdateRecorder) UpdateRepo(_ context.Context, id uint64, in biz.UpdateRepoInput) (*model.Repository, error) {
	s.id, s.branch = id, in.Branch
	row := &model.Repository{ID: id, URL: "https://example/repo.git", Branch: in.Branch, Description: in.Description}
	if in.AutoSyncEnabled != nil && in.AutoSyncIntervalMinutes != nil {
		row.AutoSyncEnabled, row.AutoSyncIntervalMinutes = *in.AutoSyncEnabled, *in.AutoSyncIntervalMinutes
	}
	return row, nil
}

func TestRepositoryConfigHTTP(t *testing.T) {
	svc := &repoUpdateRecorder{}
	router := chi.NewRouter()
	NewHandler(svc).Register(router)
	rec := jsonReq(t, router, http.MethodPatch, "/v1/knowledge/repos/7", map[string]any{"branch": "v2", "description": "docs"})
	if rec.Code != http.StatusOK || svc.id != 7 || svc.branch != "v2" {
		t.Fatalf("PATCH: %d %s", rec.Code, rec.Body.String())
	}
	rec = jsonReq(t, router, http.MethodPatch, "/v1/knowledge/repos/7", map[string]any{"branch": "v2", "auto_sync_enabled": true, "auto_sync_interval_minutes": 45})
	var result repoDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != http.StatusOK || !result.AutoSyncEnabled || result.AutoSyncIntervalMinutes != 45 {
		t.Fatalf("PATCH schedule: %d %s", rec.Code, rec.Body.String())
	}
	svc.branch = ""
	rec = jsonReq(t, router, http.MethodPatch, "/v1/knowledge/repos/7", map[string]any{"url": "https://other/repo.git", "branch": "v3"})
	if rec.Code != http.StatusBadRequest || svc.branch != "" {
		t.Fatalf("URL update accepted: %d", rec.Code)
	}
	actual, _ := newE2E(t)
	rec = jsonReq(t, actual, http.MethodPost, "/v1/knowledge/repos", map[string]any{"url": "file:///etc", "branch": "main"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid create: %d %s", rec.Code, rec.Body.String())
	}
}
