package agenttrace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/agenttrace"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	traceserver "github.com/ongridio/ongrid/internal/manager/server/traces"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

type sessionOwners map[string]uint64

func (s sessionOwners) GetSession(_ context.Context, id string) (*aiopsmodel.Session, error) {
	if owner, ok := s[id]; ok {
		return &aiopsmodel.Session{ID: id, UserID: owner}, nil
	}
	return nil, errs.ErrNotFound
}

func TestHTTPUserIsolationAcrossAgentAndTraceRoutes(t *testing.T) {
	var globalTagReads atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/search":
			fmt.Fprint(w, `{"traces":[{"traceID":"1","rootTraceName":"alice-private"},{"traceID":"2","rootTraceName":"bob-private"}],"metrics":{"inspectedTraces":2}}`)
		case strings.HasPrefix(r.URL.Path, "/api/search/tag/"):
			globalTagReads.Add(1)
			fmt.Fprint(w, `{"tagValues":["alice-private","bob-private"]}`)
		case strings.HasPrefix(r.URL.Path, "/api/traces/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/traces/")
			name, scope := "alice", "github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
			if strings.HasSuffix(id, "2") {
				name, scope = "bob", "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v2/chatruntime"
			}
			// The two users deliberately share the application identity.
			fmt.Fprintf(w, `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"ongrid-manager"}},{"key":"service.namespace","value":{"stringValue":"ongrid"}},{"key":"deployment.environment.name","value":{"stringValue":"test"}}]},"scopeSpans":[{"scope":{"name":%q},"spans":[{"traceId":%q,"spanId":"0000000000000001","name":%q,"startTimeUnixNano":"1791936000000000000","endTimeUnixNano":"1791936001000000000","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"invoke_agent"}},{"key":"ongrid.agent.session.id","value":{"stringValue":%q}},{"key":"gen_ai.conversation.id","value":{"stringValue":%q}},{"key":"input.value","value":{"stringValue":%q}}]}]}]}]}`,
				scope, id, name+"-private", name+"-session", name+"-session", name+"-question")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(backend.Close)
	owners := sessionOwners{"alice-session": 1, "bob-session": 2}
	querier := biz.NewScopedQuerier(tracequery.New(backend.URL, nil), owners)
	router := chi.NewRouter()
	router.Route("/api", func(r chi.Router) {
		NewHandler(biz.New(querier), nil).Register(r)
		traceserver.NewHandler(querier).Register(r)
	})
	request := func(path string, user uint64, role string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/"+path, nil)
		req = req.WithContext(tenantctx.With(req.Context(), tenantctx.Tenant{UserID: user, Role: role}))
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private response can be cached")
		}
		return out
	}
	window := "start=2026-10-14T00:00:00Z&end=2026-10-14T01:00:00Z"
	for _, caller := range []struct {
		id              uint64
		name, otherName string
	}{{1, "alice", "bob"}, {2, "bob", "alice"}} {
		for _, path := range []string{
			"agent-observability/runs?" + window,
			"agent-observability/sessions?" + window,
			"agent-observability/facets?" + window,
			"agent-observability/diagnostics?" + window,
			"agent-observability/sessions/detail?" + window + "&service_name=ongrid-manager&service_namespace=ongrid&environment=test&session_id=" + caller.otherName + "-session",
			"traces/search?" + window + "&q={}&user_id=999",
			"traces/tags/input.value/values",
			"traces/tags/span.ongrid.agent.session.id/values",
			"traces/tags/ongrid.agent.worker.id/values",
			"traces/tags/gen_ai.conversation.id/values",
			"traces/tags/output.value/values",
			"traces/tags/ongrid.skills.active/values",
			"traces/tags/langfuse.observation.input/values",
			"traces/tags/langfuse.observation.output/values",
			"traces/tags/langfuse.observation.status_message/values",
			"traces/tags/langfuse.observation.metadata.eino_callback_error/values",
		} {
			t.Run(caller.name+"/"+strings.Split(path, "?")[0], func(t *testing.T) {
				out := request(path, caller.id, "user")
				if out.Code != 200 || strings.Contains(out.Body.String(), caller.otherName) {
					t.Fatalf("foreign data leaked or query failed: %d %s", out.Code, out.Body.String())
				}
				if (strings.HasPrefix(path, "agent-observability/runs?") || path == "traces/tags/input.value/values") && !strings.Contains(out.Body.String(), caller.name+"-question") {
					t.Fatal("own conversation disappeared")
				}
			})
		}
		for _, prefix := range []string{"agent-observability/traces/", "traces/"} {
			for _, suffix := range []string{"", "?entry_span_id=0000000000000001"} {
				foreignID := uint64(3) - caller.id
				out := request(fmt.Sprintf("%s%032x%s", prefix, foreignID, suffix), caller.id, "user")
				if out.Code != http.StatusNotFound || strings.Contains(out.Body.String(), caller.otherName) {
					t.Fatalf("foreign direct trace: %d %s", out.Code, out.Body.String())
				}
				out = request(fmt.Sprintf("%s%032x%s", prefix, caller.id, suffix), caller.id, "user")
				if out.Code != 200 || !strings.Contains(out.Body.String(), caller.name+"-question") {
					t.Fatalf("own direct trace: %d %s", out.Code, out.Body.String())
				}
			}
		}
	}
	if globalTagReads.Load() != 0 {
		t.Fatal("non-admin queried the global attribute dictionary")
	}
	// Ordinary facets retain the original backend dictionary for every user.
	for _, tag := range []string{"service.name", "resource.service.name", "name", "span:name", "http.method", "custom.tag", "gen_ai.request.model", "gen_ai.usage.total_tokens", "ongrid.agent.kernel", "langfuse.observation.type"} {
		for _, user := range []uint64{1, 2} {
			out := request("traces/tags/"+tag+"/values", user, "user")
			if out.Code != http.StatusOK || out.Body.String() != "{\"values\":[\"alice-private\",\"bob-private\"]}\n" {
				t.Fatalf("ordinary tag dictionary changed: %d %s", out.Code, out.Body.String())
			}
		}
	}
	if globalTagReads.Load() != 20 {
		t.Fatal("ordinary tags were rebuilt from sampled traces")
	}
	// Admin detail access matches the existing chat API exception.
	out := request(fmt.Sprintf("agent-observability/traces/%032x", 2), 1, "admin")
	if out.Code != 200 || !strings.Contains(out.Body.String(), "bob-question") {
		t.Fatalf("admin access changed: %d %s", out.Code, out.Body.String())
	}
	// No positive access cache may outlive deletion of the session row.
	delete(owners, "alice-session")
	out = request(fmt.Sprintf("traces/%032x", 1), 1, "user")
	if out.Code != http.StatusNotFound {
		t.Fatalf("deleted session still accessible: %d %s", out.Code, out.Body.String())
	}
	out = request("traces/search?"+window, 1, "user")
	var result struct {
		Traces  []json.RawMessage `json:"traces"`
		Metrics json.RawMessage   `json:"metrics"`
		Partial *bool             `json:"partial"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil || len(result.Traces) != 0 || string(result.Metrics) != `{"inspectedTraces":2}` {
		t.Fatalf("hidden IDs leaked or global search metrics changed: %s, %v", out.Body.String(), err)
	}
	if result.Partial != nil {
		t.Fatal("generic Trace API gained a new partial field")
	}
}
