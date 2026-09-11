package apm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

type errorTraceFixture struct {
	body      json.RawMessage
	summaries string
	query     tracequery.SearchOptions
	failedID  string
	calls     atomic.Int32
}

type concurrentErrorTraces struct {
	active, peak, calls atomic.Int32
}

func (f *concurrentErrorTraces) SearchTraces(context.Context, tracequery.SearchOptions) (*tracequery.SearchResult, error) {
	ids := make([]map[string]string, 50)
	for i := range ids {
		ids[i] = map[string]string{"traceID": fmt.Sprintf("%032x", i+1)}
	}
	body, err := json.Marshal(ids)
	return &tracequery.SearchResult{Traces: body}, err
}

func (f *concurrentErrorTraces) GetTrace(ctx context.Context, _ string) (*tracequery.TraceResult, error) {
	active := f.active.Add(1)
	defer f.active.Add(-1)
	for old := f.peak.Load(); active > old; old = f.peak.Load() {
		if f.peak.CompareAndSwap(old, active) {
			break
		}
	}
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	f.calls.Add(1)
	return &tracequery.TraceResult{Body: json.RawMessage(`{"resourceSpans":[]}`)}, nil
}

func TestErrorGroupsBoundConcurrentReadersWithoutDroppingTraces(t *testing.T) {
	traces := &concurrentErrorTraces{}
	svc := New(nil, traces, nil)
	workers, ctx := errgroup.WithContext(t.Context())
	for range 4 {
		workers.Go(func() error {
			out, err := svc.ErrorGroups(ctx, testQuery())
			if err != nil {
				return err
			}
			if out.SampledTraces != 50 || out.FailedTraces != 0 {
				return fmt.Errorf("lost trace details: %+v", out)
			}
			return nil
		})
	}
	if err := workers.Wait(); err != nil {
		t.Fatal(err)
	}
	if traces.peak.Load() > 16 || traces.peak.Load() < 8 || traces.calls.Load() != 200 {
		t.Fatalf("wrong concurrency/coverage: peak=%d calls=%d", traces.peak.Load(), traces.calls.Load())
	}
	if len(svc.errorReads) != 0 {
		t.Fatal("leaked reader slots")
	}
}

func (f *errorTraceFixture) SearchTraces(_ context.Context, q tracequery.SearchOptions) (*tracequery.SearchResult, error) {
	f.query = q
	return &tracequery.SearchResult{Traces: json.RawMessage(f.summaries)}, nil
}
func (f *errorTraceFixture) GetTrace(_ context.Context, id string) (*tracequery.TraceResult, error) {
	f.calls.Add(1)
	if id == f.failedID {
		return nil, fmt.Errorf("unavailable")
	}
	return &tracequery.TraceResult{Body: f.body}, nil
}

