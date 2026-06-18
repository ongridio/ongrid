package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

const ToolNameListMetricCatalog = "list_metric_catalog"

const metricCatalogMaxSampleLabels = 3

const ListMetricCatalogDescription = "List currently scraped Prometheus metric names with representative labels. " +
	"Use this before drafting metric-based alert rules so the draft can use current metric names and label keys. " +
	"sample_labels are examples for understanding available labels; do not copy sample label values into selectors unless the user explicitly requested that exact source."

const listMetricCatalogWhenToUse = "When creating a metric-based alert rule from natural language, including database, custommetrics, host, and arbitrary Prometheus metrics. " +
	"Pass the user's natural-language intent as query, optionally with prefixes, selector, or labels if known. " +
	"Use returned names and label keys as evidence for draft_config_change; call analyze_database_status afterwards only if source or capability context is still needed. " +
	"NOT for executing PromQL values or trends; use query_promql for raw metric values after the metric name is known."

var ListMetricCatalogSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Natural-language or keyword hint, e.g. 'MySQL connection usage', 'Redis memory usage', 'exporter down', 'http p95 latency', 'queue depth'. The tool uses it to rank returned metric names."
    },
    "prefixes": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Optional metric-name prefixes to restrict discovery, e.g. ['mysql_'], ['pg_'], ['redis_'], ['mongodb_'], ['http_', 'custom_', 'node_']."
    },
    "metric_regex": {
      "type": "string",
      "description": "Optional RE2 regex for __name__. It must not match the empty string. Omit to list all current metric names."
    },
    "selector": {
      "type": "string",
      "description": "Optional Prometheus label selector fragment, with or without braces, e.g. 'job=\"api\"' or '{device_id=\"5\"}'."
    },
    "labels": {
      "type": "object",
      "additionalProperties": true,
      "description": "Optional exact label matchers to combine with selector, e.g. {'device_id': 5, 'ongrid_source': 'custom:api'}."
    },
    "max_metrics": {
      "type": "integer",
      "minimum": 1,
      "maximum": 200,
      "description": "Maximum metric names to return. Default 80, cap 200."
    },
    "include_label_samples": {
      "type": "boolean",
      "description": "Include up to three representative label sets per metric. Default true."
    }
  }
}`)

type ListMetricCatalogArgs struct {
	Query               string                 `json:"query,omitempty"`
	Prefixes            []string               `json:"prefixes,omitempty"`
	MetricRegex         string                 `json:"metric_regex,omitempty"`
	Selector            string                 `json:"selector,omitempty"`
	Labels              map[string]interface{} `json:"labels,omitempty"`
	MaxMetrics          int                    `json:"max_metrics,omitempty"`
	IncludeLabelSamples *bool                  `json:"include_label_samples,omitempty"`
}

type MetricCatalogResponse struct {
	Status      string              `json:"status"`
	Instruction string              `json:"instruction,omitempty"`
	GeneratedAt time.Time           `json:"generated_at"`
	Query       string              `json:"query,omitempty"`
	Selector    string              `json:"selector,omitempty"`
	PromQL      string              `json:"promql"`
	MetricCount int                 `json:"metric_count"`
	Returned    int                 `json:"returned"`
	Truncated   bool                `json:"truncated,omitempty"`
	Metrics     []MetricCatalogItem `json:"metrics"`
}

type MetricCatalogItem struct {
	Name         string              `json:"name"`
	SeriesCount  int                 `json:"series_count,omitempty"`
	SampleLabels []map[string]string `json:"sample_labels,omitempty"`
}

type ListMetricCatalogTool struct {
	promQuery PromQuerier
	log       *slog.Logger
}

func NewListMetricCatalogTool(p PromQuerier, log *slog.Logger) *ListMetricCatalogTool {
	if log == nil {
		log = slog.Default()
	}
	return &ListMetricCatalogTool{promQuery: p, log: log}
}

func (t *ListMetricCatalogTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameListMetricCatalog,
		Description: ListMetricCatalogDescription,
		WhenToUse:   listMetricCatalogWhenToUse,
		Parameters:  ListMetricCatalogSchema,
		Class:       "read",
	}, nil
}

func (t *ListMetricCatalogTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	runner := metricCatalogRunner{promQuery: t.promQuery, log: t.log}
	out, err := runner.run(ctx, []byte(argsJSON))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (r *Registry) executeListMetricCatalog(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	runner := metricCatalogRunner{promQuery: r.promQuery, log: r.log}
	out, err := runner.run(ctx, args)
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: out}, nil
}

type metricCatalogRunner struct {
	promQuery PromQuerier
	log       *slog.Logger
}

type scoredMetricCatalogItem struct {
	item  MetricCatalogItem
	score int
}

func (r metricCatalogRunner) run(ctx context.Context, args []byte) ([]byte, error) {
	if r.promQuery == nil {
		return nil, fmt.Errorf("%s: prom query client not configured", ToolNameListMetricCatalog)
	}
	var in ListMetricCatalogArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("%s: bad args: %w", ToolNameListMetricCatalog, err)
		}
	}
	maxMetrics := in.MaxMetrics
	if maxMetrics <= 0 {
		maxMetrics = 80
	}
	if maxMetrics > 200 {
		maxMetrics = 200
	}
	includeSamples := true
	if in.IncludeLabelSamples != nil {
		includeSamples = *in.IncludeLabelSamples
	}

	nameRE, nameREString, err := metricCatalogNameRegex(in.MetricRegex, in.Prefixes)
	if err != nil {
		return nil, err
	}
	exactLabels := normalizeMetricCatalogLabels(in.Labels)
	selector := metricCatalogSelector(nameREString, in.Selector, exactLabels)
	expr := fmt.Sprintf("count by (%s) ({%s})", strings.Join(metricCatalogGroupLabels(), ", "), selector)

	callCtx, cancel := context.WithTimeout(ctx, queryPromqlCallTimeout)
	defer cancel()
	res, err := r.promQuery.Query(callCtx, expr, time.Now())
	if err != nil {
		return nil, fmt.Errorf("%s: dispatch: %w", ToolNameListMetricCatalog, err)
	}

	items := aggregateMetricCatalog(instantValues(res), nameRE, exactLabels, includeSamples)
	tokens := metricCatalogQueryTokens(in.Query)
	aliases := metricCatalogQueryAliases(in.Query)
	scored := make([]scoredMetricCatalogItem, 0, len(items))
	for _, item := range items {
		scored = append(scored, scoredMetricCatalogItem{
			item:  item,
			score: metricCatalogScore(item.Name, tokens, aliases),
		})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].item.Name < scored[j].item.Name
	})

	outItems := make([]MetricCatalogItem, 0, min(maxMetrics, len(scored)))
	for i, item := range scored {
		if i >= maxMetrics {
			break
		}
		outItems = append(outItems, item.item)
	}
	status := "ok"
	instruction := ""
	if len(items) == 0 {
		status = "empty"
		instruction = "No currently scraped metric matched this query/selector. Do not invent metric names. If the user supplied an exact PromQL or exact metric name, you may call draft_config_change and let draft validation verify it; otherwise ask the user to configure collection or provide an exact metric/PromQL."
	} else if metricCatalogNeedsHTTPStatusLabel(in.Query) && !metricCatalogItemsHaveAnySampleLabel(outItems, metricCatalogHTTPStatusLabels()...) {
		instruction = "The returned metrics do not expose any HTTP status/code label in sample_labels, so they may not be enough to build a 5xx/error-rate/burn-rate SLI. Do not invent missing labels; if the user supplied an exact PromQL, you may call draft_config_change and let draft validation verify it, otherwise ask for a metric with status/code labels or exact PromQL."
	}
	resp := MetricCatalogResponse{
		Status:      status,
		Instruction: instruction,
		GeneratedAt: time.Now(),
		Query:       strings.TrimSpace(in.Query),
		Selector:    selector,
		PromQL:      expr,
		MetricCount: len(items),
		Returned:    len(outItems),
		Truncated:   len(items) > len(outItems),
		Metrics:     outItems,
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal response: %w", ToolNameListMetricCatalog, err)
	}
	return out, nil
}

func metricCatalogNameRegex(metricRegex string, prefixes []string) (*regexp.Regexp, string, error) {
	if strings.TrimSpace(metricRegex) != "" {
		metricRegex = strings.TrimSpace(metricRegex)
		re, err := regexp.Compile(metricRegex)
		if err != nil {
			return nil, "", fmt.Errorf("%s: invalid metric_regex: %w", ToolNameListMetricCatalog, err)
		}
		if re.MatchString("") {
			return nil, "", fmt.Errorf("%s: metric_regex must not match the empty string", ToolNameListMetricCatalog)
		}
		promRegex := metricCatalogPrometheusNameRegex(metricRegex)
		promRE, err := regexp.Compile(promRegex)
		if err != nil {
			return nil, "", fmt.Errorf("%s: invalid normalized metric_regex: %w", ToolNameListMetricCatalog, err)
		}
		return promRE, promRegex, nil
	}
	parts := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		parts = append(parts, regexp.QuoteMeta(p))
	}
	if len(parts) == 0 {
		return regexp.MustCompile(`.+`), ".+", nil
	}
	reString := "^(?:" + strings.Join(parts, "|") + ").*"
	return regexp.MustCompile(reString), reString, nil
}

func metricCatalogPrometheusNameRegex(metricRegex string) string {
	metricRegex = strings.TrimSpace(metricRegex)
	if metricRegex == "" || strings.HasPrefix(metricRegex, "^") || strings.HasPrefix(metricRegex, ".*") {
		return metricRegex
	}
	return ".*(?:" + metricRegex + ").*"
}

func metricCatalogSelector(nameRegex, selector string, labels map[string]string) string {
	parts := []string{fmt.Sprintf(`__name__=~"%s"`, escapePromLabelValue(nameRegex))}
	if s := normalizeMetricCatalogSelectorPart(selector); s != "" {
		parts = append(parts, s)
	}
	if len(labels) > 0 {
		parts = append(parts, labelSelector(labels))
	}
	return strings.Join(parts, ",")
}

func metricCatalogGroupLabels() []string {
	labels := []string{
		"__name__",
		"device_id",
		"edge_id",
		"ongrid_source",
		"job",
		"instance",
		"service",
		"namespace",
		"pod",
	}
	labels = append(labels, metricCatalogDimensionLabels()...)
	return labels
}

func metricCatalogDimensionLabels() []string {
	return []string{
		// Database exporters encode critical selector values in these labels.
		"datname",
		"db",
		"conn_type",
		"count_type",
		"legacy_op_type",
		"state",
		"command",
		"operation",
		"role",
		"mode",
		"mountpoint",
		"fstype",
		"device",
		// Common application and histogram dimensions that are usually low-cardinality.
		"method",
		"status",
		"code",
		"status_code",
		"http_status_code",
		"http_response_status_code",
		"response_code",
		"status_class",
		"le",
	}
}

func metricCatalogNeedsHTTPStatusLabel(query string) bool {
	q := strings.ToLower(query)
	if q == "" {
		return false
	}
	for _, phrase := range []string{"error rate", "burn rate", "错误率", "错误预算"} {
		if strings.Contains(q, phrase) {
			return true
		}
	}
	for _, token := range metricCatalogQueryTokens(q) {
		switch token {
		case "5xx", "500", "slo", "errors":
			return true
		}
	}
	return false
}

func metricCatalogHTTPStatusLabels() []string {
	return []string{
		"status",
		"code",
		"status_code",
		"http_status_code",
		"http_response_status_code",
		"response_code",
		"status_class",
	}
}

func metricCatalogItemsHaveAnySampleLabel(items []MetricCatalogItem, labels ...string) bool {
	if len(items) == 0 || len(labels) == 0 {
		return false
	}
	wanted := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		wanted[label] = struct{}{}
	}
	for _, item := range items {
		for _, sample := range item.SampleLabels {
			for key := range sample {
				if _, ok := wanted[key]; ok {
					return true
				}
			}
		}
	}
	return false
}

func normalizeMetricCatalogSelectorPart(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	return strings.TrimSpace(s)
}

func normalizeMetricCatalogLabels(raw map[string]interface{}) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		switch x := v.(type) {
		case string:
			out[k] = x
		case float64:
			if math.Trunc(x) == x {
				out[k] = strconv.FormatInt(int64(x), 10)
			} else {
				out[k] = strconv.FormatFloat(x, 'f', -1, 64)
			}
		case bool:
			out[k] = strconv.FormatBool(x)
		case nil:
			continue
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

func aggregateMetricCatalog(vals []promInstantValue, nameRE *regexp.Regexp, exactLabels map[string]string, includeSamples bool) []MetricCatalogItem {
	byName := map[string]*MetricCatalogItem{}
	sampleCandidates := map[string]map[string]map[string]string{}
	for _, row := range vals {
		if row.Metric == nil {
			continue
		}
		name := row.Metric["__name__"]
		if name == "" || !nameRE.MatchString(name) || !metricCatalogMatchesLabels(row.Metric, exactLabels) {
			continue
		}
		item := byName[name]
		if item == nil {
			item = &MetricCatalogItem{Name: name}
			byName[name] = item
			sampleCandidates[name] = map[string]map[string]string{}
		}
		if !math.IsNaN(row.Value) && !math.IsInf(row.Value, 0) && row.Value > 0 {
			item.SeriesCount += int(math.Round(row.Value))
		}
		if includeSamples {
			labels := metricCatalogSampleLabels(row.Metric)
			key := metricCatalogLabelKey(labels)
			if key != "" {
				sampleCandidates[name][key] = labels
			}
		}
	}
	out := make([]MetricCatalogItem, 0, len(byName))
	for _, item := range byName {
		if includeSamples {
			item.SampleLabels = chooseMetricCatalogSampleLabels(item.Name, sampleCandidates[item.Name], metricCatalogMaxSampleLabels)
		}
		out = append(out, *item)
	}
	return out
}

func chooseMetricCatalogSampleLabels(metric string, candidates map[string]map[string]string, limit int) []map[string]string {
	if limit <= 0 || len(candidates) == 0 {
		return nil
	}
	type sample struct {
		key      string
		labels   map[string]string
		priority int
	}
	samples := make([]sample, 0, len(candidates))
	for key, labels := range candidates {
		samples = append(samples, sample{
			key:      key,
			labels:   labels,
			priority: metricCatalogSamplePriority(metric, labels),
		})
	}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].priority != samples[j].priority {
			return samples[i].priority > samples[j].priority
		}
		return samples[i].key < samples[j].key
	})
	if len(samples) > limit {
		samples = samples[:limit]
	}
	out := make([]map[string]string, 0, len(samples))
	for _, sample := range samples {
		out = append(out, sample.labels)
	}
	return out
}

func metricCatalogSamplePriority(metric string, labels map[string]string) int {
	priority := 0
	if strings.HasPrefix(strings.ToLower(metric), "node_filesystem_") {
		switch labels["mountpoint"] {
		case "/":
			priority += 100
		case "/var", "/home":
			priority += 60
		}
	}
	if strings.EqualFold(metric, "mongodb_ss_connections") {
		switch strings.ToLower(labels["conn_type"]) {
		case "current":
			priority += 100
		case "available":
			priority += 90
		case "active":
			priority += 40
		}
	}
	switch strings.ToLower(labels["count_type"]) {
	case "total":
		priority += 20
	}
	switch strings.ToLower(labels["state"]) {
	case "current":
		priority += 20
	case "available":
		priority += 15
	}
	return priority
}

func metricCatalogMatchesLabels(metric map[string]string, labels map[string]string) bool {
	for k, want := range labels {
		if metric[k] != want {
			return false
		}
	}
	return true
}

func metricCatalogSampleLabels(metric map[string]string) map[string]string {
	keys := make([]string, 0, len(metric))
	for k, v := range metric {
		if k == "__name__" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = metric[k]
	}
	return out
}

func metricCatalogLabelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, "\xff")
}

func metricCatalogQueryTokens(q string) []string {
	fields := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
	})
	out := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, f := range fields {
		f = strings.Trim(f, "_")
		if len(f) < 2 {
			continue
		}
		if _, ok := metricCatalogStopWords[f]; ok {
			continue
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

var metricCatalogStopWords = map[string]struct{}{
	"alert":         {},
	"rule":          {},
	"create":        {},
	"prometheus":    {},
	"metric":        {},
	"metrics":       {},
	"custom":        {},
	"custommetrics": {},
}

func metricCatalogQueryAliases(q string) []string {
	lower := strings.ToLower(q)
	var out []string
	add := func(items ...string) { out = append(out, items...) }
	isPostgresQuery := strings.Contains(lower, "postgres") || strings.Contains(lower, "postgresql") ||
		strings.Contains(lower, "pgsql") || strings.Contains(lower, " pg ") || strings.HasPrefix(lower, "pg ")
	isRedisQuery := strings.Contains(lower, "redis")
	if strings.Contains(lower, "mysql") {
		add("mysql", "mysql_")
	}
	if isPostgresQuery {
		add("pg_", "postgres")
	}
	if isRedisQuery {
		add("redis", "redis_")
	}
	if strings.Contains(lower, "mongodb") || strings.Contains(lower, "mongo") {
		add("mongodb", "mongodb_", "mongo")
	}
	if strings.Contains(lower, "down") || strings.Contains(lower, "unavailable") ||
		strings.Contains(lower, "exporter") || strings.Contains(lower, "不可用") ||
		strings.Contains(lower, "离线") || strings.Contains(lower, "存活") {
		add("up")
	}
	if strings.Contains(lower, "connection") || strings.Contains(lower, "client") ||
		strings.Contains(lower, "连接") || strings.Contains(lower, "客户端") {
		add("connection", "connections", "connected", "clients", "threads_connected", "max_connections", "maxclients", "numbackends", "backends", "current", "available", "conn_type")
	}
	if strings.Contains(lower, "active") || strings.Contains(lower, "activity") || strings.Contains(lower, "活动") {
		add("active", "activity", "numbackends", "backends")
	}
	if strings.Contains(lower, "usage") || strings.Contains(lower, "pressure") ||
		strings.Contains(lower, "使用率") || strings.Contains(lower, "占用") || strings.Contains(lower, "压力") {
		add("usage", "used", "max", "limit", "total", "free")
	}
	if strings.Contains(lower, "latency") || strings.Contains(lower, "延迟") || strings.Contains(lower, "耗时") {
		add("duration", "latency", "seconds", "bucket")
	}
	if strings.Contains(lower, "error") || strings.Contains(lower, "错误") || strings.Contains(lower, "失败") {
		add("error", "errors", "failed", "failures")
	}
	if strings.Contains(lower, "request") || strings.Contains(lower, "http") || strings.Contains(lower, "请求") {
		add("http", "request", "requests")
	}
	if strings.Contains(lower, "queue") || strings.Contains(lower, "队列") || strings.Contains(lower, "积压") {
		add("queue", "depth", "pending", "backlog")
	}
	if strings.Contains(lower, "slow") || strings.Contains(lower, "慢查询") {
		add("slow", "slow_queries", "slowlog")
	}
	if strings.Contains(lower, "lock") || strings.Contains(lower, "deadlock") || strings.Contains(lower, "锁") {
		add("lock", "locks", "deadlock", "deadlocks", "wait")
	}
	if strings.Contains(lower, "hit") || strings.Contains(lower, "cache") || strings.Contains(lower, "命中率") || strings.Contains(lower, "缓存") {
		add("hit", "hits", "miss", "misses", "cache")
		if isPostgresQuery {
			add("pg_stat_database_blks_hit", "pg_stat_database_blks_read", "blks_hit", "blks_read")
		}
		if isRedisQuery {
			add("redis_keyspace_hits_total", "redis_keyspace_misses_total", "keyspace_hits", "keyspace_misses")
		}
	}
	if strings.Contains(lower, "replication") || strings.Contains(lower, "replica") || strings.Contains(lower, "复制") || strings.Contains(lower, "主从") {
		add("replication", "replica", "lag", "slave")
	}
	if strings.Contains(lower, "cpu") {
		add("cpu")
	}
	if strings.Contains(lower, "memory") || strings.Contains(lower, "mem") || strings.Contains(lower, "内存") {
		add("memory", "mem", "rss")
	}
	if strings.Contains(lower, "disk") || strings.Contains(lower, "filesystem") || strings.Contains(lower, "磁盘") {
		add("disk", "filesystem", "fs")
	}
	if strings.Contains(lower, "network") || strings.Contains(lower, "带宽") || strings.Contains(lower, "网络") {
		add("network", "net", "rx", "tx", "bytes")
	}
	return out
}

func metricCatalogScore(name string, tokens, aliases []string) int {
	lower := strings.ToLower(name)
	score := 0
	for _, token := range tokens {
		if strings.Contains(lower, token) {
			score += 2
		}
	}
	for _, alias := range aliases {
		if strings.Contains(lower, alias) {
			score += 3
		}
	}
	if lower == "up" {
		score++
	}
	return score
}
