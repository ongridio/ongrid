package agenttrace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

type accessSessions struct {
	owners map[string]uint64
	err    error
}

func (s accessSessions) GetSession(_ context.Context, id string) (*aiopsmodel.Session, error) {
	if s.err != nil {
		return nil, s.err
	}
	if owner, ok := s.owners[id]; ok {
		return &aiopsmodel.Session{ID: id, UserID: owner}, nil
	}
	return nil, errs.ErrNotFound
}

func accessContext(id uint64) context.Context {
	return tenantctx.With(context.Background(), tenantctx.Tenant{UserID: id, Role: "user"})
}

// The fixture deliberately returns a foreign trace even for a scoped search:
// search filters and the selected UI application are not authorization.
func TestSessionIsolation(t *testing.T) {
	body := testBody(t, testSpan(1, 0, map[string]any{
		"gen_ai.operation.name": "invoke_agent", "gen_ai.conversation.id": "foreign-session",
		"ongrid.agent.session.id": "foreign-session", "input.value": "private question",
	}))
	backend := &fixtureQuerier{body: body, ids: []map[string]string{{"traceID": fixtureTraceID}}, metrics: json.RawMessage(`{"totalJobs":1}`)}
	svc := New(NewScopedQuerier(backend, accessSessions{owners: map[string]uint64{"foreign-session": 2}}))
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "user"})
	out, err := svc.List(ctx, fixtureQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 0 || len(out.Sessions) != 0 {
		t.Fatal("another user's session and content are visible")
	}
	if len(out.Metadata.Failures) != 0 || out.Metadata.CandidateTraces != 0 || len(out.Facets.Applications) != 0 || out.Metadata.ReadTraces != 0 {
		t.Fatalf("private metadata remains visible: %+v", out)
	}
	if _, err := svc.Trace(ctx, fixtureTraceID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("another user's raw trace is visible by ID: %v", err)
	}
	owner, err := svc.Trace(accessContext(2), fixtureTraceID)
	if err != nil || owner.Runs[0].InputPreview != "private question" {
		t.Fatalf("owner cannot read their trace: %v", err)
	}
	admin := tenantctx.With(ctx, tenantctx.Tenant{UserID: 1, Role: "admin"})
	if _, err := svc.Trace(admin, fixtureTraceID); err != nil {
		t.Fatalf("existing admin access changed: %v", err)
	}
}

func TestRawTraceOwnershipBoundaries(t *testing.T) {
	owned := testSpan(1, 0, map[string]any{"gen_ai.operation.name": "invoke_agent", "ongrid.agent.session.id": "owned"})
	foreign := testSpan(2, 1, map[string]any{"ongrid.agent.session.id": "foreign", "input.value": "secret"})
	duplicate := testSpan(1, 0, map[string]any{"ongrid.agent.session.id": "foreign"})
	duplicateAttr := testSpan(1, 0, nil)
	duplicateAttr["attributes"] = append(testAttributes(map[string]any{"ongrid.agent.session.id": "foreign"}), testAttributes(map[string]any{"ongrid.agent.session.id": "owned"})...)
	conversationFirst := testSpan(1, 0, nil)
	conversationFirst["attributes"] = append(testAttributes(map[string]any{"gen_ai.conversation.id": "foreign"}), testAttributes(map[string]any{"ongrid.agent.session.id": "owned"})...)
	sessionFirst := testSpan(1, 0, nil)
	sessionFirst["attributes"] = append(testAttributes(map[string]any{"ongrid.agent.session.id": "owned"}), testAttributes(map[string]any{"gen_ai.conversation.id": "foreign"})...)
	for _, tc := range []struct {
		name   string
		spans  []map[string]any
		modify func(*struct {
			Resources []resourceSpans `json:"resourceSpans"`
			Batches   []resourceSpans `json:"batches"`
		})
		allow bool
	}{
		{name: "owned", spans: []map[string]any{owned}, allow: true},
		{name: "mixed trace", spans: []map[string]any{owned, foreign}},
		{name: "duplicate span", spans: []map[string]any{owned, duplicate}},
		{name: "duplicate attribute", spans: []map[string]any{duplicateAttr}},
		{name: "conversation before session marker", spans: []map[string]any{conversationFirst}},
		{name: "session marker before conversation", spans: []map[string]any{sessionFirst}},
		{name: "deleted session", spans: []map[string]any{testSpan(1, 0, map[string]any{"ongrid.agent.session.id": "deleted"})}},
		{name: "empty session", spans: []map[string]any{testSpan(1, 0, map[string]any{"ongrid.agent.session.id": ""})}},
		{name: "wrong session type", spans: []map[string]any{testSpan(1, 0, map[string]any{"ongrid.agent.session.id": 1})}},
		{name: "system session", spans: []map[string]any{testSpan(1, 0, map[string]any{"ongrid.agent.session.id": "system"})}},
		{name: "worker", spans: []map[string]any{testSpan(1, 0, map[string]any{"ongrid.agent.session.id": "worker", "gen_ai.conversation.id": "owned"})}, allow: true},
		{name: "external conversation collision", spans: []map[string]any{testSpan(1, 0, map[string]any{"gen_ai.conversation.id": "foreign", "gen_ai.operation.name": "chat"})}, allow: true},
		{name: "legacy envelope", spans: []map[string]any{owned, foreign}, modify: func(e *struct {
			Resources []resourceSpans `json:"resourceSpans"`
			Batches   []resourceSpans `json:"batches"`
		}) {
			e.Batches, e.Resources = e.Resources, nil
			e.Batches[0].Libraries, e.Batches[0].Scopes = e.Batches[0].Scopes, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := testBody(t, tc.spans...)
			if tc.modify != nil {
				var envelope struct {
					Resources []resourceSpans `json:"resourceSpans"`
					Batches   []resourceSpans `json:"batches"`
				}
				if err := json.Unmarshal(body, &envelope); err != nil {
					t.Fatal(err)
				}
				tc.modify(&envelope)
				var err error
				body, err = json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
			}
			q := NewScopedQuerier(&fixtureQuerier{body: body}, accessSessions{owners: map[string]uint64{"owned": 1, "worker": 1, "foreign": 2, "system": 0}})
			_, err := q.GetTrace(accessContext(1), fixtureTraceID)
			if tc.allow && err != nil || !tc.allow && !errors.Is(err, errs.ErrNotFound) {
				t.Fatalf("allow=%v, err=%v", tc.allow, err)
			}
		})
	}
}

