package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

func TestListMetricCatalogTool_Info(t *testing.T) {
	tool := NewListMetricCatalogTool(&fakePromQuerier{}, slog.Default())
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameListMetricCatalog {
		t.Fatalf("Name = %q, want %q", info.Name, ToolNameListMetricCatalog)
	}
	if info.Class != "read" {
		t.Fatalf("Class = %q, want read", info.Class)
	}
	if !strings.Contains(info.WhenToUse, "custommetrics") {
		t.Fatalf("WhenToUse should mention custommetrics: %s", info.WhenToUse)
	}
	if !strings.Contains(info.Description, "sample_labels are examples") ||
		!strings.Contains(info.Description, "do not copy sample label values") {
		t.Fatalf("Info should describe sample label boundaries: desc=%s when=%s", info.Description, info.WhenToUse)
	}
	if !strings.Contains(info.WhenToUse, "draft_config_change") ||
		!strings.Contains(info.WhenToUse, "query_promql") {
		t.Fatalf("Info should describe surrounding tool contract: desc=%s when=%s", info.Description, info.WhenToUse)
	}
	for _, overSpecified := range []string{`conn_type="current"`, `conn_type="active"`, "clamp_min"} {
		if strings.Contains(info.Description, overSpecified) || strings.Contains(info.WhenToUse, overSpecified) {
			t.Fatalf("Info should not hard-code database PromQL policy %q: desc=%s when=%s", overSpecified, info.Description, info.WhenToUse)
		}
	}
}

func TestListMetricCatalogTool_FiltersAndReturnsSamples(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "vector",
			Result: json.RawMessage(`[
				{"metric":{"__name__":"custom_queue_depth","device_id":"5","ongrid_source":"custom:queue","job":"queue-exporter"},"value":[1,"2"]},
				{"metric":{"__name__":"custom_queue_depth","device_id":"5","ongrid_source":"custom:queue","job":"queue-exporter","instance":"127.0.0.1:9100"},"value":[1,"3"]},
				{"metric":{"__name__":"http_requests_total","device_id":"5","job":"api"},"value":[1,"7"]},
				{"metric":{"__name__":"custom_queue_depth","device_id":"6","ongrid_source":"custom:queue"},"value":[1,"11"]}
			]`),
		},
	}
	tool := NewListMetricCatalogTool(pq, slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"query":"队列积压","prefixes":["custom_"],"labels":{"device_id":5},"max_metrics":10}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(pq.gotExpr, `__name__=~"^(?:custom_).*"`) {
		t.Fatalf("expr missing prefix matcher: %s", pq.gotExpr)
	}
	if !strings.Contains(pq.gotExpr, `device_id="5"`) {
		t.Fatalf("expr missing device label: %s", pq.gotExpr)
	}

	var resp MetricCatalogResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.MetricCount != 1 || resp.Returned != 1 {
		t.Fatalf("count = %d returned = %d, want 1/1: %s", resp.MetricCount, resp.Returned, out)
	}
	got := resp.Metrics[0]
	if got.Name != "custom_queue_depth" {
		t.Fatalf("metric name = %q", got.Name)
	}
	if got.SeriesCount != 5 {
		t.Fatalf("series_count = %d, want 5", got.SeriesCount)
	}
	if len(got.SampleLabels) == 0 || got.SampleLabels[0]["ongrid_source"] != "custom:queue" {
		t.Fatalf("sample labels missing ongrid_source: %#v", got.SampleLabels)
	}
}

func TestListMetricCatalogTool_RanksDatabaseMetricsFromNaturalLanguage(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "vector",
			Result: json.RawMessage(`[
				{"metric":{"__name__":"node_cpu_seconds_total","device_id":"5"},"value":[1,"3"]},
				{"metric":{"__name__":"mysql_global_status_threads_connected","device_id":"5","ongrid_source":"db:mysql-1"},"value":[1,"1"]},
				{"metric":{"__name__":"mysql_global_variables_max_connections","device_id":"5","ongrid_source":"db:mysql-1"},"value":[1,"1"]},
				{"metric":{"__name__":"redis_connected_clients","device_id":"5","ongrid_source":"db:redis-1"},"value":[1,"1"]}
			]`),
		},
	}
	tool := NewListMetricCatalogTool(pq, slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"query":"MySQL 连接使用率超过 80%","max_metrics":2}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var resp MetricCatalogResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Returned != 2 {
		t.Fatalf("returned = %d, want 2: %s", resp.Returned, out)
	}
	for _, item := range resp.Metrics {
		if !strings.HasPrefix(item.Name, "mysql_") {
			t.Fatalf("expected mysql metrics to rank first, got %#v", resp.Metrics)
		}
	}
}

