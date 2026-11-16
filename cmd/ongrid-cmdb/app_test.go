package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	cmdbdata "github.com/ongridio/ongrid/internal/cmdb/data"
	"github.com/ongridio/ongrid/internal/pkg/auth"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestApp wires the real CMDB handlers over a migrated SQLite database.
// Nothing is mocked: the test covers the production construction path, the
// JWT middleware and the router.
func newTestApp(t *testing.T) (http.Handler, *auth.Signer) {
	t.Helper()
	db, err := dbx.Open(config.DBConfig{Dialect: "sqlite", Path: filepath.Join(t.TempDir(), "cmdb.db")}, nil)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := dbx.RunMigrationsLocked(context.Background(), db, 10*time.Second, nil, cmdbdata.Migrate); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	signer := auth.NewSigner("test-secret", time.Minute, time.Hour)
	log := testLogger()
	return newRouter(db, signer, buildHandlers(db, log), "test"), signer
}

func tokenFor(t *testing.T, signer *auth.Signer, admin bool) string {
	t.Helper()
	role := "user"
	isSuper := false
	if admin {
		role = "admin"
		isSuper = true
	}
	tok, err := signer.SignAccess(auth.Claims{UserID: 7, Email: "tester@example.com", Role: role, IsSuperuser: isSuper})
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}

func request(t *testing.T, h http.Handler, method, path, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthAndReadyProbesAreUnauthenticated(t *testing.T) {
	app, _ := newTestApp(t)

	if rec := request(t, app, http.MethodGet, "/healthz", "", ""); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("GET /healthz = %d %q", rec.Code, rec.Body.String())
	}
	if rec := request(t, app, http.MethodGet, "/readyz", "", ""); rec.Code != http.StatusOK || rec.Body.String() != "ready" {
		t.Fatalf("GET /readyz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestCMDBRoutesRequireBearerToken(t *testing.T) {
	app, _ := newTestApp(t)
	for _, path := range []string{"/api/v1/cmdb/health", "/api/v1/cmdb/ci-types"} {
		if rec := request(t, app, http.MethodGet, path, "", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s without token = %d, want 401", path, rec.Code)
		}
	}
}

func TestCMDBHealthAndListWithAdminToken(t *testing.T) {
	app, signer := newTestApp(t)
	token := tokenFor(t, signer, true)

	rec := request(t, app, http.MethodGet, "/api/v1/cmdb/health", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/cmdb/health = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "generation") {
		t.Fatalf("health body = %s, want schema lineage", rec.Body.String())
	}

	rec = request(t, app, http.MethodGet, "/api/v1/cmdb/ci-types", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/cmdb/ci-types = %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
		Total int               `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode ci-types: %v (%s)", err, rec.Body.String())
	}
	if list.Total != 0 || len(list.Items) != 0 {
		t.Fatalf("fresh database ci-types = %+v, want empty", list)
	}
}

// The pool's routes must reach a wired use case, not the ErrNotWiredYet
// placeholder: a 200 with an empty page is the difference between "no sightings
// yet" and "the composition root forgot WithPool".
func TestDiscoveryPoolIsWired(t *testing.T) {
	app, signer := newTestApp(t)
	token := tokenFor(t, signer, true)

	for _, path := range []string{
		"/api/v1/cmdb/discovery/adc?type_id=1",
		"/api/v1/cmdb/discovery/adc/counter?type_id=1",
		"/api/v1/cmdb/discovery/adc/ci-types",
		"/api/v1/cmdb/discovery/adc/exec-histories?type_id=1",
	} {
		rec := request(t, app, http.MethodGet, path, token, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

// The global history reads are admin-only; one instance's history is not.
func TestHistoryAuthz(t *testing.T) {
	app, signer := newTestApp(t)
	user := tokenFor(t, signer, false)
	admin := tokenFor(t, signer, true)

	if rec := request(t, app, http.MethodGet, "/api/v1/cmdb/history/attributes", user, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin GET history = %d %s, want 403", rec.Code, rec.Body.String())
	}
	rec := request(t, app, http.MethodGet, "/api/v1/cmdb/history/attributes", admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET history = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"items"`) {
		t.Fatalf("history body = %s, want the list shape", rec.Body.String())
	}
	if rec := request(t, app, http.MethodGet, "/api/v1/cmdb/history/ci/100", user, ""); rec.Code != http.StatusOK {
		t.Fatalf("user GET instance history = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// The topology reads must reach the store, not the ErrNotWiredYet placeholder.
func TestTopologyIsWired(t *testing.T) {
	app, signer := newTestApp(t)
	user := tokenFor(t, signer, false)
	admin := tokenFor(t, signer, true)

	for _, path := range []string{
		"/api/v1/cmdb/topology-views",
		"/api/v1/cmdb/topology-views/groups",
	} {
		if rec := request(t, app, http.MethodGet, path, user, ""); rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := request(t, app, http.MethodPost, "/api/v1/cmdb/topology-views", user, `{"name":"x"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin create view = %d, want 403", rec.Code)
	}
	if rec := request(t, app, http.MethodPost, "/api/v1/cmdb/topology-views", admin, `{"name":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("admin create view with no central model = %d %s, want 400", rec.Code, rec.Body.String())
	}
}

func TestAdminGateRejectsNonAdminWrite(t *testing.T) {
	app, signer := newTestApp(t)
	body := `{"name":"runs_on","color":"#1890ff"}`

	rec := request(t, app, http.MethodPost, "/api/v1/cmdb/relation-types", tokenFor(t, signer, false), body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin POST relation-types = %d %s, want 403", rec.Code, rec.Body.String())
	}

	rec = request(t, app, http.MethodPost, "/api/v1/cmdb/relation-types", tokenFor(t, signer, true), body)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("admin POST relation-types = %d %s, want 2xx", rec.Code, rec.Body.String())
	}
}

func TestRouterEmitsHTTPSpanAndFiltersProbes(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	app, _ := newTestApp(t)

	// A normal request must produce one HTTP span. /version is used because
	// it needs no token; the API routes are covered by the auth tests.
	if rec := request(t, app, http.MethodGet, "/version", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /version = %d %s", rec.Code, rec.Body.String())
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if !spans[0].SpanContext().IsValid() {
		t.Fatal("HTTP span has an invalid span context")
	}

	// Liveness/readiness probes are excluded so they do not crowd out real
	// request traces in Tempo.
	if rec := request(t, app, http.MethodGet, "/healthz", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d", rec.Code)
	}
	if got := len(recorder.Ended()); got != 1 {
		t.Fatalf("health probe emitted a span (ended spans = %d)", got)
	}
}

func TestRunHealthcheckProbesTheLocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	if err := runHealthcheck(host, time.Second); err != nil {
		t.Fatalf("runHealthcheck(%s): %v", host, err)
	}
	if err := runHealthcheck("127.0.0.1:1", 100*time.Millisecond); err == nil {
		t.Fatal("runHealthcheck against a closed port succeeded")
	}
}