func TestBuiltInTraceScopesAndMissingRoots(t *testing.T) {
	for _, scope := range []string{
		"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime",
		"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v2/chatruntime",
		"github.com/ongridio/ongrid/internal/manager/biz/aiops/agent",
		"github.com/ongridio/ongrid/internal/manager/biz/aiops/graph",
		"github.com/ongridio/ongrid/internal/pkg/llm",
	} {
		t.Run(scope, func(t *testing.T) {
			for _, session := range []string{"owned", "foreign", ""} {
				attrs := map[string]any{"gen_ai.operation.name": "invoke_agent"}
				if session != "" {
					attrs["gen_ai.conversation.id"] = session
				}
				body := strings.ReplaceAll(string(testBody(t, testSpan(1, 0, attrs))), `"name":"fixture"`, `"name":"`+scope+`"`)
				q := NewScopedQuerier(&fixtureQuerier{body: []byte(body)}, accessSessions{owners: map[string]uint64{"owned": 1, "foreign": 2}})
				_, err := q.GetTrace(accessContext(1), fixtureTraceID)
				if session == "owned" && err != nil || session != "owned" && !errors.Is(err, errs.ErrNotFound) {
					t.Fatalf("session=%q: %v", session, err)
				}
			}
		})
	}
	// Official Eino callback spans use an external SDK scope, but Manager's
	// resource identity still marks an in-progress trace as private.
	body := strings.ReplaceAll(string(testBody(t, testSpan(1, 0, map[string]any{"langfuse.observation.input": "secret"}))), "support-agent", "ongrid-manager")
	q := NewScopedQuerier(&fixtureQuerier{body: []byte(body)}, accessSessions{})
	if _, err := q.GetTrace(accessContext(1), fixtureTraceID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("unfinished root leaked content: %v", err)
	}
}

func TestAccessFailsClosedWithoutCallerOrRepository(t *testing.T) {
	body := testBody(t, testSpan(1, 0, map[string]any{"ongrid.agent.session.id": "owned"}))
	backend := &fixtureQuerier{body: body}
	q := NewScopedQuerier(backend, accessSessions{owners: map[string]uint64{"owned": 0}})
	for _, ctx := range []context.Context{context.Background(), accessContext(0)} {
		if _, err := q.GetTrace(ctx, fixtureTraceID); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("missing caller acquired system ownership: %v", err)
		}
	}
	if _, err := NewScopedQuerier(backend, nil).GetTrace(accessContext(1), fixtureTraceID); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("missing repository allowed access: %v", err)
	}
	dbErr := errors.New("database unavailable")
	if _, err := NewScopedQuerier(backend, accessSessions{err: dbErr}).GetTrace(accessContext(1), fixtureTraceID); !errors.Is(err, dbErr) {
		t.Fatalf("database error ignored: %v", err)
	}
}

