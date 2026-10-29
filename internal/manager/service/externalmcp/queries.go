package externalmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// QueryTools accepts only statically built BaseTools. It never exports a final
// chat ToolBag, dynamic MCP tool, Skill bridge, Shell or Agent control tool.
func QueryTools(base []basetool.BaseTool) ([]biz.Tool, error) {
	allow := map[string][]string{"query_devices": {"devices:read"}, "get_host_summary": {"devices:read", "alerts:read"}, "query_incidents": {"alerts:read"}, "list_metric_catalog": {"metrics:read"}, "query_promql": {"metrics:read"}, "query_k8s_snapshot": {"k8s:read"}, "query_logql": {"logs:read"}, "query_traceql": {"traces:read"}, "describe_k8s_resource": {"k8s:read"}}
	allow["query_knowledge"] = []string{"knowledge:read"}
	allow["list_repo_sources"] = []string{"knowledge:read"}
	allow["read_source"] = []string{"knowledge:read"}
	allow["grep_source"] = []string{"knowledge:read"}
	allow["query_k8s_logs"] = []string{"k8s:read", "logs:read"}
	allow["get_host_processes"] = []string{"devices:read"}
	allow["get_topology"] = []string{"topology:read", "devices:read", "alerts:read"}
	allow["expand_topology"] = []string{"topology:read"}
	allow["find_topology_node"] = []string{"topology:read"}
	allow["query_network_devices"] = []string{"network:read"}
	allow["query_network_interfaces"] = []string{"network:read"}
	allow["get_network_neighbors"] = []string{"network:read", "topology:read"}
	allow["query_change_events"] = []string{"audit:read"}
	allow["list_database_sources"] = []string{"databases:read"}
	allow["analyze_database_status"] = []string{"databases:read", "metrics:read"}
	allow["host_find_large_files"] = []string{"host-files:read"}
	allow["host_du_summary"] = []string{"host-files:read"}
	allow["host_stat_file"] = []string{"host-files:read"}
	allow["rank_hosts"] = []string{"devices:read", "metrics:read"}
	allow["find_outlier_hosts"] = []string{"devices:read", "metrics:read"}
	out := []biz.Tool{}
	for _, baseTool := range base {
		info, err := baseTool.Info(context.Background())
		if err != nil {
			return nil, err
		}
		scopes, ok := allow[info.Name]
		if !ok {
			continue
		}
		if info.Class != "read" || info.Origin != "" {
			return nil, fmt.Errorf("external query %s is not a built-in read", info.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(info.Parameters, &schema); err != nil {
			return nil, err
		}
		schema["additionalProperties"] = false
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("missing properties: %s", info.Name)
		}
		for name, raw := range properties {
			p, _ := raw.(map[string]any)
			switch p["type"] {
			case "string":
				p["maxLength"] = 8192
			case "array":
				p["maxItems"] = 10
			case "integer":
				if strings.HasSuffix(name, "_id") {
					p["minimum"] = 1
					p["maximum"] = 9007199254740991
				}
			}
		}
		setRange := func(name string, max int) {
			if p, ok := properties[name].(map[string]any); ok {
				p["minimum"] = 1
				p["maximum"] = max
			}
		}
		setRange("limit", 100)
		setRange("max_metrics", 100)
		setRange("lookback_seconds", 86400)
		setRange("since_minutes", 10080)
		setRange("last_seen_within_minutes", 10080)
		if p, ok := properties["offset"].(map[string]any); ok {
			p["maximum"] = 100000
		}
		if info.Name == "get_host_summary" {
			schema["required"] = []string{"device_ids"}
			p := properties["device_ids"].(map[string]any)
			p["minItems"] = 1
			p["maxItems"] = 10
			p["uniqueItems"] = true
			p["items"] = idSchema()
			p["description"] = "Explicit DeviceIDs, 1 to 10. Empty or omitted lists are rejected."
		}
		if info.Name == "query_logql" {
			setRange("limit", 200)
		}
		if info.Name == "list_metric_catalog" {
			properties["labels"] = map[string]any{"type": "object", "maxProperties": 10, "additionalProperties": map[string]any{"type": "string", "maxLength": 256}}
		}
		if info.Name == "describe_k8s_resource" {
			properties["kind"] = enumSchema("Deployment", "StatefulSet", "DaemonSet", "Pod", "Node")
			setRange("events_limit", 50)
		}
		encoded, err := json.Marshal(schema)
		if err != nil {
			return nil, err
		}
		name := info.Name
		out = append(out, biz.Tool{Name: name, Description: info.Description + " External access is deployment-wide; candidate lists and snapshots do not prove full coverage.", Scopes: scopes, Schema: encoded, Validate: func(a biz.Arguments) error { return prepareQuery(name, a) }, Run: func(ctx context.Context, a biz.Arguments) (any, error) {
			at := time.Now().UTC()
			ctx = basetool.WithObservationTime(ctx, at)
			args, err := json.Marshal(a)
			if err != nil {
				return nil, err
			}
			raw, err := baseTool.InvokableRun(ctx, string(args))
			if err != nil {
				return nil, err
			}
			var data any
			if len(raw) > 8*1024*1024 {
				return nil, biz.ErrResultTooLarge
			}
			if err := json.Unmarshal([]byte(raw), &data); err != nil {
				return nil, err
			}
			data = projectQuery(name, data)
			if name == "query_promql" {
				return map[string]any{"result": data, "time_range": map[string]time.Time{"start": at.Add(-time.Duration(biz.Number(a, "lookback_seconds")) * time.Second), "end": at}}, nil
			}
			if name == "query_logql" || (name == "query_traceql" && biz.String(a, "trace_id") == "") {
				return map[string]any{"result": data, "time_range": map[string]any{"start": a["start"], "end": a["end"]}}, nil
			}
			return data, nil
		}})
	}
	return out, nil
}

func prepareQuery(name string, a biz.Arguments) error {
	if _, ok := a["limit"]; !ok {
		switch name {
		case "query_devices", "query_incidents", "query_logql", "query_traceql", "query_k8s_snapshot":
			a["limit"] = float64(20)
		}
	}
	switch name {
	case "query_incidents":
		if _, ok := a["since_minutes"]; !ok {
			a["since_minutes"] = float64(1440)
		}
	case "query_promql":
		if _, ok := a["lookback_seconds"]; !ok {
			a["lookback_seconds"] = float64(300)
		}
	case "list_metric_catalog":
		if _, ok := a["include_label_samples"]; !ok {
			a["include_label_samples"] = false
		}
		if _, ok := a["max_metrics"]; !ok {
			a["max_metrics"] = float64(100)
		}
		prefixes, _ := a["prefixes"].([]any)
		labels, _ := a["labels"].(map[string]any)
		if len(prefixes) == 0 && len(labels) == 0 && strings.TrimSpace(biz.String(a, "selector")) == "" && strings.TrimSpace(biz.String(a, "metric_regex")) == "" {
			return errs.ErrInvalid
		}
	case "query_logql", "query_traceql":
		if name == "query_traceql" && biz.String(a, "trace_id") != "" {
			return nil
		}
		end := time.Now().UTC()
		start := end.Add(-15 * time.Minute)
		if s := biz.String(a, "end"); s != "" {
			v, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return errs.ErrInvalid
			}
			end = v
			start = end.Add(-15 * time.Minute)
		}
		if s := biz.String(a, "start"); s != "" {
			v, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return errs.ErrInvalid
			}
			start = v
		}
		if !end.After(start) || end.Sub(start) > 24*time.Hour {
			return errs.ErrInvalid
		}
		a["start"] = start.Format(time.RFC3339Nano)
		a["end"] = end.Format(time.RFC3339Nano)
	}
	return nil
}