func TestErrorGroupsUseMatchingSpansAndPreserveSampleBoundaries(t *testing.T) {
	attrs := func(values map[string]string) []any {
		out := []any{}
		for key, value := range values {
			out = append(out, map[string]any{"key": key, "value": map[string]string{"stringValue": value}})
		}
		return out
	}
	span := func(id, operation, stack string) map[string]any {
		return map[string]any{"spanId": id, "kind": "SPAN_KIND_SERVER", "status": map[string]int{"code": 2}, "name": operation, "startTimeUnixNano": "1300000000000", "attributes": attrs(map[string]string{"http.request.method": "GET", "http.route": operation, "http.response.status_code": "500"}), "events": []any{map[string]any{"name": "exception", "attributes": attrs(map[string]string{"exception.type": "DatabaseError", "exception.stacktrace": stack})}}}
	}
	resource := func(service, version, instance string, spans ...any) map[string]any {
		return map[string]any{"resource": map[string]any{"attributes": attrs(map[string]string{"service.name": service, "service.namespace": "trade", "deployment.environment.name": "production", "service.version": version, "service.instance.id": instance, "device_id": "42"})}, "scopeSpans": []any{map[string]any{"spans": spans}}}
	}
	first, second := span("1", "/orders", "database.go:42"), span("2", "/orders", "database.go:42")
	second["startTimeUnixNano"] = "1400000000000"
	client := span("3", "/orders", "database.go:42")
	client["kind"] = 3
	old := span("4", "/orders", "database.go:42")
	old["startTimeUnixNano"] = "900000000000"
	body, err := json.Marshal(map[string]any{"resourceSpans": []any{
		resource("gateway", "v1", "foreign", span("5", "/orders", "database.go:42")),
		resource("orders", "v1", "one", first, first, client, old),
		resource("orders", "v2", "two", second, span("6", "/other", "database.go:42"), span("7", "/orders", "other.go:17")),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, version, instance, operation string
		groups, count                      int
	}{
		{"all", "", "", "", 3, 2}, {"version", "v1", "", "", 1, 1}, {"instance", "", "two", "", 3, 1}, {"operation", "", "", "GET /orders", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trace := &errorTraceFixture{body: body, summaries: `[{"traceID":"1"},{"traceID":"2"}]`, failedID: strings.Repeat("0", 31) + "2"}
			q := testQuery()
			q.MetricSource = "application_metrics"
			q.ServiceVersion = tc.version
			q.InstanceID = tc.instance
			q.Operation = tc.operation
			svc := New(nil, trace, nil)
			ctx := tenantctx.With(t.Context(), tenantctx.Tenant{UserID: 1})
			result, err := svc.ErrorGroups(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if result.Total != tc.groups || result.Items[0].Count != tc.count || result.FailedTraces != 1 || result.SampledTraces != 1 {
				t.Fatalf("wrong groups: %+v", result)
			}
			if trace.query.Limit != 50 || !strings.Contains(trace.query.Query, `resource.service.name = "orders"`) || !strings.Contains(trace.query.Query, "status = error") {
				t.Fatalf("unscoped search: %+v", trace.query)
			}
			if tc.name == "all" {
				g := result.Items[0]
				if g.FirstSeen != 1300 || g.LastSeen != 1400 || len(g.Versions) != 2 || len(g.Instances) != 2 || g.SpanID != "2" || g.Operation != "GET /orders" {
					t.Fatalf("lost evidence: %+v", g)
				}
				q.Page, q.PageSize, q.SnapshotID = 2, 1, result.SnapshotID
				page, err := svc.ErrorGroups(ctx, q)
				if err != nil || page.Total != 3 || len(page.Items) != 1 || page.Items[0].Fingerprint != result.Items[1].Fingerprint || trace.calls.Load() != 2 {
					t.Fatalf("pagination reread or lost errors: %+v %v calls=%d", page, err, trace.calls.Load())
				}
				trace.failedID = "" // Previously unavailable details arrived.
				q.Page, q.SnapshotID = 1, ""
				fresh, err := svc.ErrorGroups(ctx, q)
				if err != nil || fresh.Items[0].Count != 4 || fresh.FailedTraces != 0 || trace.calls.Load() != 4 || fresh.SnapshotID == result.SnapshotID {
					t.Fatalf("refresh missed newly available errors: %+v %v", fresh, err)
				}
			}
		})
	}
	q := testQuery()
	q.DeviceID = "other"
	trace := &errorTraceFixture{body: body, summaries: `[{"traceID":"1"}]`}
	result, err := New(nil, trace, nil).ErrorGroups(t.Context(), q)
	if err != nil || result.Total != 0 {
		t.Fatalf("leaked another device: %+v %v", result, err)
	}
	q = testQuery()
	q.ClusterNodeID = 103
	result, err = New(nil, trace, nil).WithClusterScopes(clusterScopes{}).ErrorGroups(t.Context(), q)
	if err != nil || result.Total != 0 {
		t.Fatalf("leaked empty cluster: %+v %v", result, err)
	}
}

func TestErrorSnapshotsPreserveScopeExpiryAndMemoryBounds(t *testing.T) {
	ctx := tenantctx.With(t.Context(), tenantctx.Tenant{UserID: 1})
	q := testQuery()
	cache := &errorSnapshots{}
	out := &ErrorGroups{Items: []ErrorGroup{{Fingerprint: "first", Count: 7}, {Fingerprint: "last", Count: 9}}, Total: 2, FailedTraces: 1, Truncated: true}
	cache.save(ctx, q, out)
	q.SnapshotID, q.Page, q.PageSize = out.SnapshotID, 2, 1
	page, err := cache.page(ctx, q)
	if err != nil || len(page.Items) != 1 || page.Items[0].Count != 9 || page.FailedTraces != 1 || !page.Truncated {
		t.Fatalf("lost snapshot evidence: %+v %v", page, err)
	}
	page.Items[0].Count = 0
	again, err := cache.page(ctx, q)
	if err != nil || again.Items[0].Count != 9 {
		t.Fatal("response mutated stored snapshot")
	}
	for _, field := range []string{"version", "window", "protocol", "cluster"} {
		changed := q
		switch field {
		case "version":
			changed.ServiceVersion = "another"
		case "window":
			changed.End = changed.End.Add(time.Minute)
		case "protocol":
			changed.Protocol = "rpc"
		case "cluster":
			changed.resourceScope = &ResourceScope{DeviceIDs: []string{"other"}}
		}
		if _, err := cache.page(ctx, changed); err == nil {
			t.Fatalf("snapshot crossed %s scope", field)
		}
	}
	if _, err := cache.page(tenantctx.With(t.Context(), tenantctx.Tenant{UserID: 2}), q); err == nil {
		t.Fatal("snapshot crossed caller boundary")
	}
	entry := cache.items[q.SnapshotID]
	entry.expires = time.Now().Add(-time.Second)
	cache.items[q.SnapshotID] = entry
	if _, err := cache.page(ctx, q); err == nil {
		t.Fatal("expired snapshot reused silently")
	}
	for range 12 {
		cache.save(ctx, q, out)
	}
	if len(cache.items) != 8 {
		t.Fatalf("unbounded cache: %d", len(cache.items))
	}
	out.Items[0].StackTrace = strings.Repeat("x", 2<<20)
	cache.save(ctx, q, out)
	if out.SnapshotID != "" || len(cache.items) != 8 {
		t.Fatal("oversized snapshot retained")
	}
}

func TestErrorGroupsBoundTraceReadsAndReportFailures(t *testing.T) {
	ids := []map[string]string{}
	for i := 1; i <= 60; i++ {
		ids = append(ids, map[string]string{"traceID": fmt.Sprintf("%032x", i)})
	}
	summaries, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	trace := &errorTraceFixture{body: json.RawMessage(`{"resourceSpans":[]}`), summaries: string(summaries)}
	result, err := New(nil, trace, nil).ErrorGroups(t.Context(), testQuery())
	if err != nil || !result.Truncated || trace.calls.Load() != 50 {
		t.Fatalf("unbounded details: %+v calls=%d %v", result, trace.calls.Load(), err)
	}
	trace = &errorTraceFixture{body: json.RawMessage(`broken`), summaries: `[{"traceID":"1"}]`}
	if _, err = New(nil, trace, nil).ErrorGroups(t.Context(), testQuery()); err == nil {
		t.Fatal("invalid trace became no errors")
	}
}

func TestGoErrorStacksIgnoreOnlyDynamicRuntimeValues(t *testing.T) {
	first := "goroutine 392352 [running]:\nmain.(*App).checkout(0x40001, {0x8000, 0x22})\n\tapp/checkout.go:44 +0x528\ncreated by net/http.(*Server).Serve in goroutine 13\n\tnet/http/server.go:3493 +0x384"
	second := "goroutine 14 [running]:\nmain.(*App).checkout(0x50003, {0x7000, 0x33})\n\tapp/checkout.go:44 +0x500\ncreated by net/http.(*Server).Serve in goroutine 28\n\tnet/http/server.go:3493 +0x399"
	if errorStackKey(first) != errorStackKey(second) {
		t.Fatal("goroutine IDs and addresses split the same error")
	}
	if errorStackKey(first) == errorStackKey(strings.Replace(second, "checkout.go:44", "checkout.go:45", 1)) {
		t.Fatal("different source locations were merged")
	}
	other := "TypeError: different failure\n  at orders (/app/orders.js:20:1)"
	if errorStackKey(other) != other {
		t.Fatal("unrecognized stack format was changed")
	}
}

func TestRPCErrorGroupsMatchOldAndNewMethodAttributes(t *testing.T) {
	for _, tc := range []struct{ attrs, operation string }{
		{`{"key":"rpc.system.name","value":{"stringValue":"grpc"}},{"key":"rpc.method","value":{"stringValue":"Orders/Get"}}`, "Orders/Get"},
		{`{"key":"rpc.system","value":{"stringValue":"grpc"}},{"key":"rpc.service","value":{"stringValue":"Orders"}},{"key":"rpc.method","value":{"stringValue":"Get"}}`, "Orders/Get"},
	} {
		body := `{"batches":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"orders"}},{"key":"service.namespace","value":{"stringValue":"trade"}},{"key":"deployment.environment.name","value":{"stringValue":"production"}}]},"instrumentationLibrarySpans":[{"spans":[{"spanId":"1","kind":2,"name":"root-is-not-the-method","startTimeUnixNano":"1300000000000","status":{"code":2},"attributes":[` + tc.attrs + `,{"key":"rpc.grpc.status_code","value":{"intValue":"14"}}]}]}]}]}`
		trace := &errorTraceFixture{body: json.RawMessage(body), summaries: `[{"traceID":"1"}]`}
		q := testQuery()
		q.MetricSource, q.Protocol, q.Operation = "application_metrics", "rpc", tc.operation
		out, err := New(nil, trace, nil).ErrorGroups(t.Context(), q)
		if err != nil || len(out.Items) != 1 || out.Items[0].Operation != tc.operation || out.Items[0].StatusCode != "14" || out.Items[0].ErrorType != "" {
			t.Fatalf("RPC group: %+v %v", out, err)
		}
	}
}
