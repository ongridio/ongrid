package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

type detailTraceQuerier struct {
	fakeTraceQuerier
	body    json.RawMessage
	traceID string
}

func (f *detailTraceQuerier) GetTrace(_ context.Context, id string) (*tracequery.TraceResult, error) {
	f.traceID = id
	return &tracequery.TraceResult{Body: f.body}, f.err
}

func TestQueryTraceQLDetailBothAdapters(t *testing.T) {
	for _, adapter := range []string{"registry", "base"} {
		t.Run(adapter, func(t *testing.T) {
			client := &detailTraceQuerier{body: json.RawMessage(`{"batches":[{"resource":{"attributes":[{"key":"service.instance.id","value":{"stringValue":"java-1"}}]},"scopeSpans":[{"spans":[{"spanId":"root","name":"GET /orders"},{"spanId":"child","parentSpanId":"root","status":{"code":2},"events":[{"name":"exception"}]}]}]}]}`)}
			call := func(args string) (string, error) {
				if adapter == "base" {
					return NewQueryTraceQLTool(client, nil).InvokableRun(t.Context(), args)
				}
				out, err := (&Registry{traceQuery: client}).executeQueryTraceQL(t.Context(), json.RawMessage(args))
				return string(out.ResultJSON), err
			}
			out, err := call(`{"trace_id":"0123456789abcdef0123456789abcdef","limit":1}`)
			if err != nil || !strings.Contains(out, `"total_spans":2`) || !strings.Contains(out, `"next_span_offset":1`) || !strings.Contains(out, "java-1") {
				t.Fatalf("first page = %s, %v", out, err)
			}
			out, err = call(`{"trace_id":"0123456789abcdef0123456789abcdef","span_offset":1}`)
			if err != nil || !strings.Contains(out, `"parentSpanId":"root"`) || !strings.Contains(out, `"exception"`) || !strings.Contains(out, `"truncated":false`) {
				t.Fatalf("second page = %s, %v", out, err)
			}
			for _, args := range []string{
				`{"trace_id":"../../secret"}`,
				`{"trace_id":"0123456789abcdef","device_id":650}`,
				`{"trace_id":"0123456789abcdef","service":"orders"}`,
				`{"trace_id":"0123456789abcdef","span_offset":-1}`,
			} {
				client.traceID = ""
				if _, err := call(args); err == nil || client.traceID != "" {
					t.Fatalf("invalid lookup dispatched: %s", args)
				}
			}
			client.body = json.RawMessage(`{"resourceSpans":[{"instrumentationLibrarySpans":[{"spans":[{"name":"legacy"}]}]}]}`)
			if out, err := call(`{"trace_id":"0123456789abcdef"}`); err != nil || !strings.Contains(out, "legacy") {
				t.Fatalf("legacy trace = %s, %v", out, err)
			}
			client.body = json.RawMessage(`{"batches":[{"scopeSpans":[{"spans":[{"name":"` + strings.Repeat("x", 121*1024) + `"}]}]}]}`)
			if _, err := call(`{"trace_id":"0123456789abcdef"}`); err == nil {
				t.Fatal("oversized span accepted")
			}
		})
	}
}
