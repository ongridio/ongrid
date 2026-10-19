package agenttrace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

// SessionReader resolves authoritative ownership, including worker transcripts.
type SessionReader interface {
	GetSession(context.Context, string) (*aiopsmodel.Session, error)
}

// ScopedQuerier protects built-in chat telemetry at every Manager read path.
// External SDK telemetry retains its deployment-wide scope. Do not cache an
// authorization decision across requests: sessions can be deleted meanwhile.
type ScopedQuerier struct {
	traces   TraceQuerier
	sessions SessionReader
	reads    chan struct{}
}

func NewScopedQuerier(traces TraceQuerier, sessions SessionReader) *ScopedQuerier {
	return &ScopedQuerier{traces: traces, sessions: sessions, reads: make(chan struct{}, 4)}
}

func traceAdmin(ctx context.Context) bool {
	caller, ok := tenantctx.From(ctx)
	// Match the existing chat Service.GetSession admin exception.
	return ok && caller.Role == "admin"
}

func (q *ScopedQuerier) GetTrace(ctx context.Context, id string) (*tracequery.TraceResult, error) {
	if q.traces == nil {
		return nil, errs.ErrNotWiredYet
	}
	select {
	case q.reads <- struct{}{}:
		defer func() { <-q.reads }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out, err := q.traces.GetTrace(ctx, id)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("agenttrace: empty trace response")
	}
	if !traceAdmin(ctx) {
		if err := q.authorize(ctx, out.Body); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (q *ScopedQuerier) authorize(ctx context.Context, body []byte) error {
	if len(body) > tracequery.MaxResponseBytes {
		return tracequery.ErrResponseTooLarge
	}
	var envelope struct {
		Resources []resourceSpans `json:"resourceSpans"`
		Batches   []resourceSpans `json:"batches"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("agenttrace: decode access metadata: %w", err)
	}
	private := false
	spanCount := 0
	sessions := map[string]bool{}
	type accessSpan struct {
		parent  string
		private bool
		scoped  bool
	}
	spans := map[string][]accessSpan{}
	for _, resource := range append(envelope.Resources, envelope.Batches...) {
		manager := false
		for _, attr := range resource.Resource.Attributes {
			var name string
			if attr.Key == "service.name" && json.Unmarshal(attr.Value["stringValue"], &name) == nil && name == "ongrid-manager" {
				manager = true
			}
		}
		for _, group := range append(resource.Scopes, resource.Libraries...) {
			internal := internalAgentScope(group.Scope.Name) || internalAgentScope(group.Library.Name)
			for _, raw := range group.Spans {
				spanCount++
				var span rawSpan
				if err := json.Unmarshal(raw, &span); err != nil {
					return fmt.Errorf("agenttrace: decode access span: %w", err)
				}
				// Eino model/tool spans can arrive before the root finishes.
				// Missing root metadata must never make Manager content public.
				protected := internal
				for _, attr := range span.Attributes {
					if attr.Key == "ongrid.agent.session.id" || manager && (strings.HasPrefix(attr.Key, "gen_ai.") || strings.HasPrefix(attr.Key, "langfuse.")) {
						protected = true
					}
				}
				private = private || protected
				scoped := false
				// Inspect every raw span and every duplicate attribute, before
				// Normalize can discard duplicate IDs or flatten attributes.
				for _, attr := range span.Attributes {
					if attr.Key != "ongrid.agent.session.id" && !(protected && attr.Key == "gen_ai.conversation.id") {
						continue
					}
					private = true
					protected, scoped = true, true
					var id string
					if len(attr.Value) != 1 || json.Unmarshal(attr.Value["stringValue"], &id) != nil || id == "" {
						return errs.ErrNotFound
					}
					sessions[id] = true
				}
				id := canonicalID(span.SpanID, 8)
				if protected && id == "" {
					return errs.ErrNotFound
				}
				if id != "" {
					spans[id] = append(spans[id], accessSpan{canonicalID(span.ParentID, 8), protected, scoped})
				}
			}
		}
	}
	if !private {
		return nil
	}
	if len(envelope.Resources)+len(envelope.Batches) > 1000 || spanCount > 20000 {
		return fmt.Errorf("agenttrace: access metadata limit exceeded")
	}
	caller, ok := tenantctx.From(ctx)
	if !ok || caller.UserID == 0 || len(sessions) == 0 {
		return errs.ErrNotFound
	}
	if q.sessions == nil {
		return errs.ErrNotWiredYet
	}
	for id := range sessions {
		session, err := q.sessions.GetSession(ctx, id)
		if err != nil {
			return err
		}
		if session == nil || session.UserID != caller.UserID {
			return errs.ErrNotFound
		}
	}
	// A caller can supply traceparent, so one trace can contain unrelated
	// requests. An owned root must not authorize a different, unfinished
	// conversation whose model/tool spans arrived before its root span.
	state := map[string]uint8{}
	var owned func(string) bool
	owned = func(id string) bool {
		if state[id] != 0 {
			return state[id] == 2
		}
		state[id] = 1 // Also rejects parent cycles.
		if len(spans[id]) == 0 {
			return false
		}
		for _, span := range spans[id] {
			if !span.scoped && (span.parent == "" || !owned(span.parent)) {
				return false
			}
		}
		state[id] = 2
		return true
	}
	for id, copies := range spans {
		for _, span := range copies {
			if span.private && !owned(id) {
				return errs.ErrNotFound
			}
		}
	}
	return nil
}

func internalAgentScope(name string) bool {
	return strings.HasPrefix(name, "github.com/ongridio/ongrid/internal/manager/biz/aiops/") || name == "github.com/ongridio/ongrid/internal/pkg/llm"
}

func (q *ScopedQuerier) SearchTraces(ctx context.Context, opts tracequery.SearchOptions) (*tracequery.SearchResult, error) {
	return q.search(ctx, opts, nil)
}

// visit is used for scoped facets without retaining or re-reading whole traces.
// It can run concurrently, but only after the trace is authorized.
func (q *ScopedQuerier) search(ctx context.Context, opts tracequery.SearchOptions, visit func(string, *tracequery.TraceResult) error) (*tracequery.SearchResult, error) {
	if q.traces == nil {
		return nil, errs.ErrNotWiredYet
	}
	if traceAdmin(ctx) {
		return q.traces.SearchTraces(ctx, opts)
	}
	result, err := q.traces.SearchTraces(ctx, opts)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("agenttrace: empty search response")
	}
	var rows []json.RawMessage
	if len(result.Traces) > 0 {
		if err := json.Unmarshal(result.Traces, &rows); err != nil {
			return nil, fmt.Errorf("agenttrace: decode search access metadata: %w", err)
		}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 100 // Match tracequery.Client's default without changing the query.
	}
	partial := result.Partial || len(rows) >= limit
	visible := make([]bool, len(rows))
	failures := make([]error, len(rows))
	jobs := make(chan int, len(rows))
	for i := range rows {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(4, len(rows)) {
		workers.Go(func() {
			for i := range jobs {
				var row struct {
					TraceID string `json:"traceID"`
				}
				if json.Unmarshal(rows[i], &row) != nil || len(row.TraceID) == 0 || len(row.TraceID) > 32 {
					failures[i] = errs.ErrInvalid
					continue
				}
				id := canonicalID(strings.Repeat("0", 32-len(row.TraceID))+row.TraceID, 16)
				if id == "" {
					failures[i] = errs.ErrInvalid
					continue
				}
				trace, err := q.GetTrace(ctx, id)
				if err == nil && visit != nil {
					err = visit(id, trace)
				}
				visible[i] = err == nil
				if err != nil && !errors.Is(err, errs.ErrNotFound) {
					failures[i] = err
				}
			}
		})
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	filtered := make([]json.RawMessage, 0, len(rows))
	for i, row := range rows {
		if failures[i] != nil {
			// The backend error can contain an unauthorized trace ID or
			// content. Keep the other verified results, but expose only
			// incompleteness until ownership has been proven.
			partial = true
			continue
		}
		if visible[i] {
			filtered = append(filtered, row)
		}
	}
	body, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	// Filter only unreadable Agent content; preserve Tempo's deployment-wide
	// search metrics and all fields in authorized summaries.
	out := *result
	if len(filtered) != len(rows) {
		out.Traces = body
	}
	out.Partial = partial
	return &out, nil
}

// Ordinary Trace facets keep Tempo's global dictionary. Only Agent attributes
// that can expose a session or its content need authorized trace reads.
func (q *ScopedQuerier) TagValues(ctx context.Context, tag string) ([]string, error) {
	if traceAdmin(ctx) || !privateAgentTag(tag) {
		if tags, ok := q.traces.(interface {
			TagValues(context.Context, string) ([]string, error)
		}); ok {
			return tags.TagValues(ctx, tag)
		}
		return nil, errs.ErrNotWiredYet
	}
	values := map[string]bool{}
	var mu sync.Mutex
	_, err := q.search(ctx, tracequery.SearchOptions{Query: "{} with (most_recent = true)", Limit: candidateLimit}, func(id string, trace *tracequery.TraceResult) error {
		parsed, err := Normalize(id, trace.Body)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		add := func(value string) {
			// Facets are suggestions, not an unbounded content export.
			if len(values) < 1000 && len(value) <= 1024 && value != "" {
				values[value] = true
			}
		}
		for _, step := range parsed.Steps {
			for prefix, attrs := range map[string]Attributes{"span.": step.Attributes, "resource.": step.Resource} {
				if strings.HasPrefix(tag, "span.") || strings.HasPrefix(tag, "resource.") {
					if !strings.HasPrefix(tag, prefix) {
						continue
					}
				}
				if value, ok := attrs[strings.TrimPrefix(tag, prefix)]; ok {
					add(fmt.Sprint(value))
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

// These attributes carry built-in conversation identifiers or content.
// Model names, usage counters and other Agent metadata keep global facets.
func privateAgentTag(tag string) bool {
	key := strings.TrimPrefix(strings.TrimPrefix(tag, "span."), "resource.")
	switch key {
	case "ongrid.agent.session.id", "ongrid.agent.worker.id", "gen_ai.conversation.id",
		"input.value", "output.value", "ongrid.skills.active",
		"langfuse.observation.input", "langfuse.observation.output", "langfuse.observation.status_message":
		return true
	default:
		return strings.HasPrefix(key, "langfuse.observation.metadata.")
	}
}
