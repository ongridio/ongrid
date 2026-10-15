package agenttrace

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

type fixtureQuerier struct {
	body    []byte
	ids     []map[string]string
	metrics json.RawMessage
	readErr error
	query   string
	active  atomic.Int32
	peak    atomic.Int32
}

func (f *fixtureQuerier) SearchTraces(_ context.Context, q tracequery.SearchOptions) (*tracequery.SearchResult, error) {
	f.query = q.Query
	b, _ := json.Marshal(f.ids)
	return &tracequery.SearchResult{Traces: b, Metrics: f.metrics}, nil
}
func (f *fixtureQuerier) GetTrace(ctx context.Context, id string) (*tracequery.TraceResult, error) {
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		old := f.peak.Load()
		if old >= n || f.peak.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Millisecond):
	}
	return &tracequery.TraceResult{Body: f.body}, f.readErr
}

func fixtureQuery() Query {
	return Query{Start: time.Unix(1799999990, 0), End: time.Unix(1800000100, 0)}
}

func TestBoundedQueryAndScopedSessions(t *testing.T) {
	querier := &fixtureQuerier{body: testBody(t, testSpan(1, 0, map[string]any{"openinference.span.kind": "AGENT", "session.id": "conversation"})), metrics: json.RawMessage(`{"completedJobs":1,"totalJobs":2}`)}
	for range candidateLimit {
		querier.ids = append(querier.ids, map[string]string{"traceID": fixtureTraceID})
	}
	out, err := New(querier).List(t.Context(), fixtureQuery())
	if err != nil {
		t.Fatal(err)
	}
	if !out.Metadata.Partial || !slices.Contains(out.Metadata.Reasons, "candidate_limit") || !slices.Contains(out.Metadata.Reasons, "upstream_partial") || len(out.Items) != 1 || querier.peak.Load() > 4 {
		t.Fatalf("unbounded or incorrect result: %+v, peak %d", out, querier.peak.Load())
	}
	if len(out.Sessions) != 1 || out.Sessions[0].RunCount != 1 || strings.Contains(querier.query, "kind = server") {
		t.Fatalf("wrong session/query: %+v", out)
	}
	a := out.Items[0]
	b := a
	b.Identity.ServiceNamespace = "another-app"
	if len(sessions([]Run{a, b})) != 2 {
		t.Fatal("sessions from different apps merged")
	}
}

func TestReadFailureAndCancellation(t *testing.T) {
	querier := &fixtureQuerier{ids: []map[string]string{{"traceID": fixtureTraceID}}, readErr: tracequery.ErrResponseTooLarge}
	out, err := New(querier).List(t.Context(), fixtureQuery())
	if err != nil {
		t.Fatal(err)
	}
	if out.Metadata.ReadTraces != 0 || !out.Metadata.Partial || out.Metadata.Failures[0].Reason != "trace_too_large" {
		t.Fatalf("failure hidden: %+v", out)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := New(querier).List(ctx, fixtureQuery()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestQueryValidationAndEscaping(t *testing.T) {
	q := fixtureQuery()
	q.ServiceName = `x" } | rate() { name = "`
	query := TraceQL(q)
	if !strings.Contains(query, `resource.service.name = "x\" } | rate() { name = \""`) {
		t.Fatalf("unescaped filter: %s", query)
	}
	q.SessionID = "thread"
	if !errors.Is(q.Validate(), errs.ErrInvalid) {
		t.Fatal("unscoped session accepted")
	}
	empty := ""
	q.ServiceNamespace, q.Environment = &empty, &empty
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	q.End = q.Start.Add(8 * 24 * time.Hour)
	if !errors.Is(q.Validate(), errs.ErrInvalid) {
		t.Fatal("unbounded window accepted")
	}
}

func TestSearchTraceIDWithoutLeadingZero(t *testing.T) {
	querier := &fixtureQuerier{body: testBody(t, testSpan(1, 0, map[string]any{"openinference.span.kind": "AGENT"})), ids: []map[string]string{{"traceID": strings.TrimLeft(fixtureTraceID, "0")}}}
	out, err := New(querier).List(t.Context(), fixtureQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || len(out.Metadata.Failures) != 0 {
		t.Fatalf("short Tempo ID rejected: %+v", out)
	}
}

func TestIngestionValidatesExternalDestination(t *testing.T) {
	svc := New(nil)
	q := IngestionRequest{SDK: "langsmith", Identity: Identity{"agent", "support", "test"}, Target: ReceiverTarget{Location: "external"}}
	for _, endpoint := range []string{"http://user:password@localhost:4318", "file:///tmp/trace", "https://host/otel?token=secret", "http://"} {
		q.Endpoint = endpoint
		if _, err := svc.Ingestion(t.Context(), q); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("unsafe endpoint %q accepted: %v", endpoint, err)
		}
	}
	q.Endpoint = "http://collector:4318/v1/traces"
	out, err := svc.Ingestion(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if out.Receiver.Endpoint != q.Endpoint || out.Environment["OTEL_SERVICE_NAME"] != "agent" {
		t.Fatalf("bad config: %+v", out)
	}
}