func pick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, key := range keys {
		if v, ok := m[key]; ok {
			out[key] = v
		}
	}
	return out
}
func projectQuery(name string, value any) any {
	m, ok := value.(map[string]any)
	if !ok {
		return sanitize(value)
	}
	switch name {
	case "query_devices":
		m = pick(m, "devices", "count")
	case "query_incidents":
		m = pick(m, "incidents", "count", "since_minutes")
	case "get_host_summary":
		m = pick(m, "success_count", "error_count", "results")
		if rows, ok := m["results"].([]any); ok {
			for _, raw := range rows {
				row, _ := raw.(map[string]any)
				if summary, ok := row["summary"].(map[string]any); ok {
					row["summary"] = pick(summary, "host", "current_edge", "execution_available", "host_load", "recent_incidents", "runtime_error", "host_load_error", "recent_incidents_error", "plugin_status", "plugin_status_reason")
				}
			}
		}
	case "query_k8s_snapshot":
		m = pick(m, "resource", "source", "filters", "total", "limit", "offset", "clusters", "nodes", "workloads", "pods", "events", "totals", "per_cluster", "truncated")
	case "list_metric_catalog":
		m = pick(m, "status", "generated_at", "query", "selector", "promql", "metric_count", "returned", "truncated", "metrics")
	case "describe_k8s_resource":
		result, _ := m["result"].(map[string]any)
		m = pick(result, "cluster_id", "kind", "namespace", "name", "uid", "resource_version", "fetched_at", "events")
		if object, ok := result["object"].(map[string]any); ok {
			if status, ok := object["status"].(map[string]any); ok {
				m["status"] = pick(status, "phase", "replicas", "readyReplicas", "availableReplicas", "updatedReplicas", "observedGeneration", "conditions")
			}
			if spec, ok := object["spec"].(map[string]any); ok {
				m["spec"] = pick(spec, "replicas", "nodeName", "unschedulable")
			}
		}
	case "query_traceql":
		if rows, ok := m["spans"].([]any); ok {
			for i, raw := range rows {
				row, _ := raw.(map[string]any)
				span, _ := row["span"].(map[string]any)
				rows[i] = map[string]any{"span": pick(span, "traceId", "spanId", "parentSpanId", "name", "kind", "startTimeUnixNano", "endTimeUnixNano", "status")}
			}
		}
	}
	return sanitize(m)
}