func TestListMetricCatalogTool_EmptyResultInstructsNoInventedMetricsButAllowsValidation(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "vector",
			Result:     json.RawMessage(`[]`),
		},
	}
	tool := NewListMetricCatalogTool(pq, slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"query":"HTTP 5xx burn rate","prefixes":["http_"],"max_metrics":10}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var resp MetricCatalogResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "empty" {
		t.Fatalf("status = %q, want empty", resp.Status)
	}
	for _, want := range []string{"Do not invent metric names", "exact PromQL", "draft validation verify"} {
		if !strings.Contains(resp.Instruction, want) {
			t.Fatalf("instruction = %q, want %q", resp.Instruction, want)
		}
	}
}

func TestListMetricCatalogTool_InstructsWhenBurnRateMetricsLackStatusLabel(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "vector",
			Result: json.RawMessage(`[
				{"metric":{"__name__":"traces_spanmetrics_calls_total","service":"ongrid-manager"},"value":[1,"33"]},
				{"metric":{"__name__":"traces_spanmetrics_latency_count","service":"ongrid-manager"},"value":[1,"33"]}
			]`),
		},
	}
	tool := NewListMetricCatalogTool(pq, slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"query":"API 5xx burn rate for ongrid-manager","prefixes":["http_","custom_","traces_spanmetrics_"],"max_metrics":10}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var resp MetricCatalogResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want ok", resp.Status)
	}
	for _, want := range []string{"status/code label", "Do not invent missing labels", "exact PromQL"} {
		if !strings.Contains(resp.Instruction, want) {
			t.Fatalf("instruction = %q, want %q", resp.Instruction, want)
		}
	}
}

func TestMetricCatalogNeedsHTTPStatusLabelDoesNotTreatSlowAsSLO(t *testing.T) {
	if metricCatalogNeedsHTTPStatusLabel("MySQL slow queries") {
		t.Fatalf("MySQL slow queries should not require HTTP status labels")
	}
	if !metricCatalogNeedsHTTPStatusLabel("API 5xx burn rate") {
		t.Fatalf("5xx burn rate should require HTTP status labels")
	}
	if !metricCatalogNeedsHTTPStatusLabel("SLO error budget") {
		t.Fatalf("SLO error budget should require HTTP status labels")
	}
}

func TestListMetricCatalogTool_ReturnsFilesystemMountpointSamples(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "vector",
			Result: json.RawMessage(`[
				{"metric":{"__name__":"node_filesystem_avail_bytes","device_id":"2","mountpoint":"/var","fstype":"ext4","device":"/dev/vda1"},"value":[1,"1"]},
				{"metric":{"__name__":"node_filesystem_avail_bytes","device_id":"2","mountpoint":"/","fstype":"ext4","device":"/dev/vda1"},"value":[1,"1"]},
				{"metric":{"__name__":"node_filesystem_avail_bytes","device_id":"2","mountpoint":"/home","fstype":"ext4","device":"/dev/vda1"},"value":[1,"1"]}
			]`),
		},
	}
	tool := NewListMetricCatalogTool(pq, slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"query":"磁盘根分区可用空间","prefixes":["node_"],"max_metrics":10}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(pq.gotExpr, "mountpoint") || !strings.Contains(pq.gotExpr, "fstype") {
		t.Fatalf("expr should group filesystem labels: %s", pq.gotExpr)
	}
	var resp MetricCatalogResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Metrics) != 1 || len(resp.Metrics[0].SampleLabels) == 0 {
		t.Fatalf("metrics = %#v, want filesystem sample labels", resp.Metrics)
	}
	if got := resp.Metrics[0].SampleLabels[0]["mountpoint"]; got != "/" {
		t.Fatalf("first mountpoint sample = %q, want root", got)
	}
}

