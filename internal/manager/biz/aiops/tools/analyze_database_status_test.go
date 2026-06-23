package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

type fakePluginConfigLister struct {
	rows map[uint64][]edgebiz.PluginRow
	err  error
}

func (f fakePluginConfigLister) ListForUI(_ context.Context, edgeID uint64) ([]edgebiz.PluginRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.rows[edgeID], nil
}

type fakeDatabaseProm struct {
	mu      sync.Mutex
	results map[string]*promquery.InstantResult
	errs    map[string]error
	exprs   []string
}

func (f *fakeDatabaseProm) Query(_ context.Context, expr string, _ time.Time) (*promquery.InstantResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exprs = append(f.exprs, expr)
	if err := f.errs[expr]; err != nil {
		return nil, err
	}
	if res := f.results[expr]; res != nil {
		return res, nil
	}
	return promVector(), nil
}

func (f *fakeDatabaseProm) QueryRange(_ context.Context, expr string, start, end time.Time, step time.Duration) (*promquery.InstantResult, error) {
	return f.Query(context.Background(), expr, end)
}

func (f *fakeDatabaseProm) saw(part string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, expr := range f.exprs {
		if strings.Contains(expr, part) {
			return true
		}
	}
	return false
}

type promTestValue struct {
	metric map[string]string
	value  string
}

func promVector(items ...promTestValue) *promquery.InstantResult {
	rows := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		rows = append(rows, map[string]interface{}{
			"metric": item.metric,
			"value":  []interface{}{float64(1700000000), item.value},
		})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		panic(err)
	}
	return &promquery.InstantResult{ResultType: "vector", Result: raw}
}

func TestAnalyzeDatabaseStatus_RegisteredWhenPromEdgesAndPluginConfigsPresent(t *testing.T) {
	deviceID := uint64(42)
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(&edgemodel.Edge{ID: 1, Name: "node-a", DeviceID: &deviceID}), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, &fakeDatabaseProm{}, nil, nil, nil, slog.Default())

	if containsName(schemaNames(reg.Schemas()), ToolNameAnalyzeDatabaseStatus) {
		t.Fatalf("%s should not register before plugin config lister is wired", ToolNameAnalyzeDatabaseStatus)
	}
	if containsToolName(t, reg.BuildBaseTools().AllTools(), ToolNameAnalyzeDatabaseStatus) {
		t.Fatalf("%s should not be in BaseTool bag before plugin config lister is wired", ToolNameAnalyzeDatabaseStatus)
	}

	reg.SetPluginConfigLister(fakePluginConfigLister{rows: map[uint64][]edgebiz.PluginRow{}})
	if !containsName(schemaNames(reg.Schemas()), ToolNameAnalyzeDatabaseStatus) {
		t.Fatalf("%s not registered: %v", ToolNameAnalyzeDatabaseStatus, schemaNames(reg.Schemas()))
	}
	if !containsToolName(t, reg.BuildBaseTools().AllTools(), ToolNameAnalyzeDatabaseStatus) {
		t.Fatalf("%s missing from BaseTool bag", ToolNameAnalyzeDatabaseStatus)
	}
}

func TestAnalyzeDatabaseStatus_NotRegisteredWhenPromNil(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, nil, nil, nil, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{rows: map[uint64][]edgebiz.PluginRow{}})

	if containsName(schemaNames(reg.Schemas()), ToolNameAnalyzeDatabaseStatus) {
		t.Fatalf("%s should not register without Prom", ToolNameAnalyzeDatabaseStatus)
	}
}

func TestAnalyzeDatabaseStatus_WhenAskedInventory_AdvertisesCoverage(t *testing.T) {
	tool := NewAnalyzeDatabaseStatusTool(&fakeDatabaseProm{}, nil, nil, nil, slog.Default())
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	text := strings.ToLower(info.Description + " " + info.WhenToUse)
	for _, want := range []string{
		"first tool",
		"exporter metrics can cover",
		"advanced/optional exporter collectors",
		"typed object-inventory",
		"logical database/schema/table/collection/key counts",
		"object inventory metrics",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("tool routing hints missing %q in %q", want, text)
		}
	}
	for _, old := range []string{"database/source counts", "not for database inventory", "do not use for simple inventory/topology"} {
		if strings.Contains(text, old) {
			t.Fatalf("tool routing hints still contain old exclusion %q in %q", old, text)
		}
	}
}

func TestListDatabaseSources_WhenAskedGenericInventory_AdvertisesRoute(t *testing.T) {
	tool := NewListDatabaseSourcesTool(nil, nil, nil, nil, slog.Default())
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	text := strings.ToLower(info.Description + " " + info.WhenToUse)
	for _, want := range []string{
		"how many databases do i have",
		"without naming a db type",
		"which device or edge hosts them",
		"databasemetrics or custommetrics",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("list datasource routing hints missing %q in %q", want, text)
		}
	}
}