func TestExternalSDKAccessUnchanged(t *testing.T) {
	for _, sdk := range []string{"langsmith", "openinference", "openllmetry", "langfuse"} {
		t.Run(sdk, func(t *testing.T) {
			body, err := os.ReadFile("testdata/sdk/" + sdk + ".json")
			if err != nil {
				t.Fatal(err)
			}
			q := NewScopedQuerier(&fixtureQuerier{body: body}, accessSessions{err: errors.New("must not query chat DB")})
			for _, ctx := range []context.Context{accessContext(1), context.Background()} {
				out, err := q.GetTrace(ctx, fixtureTraceID)
				if err != nil || string(out.Body) != string(body) {
					t.Fatalf("external SDK telemetry changed: %v", err)
				}
			}
		})
	}
}

func TestOwnedRootCannotAuthorizeUnrelatedIncompleteSpans(t *testing.T) {
	root := testSpan(1, 0, map[string]any{"ongrid.agent.session.id": "owned"})
	for _, tc := range []struct {
		name  string
		spans []map[string]any
		allow bool
	}{
		{"own child", []map[string]any{root, testSpan(2, 1, map[string]any{"langfuse.observation.input": "own question"})}, true},
		{"foreign root pending", []map[string]any{root, testSpan(2, 99, map[string]any{"langfuse.observation.input": "foreign question"})}, false},
		{"separate root", []map[string]any{root, testSpan(2, 0, map[string]any{"gen_ai.input.messages": "foreign question"})}, false},
		{"intermediate span", []map[string]any{root, testSpan(3, 1, nil), testSpan(2, 3, map[string]any{"langfuse.observation.input": "own question"})}, true},
		{"parent cycle", []map[string]any{root, testSpan(3, 2, nil), testSpan(2, 3, map[string]any{"langfuse.observation.input": "foreign question"})}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.ReplaceAll(string(testBody(t, tc.spans...)), "support-agent", "ongrid-manager")
			q := NewScopedQuerier(&fixtureQuerier{body: []byte(body)}, accessSessions{owners: map[string]uint64{"owned": 1}})
			_, err := q.GetTrace(accessContext(1), fixtureTraceID)
			if tc.allow && err != nil || !tc.allow && !errors.Is(err, errs.ErrNotFound) {
				t.Fatalf("allow=%v: %v", tc.allow, err)
			}
		})
	}
}

type accessReadFailure struct {
	*fixtureQuerier
	badID string
	err   error
}

func (q accessReadFailure) GetTrace(ctx context.Context, id string) (*tracequery.TraceResult, error) {
	if id == q.badID {
		if q.err != nil {
			return nil, q.err
		}
		return &tracequery.TraceResult{Body: json.RawMessage(`{"broken":`)}, nil
	}
	return q.fixtureQuerier.GetTrace(ctx, id)
}

func TestSearchPreservesVerifiedResultsAndPartialWithoutHiddenIDs(t *testing.T) {
	for _, readErr := range []error{tracequery.ErrResponseTooLarge, errors.New("foreign private error"), nil} {
		badID := "ffffffffffffffffffffffffffffffff"
		backend := accessReadFailure{fixtureQuerier: &fixtureQuerier{
			body: testBody(t, testSpan(1, 0, map[string]any{"gen_ai.operation.name": "invoke_agent", "ongrid.agent.session.id": "owned"})),
			ids:  []map[string]string{{"traceID": fixtureTraceID}, {"traceID": badID, "rootTraceName": "foreign private"}},
		}, badID: badID, err: readErr}
		out, err := New(NewScopedQuerier(backend, accessSessions{owners: map[string]uint64{"owned": 1}})).List(accessContext(1), fixtureQuery())
		if err != nil || len(out.Items) != 1 || !out.Metadata.Partial || len(out.Metadata.Failures) != 0 {
			t.Fatalf("safe partial results lost: %+v %v", out, err)
		}
		encoded, err := json.Marshal(out)
		if err != nil || strings.Contains(string(encoded), "foreign private") || strings.Contains(string(encoded), badID) {
			t.Fatalf("unverified details leaked: %s %v", encoded, err)
		}
	}
}

