package apm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

type errorAttribute struct {
	Key   string `json:"key"`
	Value struct {
		String string          `json:"stringValue"`
		Int    json.RawMessage `json:"intValue"`
	} `json:"value"`
}

func errorAttributes(attrs []errorAttribute) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		out[attr.Key] = attr.Value.String
		if len(attr.Value.Int) > 0 {
			out[attr.Key] = strings.Trim(string(attr.Value.Int), `"`)
		}
	}
	return out
}

type ErrorGroup struct {
	Fingerprint            string   `json:"fingerprint"`
	Operation              string   `json:"operation"`
	ErrorType              string   `json:"error_type"`
	StatusCode             string   `json:"status_code"`
	StackTrace             string   `json:"stack_trace"`
	Count                  int      `json:"count"`
	FirstSeen              float64  `json:"first_seen"`
	LastSeen               float64  `json:"last_seen"`
	Versions               []string `json:"versions"`
	Instances              []string `json:"instances"`
	TraceID                string   `json:"trace_id"`
	SpanID                 string   `json:"span_id"`
	RepresentativeVersion  string   `json:"representative_version"`
	RepresentativeInstance string   `json:"representative_instance"`
}

type ErrorGroups struct {
	Items         []ErrorGroup `json:"items"`
	Total         int          `json:"total"`
	Page          int          `json:"page"`
	PageSize      int          `json:"page_size"`
	SampledTraces int          `json:"sampled_traces"`
	FailedTraces  int          `json:"failed_traces"`
	Truncated     bool         `json:"truncated"`
	Metadata      Metadata     `json:"metadata"`
	SnapshotID    string       `json:"snapshot_id,omitempty"`
}

// ErrorGroups counts matching spans, not root-trace summaries or all failed
// requests. Reading details is bounded; no per-group query or persistent index.
func (s *Service) ErrorGroups(ctx context.Context, q Query) (*ErrorGroups, error) {
	if err := s.validateQuery(ctx, &q, true); err != nil {
		return nil, err
	}
	if q.SnapshotID != "" {
		return s.errorSnapshots.page(ctx, q)
	}
	if s.traces == nil {
		return nil, errs.ErrNotWiredYet
	}
	const traceLimit = 50
	query := strings.TrimSuffix(TraceQL(q), " }") + " && status = error } with (most_recent=true)"
	search, err := s.traces.SearchTraces(ctx, tracequery.SearchOptions{Query: query, Start: q.Start, End: q.End, Limit: traceLimit})
	if err != nil {
		return nil, err
	}
	if search == nil {
		return nil, fmt.Errorf("apm: missing error trace search response")
	}
	var summaries []struct {
		TraceID string `json:"traceID"`
	}
	if err := json.Unmarshal(search.Traces, &summaries); err != nil {
		return nil, fmt.Errorf("apm: decode error search: %w", err)
	}
	out := &ErrorGroups{Items: []ErrorGroup{}, Page: q.Page, PageSize: q.PageSize, Truncated: len(summaries) >= traceLimit, Metadata: metadata(q)}
	out.Metadata.MetricSource, out.Metadata.Sampling = "tempo_error_spans", "sampled"
	ids := []string{}
	for _, summary := range summaries[:min(len(summaries), traceLimit)] {
		id := canonicalTraceID(summary.TraceID)
		if id == "" {
			out.FailedTraces++
			continue
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	// Each worker owns one slot; merge after Wait to avoid shared map writes.
	results := make([][]ErrorGroup, len(ids))
	failures := make([]bool, len(ids))
	workers, workerCtx := errgroup.WithContext(ctx)
	workers.SetLimit(8)
	for i, id := range ids {
		workers.Go(func() (err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = fmt.Errorf("apm: error trace worker panicked: %v", recovered)
				}
			}()
			if err := workerCtx.Err(); err != nil {
				return err
			}
			select {
			case s.errorReads <- struct{}{}:
				defer func() { <-s.errorReads }()
			case <-workerCtx.Done():
				return workerCtx.Err()
			}
			trace, err := s.traces.GetTrace(workerCtx, id)
			if err != nil || trace == nil || len(trace.Body) > 2<<20 {
				failures[i] = true
				return nil
			}
			resources, err := decodeTrace(trace.Body)
			if err != nil {
				failures[i] = true
				return nil
			}
			results[i], failures[i] = errorSamples(q, id, resources)
			return nil
		})
	}
	if err := workers.Wait(); err != nil {
		return nil, err
	}
	groups := map[string]*ErrorGroup{}
	for i, samples := range results {
		if failures[i] {
			out.FailedTraces++
			continue
		}
		out.SampledTraces++
		for _, sample := range samples {
			group := groups[sample.Fingerprint]
			if group == nil {
				copy := sample
				groups[sample.Fingerprint] = &copy
				continue
			}
			group.Count++
			group.FirstSeen = min(group.FirstSeen, sample.FirstSeen)
			if sample.LastSeen > group.LastSeen {
				group.LastSeen, group.TraceID, group.SpanID = sample.LastSeen, sample.TraceID, sample.SpanID
				group.RepresentativeVersion, group.RepresentativeInstance = sample.RepresentativeVersion, sample.RepresentativeInstance
			}
			group.Versions = append(group.Versions, sample.Versions...)
			group.Instances = append(group.Instances, sample.Instances...)
		}
	}
	if out.SampledTraces == 0 && out.FailedTraces > 0 {
		return nil, fmt.Errorf("apm: error trace details unavailable")
	}
	for _, group := range groups {
		sort.Strings(group.Versions)
		group.Versions = slices.Compact(group.Versions)
		sort.Strings(group.Instances)
		group.Instances = slices.Compact(group.Instances)
		out.Items = append(out.Items, *group)
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.LastSeen != b.LastSeen {
			return a.LastSeen > b.LastSeen
		}
		return a.Fingerprint < b.Fingerprint
	})
	out.Total = len(out.Items)
	s.errorSnapshots.save(ctx, q, out)
	return errorGroupsPage(out, q), nil
}

