package knowledge

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
)

type repoUpdateRecorder struct {
	Service
	id     uint64
	branch string
}

func (s *repoUpdateRecorder) UpdateRepo(_ context.Context, id uint64, branch, description string) (*model.Repository, error) {
	s.id, s.branch = id, branch
	return &model.Repository{ID: id, URL: "https://example/repo.git", Branch: branch, Description: description}, nil
}

func TestRepositoryConfigHTTP(t *testing.T) {
	svc := &repoUpdateRecorder{}
	router := chi.NewRouter()
	NewHandler(svc).Register(router)
	rec := jsonReq(t, router, http.MethodPatch, "/v1/knowledge/repos/7", map[string]any{"branch": "v2", "description": "docs"})
	if rec.Code != http.StatusOK || svc.id != 7 || svc.branch != "v2" {
		t.Fatalf("PATCH: %d %s", rec.Code, rec.Body.String())
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