func TestFilteredCandidatesRetainSearchLimit(t *testing.T) {
	backend := &fixtureQuerier{body: testBody(t, testSpan(1, 0, map[string]any{"ongrid.agent.session.id": "foreign"}))}
	for range candidateLimit {
		backend.ids = append(backend.ids, map[string]string{"traceID": fixtureTraceID})
	}
	out, err := New(NewScopedQuerier(backend, accessSessions{owners: map[string]uint64{"foreign": 2}})).List(accessContext(1), fixtureQuery())
	if err != nil || !out.Metadata.Partial || len(out.Items) != 0 || len(out.Metadata.Failures) != 0 || backend.peak.Load() > 4 {
		t.Fatalf("filter lost bounds or leaked hidden candidates: %+v %v", out, err)
	}
}

// Search options, raw summaries, backend metrics and ordinary facets retain
// their original contract when the query contains no private Agent data.
type accessContractQuerier struct {
	*fixtureQuerier
	options  tracequery.SearchOptions
	result   *tracequery.SearchResult
	tag      string
	searches int
}

func (q *accessContractQuerier) SearchTraces(_ context.Context, opts tracequery.SearchOptions) (*tracequery.SearchResult, error) {
	q.options = opts
	q.searches++
	return q.result, nil
}

func (q *accessContractQuerier) TagValues(_ context.Context, tag string) ([]string, error) {
	q.tag = tag
	return []string{"z-old-service", "a-custom-value"}, nil
}

func TestOrdinaryTraceSearchAndTagsUnchanged(t *testing.T) {
	backend := &accessContractQuerier{
		fixtureQuerier: &fixtureQuerier{body: testBody(t, testSpan(1, 0, map[string]any{"http.method": "GET"}))},
		result: &tracequery.SearchResult{
			Traces:  json.RawMessage(`[{ "traceID": "` + fixtureTraceID + `", "spanSet": {"attributes": [{"key":"custom.tag","value":{"stringValue":"keep"}}]}, "futureField": 42 }]`),
			Metrics: json.RawMessage(`{ "inspectedTraces": 123, "completedJobs": 2, "totalJobs": 3 }`),
		},
	}
	q := NewScopedQuerier(backend, accessSessions{err: errors.New("ordinary traces must not require chat ownership")})
	for _, limit := range []int{0, 1, 2000} {
		opts := tracequery.SearchOptions{Query: `{ resource.service.name = "web" } | select(span.custom.tag)`, Tags: map[string]string{"service.name": "web"}, Limit: limit, Start: time.Unix(100, 0), End: time.Unix(200, 0), MinDuration: time.Millisecond, MaxDuration: time.Second}
		out, err := q.SearchTraces(accessContext(1), opts)
		if err != nil || !reflect.DeepEqual(backend.options, opts) || string(out.Traces) != string(backend.result.Traces) || string(out.Metrics) != string(backend.result.Metrics) {
			t.Fatalf("ordinary search changed: %+v, %v", out, err)
		}
		encoded, err := json.Marshal(out)
		if err != nil || strings.Contains(string(encoded), "partial") {
			t.Fatalf("generic search response schema changed: %s, %v", encoded, err)
		}
	}
	searches := backend.searches
	for _, tag := range []string{"service.name", "resource.service.name", "service.namespace", "deployment.environment.name", "name", "span:name", "http.method", "custom.tag", "gen_ai.request.model", "gen_ai.usage.total_tokens", "ongrid.agent.kernel", "langfuse.observation.type"} {
		values, err := q.TagValues(accessContext(1), tag)
		if err != nil || backend.tag != tag || !reflect.DeepEqual(values, []string{"z-old-service", "a-custom-value"}) || backend.searches != searches {
			t.Fatalf("ordinary tags changed or sampled: tag=%q values=%v err=%v", tag, values, err)
		}
	}
}

func TestOrdinaryRawTraceHasNoAgentResourceLimit(t *testing.T) {
	resources := make([]resourceSpans, 1001)
	body, err := json.Marshal(map[string]any{"resourceSpans": resources})
	if err != nil {
		t.Fatal(err)
	}
	q := NewScopedQuerier(&fixtureQuerier{body: body}, accessSessions{err: errors.New("must not query chat DB")})
	out, err := q.GetTrace(accessContext(1), fixtureTraceID)
	if err != nil || string(out.Body) != string(body) {
		t.Fatalf("ordinary raw trace acquired Agent limits: %v", err)
	}
}
