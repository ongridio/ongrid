package knowledge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	orgbiz "github.com/ongridio/ongrid/internal/manager/biz/knowledge"
	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	orgmodel "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type serviceStub struct {
	compiled bool
	tenantID uint64
	force    bool
	deleted  string
	syncDocs []biz.OrganizationSource
	syncRuns int
}

func (*serviceStub) ListTree(context.Context, string, string) ([]biz.TreeNode, error) {
	return nil, nil
}

func (*serviceStub) GetNode(context.Context, string) (*biz.NodeDetail, error) { return nil, nil }

func (*serviceStub) PreviewNode(context.Context, string) (*biz.NodePreview, error) {
	return &biz.NodePreview{Name: "guide.pdf", ContentType: "application/pdf", Content: []byte("%PDF")}, nil
}

func (s *serviceStub) DeleteNode(_ context.Context, id string) error {
	s.deleted = id
	return nil
}

func (*serviceStub) ListSources(context.Context, uint64, string, int) ([]*model.Source, int64, error) {
	return nil, 0, nil
}

func (*serviceStub) ListJobs(context.Context, uint64, int) ([]*model.CompileJob, int64, error) {
	return nil, 0, nil
}

func (s *serviceStub) CreateCompileJob(_ context.Context, tenantID uint64, force bool, sourceIDs []uint64) (*model.CompileJob, error) {
	s.compiled = true
	s.tenantID = tenantID
	s.force = force
	return &model.CompileJob{ID: 9007199254740993, ForceCompile: force, Status: model.JobPending, Stage: "queued"}, nil
}

func (*serviceStub) RetryJob(context.Context, uint64, uint64) (*model.CompileJob, error) {
	return nil, nil
}

func (*serviceStub) CancelJob(context.Context, uint64, uint64) (*model.CompileJob, error) {
	return nil, nil
}

func (*serviceStub) Search(context.Context, uint64, string, int) ([]biz.SearchHit, error) {
	return nil, nil
}

func (s *serviceStub) SyncOrganizationSources(_ context.Context, docs []biz.OrganizationSource) (*biz.SyncResult, error) {
	s.syncRuns++
	s.syncDocs = append([]biz.OrganizationSource(nil), docs...)
	return &biz.SyncResult{Total: len(docs)}, nil
}

type organizationDocsStub struct {
	Service
	docs    map[string][]*orgmodel.Doc
	filters []orgbiz.ListDocsFilter
	errType string
}

func (s *organizationDocsStub) ListDocs(_ context.Context, filter orgbiz.ListDocsFilter) ([]*orgmodel.Doc, error) {
	s.filters = append(s.filters, filter)
	if filter.SourceType == s.errType {
		return nil, errors.New("list docs failed")
	}
	return s.docs[filter.SourceType], nil
}

func newLLMWikiHandler(svc *serviceStub) *Handler {
	h := NewHandler(nil)
	h.SetLLMWikiService(svc)
	return h
}

func TestSyncOrganizationSources_IncludesEveryOrganizationSourceType(t *testing.T) {
	orgSvc := &organizationDocsStub{docs: map[string][]*orgmodel.Doc{
		"manual": {{ID: 1, Title: "Manual", Path: "ops", Content: "manual body"}},
		"upload": {{ID: 2, Title: "Upload", Path: "docs", Content: "upload body"}},
		"repo":   {{ID: 3, SourceType: "repo", RepoID: ptrUint64(9), URL: "cmd/main.go", Title: "main", Path: "cmd", Content: "package main"}},
	}}
	wikiSvc := &serviceStub{}
	handler := NewHandler(orgSvc)
	handler.SetLLMWikiService(wikiSvc)
	router := chi.NewRouter()
	handler.Register(router)

	request := httptest.NewRequest(http.MethodPost, "/v1/knowledge/llm-wiki/sync", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, orgSvc.filters, 3)
	assert.Equal(t, []string{"manual", "upload", "repo"}, []string{
		orgSvc.filters[0].SourceType,
		orgSvc.filters[1].SourceType,
		orgSvc.filters[2].SourceType,
	})
	for _, filter := range orgSvc.filters {
		assert.True(t, filter.All)
	}
	require.Equal(t, 1, wikiSvc.syncRuns)
	require.Len(t, wikiSvc.syncDocs, 3)
	assert.Equal(t, biz.OrganizationSource{ID: 3, Title: "main", Path: "cmd", Content: "package main"}, wikiSvc.syncDocs[2])
}