func TestListMetricCatalogTool_NormalizesMetricRegexForPrometheusFullMatch(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "vector",
			Result: json.RawMessage(`[
				{"metric":{"__name__":"node_filesystem_avail_bytes","device_id":"2","mountpoint":"/","fstype":"ext4"},"value":[1,"1"]},
				{"metric":{"__name__":"node_filesystem_size_bytes","device_id":"2","mountpoint":"/","fstype":"ext4"},"value":[1,"1"]},
				{"metric":{"__name__":"node_cpu_seconds_total","device_id":"2","mode":"idle"},"value":[1,"1"]}
			]`),
		},
	}
	tool := NewListMetricCatalogTool(pq, slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"query":"根分区可用空间","prefixes":["node_"],"metric_regex":"node_filesystem_avail|node_filesystem_size","max_metrics":10}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(pq.gotExpr, `__name__=~".*(?:node_filesystem_avail|node_filesystem_size).*"`) {
		t.Fatalf("expr = %s, want substring regex normalized for Prometheus full-match semantics", pq.gotExpr)
	}
	var resp MetricCatalogResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	names := make([]string, 0, len(resp.Metrics))
	for _, item := range resp.Metrics {
		names = append(names, item.Name)
	}
	if !containsName(names, "node_filesystem_avail_bytes") || !containsName(names, "node_filesystem_size_bytes") {
		t.Fatalf("metrics = %#v, want filesystem avail and size metrics", names)
	}
	if containsName(names, "node_cpu_seconds_total") {
		t.Fatalf("metrics = %#v, should be filtered by normalized regex", names)
	}
}

func TestListMetricCatalogTool_ReturnsDatabaseDimensionSamples(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "vector",
			Result: json.RawMessage(`[
				{"metric":{"__name__":"mongodb_ss_connections","device_id":"2","ongrid_source":"db:mongo-test","conn_type":"active"},"value":[1,"1"]},
				{"metric":{"__name__":"mongodb_ss_connections","device_id":"2","ongrid_source":"db:mongo-test","conn_type":"available"},"value":[1,"1"]},
				{"metric":{"__name__":"mongodb_ss_connections","device_id":"2","ongrid_source":"db:mongo-test","conn_type":"awaitingTopologyChanges"},"value":[1,"1"]},
				{"metric":{"__name__":"mongodb_ss_connections","device_id":"2","ongrid_source":"db:mongo-test","conn_type":"current"},"value":[1,"1"]},
				{"metric":{"__name__":"mongodb_ss_globalLock_currentQueue","device_id":"2","ongrid_source":"db:mongo-test","count_type":"total"},"value":[1,"1"]}
			]`),
		},
	}
	tool := NewListMetricCatalogTool(pq, slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"query":"MongoDB 当前连接 可用连接 全局锁队列","prefixes":["mongodb_"],"max_metrics":10}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(pq.gotExpr, "conn_type") || !strings.Contains(pq.gotExpr, "count_type") {
		t.Fatalf("expr should preserve database discriminator labels: %s", pq.gotExpr)
	}

	var resp MetricCatalogResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, item := range resp.Metrics {
		switch item.Name {
		case "mongodb_ss_connections":
			if !sampleLabelsContain(item.SampleLabels, "conn_type", "current") ||
				!sampleLabelsContain(item.SampleLabels, "conn_type", "available") {
				t.Fatalf("connections samples = %#v, want current and available even when current is not among first rows", item.SampleLabels)
			}
		case "mongodb_ss_globalLock_currentQueue":
			if len(item.SampleLabels) == 0 || item.SampleLabels[0]["count_type"] != "total" {
				t.Fatalf("queue samples missing count_type: %#v", item.SampleLabels)
			}
		}
	}
}

func sampleLabelsContain(samples []map[string]string, key, value string) bool {
	for _, sample := range samples {
		if sample[key] == value {
			return true
		}
	}
	return false
}