func TestListDatabaseSources_FromPluginConfigs(t *testing.T) {
	deviceID := uint64(42)
	edge := &edgemodel.Edge{ID: 1, Name: "db-node", DeviceID: &deviceID}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(edge), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, nil, nil, nil, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{rows: map[uint64][]edgebiz.PluginRow{
		1: {
			{
				PluginName: "databasemetrics",
				Enabled:    true,
				Spec: map[string]interface{}{
					"sources": []interface{}{
						map[string]interface{}{
							"id":           "mysql-prod",
							"name":         "MySQL prod",
							"db_type":      "mysql",
							"enabled":      true,
							"source_label": "db:mysql-prod",
						},
					},
				},
			},
			{
				PluginName: "custommetrics",
				Enabled:    true,
				Spec: map[string]interface{}{
					"targets": []interface{}{
						map[string]interface{}{
							"id":           "redis-exporter",
							"name":         "Redis exporter",
							"enabled":      true,
							"source_label": "custom:redis-exporter",
							"resource": map[string]interface{}{
								"category": "database",
								"type":     "redis",
							},
						},
					},
				},
			},
		},
	}})

	if !containsName(schemaNames(reg.Schemas()), ToolNameListDatabaseSources) {
		t.Fatalf("%s not registered: %v", ToolNameListDatabaseSources, schemaNames(reg.Schemas()))
	}
	if !containsToolName(t, reg.BuildBaseTools().AllTools(), ToolNameListDatabaseSources) {
		t.Fatalf("%s missing from BaseTool bag", ToolNameListDatabaseSources)
	}

	out, err := reg.Invoke(context.Background(), ToolNameListDatabaseSources, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var resp DatabaseSourcesResponse
	if err := json.Unmarshal(out.ResultJSON, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 2 || resp.ByDBType["mysql"] != 1 || resp.ByDBType["redis"] != 1 {
		t.Fatalf("wrong counts: %+v", resp)
	}
	if resp.Sources[0].Relationship == "" || resp.Sources[0].DeviceID != deviceID {
		t.Fatalf("missing source placement: %+v", resp.Sources[0])
	}
}

func TestAnalyzeDatabaseStatus_MySQLConnectionPressure(t *testing.T) {
	deviceID := uint64(42)
	edge := &edgemodel.Edge{ID: 1, Name: "db-node", DeviceID: &deviceID}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(edge), nil, nil, slog.Default())
	uc.RecordPluginHealth(1, []edgebiz.PluginHealth{
		{
			Name:  "databasemetrics",
			State: "running",
			Targets: []edgebiz.PluginTargetHealth{
				{ID: "mysql-managed", State: "running", Samples: 1045, LastSuccessAt: time.Now().UTC()},
			},
		},
	})

	selector := `device_id="42",ongrid_source="db:mysql-managed"`
	prom := &fakeDatabaseProm{results: map[string]*promquery.InstantResult{
		`sum(count by (__name__) ({` + selector + `}))`: promVector(promTestValue{value: "1045"}),
		`count by (__name__) ({` + selector + `})`: promVector(
			promTestValue{metric: map[string]string{"__name__": "mysql_up"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mysql_global_status_threads_connected"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mysql_global_variables_max_connections"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mysql_global_status_threads_running"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mysql_global_status_questions"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mysql_global_status_slow_queries"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mysql_global_status_aborted_connects"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mysql_global_status_innodb_buffer_pool_reads"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mysql_global_status_innodb_buffer_pool_read_requests"}, value: "1"},
		),
		`min(mysql_up{` + selector + `})`:                                promVector(promTestValue{value: "1"}),
		`max(mysql_global_status_threads_connected{` + selector + `})`:   promVector(promTestValue{value: "138"}),
		`max(mysql_global_variables_max_connections{` + selector + `})`:  promVector(promTestValue{value: "150"}),
		`max(mysql_global_status_threads_running{` + selector + `})`:     promVector(promTestValue{value: "4"}),
		`sum(rate(mysql_global_status_questions{` + selector + `}[5m]))`: promVector(promTestValue{value: "12.5"}),
		`100 * max(mysql_global_status_threads_connected{` + selector + `}) / clamp_min(max(mysql_global_variables_max_connections{` + selector + `}), 1)`:                                                promVector(promTestValue{value: "92"}),
		`sum(rate(mysql_global_status_slow_queries{` + selector + `}[5m]))`:                                                                                                                               promVector(promTestValue{value: "0"}),
		`sum(increase(mysql_global_status_aborted_connects{` + selector + `}[15m]))`:                                                                                                                      promVector(promTestValue{value: "0"}),
		`100 * (1 - sum(rate(mysql_global_status_innodb_buffer_pool_reads{` + selector + `}[5m])) / clamp_min(sum(rate(mysql_global_status_innodb_buffer_pool_read_requests{` + selector + `}[5m])), 1))`: promVector(promTestValue{value: "96"}),
	}}
	reg := NewRegistry(&fakeCaller{}, uc, nil, prom, nil, nil, nil, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{rows: map[uint64][]edgebiz.PluginRow{
		1: {
			{
				PluginName: "databasemetrics",
				Enabled:    true,
				Spec: map[string]interface{}{
					"sources": []interface{}{
						map[string]interface{}{
							"id":      "mysql-managed",
							"enabled": true,
							"db_type": "mysql",
							"name":    "MySQL managed",
						},
					},
				},
			},
		},
	}})

	out, err := reg.Invoke(context.Background(), ToolNameAnalyzeDatabaseStatus, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var resp DatabaseStatusResponse
	if err := json.Unmarshal(out.ResultJSON, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "critical" {
		t.Fatalf("status = %q, want critical; resp=%s", resp.Status, string(out.ResultJSON))
	}
	if len(resp.Sources) != 1 {
		t.Fatalf("sources=%d, want 1", len(resp.Sources))
	}
	src := resp.Sources[0]
	if src.DeviceID != 42 || src.EdgeID != 1 || src.SourceLabel != "db:mysql-managed" {
		t.Fatalf("source identity wrong: %+v", src)
	}
	if src.Metrics["connection_usage_pct"] != 92 {
		t.Fatalf("connection_usage_pct=%v", src.Metrics["connection_usage_pct"])
	}
	if src.Metrics["current_connections"] != 138 || src.Metrics["max_connections"] != 150 {
		t.Fatalf("connection metrics wrong: %+v", src.Metrics)
	}
	if src.Metrics["threads_running"] != 4 || src.Metrics["queries_per_second"] != 12.5 || src.Metrics["innodb_buffer_pool_hit_pct"] != 96 {
		t.Fatalf("extended mysql metrics wrong: %+v", src.Metrics)
	}
	if got := capabilityStatus(src.Capabilities, "connections"); got != "available" {
		t.Fatalf("connections capability=%q want available: %+v", got, src.Capabilities)
	}
	if got := capabilityStatus(src.Capabilities, "schema_table_inventory"); got != "unavailable" {
		t.Fatalf("schema_table_inventory capability=%q want unavailable: %+v", got, src.Capabilities)
	}
	if !prom.saw(`device_id="42",ongrid_source="db:mysql-managed"`) {
		t.Fatalf("expected PromQL selector to include device_id and ongrid_source")
	}
	found := false
	for _, finding := range src.Findings {
		if finding.Code == "mysql_connection_pressure" && finding.Severity == "critical" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing mysql_connection_pressure finding: %+v", src.Findings)
	}
}

func TestAnalyzeDatabaseStatus_NonFiniteThresholdValueDoesNotBecomeCritical(t *testing.T) {
	expr := `100 * max(mysql_global_status_threads_connected{}) / max(mysql_global_variables_max_connections{})`
	runner := databaseStatusRunner{promQuery: &fakeDatabaseProm{results: map[string]*promquery.InstantResult{
		expr: promVector(promTestValue{value: "+Inf"}),
	}}}
	row := &DatabaseStatusSource{Metrics: map[string]float64{}}

	runner.checkThreshold(context.Background(), "", map[string]struct{}{
		"mysql_global_status_threads_connected":  {},
		"mysql_global_variables_max_connections": {},
	}, row, numericCheck{
		MetricKey: "connection_usage_pct",
		Code:      "mysql_connection_pressure",
		Title:     "MySQL 连接使用率偏高",
		Expr:      expr,
		Required:  []string{"mysql_global_status_threads_connected", "mysql_global_variables_max_connections"},
		CritAbove: floatPtr(90),
		Unit:      "%",
	})

	if _, ok := row.Metrics["connection_usage_pct"]; ok {
		t.Fatalf("non-finite value should not be stored as a metric: %+v", row.Metrics)
	}
	if len(row.Findings) != 1 {
		t.Fatalf("findings=%d want 1 unknown finding: %+v", len(row.Findings), row.Findings)
	}
	if row.Findings[0].Severity != "unknown" || row.Findings[0].Code != "prom_query_error" {
		t.Fatalf("finding = %+v, want structured unknown prom_query_error", row.Findings[0])
	}
	if strings.Contains(row.Findings[0].Message, "critical") {
		t.Fatalf("non-finite value was reported like a critical threshold: %+v", row.Findings[0])
	}
}

func TestAnalyzeDatabaseStatus_PostgreSQLObjectInventory(t *testing.T) {
	deviceID := uint64(42)
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(&edgemodel.Edge{ID: 1, Name: "pg-node", DeviceID: &deviceID}), nil, nil, slog.Default())
	selector := `device_id="42",ongrid_source="db:pg-managed"`
	prom := &fakeDatabaseProm{results: map[string]*promquery.InstantResult{
		`sum(count by (__name__) ({` + selector + `}))`: promVector(promTestValue{value: "554"}),
		`count by (__name__) ({` + selector + `})`: promVector(
			promTestValue{metric: map[string]string{"__name__": "pg_up"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "pg_database_size_bytes"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "pg_stat_user_tables_n_live_tup"}, value: "1"},
		),
		`min(pg_up{` + selector + `})`:                                                             promVector(promTestValue{value: "1"}),
		`sum(pg_database_size_bytes{` + selector + `})`:                                            promVector(promTestValue{value: "3072"}),
		`count(count by (datname) (pg_database_size_bytes{` + selector + `}))`:                     promVector(promTestValue{value: "2"}),
		`count(count by (schemaname, relname) (pg_stat_user_tables_n_live_tup{` + selector + `}))`: promVector(promTestValue{value: "17"}),
	}}
	reg := NewRegistry(&fakeCaller{}, uc, nil, prom, nil, nil, nil, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{rows: map[uint64][]edgebiz.PluginRow{
		1: {
			{
				PluginName: "databasemetrics",
				Enabled:    true,
				Spec: map[string]interface{}{
					"sources": []interface{}{
						map[string]interface{}{"id": "pg-managed", "db_type": "postgresql", "enabled": true},
					},
				},
			},
		},
	}})

	out, err := reg.Invoke(context.Background(), ToolNameAnalyzeDatabaseStatus, json.RawMessage(`{"db_types":["postgresql"]}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var resp DatabaseStatusResponse
	if err := json.Unmarshal(out.ResultJSON, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 1 || resp.ByDBType["postgresql"] != 1 || resp.ByPlugin["databasemetrics"] != 1 {
		t.Fatalf("wrong source summary: %+v", resp)
	}
	if len(resp.Sources) != 1 {
		t.Fatalf("sources=%d want 1: %s", len(resp.Sources), string(out.ResultJSON))
	}
	src := resp.Sources[0]
	if src.Metrics["database_count"] != 2 || src.Metrics["table_count"] != 17 {
		t.Fatalf("object inventory metrics wrong: %+v", src.Metrics)
	}
	if src.Metrics["database_size_bytes"] != 3072 {
		t.Fatalf("database_size_bytes=%v want 3072", src.Metrics["database_size_bytes"])
	}
	if got := capabilityStatus(src.Capabilities, "object_inventory"); got != "available" {
		t.Fatalf("object_inventory capability=%q want available: %+v", got, src.Capabilities)
	}
	for _, finding := range src.Findings {
		if finding.Code == "database_object_inventory_missing" {
			t.Fatalf("object inventory should be available, got missing finding: %+v", src.Findings)
		}
	}
}

func TestAnalyzeDatabaseStatus_MongoDBLimitedMetricsCapability(t *testing.T) {
	deviceID := uint64(42)
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(&edgemodel.Edge{ID: 1, Name: "node", DeviceID: &deviceID}), nil, nil, slog.Default())
	selector := `device_id="42",ongrid_source="db:mongodb-managed"`
	prom := &fakeDatabaseProm{results: map[string]*promquery.InstantResult{
		`sum(count by (__name__) ({` + selector + `}))`: promVector(promTestValue{value: "42"}),
		`count by (__name__) ({` + selector + `})`: promVector(
			promTestValue{metric: map[string]string{"__name__": "mongodb_up"}, value: "1"},
		),
		`min(mongodb_up{` + selector + `})`: promVector(promTestValue{value: "1"}),
	}}
	reg := NewRegistry(&fakeCaller{}, uc, nil, prom, nil, nil, nil, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{rows: map[uint64][]edgebiz.PluginRow{
		1: {
			{
				PluginName: "databasemetrics",
				Enabled:    true,
				Spec: map[string]interface{}{
					"sources": []interface{}{
						map[string]interface{}{"id": "mongodb-managed", "db_type": "mongodb", "enabled": true},
					},
				},
			},
		},
	}})

	out, err := reg.Invoke(context.Background(), ToolNameAnalyzeDatabaseStatus, json.RawMessage(`{"db_types":["mongodb"]}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var resp DatabaseStatusResponse
	if err := json.Unmarshal(out.ResultJSON, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status=%q want ok: %s", resp.Status, string(out.ResultJSON))
	}
	if len(resp.Sources) != 1 {
		t.Fatalf("sources=%d want 1: %s", len(resp.Sources), string(out.ResultJSON))
	}
	src := resp.Sources[0]
	if got := capabilityStatus(src.Capabilities, "liveness"); got != "available" {
		t.Fatalf("liveness capability=%q want available: %+v", got, src.Capabilities)
	}
	if got := capabilityStatus(src.Capabilities, "connections"); got != "unavailable" {
		t.Fatalf("connections capability=%q want unavailable: %+v", got, src.Capabilities)
	}
	dbstatsCap := capabilityByName(src.Capabilities, "dbstats")
	if dbstatsCap.Status != "unavailable" || !containsString(dbstatsCap.MissingMetrics, "mongodb_dbstats_dataSize") {
		t.Fatalf("dbstats capability should explain missing metrics: %+v", dbstatsCap)
	}
	if got := capabilityStatus(src.Capabilities, "object_inventory"); got != "unavailable" {
		t.Fatalf("object_inventory capability=%q want unavailable: %+v", got, src.Capabilities)
	}
	found := false
	foundObjectInventoryMissing := false
	for _, finding := range src.Findings {
		if finding.Code == "mongodb_limited_metrics" && finding.Severity == "info" {
			found = true
		}
		if finding.Code == "database_object_inventory_missing" && finding.Severity == "info" {
			foundObjectInventoryMissing = true
		}
	}
	if !found {
		t.Fatalf("missing mongodb_limited_metrics finding: %+v", src.Findings)
	}
	if !foundObjectInventoryMissing {
		t.Fatalf("missing database_object_inventory_missing finding: %+v", src.Findings)
	}
}

func TestAnalyzeDatabaseStatus_MongoDBServerStatusMetrics(t *testing.T) {
	deviceID := uint64(42)
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(&edgemodel.Edge{ID: 1, Name: "node", DeviceID: &deviceID}), nil, nil, slog.Default())
	selector := `device_id="42",ongrid_source="db:mongodb-managed"`
	prom := &fakeDatabaseProm{results: map[string]*promquery.InstantResult{
		`sum(count by (__name__) ({` + selector + `}))`: promVector(promTestValue{value: "2337"}),
		`count by (__name__) ({` + selector + `})`: promVector(
			promTestValue{metric: map[string]string{"__name__": "mongodb_up"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_fcv_feature_compatibility_version"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_connections"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_connections_establishmentRateLimit_rejected"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_opcounters"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_asserts"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_mem_resident"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_extra_info_page_faults"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_network_bytesIn"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_network_bytesOut"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_globalLock_currentQueue"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_flowControl_isLagged"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_wt_cache_bytes_currently_in_the_cache"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_wt_cache_maximum_bytes_configured"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_wt_cache_tracked_dirty_bytes_in_the_cache"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_wt_cache_operations_timed_out_waiting_for_space_in_cache"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_opLatencies_latency"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_opLatencies_ops"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_metrics_queryExecutor_collectionScans_total"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_metrics_query_sort_spillToDisk"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_metrics_cursor_timedOut"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_ss_transactions_currentOpen"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_dbstats_dataSize"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_dbstats_freeStorageSize"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_top_total_count"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_currentop_fsync_lock_state"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_profile_slow_query_count"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_pbm_cluster_backup_configured"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "mongodb_future_new_metric"}, value: "1"},
		),
		`min(mongodb_up{` + selector + `})`:                                                            promVector(promTestValue{value: "1"}),
		`max(mongodb_ss_connections{` + selector + `,conn_type="current"})`:                            promVector(promTestValue{value: "12"}),
		`max(mongodb_ss_connections{` + selector + `,conn_type="available"})`:                          promVector(promTestValue{value: "838848"}),
		`sum(rate(mongodb_ss_opcounters{` + selector + `}[5m]))`:                                       promVector(promTestValue{value: "9.5"}),
		`sum(increase(mongodb_ss_asserts{` + selector + `}[15m]))`:                                     promVector(promTestValue{value: "0"}),
		`sum(increase(mongodb_ss_extra_info_page_faults{` + selector + `}[15m]))`:                      promVector(promTestValue{value: "0"}),
		`max(mongodb_ss_mem_resident{` + selector + `}) * 1024 * 1024`:                                 promVector(promTestValue{value: "268435456"}),
		`max(mongodb_fcv_feature_compatibility_version{` + selector + `})`:                             promVector(promTestValue{value: "8"}),
		`sum(rate(mongodb_ss_network_bytesIn{` + selector + `}[5m]))`:                                  promVector(promTestValue{value: "128"}),
		`sum(rate(mongodb_ss_network_bytesOut{` + selector + `}[5m]))`:                                 promVector(promTestValue{value: "256"}),
		`sum(increase(mongodb_ss_connections_establishmentRateLimit_rejected{` + selector + `}[15m]))`: promVector(promTestValue{value: "0"}),
		`max(mongodb_ss_globalLock_currentQueue{` + selector + `})`:                                    promVector(promTestValue{value: "0"}),
		`max(mongodb_ss_flowControl_isLagged{` + selector + `})`:                                       promVector(promTestValue{value: "0"}),
		`100 * max(mongodb_ss_wt_cache_bytes_currently_in_the_cache{` + selector + `}) / clamp_min(max(mongodb_ss_wt_cache_maximum_bytes_configured{` + selector + `}), 1)`:     promVector(promTestValue{value: "25"}),
		`100 * max(mongodb_ss_wt_cache_tracked_dirty_bytes_in_the_cache{` + selector + `}) / clamp_min(max(mongodb_ss_wt_cache_maximum_bytes_configured{` + selector + `}), 1)`: promVector(promTestValue{value: "1"}),
		`sum(increase(mongodb_ss_wt_cache_operations_timed_out_waiting_for_space_in_cache{` + selector + `}[15m]))`:                                                             promVector(promTestValue{value: "0"}),
		`sum(rate(mongodb_ss_opLatencies_latency{` + selector + `}[5m])) / clamp_min(sum(rate(mongodb_ss_opLatencies_ops{` + selector + `}[5m])), 1) / 1000`:                    promVector(promTestValue{value: "10"}),
		`sum(increase(mongodb_ss_metrics_queryExecutor_collectionScans_total{` + selector + `}[15m]))`:                                                                          promVector(promTestValue{value: "0"}),
		`sum(increase(mongodb_ss_metrics_query_sort_spillToDisk{` + selector + `}[15m]))`:                                                                                       promVector(promTestValue{value: "0"}),
		`sum(increase(mongodb_ss_metrics_cursor_timedOut{` + selector + `}[15m]))`:                                                                                              promVector(promTestValue{value: "0"}),
		`max(mongodb_ss_transactions_currentOpen{` + selector + `})`:                                                                                                            promVector(promTestValue{value: "1"}),
		`count(count by (database) (mongodb_dbstats_dataSize{` + selector + `}))`:                                                                                               promVector(promTestValue{value: "3"}),
		`sum(mongodb_dbstats_dataSize{` + selector + `})`:                                                                                                                       promVector(promTestValue{value: "4096"}),
		`sum(mongodb_dbstats_freeStorageSize{` + selector + `})`:                                                                                                                promVector(promTestValue{value: "512"}),
		`sum(rate(mongodb_top_total_count{` + selector + `}[5m]))`:                                                                                                              promVector(promTestValue{value: "2"}),
		`max(mongodb_currentop_fsync_lock_state{` + selector + `})`:                                                                                                             promVector(promTestValue{value: "0"}),
		`sum(increase(mongodb_profile_slow_query_count{` + selector + `}[15m]))`:                                                                                                promVector(promTestValue{value: "0"}),
		`max(mongodb_pbm_cluster_backup_configured{` + selector + `})`:                                                                                                          promVector(promTestValue{value: "1"}),
	}}
	reg := NewRegistry(&fakeCaller{}, uc, nil, prom, nil, nil, nil, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{rows: map[uint64][]edgebiz.PluginRow{
		1: {
			{
				PluginName: "databasemetrics",
				Enabled:    true,
				Spec: map[string]interface{}{
					"sources": []interface{}{
						map[string]interface{}{"id": "mongodb-managed", "db_type": "mongodb", "enabled": true},
					},
				},
			},
		},
	}})

	out, err := reg.Invoke(context.Background(), ToolNameAnalyzeDatabaseStatus, json.RawMessage(`{"db_types":["mongodb"]}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var resp DatabaseStatusResponse
	if err := json.Unmarshal(out.ResultJSON, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status=%q want ok: %s", resp.Status, string(out.ResultJSON))
	}
	if len(resp.Sources) != 1 {
		t.Fatalf("sources=%d want 1: %s", len(resp.Sources), string(out.ResultJSON))
	}
	src := resp.Sources[0]
	if src.MetricCount == 0 || !containsString(src.MetricNames, "mongodb_ss_connections") {
		t.Fatalf("source should expose discovered metric names and count: %+v", src)
	}
	if !containsString(src.UnmappedNames, "mongodb_future_new_metric") {
		t.Fatalf("unmapped exporter metric not surfaced: %+v", src.UnmappedNames)
	}
	for _, name := range []string{
		"liveness", "feature_compatibility", "connections", "connection_rate_limits", "operations",
		"errors_asserts", "memory", "page_faults", "network", "global_locks", "flow_control",
		"wiredtiger_cache", "op_latency", "query_planner", "query_spills", "cursors",
		"transactions", "dbstats", "dbstats_free_storage", "topmetrics", "current_ops",
		"profile", "backup_pbm",
	} {
		if got := capabilityStatus(src.Capabilities, name); got != "available" {
			t.Fatalf("%s capability=%q want available: %+v", name, got, src.Capabilities)
		}
	}
	wtCap := capabilityByName(src.Capabilities, "wiredtiger_cache")
	if wtCap.MetricCount < 4 || !containsString(wtCap.Metrics, "mongodb_ss_wt_cache_bytes_currently_in_the_cache") {
		t.Fatalf("wiredtiger_cache should expose actual matched metrics: %+v", wtCap)
	}
	if src.Metrics["connections_current"] != 12 || src.Metrics["connections_available"] != 838848 {
		t.Fatalf("connection metrics wrong: %+v", src.Metrics)
	}
	if src.Metrics["operations_per_second"] != 9.5 || src.Metrics["resident_memory_bytes"] != 268435456 {
		t.Fatalf("serverStatus metrics wrong: %+v", src.Metrics)
	}
	if _, ok := src.Metrics["page_faults_15m"]; !ok {
		t.Fatalf("missing page_faults_15m metric: %+v", src.Metrics)
	}
	if src.Metrics["wiredtiger_cache_usage_pct"] != 25 || src.Metrics["network_input_bytes_per_second"] != 128 || src.Metrics["dbstats_data_size_bytes"] != 4096 || src.Metrics["database_count"] != 3 {
		t.Fatalf("expanded mongodb metrics wrong: %+v", src.Metrics)
	}
}

func TestAnalyzeDatabaseStatus_CustomMetricsDatabaseTarget(t *testing.T) {
	deviceID := uint64(7)
	edge := &edgemodel.Edge{ID: 2, Name: "redis-node", DeviceID: &deviceID}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(edge), nil, nil, slog.Default())
	selector := `device_id="7",ongrid_source="custom:redis-exporter"`
	prom := &fakeDatabaseProm{results: map[string]*promquery.InstantResult{
		`sum(count by (__name__) ({` + selector + `}))`: promVector(promTestValue{value: "10"}),
		`count by (__name__) ({` + selector + `})`: promVector(
			promTestValue{metric: map[string]string{"__name__": "redis_up"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "redis_memory_used_bytes"}, value: "1"},
			promTestValue{metric: map[string]string{"__name__": "redis_memory_max_bytes"}, value: "1"},
		),
		`min(redis_up{` + selector + `})`:                promVector(promTestValue{value: "1"}),
		`max(redis_memory_used_bytes{` + selector + `})`: promVector(promTestValue{value: "81"}),
		`max(redis_memory_max_bytes{` + selector + `})`:  promVector(promTestValue{value: "100"}),
	}}
	reg := NewRegistry(&fakeCaller{}, uc, nil, prom, nil, nil, nil, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{rows: map[uint64][]edgebiz.PluginRow{
		2: {
			{
				PluginName: "custommetrics",
				Enabled:    true,
				Spec: map[string]interface{}{
					"targets": []interface{}{
						map[string]interface{}{
							"id":      "redis-exporter",
							"enabled": true,
							"resource": map[string]interface{}{
								"category": "database",
								"type":     "redis",
							},
						},
					},
				},
			},
		},
	}})

	out, err := reg.Invoke(context.Background(), ToolNameAnalyzeDatabaseStatus, json.RawMessage(`{"db_types":["redis"]}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var resp DatabaseStatusResponse
	if err := json.Unmarshal(out.ResultJSON, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Sources) != 1 {
		t.Fatalf("sources=%d, want 1: %s", len(resp.Sources), string(out.ResultJSON))
	}
	if resp.Sources[0].Plugin != "custommetrics" || resp.Sources[0].DBType != "redis" {
		t.Fatalf("wrong source: %+v", resp.Sources[0])
	}
	if resp.Status != "warning" {
		t.Fatalf("status=%q want warning: %s", resp.Status, string(out.ResultJSON))
	}
}

func TestDatabaseCapabilitySpecsCoverExporterCollectorFamilies(t *testing.T) {
	cases := map[string][]string{
		"mysql": {
			"global_status", "global_variables", "binlog_size", "heartbeat", "engine_innodb_status",
			"engine_tokudb_status", "mysql_user", "slave_status", "slave_hosts",
			"info_schema_processlist", "info_schema_clientstats", "info_schema_userstats",
			"info_schema_tablestats", "info_schema_schemastats", "info_schema_innodb_metrics",
			"info_schema_innodb_tablespaces", "info_schema_query_response_time",
			"info_schema_replica_host", "rocksdb_perf_context", "object_inventory", "perf_schema_events_statements",
			"perf_schema_events_statements_sum", "perf_schema_events_waits", "perf_schema_file_events",
			"perf_schema_file_instances", "perf_schema_index_io_waits", "perf_schema_memory_events",
			"perf_schema_table_io_waits", "perf_schema_table_locks", "perf_schema_replication_group",
			"perf_schema_replication_applier", "sys_user_summary",
		},
		"postgresql": {
			"database_collector", "stat_database_collector", "settings", "roles",
			"database_wraparound", "long_running_transactions", "postmaster", "process_idle",
			"replication_slot", "stat_activity_autovacuum", "stat_checkpointer",
			"stat_progress_vacuum", "stat_statements", "stat_user_tables", "object_inventory", "stat_wal_receiver",
			"statio_user_indexes", "statio_user_tables", "wal_directory", "xlog_location",
			"buffercache_summary",
		},
		"redis": {
			"instance_info", "client_list", "command_stats", "object_inventory", "key_value_metrics", "key_groups",
			"rdb_file_size", "latency_latest", "latency_histogram", "cluster_nodes", "streams",
			"lua_scripts", "module_info", "search_indexes", "config_metrics", "system_metrics",
			"sentinel", "tile38",
		},
		"mongodb": {
			"general_info", "diagnosticdata", "server_status", "replset_status", "replset_config",
			"oplog_stats", "object_inventory", "dbstats", "dbstats_free_storage", "topmetrics", "current_ops",
			"indexstats", "collstats", "profile", "sharding", "backup_pbm", "collect_all",
			"compatible_mode",
		},
	}

	for dbType, wantNames := range cases {
		got := capabilitySpecNames(databaseCapabilitySpecs(dbType))
		for _, want := range wantNames {
			if !got[want] {
				t.Fatalf("%s missing capability family %q", dbType, want)
			}
		}
	}
}

func TestDatabaseCapabilitiesDetectExporterDynamicFamilies(t *testing.T) {
	cases := []struct {
		name    string
		dbType  string
		metrics []string
		want    []string
	}{
		{
			name:   "mysql optional collectors",
			dbType: "mysql",
			metrics: []string{
				"mysql_perf_schema_events_statements_sum_total",
				"mysql_perf_schema_last_applied_transaction_end_apply_timestamp_seconds",
				"mysql_heartbeat_mysql_slave_hosts_info",
			},
			want: []string{"perf_schema_events_statements_sum", "perf_schema_replication_applier", "slave_hosts"},
		},
		{
			name:   "postgres optional collectors",
			dbType: "postgresql",
			metrics: []string{
				"pg_stat_checkpointer_num_timed_total",
				"pg_stat_wal_receiver_upstream_node",
				"pg_buffercache_summary_buffers_dirty",
			},
			want: []string{"stat_checkpointer", "stat_wal_receiver", "buffercache_summary"},
		},
		{
			name:   "redis optional collectors",
			dbType: "redis",
			metrics: []string{
				"redis_connected_client_info",
				"redis_search_index_total_index_memory_size_bytes",
				"redis_stream_group_lag",
				"redis_script_result",
			},
			want: []string{"client_list", "search_indexes", "streams", "lua_scripts"},
		},
		{
			name:   "mongodb optional collectors",
			dbType: "mongodb",
			metrics: []string{
				"mongodb_rs_cfg_protocolVersion",
				"mongodb_mongod_wiredtiger_log_bytes_total",
				"mongodb_oplog_stats_wt_transaction_update_conflicts",
				"mongodb_indexstats_accesses_ops",
			},
			want: []string{"replset_config", "compatible_mode", "oplog_stats", "indexstats"},
		},
	}

	for _, tc := range cases {
		discovered := map[string]struct{}{}
		for _, metric := range tc.metrics {
			discovered[metric] = struct{}{}
		}
		caps := databaseCapabilities(tc.dbType, discovered)
		for _, want := range tc.want {
			if got := capabilityStatus(caps, want); got != "available" {
				t.Fatalf("%s capability %s=%q want available: %+v", tc.name, want, got, caps)
			}
		}
	}
}

func capabilityStatus(caps []DatabaseCapability, name string) string {
	return capabilityByName(caps, name).Status
}

func capabilitySpecNames(specs []databaseCapabilitySpec) map[string]bool {
	out := make(map[string]bool, len(specs))
	for _, spec := range specs {
		out[spec.Name] = true
	}
	return out
}

func capabilityByName(caps []DatabaseCapability, name string) DatabaseCapability {
	for _, cap := range caps {
		if cap.Name == name {
			return cap
		}
	}
	return DatabaseCapability{}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestAnalyzeDatabaseStatus_PromErrorStaysStructured(t *testing.T) {
	deviceID := uint64(42)
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(&edgemodel.Edge{ID: 1, Name: "node", DeviceID: &deviceID}), nil, nil, slog.Default())
	selector := `device_id="42",ongrid_source="db:mysql-managed"`
	prom := &fakeDatabaseProm{
		results: map[string]*promquery.InstantResult{},
		errs: map[string]error{
			`sum(count by (__name__) ({` + selector + `}))`: errors.New("prom 500"),
		},
	}
	reg := NewRegistry(&fakeCaller{}, uc, nil, prom, nil, nil, nil, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{rows: map[uint64][]edgebiz.PluginRow{
		1: {
			{
				PluginName: "databasemetrics",
				Enabled:    true,
				Spec: map[string]interface{}{
					"sources": []interface{}{
						map[string]interface{}{"id": "mysql-managed", "db_type": "mysql", "enabled": true},
					},
				},
			},
		},
	}})

	out, err := reg.Invoke(context.Background(), ToolNameAnalyzeDatabaseStatus, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Invoke should return structured result, got error: %v", err)
	}
	var resp DatabaseStatusResponse
	if err := json.Unmarshal(out.ResultJSON, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "unknown" {
		t.Fatalf("status=%q want unknown: %s", resp.Status, string(out.ResultJSON))
	}
	if len(resp.Sources) != 1 || len(resp.Sources[0].Findings) == 0 {
		t.Fatalf("missing structured finding: %s", string(out.ResultJSON))
	}
	for _, finding := range resp.Sources[0].Findings {
		if finding.Code == "no_recent_samples" {
			t.Fatalf("prom query error should not be converted to no_recent_samples: %+v", resp.Sources[0].Findings)
		}
	}
}