func TestSyncOrganizationSources_DoesNotMirrorPartialListing(t *testing.T) {
	orgSvc := &organizationDocsStub{
		docs:    map[string][]*orgmodel.Doc{"manual": {{ID: 1, Title: "Manual", Content: "body"}}},
		errType: "upload",
	}
	wikiSvc := &serviceStub{}
	handler := NewHandler(orgSvc)
	handler.SetLLMWikiService(wikiSvc)
	router := chi.NewRouter()
	handler.Register(router)

	request := httptest.NewRequest(http.MethodPost, "/v1/knowledge/llm-wiki/sync", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.Zero(t, wikiSvc.syncRuns)
	assert.Len(t, orgSvc.filters, 2)
}

func ptrUint64(value uint64) *uint64 { return &value }

func TestCompile_ReturnsEnvelopeAndStringIDs(t *testing.T) {
	svc := &serviceStub{}
	router := chi.NewRouter()
	newLLMWikiHandler(svc).Register(router)
	req := httptest.NewRequest(http.MethodPost, "/v1/knowledge/llm-wiki/compile", strings.NewReader(`{"force":true}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if !svc.compiled || svc.tenantID != biz.DefaultTenantID || !svc.force {
		t.Fatalf("compile request = %+v", svc)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"code":"ok"`) || !strings.Contains(body, `"id":"9007199254740993"`) {
		t.Fatalf("response = %s", body)
	}
}

func TestPreview_ReturnsInlineBinaryContent(t *testing.T) {
	router := chi.NewRouter()
	newLLMWikiHandler(&serviceStub{}).Register(router)
	req := httptest.NewRequest(http.MethodGet, "/v1/knowledge/llm-wiki/nodes/raw-guide/preview", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("content type = %q", got)
	}
	if got := response.Header().Get("Content-Disposition"); !strings.Contains(got, `inline`) || !strings.Contains(got, `guide.pdf`) {
		t.Fatalf("content disposition = %q", got)
	}
	if got := response.Body.String(); got != "%PDF" {
		t.Fatalf("body = %q", got)
	}
}

func TestDeleteNode_ReturnsSuccessEnvelope(t *testing.T) {
	router := chi.NewRouter()
	newLLMWikiHandler(&serviceStub{}).Register(router)
	req := httptest.NewRequest(http.MethodDelete, "/v1/knowledge/llm-wiki/nodes/raw-guide", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"deleted":true`) {
		t.Fatalf("response = %s", response.Body.String())
	}
}

// denyAuthz records which permission each route asks for and rejects the
// request, so a mutating route that lost its middleware shows up as a 200.
type denyAuthz struct{ required []string }

func (a *denyAuthz) Require(obj, act string) func(http.Handler) http.Handler {
	a.required = append(a.required, obj+":"+act)
	return func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
	}
}

func (a *denyAuthz) grants(permission string) bool {
	return slices.Contains(a.required, permission)
}

// TestDeleteNode_RequiresDeletePermission — the Wiki delete route removes a
// source, its versions and the published build, so it must be guarded like the
// other mutating Wiki routes.
func TestDeleteNode_RequiresDeletePermission(t *testing.T) {
	authz := &denyAuthz{}
	svc := &serviceStub{}
	handler := newLLMWikiHandler(svc)
	handler.SetAuthz(authz)
	router := chi.NewRouter()
	handler.Register(router)

	req := httptest.NewRequest(http.MethodDelete, "/v1/knowledge/llm-wiki/nodes/raw-guide", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if !authz.grants("knowledge:doc:delete") {
		t.Fatalf("required permissions = %v", authz.required)
	}
	if svc.deleted != "" {
		t.Fatalf("delete reached the service with id %q", svc.deleted)
	}
}