var bearerText = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`)
var secretText = regexp.MustCompile(`(?i)ogmcp_[a-f0-9]{32}\.[a-f0-9]{64}|["']?(?:password|passwd|api[_-]?key|authorization|cookie|secret|access[_-]?token)["']?\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
var credentialURL = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/@]+:[^\s/@]+@`)
var privateKeyText = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)

// sanitize is the second layer after each tool's field projection. Free text
// still needs operator consent; patterns cannot recognize every business secret.
func sanitize(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range v {
			lower := strings.ToLower(key)
			compact := strings.NewReplacer("_", "", "-", "", " ", "").Replace(lower)
			if strings.Contains(compact, "apikey") || strings.Contains(compact, "accesskey") || strings.Contains(compact, "passwd") || strings.Contains(compact, "headertemplate") || strings.Contains(compact, "encryptedrequest") {
				continue
			}
			if lower == "annotations" || lower == "env_from" || (strings.Contains(lower, "token") && lower != "preflight_token") {
				continue
			}
			if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") || strings.Contains(lower, "api_key") || strings.Contains(lower, "access_key") || strings.Contains(lower, "credential") || strings.Contains(lower, "private_key") || strings.Contains(lower, "privatekey") || lower == "dsn" || lower == "payload_json" || lower == "input_json" || lower == "headers" || lower == "env" || lower == "attributes" || lower == "resource_attributes" {
				continue
			}
			if lower == "error" || strings.HasSuffix(lower, "_error") {
				if item != "" && item != nil {
					out[key] = "BACKEND_UNAVAILABLE"
				}
				continue
			}
			out[key] = sanitize(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = sanitize(item)
		}
		return out
	case string:
		v = privateKeyText.ReplaceAllString(v, "[REDACTED]")
		v = credentialURL.ReplaceAllString(v, "${1}[REDACTED]@")
		// Remove the entire Bearer value before matching an Authorization key.
		v = bearerText.ReplaceAllString(v, "[REDACTED]")
		v = secretText.ReplaceAllString(v, "[REDACTED]")
		if len(v) > 8192 {
			v = v[:8192]
			for !utf8.ValidString(v) {
				v = v[:len(v)-1]
			}
			v += "… [TRUNCATED]"
		}
		return v
	default:
		return value
	}
}