func errorGroupsPage(out *ErrorGroups, q Query) *ErrorGroups {
	out.Page, out.PageSize = q.Page, q.PageSize
	start := min((q.Page-1)*q.PageSize, out.Total)
	out.Items = out.Items[start:min(start+q.PageSize, out.Total)]
	return out
}

func errorSamples(q Query, traceID string, resources []traceResource) ([]ErrorGroup, bool) {
	out := []ErrorGroup{}
	seen := map[string]bool{}
	count := 0
	for _, resource := range resources {
		attrs := resource.attributes()
		if attrs["service.name"] != q.ServiceName || attrs["service.namespace"] != *q.ServiceNamespace || attrs["deployment.environment.name"] != *q.Environment {
			continue
		}
		if q.ServiceVersion != "" && attrs["service.version"] != q.ServiceVersion || q.InstanceID != "" && attrs["service.instance.id"] != q.InstanceID || q.DeviceID != "" && attrs["device_id"] != q.DeviceID || q.ClusterID != "" && attrs["cluster_id"] != q.ClusterID {
			continue
		}
		if scope := q.resourceScope; scope != nil {
			if scope.ClusterID != "" && attrs["cluster_id"] != scope.ClusterID || scope.ClusterID == "" && !slices.Contains(scope.DeviceIDs, attrs["device_id"]) {
				continue
			}
		}
		for _, span := range resource.spans() {
			count++
			if count > 10000 {
				return nil, true
			}
			kind := string(span.Kind)
			if q.SpanKind == "server" && kind != "2" && kind != `"SPAN_KIND_SERVER"` || q.SpanKind == "consumer" && kind != "5" && kind != `"SPAN_KIND_CONSUMER"` {
				continue
			}
			if string(span.Status.Code) != "2" && string(span.Status.Code) != `"STATUS_CODE_ERROR"` {
				continue
			}
			nanos, err := strconv.ParseInt(span.StartTime, 10, 64)
			if err != nil || nanos < q.Start.UnixNano() || nanos > q.End.UnixNano() {
				continue
			}
			if span.SpanID == "" {
				return nil, true
			}
			if seen[span.SpanID] {
				continue
			}
			seen[span.SpanID] = true
			a := errorAttributes(span.Attributes)
			first := func(keys ...string) string {
				for _, key := range keys {
					if a[key] != "" {
						return a[key]
					}
				}
				return ""
			}
			operation, status := span.Name, ""
			if q.Protocol == "rpc" {
				if first("rpc.system.name", "rpc.system") == "" {
					continue
				}
				method := first("rpc.method")
				if service := first("rpc.service"); service != "" && !strings.Contains(method, "/") {
					method = service + "/" + method
				}
				if method != "" {
					operation = method
				}
				status = first("rpc.response.status_code", "rpc.grpc.status_code")
			} else {
				method := first("http.request.method", "http.method")
				if method == "" {
					continue
				}
				if route := first("http.route"); route != "" {
					operation = method + " " + route
				}
				status = first("http.response.status_code", "http.status_code")
			}
			if q.Operation != "" {
				target := operation
				if q.MetricSource == "tempo_spanmetrics" {
					target = span.Name
				} else if q.Protocol == "http" {
					target = first("http.request.method", "http.method") + " " + first("http.route")
				}
				if target != q.Operation {
					continue
				}
			}
			errorType, stack := first("exception.type", "error.type"), first("exception.stacktrace")
			for _, event := range span.Events {
				if event.Name != "exception" {
					continue
				}
				e := errorAttributes(event.Attributes)
				if e["exception.type"] != "" {
					errorType = e["exception.type"]
				}
				if e["exception.stacktrace"] != "" {
					stack = e["exception.stacktrace"]
				}
				break
			}
			stack = strings.TrimSpace(strings.ReplaceAll(stack, "\r\n", "\n"))
			key, err := json.Marshal([]string{operation, errorType, status, errorStackKey(stack)})
			if err != nil {
				return nil, true
			}
			fingerprint := fmt.Sprintf("%x", sha256.Sum256(key))
			if len(stack) > 8192 {
				stack = string([]rune(stack)[:min(2048, len([]rune(stack)))]) + "…"
			}
			ts := float64(nanos) / 1e9
			out = append(out, ErrorGroup{Fingerprint: fingerprint, Operation: operation, ErrorType: errorType, StatusCode: status, StackTrace: stack, Count: 1, FirstSeen: ts, LastSeen: ts, Versions: []string{attrs["service.version"]}, Instances: []string{attrs["service.instance.id"]}, TraceID: traceID, SpanID: span.SpanID, RepresentativeVersion: attrs["service.version"], RepresentativeInstance: attrs["service.instance.id"]})
		}
	}
	return out, false
}

// Go runtime stacks contain goroutine IDs, argument addresses and PC offsets.
// Keep symbols and source locations for grouping, preserving the original stack
// separately. Never strip source line numbers: different call sites may differ.
func errorStackKey(stack string) string {
	if !strings.HasPrefix(stack, "goroutine ") {
		// ponytail: other stack formats match exactly; normalize a language only
		// when real samples show dynamic fields splitting equivalent frames.
		return stack
	}
	frames := []string{}
	for _, line := range strings.Split(stack, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "goroutine ") {
			continue
		}
		if strings.HasPrefix(line, "created by ") {
			line, _, _ = strings.Cut(line, " in goroutine ")
		} else if strings.Contains(line, ".go:") {
			line, _, _ = strings.Cut(line, " +0x")
		} else if i := strings.LastIndex(line, "("); i >= 0 && strings.HasSuffix(line, ")") {
			line = line[:i]
		}
		frames = append(frames, line)
	}
	return strings.Join(frames, "\n")
}
