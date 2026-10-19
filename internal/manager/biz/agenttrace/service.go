package agenttrace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

const candidateLimit = 50
const runLimit = 500

type TraceQuerier interface {
	SearchTraces(context.Context, tracequery.SearchOptions) (*tracequery.SearchResult, error)
	GetTrace(context.Context, string) (*tracequery.TraceResult, error)
}

type Service struct {
	traces    TraceQuerier
	reads     chan struct{}
	receivers ReceiverResolver
}

func New(traces TraceQuerier) *Service {
	return &Service{traces: traces, reads: make(chan struct{}, 4)}
}

type Failure struct {
	TraceID string `json:"trace_id"`
	Reason  string `json:"reason"`
}

type Metadata struct {
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	CandidateLimit  int       `json:"candidate_limit"`
	CandidateTraces int       `json:"candidate_traces"`
	ReadTraces      int       `json:"read_traces"`
	Partial         bool      `json:"partial"`
	Reasons         []string  `json:"reasons"`
	Failures        []Failure `json:"failures"`
	Sampling        string    `json:"sampling"`
}

type Session struct {
	SessionID     string    `json:"session_id"`
	Identity      Identity  `json:"identity"`
	RunCount      int       `json:"run_count"`
	ErrorCount    int       `json:"error_count"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	InputPreview  string    `json:"input_preview"`
	OutputPreview string    `json:"output_preview"`
}

type Check struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type Facets struct {
	Applications []Identity `json:"applications"`
	SDKs         []string   `json:"sdks"`
	Models       []string   `json:"models"`
}

type ListResult struct {
	Items    []Run     `json:"items"`
	Sessions []Session `json:"sessions"`
	Facets   Facets    `json:"facets"`
	Checks   []Check   `json:"checks"`
	Metadata Metadata  `json:"metadata"`
}

func (s *Service) List(ctx context.Context, q Query) (*ListResult, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	if s.traces == nil {
		return nil, errs.ErrNotWiredYet
	}
	out := &ListResult{Items: []Run{}, Metadata: Metadata{Start: q.Start, End: q.End, CandidateLimit: candidateLimit, Reasons: []string{}, Failures: []Failure{}, Sampling: "unknown"}}
	result, err := s.traces.SearchTraces(ctx, tracequery.SearchOptions{Query: TraceQL(q), Start: q.Start, End: q.End, Limit: candidateLimit})
	if err != nil {
		return nil, fmt.Errorf("agenttrace: search: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("agenttrace: empty search response")
	}
	var candidates []struct {
		TraceID string `json:"traceID"`
	}
	if len(result.Traces) != 0 {
		if err := json.Unmarshal(result.Traces, &candidates); err != nil {
			return nil, fmt.Errorf("agenttrace: decode search: %w", err)
		}
	}
	if result.Partial {
		out.Metadata.Reasons = append(out.Metadata.Reasons, "upstream_partial")
	}
	if len(candidates) >= candidateLimit {
		out.Metadata.Reasons = append(out.Metadata.Reasons, "candidate_limit")
	}
	if len(candidates) > candidateLimit {
		candidates = candidates[:candidateLimit]
	}
	for i := range candidates {
		// Tempo search can omit leading zeroes from a trace ID.
		id := candidates[i].TraceID
		if len(id) > 0 && len(id) <= 32 {
			candidates[i].TraceID = canonicalID(strings.Repeat("0", 32-len(id))+id, 16)
		}
	}
	out.Metadata.CandidateTraces = len(candidates)
	var metrics struct {
		CompletedJobs int `json:"completedJobs"`
		TotalJobs     int `json:"totalJobs"`
	}
	if len(result.Metrics) != 0 && json.Unmarshal(result.Metrics, &metrics) == nil && metrics.CompletedJobs < metrics.TotalJobs {
		out.Metadata.Reasons = appendUnique(out.Metadata.Reasons, "upstream_partial")
	}
	type readResult struct {
		id   string
		runs []Run
		err  error
	}
	results := make(chan readResult)
	var workers sync.WaitGroup
	jobs := make(chan int, len(candidates))
	for i := range candidates {
		jobs <- i
	}
	close(jobs)
	for range min(4, len(candidates)) {
		workers.Go(func() {
			for i := range jobs {
				trace, err := s.Trace(ctx, candidates[i].TraceID)
				result := readResult{id: candidates[i].TraceID, err: err}
				if err == nil {
					result.runs = trace.Runs
				}
				select {
				case results <- result:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	go func() { workers.Wait(); close(results) }()
	seen := map[string]bool{}
	bytes := 0
	for result := range results {
		if result.err != nil {
			if errors.Is(result.err, errs.ErrNotFound) {
				continue // Ownership may change between search and detail reads.
			}
			reason := "trace_read_failed"
			if errors.Is(result.err, tracequery.ErrResponseTooLarge) {
				reason = "trace_too_large"
			}
			out.Metadata.Failures = append(out.Metadata.Failures, Failure{result.id, reason})
			out.Metadata.Reasons = appendUnique(out.Metadata.Reasons, reason)
			continue
		}
		out.Metadata.ReadTraces++
		for _, run := range result.runs {
			key := run.TraceID + run.EntrySpanID
			if seen[key] || !q.matches(run) {
				continue
			}
			seen[key] = true
			if !summaryFits(run) {
				out.Metadata.Reasons = appendUnique(out.Metadata.Reasons, "result_limit")
				continue
			}
			body, err := json.Marshal(run)
			if err != nil {
				return nil, fmt.Errorf("agenttrace: encode summary: %w", err)
			}
			if len(out.Items) >= runLimit || bytes+len(body) > 2*1024*1024 {
				out.Metadata.Reasons = appendUnique(out.Metadata.Reasons, "result_limit")
				continue
			}
			bytes += len(body)
			out.Items = append(out.Items, run)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out.Metadata.Partial = len(out.Metadata.Reasons) > 0
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].Start.Equal(out.Items[j].Start) {
			return out.Items[i].TraceID+out.Items[i].EntrySpanID < out.Items[j].TraceID+out.Items[j].EntrySpanID
		}
		return out.Items[i].Start.After(out.Items[j].Start)
	})
	out.Sessions = sessions(out.Items)
	out.Facets = facets(out.Items)
	out.Checks = checks(out.Items, out.Metadata)
	return out, nil
}

func summaryFits(run Run) bool {
	size := len(run.Name) + len(run.SessionID) + len(run.InputPreview) + len(run.OutputPreview) + len(run.Identity.ServiceName) + len(run.Identity.ServiceNamespace) + len(run.Identity.Environment)
	for _, model := range run.Models {
		size += len(model)
	}
	return size <= 16*1024
}

func (s *Service) Trace(ctx context.Context, id string) (*Trace, error) {
	if canonicalID(id, 16) != id {
		return nil, fmt.Errorf("%w: trace ID must be 32 lowercase hex characters", errs.ErrInvalid)
	}
	if s.traces == nil {
		return nil, errs.ErrNotWiredYet
	}
	select {
	case s.reads <- struct{}{}:
		defer func() { <-s.reads }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	result, err := s.traces.GetTrace(ctx, id)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("agenttrace: empty trace response")
	}
	out, err := Normalize(id, result.Body)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func sessions(runs []Run) []Session {
	type key struct {
		Identity Identity
		ID       string
	}
	groups := map[key]*Session{}
	for _, run := range runs {
		if run.SessionID == "" {
			continue
		}
		k := key{run.Identity, run.SessionID}
		session := groups[k]
		if session == nil {
			session = &Session{SessionID: run.SessionID, Identity: run.Identity, FirstSeen: run.Start, LastSeen: run.Start, InputPreview: run.InputPreview, OutputPreview: run.OutputPreview}
			groups[k] = session
		}
		session.RunCount++
		if run.Status == "error" {
			session.ErrorCount++
		}
		if run.Start.Before(session.FirstSeen) {
			session.FirstSeen = run.Start
			session.InputPreview = run.InputPreview
		}
		if run.Start.After(session.LastSeen) {
			session.LastSeen = run.Start
			session.OutputPreview = run.OutputPreview
		}
	}
	out := make([]Session, 0, len(groups))
	for _, session := range groups {
		out = append(out, *session)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeen.Equal(out[j].LastSeen) {
			return fmt.Sprint(out[i].Identity, out[i].SessionID) < fmt.Sprint(out[j].Identity, out[j].SessionID)
		}
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out
}

func facets(runs []Run) Facets {
	out := Facets{Applications: []Identity{}, SDKs: []string{}, Models: []string{}}
	apps := map[Identity]bool{}
	for _, run := range runs {
		if !apps[run.Identity] {
			out.Applications = append(out.Applications, run.Identity)
			apps[run.Identity] = true
		}
		for _, sdk := range run.SDKs {
			out.SDKs = appendUnique(out.SDKs, sdk)
		}
		for _, model := range run.Models {
			out.Models = appendUnique(out.Models, model)
		}
	}
	sort.Slice(out.Applications, func(i, j int) bool { return fmt.Sprint(out.Applications[i]) < fmt.Sprint(out.Applications[j]) })
	sort.Strings(out.SDKs)
	sort.Strings(out.Models)
	return out
}

func checks(runs []Run, meta Metadata) []Check {
	counts := map[string]int{}
	for _, run := range runs {
		for _, issue := range run.Issues {
			counts[issue]++
		}
		if !run.ContentAvailable {
			counts["content_not_recorded"]++
		}
		if !run.Usage.Complete {
			counts["usage_incomplete"]++
		}
		if run.SessionID == "" {
			counts["session_not_recorded"]++
		}
	}
	out := []Check{{"traces", "observed", meta.ReadTraces}, {"sampling", "unknown", 0}}
	if meta.ReadTraces == 0 {
		out[0].Status = "not_observed"
	}
	if len(meta.Failures) > 0 {
		out = append(out, Check{"trace_read_failed", "incomplete", len(meta.Failures)})
	}
	keys := []string{"incomplete_identity", "missing_parent", "parent_cycle", "dropped_telemetry", "duplicate_span_id", "invalid_span_identity", "content_not_recorded", "usage_incomplete", "usage_conflict", "usage_overlap", "session_not_recorded", "session_conflict"}
	for _, key := range keys {
		status := "observed"
		if len(runs) == 0 {
			status = "not_observed"
		} else if counts[key] > 0 {
			status = "incomplete"
		}
		out = append(out, Check{key, status, counts[key]})
	}
	return out
}
