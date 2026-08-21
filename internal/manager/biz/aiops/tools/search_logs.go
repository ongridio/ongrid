package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

const ToolNameSearchLogs = "search_logs"

const SearchLogsDescription = "Search log content through Ongrid's backend-neutral log API. " +
	"Use it for keyword, exact-phrase, level, device, cluster, Kubernetes, service, or source-scoped investigations. " +
	"It searches Loki history and Elasticsearch data without requiring LogQL or Elasticsearch DSL and returns normalized records."

const searchLogsWhenToUse = "Use for production log content, errors, panic/fatal lines, service failures, or correlated trace IDs. " +
	"Prefer this over query_logql because it keeps working when Elasticsearch is active. " +
	"NOT for CPU or memory trends (query_promql), filesystem metadata, or trace timelines."

var SearchLogsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "keywords": {"type": "array", "items": {"type": "string"}, "maxItems": 20, "description": "Terms or phrases to include."},
    "exclude_keywords": {"type": "array", "items": {"type": "string"}, "maxItems": 20, "description": "Terms to exclude."},
    "match_mode": {"type": "string", "enum": ["any", "all", "phrase"], "description": "How include keywords are matched; default any. Phrase requires exactly one keywords item."},
    "device_ids": {"type": "array", "items": {"type": "integer", "minimum": 1}, "maxItems": 20},
    "cluster_ids": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
    "namespaces": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
    "workloads": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
    "pods": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
    "containers": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
    "nodes": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
    "service_names": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
    "source_ids": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
    "levels": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
    "files": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
    "units": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
    "start": {"type": "string", "description": "RFC3339 or relative time such as now-1h; defaults to now-1h."},
    "end": {"type": "string", "description": "RFC3339 or now; defaults to now."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 500, "description": "Maximum normalized records; default 200."},
    "direction": {"type": "string", "enum": ["backward", "forward"], "description": "Newest-first or oldest-first; default backward."}
  },
  "additionalProperties": false
}`)

type SearchLogsArgs struct {
	Keywords        []string `json:"keywords,omitempty"`
	ExcludeKeywords []string `json:"exclude_keywords,omitempty"`
	MatchMode       string   `json:"match_mode,omitempty"`
	DeviceIDs       []uint64 `json:"device_ids,omitempty"`
	ClusterIDs      []string `json:"cluster_ids,omitempty"`
	Namespaces      []string `json:"namespaces,omitempty"`
	Workloads       []string `json:"workloads,omitempty"`
	Pods            []string `json:"pods,omitempty"`
	Containers      []string `json:"containers,omitempty"`
	Nodes           []string `json:"nodes,omitempty"`
	ServiceNames    []string `json:"service_names,omitempty"`
	SourceIDs       []string `json:"source_ids,omitempty"`
	Levels          []string `json:"levels,omitempty"`
	Files           []string `json:"files,omitempty"`
	Units           []string `json:"units,omitempty"`
	Start           string   `json:"start,omitempty"`
	End             string   `json:"end,omitempty"`
	Limit           int      `json:"limit,omitempty"`
	Direction       string   `json:"direction,omitempty"`
}

type SearchLogsTool struct {
	search logquery.Searcher
	log    *slog.Logger
}

func NewSearchLogsTool(search logquery.Searcher, log *slog.Logger) *SearchLogsTool {
	if log == nil {
		log = slog.Default()
	}
	return &SearchLogsTool{search: search, log: log}
}

func (t *SearchLogsTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name: ToolNameSearchLogs, Description: SearchLogsDescription, WhenToUse: searchLogsWhenToUse,
		Parameters: SearchLogsSchema, Class: "read",
	}, nil
}

func (t *SearchLogsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.search == nil {
		return "", fmt.Errorf("search_logs: log search backend not configured")
	}
	out, err := executeStructuredLogSearch(ctx, t.search, []byte(argsJSON))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (r *Registry) executeSearchLogs(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.logSearch == nil {
		return ExecuteResult{}, fmt.Errorf("search_logs: log search backend not configured")
	}
	out, err := executeStructuredLogSearch(ctx, r.logSearch, args)
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: out}, nil
}

func executeStructuredLogSearch(ctx context.Context, search logquery.Searcher, raw []byte) (json.RawMessage, error) {
	var in SearchLogsArgs
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return nil, fmt.Errorf("search_logs: bad args: %w", err)
	}
	end := time.Now().UTC()
	start := end.Add(-time.Hour)
	var err error
	if in.End != "" {
		end, err = parseLogQLTime(in.End, end)
		if err != nil {
			return nil, fmt.Errorf("search_logs: parse end: %w", err)
		}
	}
	if in.Start != "" {
		start, err = parseLogQLTime(in.Start, start)
		if err != nil {
			return nil, fmt.Errorf("search_logs: parse start: %w", err)
		}
	} else if in.End != "" {
		start = end.Add(-time.Hour)
	}
	mode := logquery.MatchMode(in.MatchMode)
	if mode == "" {
		mode = logquery.MatchAny
	}
	if mode == logquery.MatchPhrase && len(in.Keywords) != 1 {
		return nil, fmt.Errorf("search_logs: phrase mode requires exactly one keyword phrase")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 200
	}
	direction := logquery.SortDirection(in.Direction)
	if direction == "" {
		direction = logquery.SortBackward
	}
	req := logquery.SearchRequest{
		Start: start.UTC(), End: end.UTC(), Limit: limit, Direction: direction,
		Keywords: logquery.Keywords{Include: in.Keywords, Exclude: in.ExcludeKeywords, Mode: mode},
		Scope: logquery.Scope{
			DeviceIDs: in.DeviceIDs, ClusterIDs: in.ClusterIDs, Namespaces: in.Namespaces,
			Workloads: in.Workloads, Pods: in.Pods, Containers: in.Containers, Nodes: in.Nodes,
			ServiceNames: in.ServiceNames, SourceIDs: in.SourceIDs, Levels: in.Levels,
			Files: in.Files, Units: in.Units,
		},
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := search.Search(callCtx, req)
	if err != nil {
		return nil, fmt.Errorf("search_logs: dispatch: %w", err)
	}
	out, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("search_logs: marshal response: %w", err)
	}
	return out, nil
}