func TestListMetricCatalogTool_RanksPostgresNumBackendsForConnections(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "vector",
			Result: json.RawMessage(`[
				{"metric":{"__name__":"pg_settings_log_connections","device_id":"2","ongrid_source":"db:pg-test"},"value":[1,"1"]},
				{"metric":{"__name__":"pg_stat_database_numbackends","device_id":"2","ongrid_source":"db:pg-test","datname":"postgres"},"value":[1,"1"]},
				{"metric":{"__name__":"pg_settings_max_connections","device_id":"2","ongrid_source":"db:pg-test"},"value":[1,"1"]}
			]`),
		},
	}
	tool := NewListMetricCatalogTool(pq, slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"query":"PostgreSQL 活动连接数 max_connections","prefixes":["pg_"],"max_metrics":2}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}

	var resp MetricCatalogResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	names := make([]string, 0, len(resp.Metrics))
	for _, item := range resp.Metrics {
		names = append(names, item.Name)
	}
	if !containsName(names, "pg_stat_database_numbackends") || !containsName(names, "pg_settings_max_connections") {
		t.Fatalf("expected connection numerator and denominator in top results, got %#v", names)
	}
}

func TestListMetricCatalogTool_RanksPostgresCacheHitPairTogether(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "vector",
			Result: json.RawMessage(`[
				{"metric":{"__name__":"pg_settings_effective_cache_size_bytes","device_id":"2","ongrid_source":"db:pg-test"},"value":[1,"1"]},
				{"metric":{"__name__":"pg_settings_debug_discard_caches","device_id":"2","ongrid_source":"db:pg-test"},"value":[1,"1"]},
				{"metric":{"__name__":"pg_stat_database_blks_hit","device_id":"2","ongrid_source":"db:pg-test","datname":"postgres"},"value":[1,"1"]},
				{"metric":{"__name__":"pg_stat_database_blks_read","device_id":"2","ongrid_source":"db:pg-test","datname":"postgres"},"value":[1,"1"]},
				{"metric":{"__name__":"pg_statio_user_tables_heap_blocks_hit","device_id":"2","ongrid_source":"db:pg-test"},"value":[1,"1"]}
			]`),
		},
	}
	tool := NewListMetricCatalogTool(pq, slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"query":"PostgreSQL 缓存命中率 cache hit ratio","prefixes":["pg_"],"max_metrics":2}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}

	var resp MetricCatalogResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	names := make([]string, 0, len(resp.Metrics))
	for _, item := range resp.Metrics {
		names = append(names, item.Name)
	}
	if !containsName(names, "pg_stat_database_blks_hit") || !containsName(names, "pg_stat_database_blks_read") {
		t.Fatalf("expected cache hit numerator and denominator in top results, got %#v", names)
	}
}

func TestMetricCatalogQueryAliasesIncludeMongoConnectionParts(t *testing.T) {
	aliases := metricCatalogQueryAliases("MongoDB 连接使用率超过 80%")
	for _, want := range []string{"mongodb_", "current", "available", "conn_type"} {
		if !containsName(aliases, want) {
			t.Fatalf("aliases = %#v, want %q", aliases, want)
		}
	}
}

func TestListMetricCatalogTool_RejectsEmptyMatchingRegex(t *testing.T) {
	tool := NewListMetricCatalogTool(&fakePromQuerier{}, slog.Default())
	_, err := tool.InvokableRun(context.Background(), `{"metric_regex":".*"}`)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "must not match the empty string") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegistryRegistersMetricCatalogWithProm(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, &fakePromQuerier{}, nil, nil, nil, slog.Default())
	if !containsName(schemaNames(reg.Schemas()), ToolNameListMetricCatalog) {
		t.Fatalf("closure registry missing %q: %v", ToolNameListMetricCatalog, schemaNames(reg.Schemas()))
	}
	if !containsName(toolInfoNames(t, reg.BuildBaseTools().AllTools()), ToolNameListMetricCatalog) {
		t.Fatalf("BaseTool registry missing %q", ToolNameListMetricCatalog)
	}
}
