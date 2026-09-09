package tools

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

// Both tool adapters use the same exact-ID lookup and bounded pagination.
func queryTraceByID(ctx context.Context, client TraceQuerier, in QueryTraceQLArgs) (json.RawMessage, error) {
	if _, err := hex.DecodeString(in.TraceID); err != nil || (len(in.TraceID) != 16 && len(in.TraceID) != 32) {
		return nil, fmt.Errorf("query_traceql: trace_id must be 16 or 32 hexadecimal characters")
	}
	if in.Query != "" || in.DeviceID != nil || in.Service != "" || in.Operation != "" || in.Start != "" || in.End != "" || in.MinDuration != "" || in.MaxDuration != "" {
		return nil, fmt.Errorf("query_traceql: trace_id cannot be combined with search filters or time bounds")
	}
	if in.SpanOffset < 0 || in.Limit < 0 {
		return nil, fmt.Errorf("query_traceql: invalid span_offset or limit")
	}
	getter, ok := client.(interface {
		GetTrace(context.Context, string) (*tracequery.TraceResult, error)
	})
	if !ok {
		return nil, fmt.Errorf("query_traceql: trace detail is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, queryTraceqlCallTimeout)
	defer cancel()
	trace, err := getter.GetTrace(ctx, in.TraceID)
	if err != nil {
		return nil, fmt.Errorf("query_traceql: get trace: %w", err)
	}
	if trace == nil {
		return nil, fmt.Errorf("query_traceql: empty trace response")
	}
	type scope struct {
		Spans []json.RawMessage `json:"spans"`
	}
	type batch struct {
		Resource     json.RawMessage `json:"resource"`
		Scopes       []scope         `json:"scopeSpans"`
		LegacyScopes []scope         `json:"instrumentationLibrarySpans"`
	}
	var body struct {
		Batches   []batch `json:"batches"`
		Resources []batch `json:"resourceSpans"`
	}
	if err := json.Unmarshal(trace.Body, &body); err != nil {
		return nil, fmt.Errorf("query_traceql: decode trace: %w", err)
	}
	type entry struct {
		Resource json.RawMessage `json:"resource"`
		Span     json.RawMessage `json:"span"`
	}
	spans := []entry{}
	for _, group := range append(body.Batches, body.Resources...) {
		for _, scope := range append(group.Scopes, group.LegacyScopes...) {
			for _, span := range scope.Spans {
				spans = append(spans, entry{group.Resource, span})
			}
		}
	}
	if in.SpanOffset > len(spans) {
		return nil, fmt.Errorf("query_traceql: span_offset exceeds total spans")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 50
	}
	end, bytes := in.SpanOffset, 0
	for end < len(spans) && end-in.SpanOffset < min(limit, 100) {
		size := len(spans[end].Resource) + len(spans[end].Span)
		if bytes+size > 120*1024 {
			break
		}
		bytes += size
		end++
	}
	if end == in.SpanOffset && end < len(spans) {
		return nil, fmt.Errorf("query_traceql: single span exceeds 120 KiB; inspect trace %s in the Traces UI", in.TraceID)
	}
	var next *int
	if end < len(spans) {
		next = &end
	}
	out, err := json.Marshal(struct {
		TraceID   string  `json:"trace_id"`
		Total     int     `json:"total_spans"`
		Spans     []entry `json:"spans"`
		Truncated bool    `json:"truncated"`
		Next      *int    `json:"next_span_offset,omitempty"`
	}{in.TraceID, len(spans), spans[in.SpanOffset:end], next != nil, next})
	if err != nil {
		return nil, fmt.Errorf("query_traceql: encode trace: %w", err)
	}
	return out, nil
}
