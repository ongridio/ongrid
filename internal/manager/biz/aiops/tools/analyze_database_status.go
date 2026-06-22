package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

const ToolNameAnalyzeDatabaseStatus = "analyze_database_status"
const ToolNameListDatabaseSources = "list_database_sources"

const AnalyzeDatabaseStatusDescription = "Analyze database health and performance from ongrid database metrics sources. " +
	"Use this as the first tool for any MySQL, PostgreSQL, Redis, or MongoDB question that exporter metrics can cover, before raw query_promql, query_knowledge, or AgentTool. " +
	"This includes health, performance, logical database/schema/table/collection/key counts from metrics, cluster/replication, advanced/optional exporter collectors, and metric coverage. " +
	"For database alert-rule creation, list_metric_catalog must discover the current metric names first; use this afterwards only if source, device, or capability context is still needed before drafting. " +
	"It can answer typed object-inventory questions such as how many MySQL schemas, PostgreSQL databases/tables, Redis logical DBs/keys, or MongoDB databases/collections exist from collected exporter metrics; if object inventory metrics are not collected, the response says which capability is missing. " +
	"The response includes discovered metric names, unmapped metric names, and a capability matrix; unavailable capabilities mean the exporter has not collected the needed metrics."

const analyzeDatabaseStatusWhenToUse = "When the user asks about database health, connection pressure, slow queries, Redis memory, " +
	"PostgreSQL deadlocks/cache hit, MongoDB exporter status, DB throughput, lock waits, temp files, replication, persistence, " +
	"network IO, storage size, TLS/SSL, exporter collector coverage, logical database/schema/table/collection/key counts, cluster status, or which database metrics are available from the current exporter. " +
	"For database alert-rule creation, use list_metric_catalog first to expose current metric names; call this tool afterwards only when the draft still needs database source/capability context. " +
	"For database questions, prefer this tool whenever MySQL/PostgreSQL/Redis/MongoDB exporters could provide the answer, including advanced and optional collectors. " +
	"If the user asks a database object question and the exporter has not collected that metric family, use this tool and report the missing capability instead of falling through to raw PromQL. " +
	"NOT for arbitrary metric exploration after the high-level status is already known; use query_promql for deep custom PromQL."

const (
	maxDatabaseSourceMetricNames     = 128
	maxDatabaseCapabilityMetricNames = 64
	maxDatabaseUnmappedMetricNames   = 64
)

const ListDatabaseSourcesDescription = "List configured database metrics sources without querying Prometheus. " +
	"Use this only for configured-source lists, source counts, generic database inventory questions like 'how many databases do I have', where a database source is located, and simple device/edge/plugin/source relationships when the user asks about configuration rather than current database state or collected database objects. " +
	"Not for health, performance, or metric coverage analysis; use analyze_database_status for those."

const listDatabaseSourcesWhenToUse = "When the user explicitly asks which MySQL/PostgreSQL/Redis/MongoDB metric sources are configured, " +
	"how many databases they have without naming a DB type or an object metric, how many database metric sources are configured, which device or edge hosts them, whether they come from databasemetrics or custommetrics, or what simple relationship the configured sources have. " +
	"Do NOT use for current health, database/table/collection/key counts from metrics, connection pressure, slow queries, cache/memory, replication, TLS/SSL status, or exporter metric coverage."

var AnalyzeDatabaseStatusSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_ids": {
      "type": "array",
      "items": {"type": "integer"},
      "minItems": 1,
      "maxItems": 16,
      "description": "Optional device ids to analyze. Omit to analyze discovered database metric sources across all devices."
    },
    "db_types": {
      "type": "array",
      "items": {"type": "string", "enum": ["mysql", "postgresql", "postgres", "pg", "redis", "mongodb", "mongo"]},
      "description": "Optional database type filter."
    },
    "source_ids": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Optional databasemetrics/custommetrics source ids to analyze."
    },
    "lookback_seconds": {
      "type": "integer",
      "minimum": 300,
      "maximum": 86400,
      "description": "Analysis window in seconds. Default 3600."
    },
    "include_custommetrics": {
      "type": "boolean",
      "description": "Include custommetrics targets with resource.category=database and resource.type set to mysql/postgresql/redis/mongodb. Default true."
    },
    "include_disabled": {
      "type": "boolean",
      "description": "Include disabled plugin rows or disabled sources. Default false."
    }
  }
}`)

var ListDatabaseSourcesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_ids": {
      "type": "array",
      "items": {"type": "integer"},
      "minItems": 1,
      "maxItems": 64,
      "description": "Optional device ids to list. Omit to list discovered database metric sources across all devices."
    },
    "db_types": {
      "type": "array",
      "items": {"type": "string", "enum": ["mysql", "postgresql", "postgres", "pg", "redis", "mongodb", "mongo"]},
      "description": "Optional database type filter."
    },
    "source_ids": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Optional databasemetrics/custommetrics source ids to list."
    },
    "include_custommetrics": {
      "type": "boolean",
      "description": "Include custommetrics targets with resource.category=database and resource.type set to mysql/postgresql/redis/mongodb. Default true."
    },
    "include_disabled": {
      "type": "boolean",
      "description": "Include disabled plugin rows or disabled sources. Default false."
    }
  }
}`)

type AnalyzeDatabaseStatusArgs struct {
	DeviceIDs            []uint64 `json:"device_ids,omitempty"`
	DBTypes              []string `json:"db_types,omitempty"`
	SourceIDs            []string `json:"source_ids,omitempty"`
	LookbackSeconds      int      `json:"lookback_seconds,omitempty"`
	IncludeCustomMetrics *bool    `json:"include_custommetrics,omitempty"`
	IncludeDisabled      bool     `json:"include_disabled,omitempty"`
}

type ListDatabaseSourcesArgs struct {
	DeviceIDs            []uint64 `json:"device_ids,omitempty"`
	DBTypes              []string `json:"db_types,omitempty"`
	SourceIDs            []string `json:"source_ids,omitempty"`
	IncludeCustomMetrics *bool    `json:"include_custommetrics,omitempty"`
	IncludeDisabled      bool     `json:"include_disabled,omitempty"`
}

type DatabaseSourcesResponse struct {
	Status      string                    `json:"status"`
	GeneratedAt time.Time                 `json:"generated_at"`
	Count       int                       `json:"count"`
	Truncated   bool                      `json:"truncated,omitempty"`
	ByDBType    map[string]int            `json:"by_db_type"`
	ByPlugin    map[string]int            `json:"by_plugin"`
	Sources     []DatabaseSourceInventory `json:"sources"`
	Errors      []string                  `json:"errors,omitempty"`
}

type DatabaseSourceInventory struct {
	DeviceID     uint64 `json:"device_id"`
	EdgeID       uint64 `json:"edge_id"`
	DeviceName   string `json:"device_name,omitempty"`
	SourceID     string `json:"source_id"`
	SourceName   string `json:"source_name,omitempty"`
	SourceLabel  string `json:"source_label"`
	DBType       string `json:"db_type"`
	Plugin       string `json:"plugin"`
	Enabled      bool   `json:"enabled"`
	Relationship string `json:"relationship"`
}

type DatabaseStatusResponse struct {
	Status          string                 `json:"status"`
	GeneratedAt     time.Time              `json:"generated_at"`
	LookbackSeconds int                    `json:"lookback_seconds"`
	Count           int                    `json:"count"`
	Truncated       bool                   `json:"truncated,omitempty"`
	ByDBType        map[string]int         `json:"by_db_type,omitempty"`
	ByPlugin        map[string]int         `json:"by_plugin,omitempty"`
	Sources         []DatabaseStatusSource `json:"sources"`
	Errors          []string               `json:"errors,omitempty"`
}

type DatabaseStatusSource struct {
	DeviceID      uint64                  `json:"device_id"`
	EdgeID        uint64                  `json:"edge_id"`
	DeviceName    string                  `json:"device_name,omitempty"`
	SourceID      string                  `json:"source_id"`
	SourceName    string                  `json:"source_name,omitempty"`
	SourceLabel   string                  `json:"source_label"`
	DBType        string                  `json:"db_type"`
	Plugin        string                  `json:"plugin"`
	Enabled       bool                    `json:"enabled"`
	HealthState   string                  `json:"health_state,omitempty"`
	SampleCount5m float64                 `json:"sample_count_5m"`
	LastSuccessAt *time.Time              `json:"last_success_at,omitempty"`
	LastError     string                  `json:"last_error,omitempty"`
	Status        string                  `json:"status"`
	Capabilities  []DatabaseCapability    `json:"capabilities,omitempty"`
	MetricCount   int                     `json:"metric_count,omitempty"`
	MetricNames   []string                `json:"metric_names,omitempty"`
	UnmappedNames []string                `json:"unmapped_metric_names,omitempty"`
	Metrics       map[string]float64      `json:"metrics,omitempty"`
	Findings      []DatabaseStatusFinding `json:"findings,omitempty"`
}

type DatabaseCapability struct {
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	MetricCount    int      `json:"metric_count,omitempty"`
	Metrics        []string `json:"metrics,omitempty"`
	MissingMetrics []string `json:"missing_metrics,omitempty"`
	Message        string   `json:"message,omitempty"`
}

type DatabaseStatusFinding struct {
	Severity  string  `json:"severity"`
	Code      string  `json:"code"`
	Title     string  `json:"title"`
	Value     float64 `json:"value,omitempty"`
	Threshold string  `json:"threshold,omitempty"`
	PromQL    string  `json:"promql,omitempty"`
	Message   string  `json:"message,omitempty"`
}

// PluginConfigLister is the narrow seam used to discover configured metric
// sources. *edge.PluginConfigUC satisfies it.
type PluginConfigLister interface {
	ListForUI(ctx context.Context, edgeID uint64) ([]edgebiz.PluginRow, error)
}

type AnalyzeDatabaseStatusTool struct {
	promQuery     PromQuerier
	edges         *edgebiz.Usecase
	devices       *devicebiz.Usecase
	pluginConfigs PluginConfigLister
	log           *slog.Logger
}

type ListDatabaseSourcesTool struct {
	edges         *edgebiz.Usecase
	devices       *devicebiz.Usecase
	pluginConfigs PluginConfigLister
	log           *slog.Logger
}

func NewAnalyzeDatabaseStatusTool(p PromQuerier, edges *edgebiz.Usecase, devices *devicebiz.Usecase, plugins PluginConfigLister, log *slog.Logger) *AnalyzeDatabaseStatusTool {
	if log == nil {
		log = slog.Default()
	}
	return &AnalyzeDatabaseStatusTool{promQuery: p, edges: edges, devices: devices, pluginConfigs: plugins, log: log}
}

func NewListDatabaseSourcesTool(edges *edgebiz.Usecase, devices *devicebiz.Usecase, plugins PluginConfigLister, log *slog.Logger) *ListDatabaseSourcesTool {
	if log == nil {
		log = slog.Default()
	}
	return &ListDatabaseSourcesTool{edges: edges, devices: devices, pluginConfigs: plugins, log: log}
}

func (t *ListDatabaseSourcesTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameListDatabaseSources,
		Description: ListDatabaseSourcesDescription,
		WhenToUse:   listDatabaseSourcesWhenToUse,
		Parameters:  ListDatabaseSourcesSchema,
		Class:       "read",
	}, nil
}

func (t *ListDatabaseSourcesTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	runner := databaseSourceInventoryRunner{
		edges:         t.edges,
		devices:       t.devices,
		pluginConfigs: t.pluginConfigs,
		log:           t.log,
	}
	out, err := runner.run(ctx, []byte(argsJSON))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (t *AnalyzeDatabaseStatusTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameAnalyzeDatabaseStatus,
		Description: AnalyzeDatabaseStatusDescription,
		WhenToUse:   analyzeDatabaseStatusWhenToUse,
		Parameters:  AnalyzeDatabaseStatusSchema,
		Class:       "read",
	}, nil
}

func (t *AnalyzeDatabaseStatusTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	runner := databaseStatusRunner{
		promQuery:     t.promQuery,
		edges:         t.edges,
		devices:       t.devices,
		pluginConfigs: t.pluginConfigs,
		log:           t.log,
	}
	out, err := runner.run(ctx, []byte(argsJSON))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (r *Registry) executeListDatabaseSources(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	runner := databaseSourceInventoryRunner{
		edges:         r.edges,
		devices:       r.devices,
		pluginConfigs: r.pluginConfigs,
		log:           r.log,
	}
	out, err := runner.run(ctx, args)
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: out}, nil
}

func (r *Registry) executeAnalyzeDatabaseStatus(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	runner := databaseStatusRunner{
		promQuery:     r.promQuery,
		edges:         r.edges,
		devices:       r.devices,
		pluginConfigs: r.pluginConfigs,
		log:           r.log,
	}
	out, err := runner.run(ctx, args)
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: out}, nil
}

type databaseStatusRunner struct {
	promQuery     PromQuerier
	edges         *edgebiz.Usecase
	devices       *devicebiz.Usecase
	pluginConfigs PluginConfigLister
	log           *slog.Logger
}

type databaseSourceInventoryRunner struct {
	edges         *edgebiz.Usecase
	devices       *devicebiz.Usecase
	pluginConfigs PluginConfigLister
	log           *slog.Logger
}

type databaseDeviceCandidate struct {
	DeviceID   uint64
	EdgeID     uint64
	DeviceName string
}

type databaseMetricSource struct {
	databaseDeviceCandidate
	Plugin      string
	SourceID    string
	SourceName  string
	SourceLabel string
	DBType      string
	Enabled     bool
}

func (r databaseSourceInventoryRunner) run(ctx context.Context, argsJSON []byte) (json.RawMessage, error) {
	if r.edges == nil {
		return nil, fmt.Errorf("%s: edge usecase not configured", ToolNameListDatabaseSources)
	}
	if r.pluginConfigs == nil {
		return nil, fmt.Errorf("%s: plugin config lister not configured", ToolNameListDatabaseSources)
	}
	var in ListDatabaseSourcesArgs
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &in); err != nil {
			return nil, fmt.Errorf("%s: bad args: %w", ToolNameListDatabaseSources, err)
		}
	}
	includeCustom := true
	if in.IncludeCustomMetrics != nil {
		includeCustom = *in.IncludeCustomMetrics
	}

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	statusRunner := databaseStatusRunner{
		edges:         r.edges,
		devices:       r.devices,
		pluginConfigs: r.pluginConfigs,
		log:           r.log,
	}
	candidates, err := statusRunner.resolveCandidates(callCtx, in.DeviceIDs)
	if err != nil {
		return nil, err
	}
	sources, errs := statusRunner.discoverSources(callCtx, candidates, normalizeDBTypeSet(in.DBTypes), stringSet(in.SourceIDs), includeCustom, in.IncludeDisabled)

	resp := DatabaseSourcesResponse{
		Status:      "empty",
		GeneratedAt: time.Now().UTC(),
		Count:       len(sources),
		ByDBType:    map[string]int{},
		ByPlugin:    map[string]int{},
		Sources:     []DatabaseSourceInventory{},
		Errors:      errs,
	}
	if len(sources) > 0 {
		resp.Status = "ok"
	}
	for i, src := range sources {
		resp.ByDBType[src.DBType]++
		resp.ByPlugin[src.Plugin]++
		if i >= 256 {
			resp.Truncated = true
			continue
		}
		resp.Sources = append(resp.Sources, DatabaseSourceInventory{
			DeviceID:     src.DeviceID,
			EdgeID:       src.EdgeID,
			DeviceName:   src.DeviceName,
			SourceID:     src.SourceID,
			SourceName:   src.SourceName,
			SourceLabel:  src.SourceLabel,
			DBType:       src.DBType,
			Plugin:       src.Plugin,
			Enabled:      src.Enabled,
			Relationship: databaseSourceRelationship(src),
		})
	}
	if resp.Truncated {
		resp.Errors = append(resp.Errors, "database metric source list truncated to 256; narrow by device_ids, db_types, or source_ids")
	}

	out, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal response: %w", ToolNameListDatabaseSources, err)
	}
	return out, nil
}

func databaseSourceRelationship(src databaseMetricSource) string {
	device := src.DeviceName
	if device == "" {
		device = strconv.FormatUint(src.DeviceID, 10)
	}
	return fmt.Sprintf("device:%s(%d) -> edge:%d -> %s -> source:%s", device, src.DeviceID, src.EdgeID, src.Plugin, src.SourceID)
}

func (r databaseStatusRunner) run(ctx context.Context, argsJSON []byte) (json.RawMessage, error) {
	if r.promQuery == nil {
		return nil, fmt.Errorf("%s: prom query client not configured", ToolNameAnalyzeDatabaseStatus)
	}
	if r.edges == nil {
		return nil, fmt.Errorf("%s: edge usecase not configured", ToolNameAnalyzeDatabaseStatus)
	}
	if r.pluginConfigs == nil {
		return nil, fmt.Errorf("%s: plugin config lister not configured", ToolNameAnalyzeDatabaseStatus)
	}
	var in AnalyzeDatabaseStatusArgs
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &in); err != nil {
			return nil, fmt.Errorf("%s: bad args: %w", ToolNameAnalyzeDatabaseStatus, err)
		}
	}
	if in.LookbackSeconds <= 0 {
		in.LookbackSeconds = 3600
	}
	if in.LookbackSeconds < 300 {
		in.LookbackSeconds = 300
	}
	if in.LookbackSeconds > 86400 {
		in.LookbackSeconds = 86400
	}
	includeCustom := true
	if in.IncludeCustomMetrics != nil {
		includeCustom = *in.IncludeCustomMetrics
	}

	callCtx, cancel := context.WithTimeout(ctx, queryPromqlCallTimeout)
	defer cancel()

	resp := DatabaseStatusResponse{
		Status:          "unknown",
		GeneratedAt:     time.Now().UTC(),
		LookbackSeconds: in.LookbackSeconds,
		ByDBType:        map[string]int{},
		ByPlugin:        map[string]int{},
		Sources:         []DatabaseStatusSource{},
	}

	candidates, err := r.resolveCandidates(callCtx, in.DeviceIDs)
	if err != nil {
		return nil, err
	}
	dbTypes := normalizeDBTypeSet(in.DBTypes)
	sourceIDs := stringSet(in.SourceIDs)
	sources, errs := r.discoverSources(callCtx, candidates, dbTypes, sourceIDs, includeCustom, in.IncludeDisabled)
	resp.Errors = append(resp.Errors, errs...)
	resp.Count = len(sources)
	for _, src := range sources {
		resp.ByDBType[src.DBType]++
		resp.ByPlugin[src.Plugin]++
	}
	if len(sources) > 32 {
		sources = sources[:32]
		resp.Truncated = true
		resp.Errors = append(resp.Errors, "database metric source list truncated to 32; narrow by device_ids, db_types, or source_ids")
	}
	for _, src := range sources {
		row := r.analyzeSource(callCtx, src, in.LookbackSeconds)
		resp.Sources = append(resp.Sources, row)
	}
	if len(resp.Sources) == 0 {
		resp.Status = "unknown"
		resp.Errors = append(resp.Errors, "no database metric sources found")
	} else {
		resp.Status = aggregateDatabaseStatus(resp.Sources)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal response: %w", ToolNameAnalyzeDatabaseStatus, err)
	}
	return out, nil
}

func (r databaseStatusRunner) resolveCandidates(ctx context.Context, deviceIDs []uint64) ([]databaseDeviceCandidate, error) {
	if len(deviceIDs) > 0 {
		out := make([]databaseDeviceCandidate, 0, len(deviceIDs))
		seen := map[uint64]struct{}{}
		for _, id := range deviceIDs {
			if id == 0 {
				return nil, fmt.Errorf("%s: device_ids must be > 0", ToolNameAnalyzeDatabaseStatus)
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			c, err := r.resolveCandidate(ctx, id)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	}

	if r.devices != nil {
		devs, err := r.devices.List(ctx, devicebiz.ListFilter{Limit: 500})
		if err != nil {
			return nil, fmt.Errorf("%s: list devices: %w", ToolNameAnalyzeDatabaseStatus, err)
		}
		out := make([]databaseDeviceCandidate, 0, len(devs))
		for _, d := range devs {
			if d == nil || d.ID == 0 {
				continue
			}
			edgeID, err := r.devices.LookupEdgeForDevice(ctx, d.ID)
			if err != nil || edgeID == 0 {
				continue
			}
			name := firstNonEmpty(d.Name, d.Hostname)
			out = append(out, databaseDeviceCandidate{DeviceID: d.ID, EdgeID: edgeID, DeviceName: name})
		}
		return out, nil
	}

	edges, err := r.edges.List(ctx, edgebiz.ListFilter{Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("%s: list edges: %w", ToolNameAnalyzeDatabaseStatus, err)
	}
	out := make([]databaseDeviceCandidate, 0, len(edges))
	for _, e := range edges {
		if e == nil {
			continue
		}
		deviceID := e.ID
		if e.DeviceID != nil && *e.DeviceID != 0 {
			deviceID = *e.DeviceID
		}
		out = append(out, databaseDeviceCandidate{DeviceID: deviceID, EdgeID: e.ID, DeviceName: e.Name})
	}
	return out, nil
}

func (r databaseStatusRunner) resolveCandidate(ctx context.Context, deviceID uint64) (databaseDeviceCandidate, error) {
	if r.devices != nil {
		if edgeID, err := r.devices.LookupEdgeForDevice(ctx, deviceID); err == nil && edgeID != 0 {
			name := ""
			if d, derr := r.devices.Get(ctx, deviceID); derr == nil && d != nil {
				name = firstNonEmpty(d.Name, d.Hostname)
			}
			return databaseDeviceCandidate{DeviceID: deviceID, EdgeID: edgeID, DeviceName: name}, nil
		}
		if d, err := r.devices.Get(ctx, deviceID); err == nil && d != nil {
			return databaseDeviceCandidate{}, fmt.Errorf("%s: device_id=%d has no host-edge link", ToolNameAnalyzeDatabaseStatus, deviceID)
		}
	}
	if r.edges != nil {
		e, err := r.edges.Get(ctx, deviceID)
		if err == nil && e != nil {
			return candidateFromEdge(e), nil
		}
	}
	return databaseDeviceCandidate{}, fmt.Errorf("%s: device_id=%d not found", ToolNameAnalyzeDatabaseStatus, deviceID)
}

func candidateFromEdge(e *edgemodel.Edge) databaseDeviceCandidate {
	deviceID := e.ID
	if e.DeviceID != nil && *e.DeviceID != 0 {
		deviceID = *e.DeviceID
	}
	return databaseDeviceCandidate{DeviceID: deviceID, EdgeID: e.ID, DeviceName: e.Name}
}

func (r databaseStatusRunner) discoverSources(ctx context.Context, candidates []databaseDeviceCandidate, dbTypes, sourceIDs map[string]struct{}, includeCustom, includeDisabled bool) ([]databaseMetricSource, []string) {
	out := []databaseMetricSource{}
	errs := []string{}
	for _, c := range candidates {
		rows, err := r.pluginConfigs.ListForUI(ctx, c.EdgeID)
		if err != nil {
			errs = append(errs, fmt.Sprintf("edge_id=%d plugin config list: %v", c.EdgeID, err))
			continue
		}
		for _, row := range rows {
			switch row.PluginName {
			case "databasemetrics":
				out = append(out, discoverDatabaseMetricsSources(c, row, dbTypes, sourceIDs, includeDisabled)...)
			case "custommetrics":
				if includeCustom {
					out = append(out, discoverCustomMetricSources(c, row, dbTypes, sourceIDs, includeDisabled)...)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeviceID != out[j].DeviceID {
			return out[i].DeviceID < out[j].DeviceID
		}
		if out[i].DBType != out[j].DBType {
			return out[i].DBType < out[j].DBType
		}
		return out[i].SourceID < out[j].SourceID
	})
	return out, errs
}

func discoverDatabaseMetricsSources(c databaseDeviceCandidate, row edgebiz.PluginRow, dbTypes, sourceIDs map[string]struct{}, includeDisabled bool) []databaseMetricSource {
	raw, ok := row.Spec["sources"].([]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]databaseMetricSource, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		id := stringFromMap(m, "id")
		if id == "" {
			continue
		}
		dbType := normalizeDBType(stringFromMap(m, "db_type"))
		if !isSupportedDatabaseType(dbType) {
			continue
		}
		if len(dbTypes) > 0 {
			if _, ok := dbTypes[dbType]; !ok {
				continue
			}
		}
		if len(sourceIDs) > 0 {
			if _, ok := sourceIDs[id]; !ok {
				continue
			}
		}
		enabled := row.Enabled && boolFromMap(m, "enabled", true)
		if !enabled && !includeDisabled {
			continue
		}
		label := stringFromMap(m, "source_label")
		if label == "" {
			label = "db:" + id
		}
		out = append(out, databaseMetricSource{
			databaseDeviceCandidate: c,
			Plugin:                  "databasemetrics",
			SourceID:                id,
			SourceName:              firstNonEmpty(stringFromMap(m, "name"), id),
			SourceLabel:             label,
			DBType:                  dbType,
			Enabled:                 enabled,
		})
	}
	return out
}

func discoverCustomMetricSources(c databaseDeviceCandidate, row edgebiz.PluginRow, dbTypes, sourceIDs map[string]struct{}, includeDisabled bool) []databaseMetricSource {
	raw, ok := row.Spec["targets"].([]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]databaseMetricSource, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		id := stringFromMap(m, "id")
		if id == "" {
			continue
		}
		resource := mapFromMap(m, "resource")
		if normalizeDatabaseResourceCategory(stringFromMap(resource, "category")) != "database" {
			continue
		}
		dbType := normalizeDBType(stringFromMap(resource, "type"))
		if !isSupportedDatabaseType(dbType) {
			continue
		}
		if len(dbTypes) > 0 {
			if _, ok := dbTypes[dbType]; !ok {
				continue
			}
		}
		if len(sourceIDs) > 0 {
			if _, ok := sourceIDs[id]; !ok {
				continue
			}
		}
		enabled := row.Enabled && boolFromMap(m, "enabled", true)
		if !enabled && !includeDisabled {
			continue
		}
		label := stringFromMap(m, "source_label")
		if label == "" {
			label = "custom:" + id
		}
		out = append(out, databaseMetricSource{
			databaseDeviceCandidate: c,
			Plugin:                  "custommetrics",
			SourceID:                id,
			SourceName:              firstNonEmpty(stringFromMap(m, "name"), id),
			SourceLabel:             label,
			DBType:                  dbType,
			Enabled:                 enabled,
		})
	}
	return out
}

func (r databaseStatusRunner) analyzeSource(ctx context.Context, src databaseMetricSource, lookbackSeconds int) DatabaseStatusSource {
	row := DatabaseStatusSource{
		DeviceID:    src.DeviceID,
		EdgeID:      src.EdgeID,
		DeviceName:  src.DeviceName,
		SourceID:    src.SourceID,
		SourceName:  src.SourceName,
		SourceLabel: src.SourceLabel,
		DBType:      src.DBType,
		Plugin:      src.Plugin,
		Enabled:     src.Enabled,
		Status:      "unknown",
		Metrics:     map[string]float64{},
	}
	r.attachHealth(&row)
	if !src.Enabled {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "info",
			Code:     "source_disabled",
			Title:    "采集源未启用",
			Message:  "This source is disabled in plugin config.",
		})
		row.Status = "unknown"
		return row
	}

	selector := labelSelector(map[string]string{
		"device_id":     strconv.FormatUint(src.DeviceID, 10),
		"ongrid_source": src.SourceLabel,
	})
	activeSeriesExpr := fmt.Sprintf("sum(count by (__name__) ({%s}))", selector)
	activeSeriesQueried := false
	if v, ok, err := r.queryScalar(ctx, activeSeriesExpr); err != nil {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "unknown",
			Code:     "prom_query_error",
			Title:    "Prometheus 查询失败",
			PromQL:   activeSeriesExpr,
			Message:  err.Error(),
		})
	} else {
		activeSeriesQueried = true
		if ok {
			row.SampleCount5m = v
			row.Metrics["sample_count_5m"] = v
			row.Metrics["active_series_count"] = v
		}
	}
	if activeSeriesQueried && row.SampleCount5m <= 0 {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity:  "critical",
			Code:      "no_recent_samples",
			Title:     "当前没有数据库指标序列",
			Value:     row.SampleCount5m,
			Threshold: "> 0",
			PromQL:    activeSeriesExpr,
		})
		row.Status = statusFromFindings(row.Findings)
		return row
	}

	discovered, discoveryErr := r.discoverMetricNames(ctx, selector)
	if discoveryErr != nil {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "unknown",
			Code:     "metric_discovery_error",
			Title:    "指标发现失败",
			Message:  discoveryErr.Error(),
		})
	}
	metricNames := sortedMetricNames(discovered)
	row.MetricCount = len(metricNames)
	row.MetricNames = limitStrings(metricNames, maxDatabaseSourceMetricNames)
	row.Capabilities = databaseCapabilities(src.DBType, discovered)
	row.UnmappedNames = limitStrings(unmappedDatabaseMetricNames(src.DBType, discovered), maxDatabaseUnmappedMetricNames)
	r.analyzeDatabaseObjectInventory(ctx, src.DBType, selector, discovered, &row)
	switch src.DBType {
	case "mysql":
		r.analyzeMySQL(ctx, selector, discovered, &row)
	case "postgresql":
		r.analyzePostgreSQL(ctx, selector, discovered, &row)
	case "redis":
		r.analyzeRedis(ctx, selector, discovered, &row)
	case "mongodb":
		r.analyzeMongoDB(ctx, selector, discovered, &row)
	}
	row.Status = statusFromFindings(row.Findings)
	if row.Status == "unknown" && row.SampleCount5m > 0 {
		row.Status = "ok"
	}
	return row
}

func (r databaseStatusRunner) attachHealth(row *DatabaseStatusSource) {
	if r.edges == nil {
		return
	}
	items := r.edges.PluginHealth(row.EdgeID)
	for _, h := range items {
		if h.Name != row.Plugin {
			continue
		}
		row.HealthState = h.State
		if h.LastError != "" {
			row.LastError = h.LastError
		}
		for _, t := range h.Targets {
			if t.ID != row.SourceID {
				continue
			}
			if t.State != "" {
				row.HealthState = t.State
			}
			if t.LastError != "" {
				row.LastError = t.LastError
			}
			if t.Samples > 0 {
				row.Metrics["last_scrape_samples"] = float64(t.Samples)
			}
			if !t.LastSuccessAt.IsZero() {
				ts := t.LastSuccessAt
				row.LastSuccessAt = &ts
			}
			break
		}
		break
	}
	switch strings.ToLower(row.HealthState) {
	case "", "running":
	case "starting":
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "warning",
			Code:     "source_starting",
			Title:    "采集源正在启动",
		})
	case "stopped", "disabled":
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "critical",
			Code:     "source_not_running",
			Title:    "采集源未运行",
			Message:  row.LastError,
		})
	default:
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "critical",
			Code:     "source_failed",
			Title:    "采集源异常",
			Message:  row.LastError,
		})
	}
}

type databaseCapabilitySpec struct {
	Name     string
	All      []string
	Any      []string
	Prefixes []string
	Message  string
}

func databaseCapabilities(dbType string, discovered map[string]struct{}) []DatabaseCapability {
	specs := databaseCapabilitySpecs(dbType)
	out := make([]DatabaseCapability, 0, len(specs))
	for _, spec := range specs {
		out = append(out, evaluateDatabaseCapability(spec, discovered))
	}
	return out
}

func databaseCapabilitySpecs(dbType string) []databaseCapabilitySpec {
	switch dbType {
	case "mysql":
		return []databaseCapabilitySpec{
			{Name: "liveness", All: []string{"mysql_up"}, Message: "Exporter connectivity and scrape health."},
			{Name: "exporter_health", Any: []string{"mysql_exporter_collector_success", "mysql_exporter_collector_duration_seconds", "mysqld_exporter_build_info"}, Message: "mysqld_exporter collector success, duration, and build metadata."},
			{Name: "global_status", Prefixes: []string{"mysql_global_status_"}, Message: "SHOW GLOBAL STATUS metric family; base for runtime counters and server state."},
			{Name: "global_variables", Prefixes: []string{"mysql_global_variables_"}, Message: "SHOW GLOBAL VARIABLES metric family; base for server limits and feature settings."},
			{Name: "version_info", Any: []string{"mysql_version_info", "mysql_transaction_isolation"}, Message: "MySQL version, distribution, and transaction isolation metadata."},
			{Name: "connections", All: []string{"mysql_global_status_threads_connected", "mysql_global_variables_max_connections"}, Any: []string{"mysql_global_status_threads_running", "mysql_global_status_max_used_connections"}, Message: "Current/max connections, connection pressure, running threads."},
			{Name: "query_throughput", Any: []string{"mysql_global_status_questions", "mysql_global_status_commands_total"}, Message: "QPS or command rate from global status counters."},
			{Name: "slow_queries", All: []string{"mysql_global_status_slow_queries"}, Message: "Slow query growth from global status."},
			{Name: "connection_errors", Any: []string{"mysql_global_status_aborted_connects", "mysql_global_status_aborted_clients", "mysql_global_status_connection_errors_total"}, Message: "Aborted connects/clients and connection error growth."},
			{Name: "network_io", Any: []string{"mysql_global_status_bytes_received", "mysql_global_status_bytes_sent"}, Message: "Client/server network bytes in and out."},
			{Name: "innodb_buffer_pool", All: []string{"mysql_global_status_innodb_buffer_pool_reads", "mysql_global_status_innodb_buffer_pool_read_requests"}, Any: []string{"mysql_global_status_innodb_buffer_pool_bytes_data", "mysql_global_status_innodb_buffer_pool_bytes_dirty"}, Message: "InnoDB buffer pool hit ratio and dirty/data bytes."},
			{Name: "innodb_io", Any: []string{"mysql_global_status_innodb_data_reads", "mysql_global_status_innodb_data_writes", "mysql_global_status_innodb_data_read", "mysql_global_status_innodb_data_written", "mysql_global_status_innodb_data_fsyncs"}, Message: "InnoDB reads/writes, bytes, pages, and fsync activity."},
			{Name: "innodb_redo_log", Any: []string{"mysql_global_status_innodb_log_waits", "mysql_global_status_innodb_log_writes", "mysql_global_status_innodb_os_log_written", "mysql_global_status_innodb_os_log_pending_writes", "mysql_global_status_innodb_redo_log_current_lsn"}, Message: "InnoDB redo log writes, waits, pending writes, and LSN state."},
			{Name: "locks_waits", Any: []string{"mysql_global_status_innodb_row_lock_current_waits", "mysql_global_status_innodb_row_lock_waits", "mysql_global_status_innodb_row_lock_time", "mysql_global_status_table_locks_waited"}, Message: "InnoDB row lock waits, lock time, and table lock waits."},
			{Name: "temp_tables", Any: []string{"mysql_global_status_created_tmp_disk_tables", "mysql_global_status_created_tmp_tables"}, Message: "Temporary table and disk temporary table growth."},
			{Name: "table_cache", Any: []string{"mysql_global_status_open_tables", "mysql_global_status_opened_tables", "mysql_global_status_table_open_cache_hits", "mysql_global_status_table_open_cache_misses", "mysql_global_status_table_open_cache_overflows", "mysql_global_variables_table_open_cache"}, Message: "Open table count, opened table growth, and table cache hit/miss/overflow state."},
			{Name: "file_descriptors", Any: []string{"mysql_global_status_open_files", "mysql_global_variables_open_files_limit"}, Message: "Open file usage against MySQL open_files_limit."},
			{Name: "thread_cache", Any: []string{"mysql_global_status_threads_cached", "mysql_global_status_threads_created", "mysql_global_variables_thread_cache_size"}, Message: "Thread cache size, cached threads, and thread creation growth."},
			{Name: "query_access_patterns", Any: []string{"mysql_global_status_select_scan", "mysql_global_status_select_full_join", "mysql_global_status_select_range_check"}, Message: "Full scans/full joins/range checks that indicate inefficient access patterns."},
			{Name: "sorts", Any: []string{"mysql_global_status_sort_merge_passes", "mysql_global_status_sort_scan", "mysql_global_status_sort_range", "mysql_global_status_sort_rows"}, Message: "Sort activity and merge passes."},
			{Name: "binlog_replication", Any: []string{"mysql_global_status_binlog_cache_disk_use", "mysql_global_status_binlog_cache_use", "mysql_global_status_replica_open_temp_tables", "mysql_slave_status_slave_io_running", "mysql_slave_status_slave_sql_running"}, Message: "Binlog cache activity and replication status when exporter exposes replica/slave collectors."},
			{Name: "ssl", Any: []string{"mysql_global_variables_have_ssl", "mysql_global_variables_have_openssl", "mysql_global_status_ssl_accepts", "mysql_global_status_ssl_finished_accepts"}, Prefixes: []string{"mysql_global_status_ssl_"}, Message: "Server SSL capability and SSL connection counters."},
			{Name: "mysqlx", Prefixes: []string{"mysql_global_status_mysqlx_"}, Message: "MySQL X Plugin connection and network counters."},
			{Name: "binlog_size", Any: []string{"mysql_binlog_size_bytes", "mysql_binlog_files", "mysql_binlog_file_number"}, Prefixes: []string{"mysql_binlog_"}, Message: "Optional binary log file count, active file number, and total binlog size collector."},
			{Name: "heartbeat", Prefixes: []string{"mysql_heartbeat_"}, Message: "Optional heartbeat table timestamps and replication lag collector."},
			{Name: "engine_innodb_status", Prefixes: []string{"mysql_engine_innodb_"}, Message: "Optional SHOW ENGINE INNODB STATUS parser for queue/read-view internals."},
			{Name: "engine_tokudb_status", Prefixes: []string{"mysql_engine_tokudb_"}, Message: "Optional SHOW ENGINE TOKUDB STATUS metric family."},
			{Name: "mysql_user", Prefixes: []string{"mysql_mysql_"}, Message: "Optional mysql.user account limit and privilege metrics."},
			{Name: "galera", Prefixes: []string{"mysql_galera_", "mysql_galera_evs_repl_latency_"}, Message: "PXC/Galera status, variable, gcache, and EVS replication latency metrics."},
			{Name: "slave_status", Prefixes: []string{"mysql_slave_status_"}, Message: "Optional SHOW SLAVE STATUS / replication status collector."},
			{Name: "slave_hosts", Any: []string{"mysql_heartbeat_mysql_slave_hosts_info"}, Message: "Optional SHOW SLAVE HOSTS collector."},
			{Name: "innodb_compression", Prefixes: []string{"mysql_info_schema_innodb_cmp", "mysql_info_schema_innodb_cmpmem"}, Message: "Optional InnoDB compression collector metrics."},
			{Name: "auto_increment_columns", Prefixes: []string{"mysql_info_schema_auto_increment_column"}, Message: "Optional information_schema auto-increment exhaustion metrics."},
			{Name: "info_schema_processlist", Prefixes: []string{"mysql_info_schema_processlist_"}, Message: "Optional processlist thread, time, user, and host metrics."},
			{Name: "info_schema_clientstats", Prefixes: []string{"mysql_info_schema_client_statistics_"}, Message: "Optional client statistics metrics for connection, row, byte, SSL, and command activity."},
			{Name: "info_schema_userstats", Prefixes: []string{"mysql_info_schema_user_statistics_"}, Message: "Optional user statistics metrics for connection, row, byte, and command activity."},
			{Name: "info_schema_tablestats", Prefixes: []string{"mysql_info_schema_table_statistics_"}, Message: "Optional table statistics metrics for rows read/changed and changed-with-indexes."},
			{Name: "info_schema_schemastats", Prefixes: []string{"mysql_info_schema_schema_statistics_"}, Message: "Optional schema statistics metrics for rows read/changed and changed-with-indexes."},
			{Name: "info_schema_innodb_metrics", Prefixes: []string{"mysql_info_schema_innodb_metrics_"}, Message: "Optional INFORMATION_SCHEMA.INNODB_METRICS collector."},
			{Name: "info_schema_innodb_tablespaces", Prefixes: []string{"mysql_info_schema_innodb_tablespace_"}, Message: "Optional InnoDB tablespace file and allocation metrics."},
			{Name: "info_schema_query_response_time", Prefixes: []string{"mysql_info_schema_query_response_time_"}, Message: "Optional query response time histogram collector."},
			{Name: "info_schema_replica_host", Prefixes: []string{"mysql_info_schema_replica_host_"}, Message: "Optional replica host CPU/lag/latency/log stream metrics."},
			{Name: "rocksdb_perf_context", Prefixes: []string{"mysql_info_schema_rocksdb_perf_context_"}, Message: "Optional RocksDB performance context metrics."},
			{Name: "object_inventory", Any: []string{"mysql_info_schema_table_size_data_length", "mysql_info_schema_table_size_index_length"}, Prefixes: []string{"mysql_info_schema_table_size_", "mysql_info_schema_schema_statistics_"}, Message: "Schema and table counts/sizes from optional information_schema collectors. Missing means schema/table counts cannot be answered from metrics."},
			{Name: "schema_table_inventory", Any: []string{"mysql_info_schema_table_size_data_length", "mysql_info_schema_table_size_index_length"}, Prefixes: []string{"mysql_info_schema_table_size_"}, Message: "Optional high-cardinality schema/table inventory; not enabled in the default local exporter."},
			{Name: "perf_schema_events_statements", Prefixes: []string{"mysql_perf_schema_events_statements_"}, Message: "Optional performance_schema statement counters, latency, errors, rows, temp tables, and no-index usage."},
			{Name: "perf_schema_events_statements_sum", Prefixes: []string{"mysql_perf_schema_events_statements_sum_"}, Message: "Optional performance_schema statement summary metrics."},
			{Name: "perf_schema_events_waits", Prefixes: []string{"mysql_perf_schema_events_waits_"}, Message: "Optional performance_schema global wait event counters and timings."},
			{Name: "perf_schema_file_events", Prefixes: []string{"mysql_perf_schema_file_events_"}, Message: "Optional performance_schema file event count, time, and byte metrics."},
			{Name: "perf_schema_file_instances", Prefixes: []string{"mysql_perf_schema_file_instances_"}, Message: "Optional performance_schema file instance inventory metrics."},
			{Name: "perf_schema_index_io_waits", Prefixes: []string{"mysql_perf_schema_index_io_waits_"}, Message: "Optional performance_schema table/index IO wait count and timing metrics."},
			{Name: "perf_schema_memory_events", Prefixes: []string{"mysql_perf_schema_memory_"}, Message: "Optional performance_schema memory event metrics."},
			{Name: "perf_schema_table_io_waits", Prefixes: []string{"mysql_perf_schema_table_io_waits_"}, Message: "Optional performance_schema table IO wait count and timing metrics."},
			{Name: "perf_schema_table_locks", Any: []string{"mysql_perf_schema_sql_lock_waits_total", "mysql_perf_schema_external_lock_waits_total", "mysql_perf_schema_sql_lock_waits_seconds_total", "mysql_perf_schema_external_lock_waits_seconds_total"}, Message: "Optional performance_schema table lock wait count and timing metrics."},
			{Name: "perf_schema_replication_group", Prefixes: []string{"mysql_perf_schema_replication_group_members_", "mysql_perf_schema_replication_group_member_stats_"}, Message: "Optional Group Replication member and member-stat metrics."},
			{Name: "perf_schema_replication_applier", Prefixes: []string{"mysql_perf_schema_last_applied_transaction_", "mysql_perf_schema_applying_transaction_"}, Message: "Optional replication applier worker status metrics."},
			{Name: "sys_user_summary", Prefixes: []string{"mysql_sys_"}, Message: "Optional sys.user_summary metrics."},
		}
	case "postgresql":
		return []databaseCapabilitySpec{
			{Name: "liveness", All: []string{"pg_up"}, Message: "Exporter connectivity and scrape health."},
			{Name: "exporter_health", Any: []string{"pg_exporter_last_scrape_error", "pg_exporter_last_scrape_duration_seconds", "pg_scrape_collector_success", "postgres_exporter_build_info"}, Message: "postgres_exporter scrape errors, durations, collector success, and build metadata."},
			{Name: "database_collector", Any: []string{"pg_database_size_bytes", "pg_database_connection_limit"}, Prefixes: []string{"pg_database_"}, Message: "pg_database collector for database size and per-database connection limits."},
			{Name: "stat_database_collector", Prefixes: []string{"pg_stat_database_"}, Message: "pg_stat_database collector for transactions, tuples, cache, conflicts, temp files, deadlocks, IO timing, and active time."},
			{Name: "settings", Prefixes: []string{"pg_settings_"}, Message: "Server settings metrics exported from pg_settings/default metrics."},
			{Name: "connections", All: []string{"pg_stat_activity_count", "pg_settings_max_connections"}, Message: "Current connection count, max connections, connection pressure."},
			{Name: "activity_states", Any: []string{"pg_stat_activity_count", "pg_stat_activity_max_tx_duration"}, Message: "Backend states and longest transaction duration."},
			{Name: "connection_limits", Any: []string{"pg_database_connection_limit", "pg_roles_connection_limit", "pg_settings_reserved_connections", "pg_settings_superuser_reserved_connections"}, Message: "Database/role/server connection limits."},
			{Name: "roles", Prefixes: []string{"pg_roles_"}, Message: "pg_roles collector for role-level connection limits."},
			{Name: "transactions", Any: []string{"pg_stat_database_xact_commit", "pg_stat_database_xact_rollback"}, Message: "Commit/rollback rates and rollback ratio."},
			{Name: "tuple_io", Any: []string{"pg_stat_database_tup_inserted", "pg_stat_database_tup_updated", "pg_stat_database_tup_deleted", "pg_stat_database_tup_returned", "pg_stat_database_tup_fetched"}, Message: "Tuple read/write activity from pg_stat_database."},
			{Name: "cache_hit", All: []string{"pg_stat_database_blks_hit", "pg_stat_database_blks_read"}, Message: "Database block cache hit ratio."},
			{Name: "io_timing", Any: []string{"pg_stat_database_blk_read_time", "pg_stat_database_blk_write_time", "pg_settings_track_io_timing"}, Message: "Block read/write timing when track_io_timing is enabled."},
			{Name: "deadlocks_conflicts", Any: []string{"pg_stat_database_deadlocks", "pg_stat_database_conflicts"}, Message: "Deadlocks and recovery/query conflicts."},
			{Name: "locks", All: []string{"pg_locks_count"}, Message: "Lock counts by mode/type."},
			{Name: "temp_storage", Any: []string{"pg_stat_database_temp_bytes", "pg_stat_database_temp_files"}, Message: "Temporary file/byte growth."},
			{Name: "object_inventory", Any: []string{"pg_database_size_bytes", "pg_stat_user_tables_n_live_tup", "pg_stat_user_tables_n_dead_tup"}, Prefixes: []string{"pg_stat_user_tables_"}, Message: "Database and user-table counts from pg_database and pg_stat_user_tables collectors. Missing means database/table counts cannot be answered from metrics."},
			{Name: "database_size", All: []string{"pg_database_size_bytes"}, Message: "Database size by database label."},
			{Name: "bgwriter_checkpoints", Any: []string{"pg_stat_bgwriter_checkpoints_timed_total", "pg_stat_bgwriter_checkpoints_req_total", "pg_stat_bgwriter_buffers_checkpoint_total", "pg_stat_bgwriter_buffers_backend_fsync_total"}, Message: "Checkpoint frequency, checkpoint buffers, and backend fsync pressure."},
			{Name: "archiver", Any: []string{"pg_stat_archiver_archived_count", "pg_stat_archiver_failed_count"}, Message: "WAL archiver success and failure counters."},
			{Name: "replication", Any: []string{"pg_replication_lag_seconds", "pg_replication_is_replica", "pg_replication_last_replay_seconds"}, Message: "Replica status and lag when the exporter exposes replication collectors."},
			{Name: "replication_slot", Prefixes: []string{"pg_replication_slot_"}, Message: "Replication slot active status, LSNs, safe WAL size, and WAL availability status."},
			{Name: "ssl", Any: []string{"pg_settings_ssl", "pg_settings_ssl_prefer_server_ciphers", "pg_settings_ssl_passphrase_command_supports_reload"}, Message: "PostgreSQL SSL-related server settings."},
			{Name: "autovacuum_settings", Any: []string{"pg_settings_autovacuum", "pg_settings_autovacuum_max_workers", "pg_settings_autovacuum_vacuum_scale_factor", "pg_settings_autovacuum_analyze_scale_factor"}, Message: "Autovacuum configuration coverage."},
			{Name: "wal_settings", Any: []string{"pg_settings_wal_buffers_bytes", "pg_settings_wal_keep_size_bytes", "pg_settings_max_wal_size_bytes", "pg_settings_max_wal_senders", "pg_settings_max_replication_slots"}, Prefixes: []string{"pg_settings_wal_"}, Message: "WAL size, sender, slot, and retention-related settings."},
			{Name: "logging_slow_query_settings", Any: []string{"pg_settings_log_min_duration_statement_seconds", "pg_settings_log_lock_waits", "pg_settings_log_temp_files_bytes", "pg_settings_deadlock_timeout_seconds"}, Message: "Settings that determine whether slow SQL, lock waits, and temp files are logged."},
			{Name: "database_wraparound", Prefixes: []string{"pg_database_wraparound_"}, Message: "Optional database wraparound age metrics for xid/mxid risk."},
			{Name: "long_running_transactions", Any: []string{"pg_long_running_transactions", "pg_long_running_transactions_oldest_timestamp_seconds"}, Prefixes: []string{"pg_long_running_transactions_"}, Message: "Optional long-running transaction count and oldest transaction age."},
			{Name: "postmaster", Prefixes: []string{"pg_postmaster_"}, Message: "Optional postmaster start time collector."},
			{Name: "process_idle", Prefixes: []string{"pg_process_idle_"}, Message: "Optional idle backend duration metrics from pg_stat_activity."},
			{Name: "stat_activity_autovacuum", Prefixes: []string{"pg_stat_activity_autovacuum_"}, Message: "Optional active autovacuum age metrics."},
			{Name: "stat_checkpointer", Prefixes: []string{"pg_stat_checkpointer_"}, Message: "Optional PostgreSQL 17+ checkpointer counters and timing metrics."},
			{Name: "stat_progress_vacuum", Prefixes: []string{"pg_stat_progress_vacuum_"}, Message: "Vacuum progress phase, heap block, and dead tuple metrics."},
			{Name: "stat_statements", Prefixes: []string{"pg_stat_statements_"}, Message: "Optional pg_stat_statements query calls, time, rows, block IO time, and query id metrics."},
			{Name: "stat_user_tables", Prefixes: []string{"pg_stat_user_tables_"}, Message: "User table scan, tuple, vacuum/analyze, live/dead tuple, and table/index size metrics."},
			{Name: "stat_wal_receiver", Prefixes: []string{"pg_stat_wal_receiver_"}, Message: "Optional WAL receiver LSN, message time, and upstream node metrics."},
			{Name: "statio_user_indexes", Prefixes: []string{"pg_statio_user_indexes_"}, Message: "Optional user index IO hit/read counters."},
			{Name: "statio_user_tables", Prefixes: []string{"pg_statio_user_tables_"}, Message: "User table heap/index/toast block hit/read metrics."},
			{Name: "wal_directory", Prefixes: []string{"pg_wal_"}, Message: "WAL directory segment count and byte size metrics."},
			{Name: "xlog_location", Prefixes: []string{"pg_xlog_location_"}, Message: "Optional legacy xlog location byte position metrics."},
			{Name: "buffercache_summary", Prefixes: []string{"pg_buffercache_summary_"}, Message: "Optional pg_buffercache summary buffers used/unused/dirty/pinned and usage count."},
		}
	case "redis":
		return []databaseCapabilitySpec{
			{Name: "liveness", All: []string{"redis_up"}, Message: "Exporter connectivity and scrape health."},
			{Name: "exporter_health", Any: []string{"redis_exporter_last_scrape_error", "redis_exporter_last_scrape_duration_seconds", "redis_exporter_scrapes_total", "redis_exporter_build_info"}, Message: "redis_exporter scrape errors, duration, count, and build metadata."},
			{Name: "instance_info", Any: []string{"redis_instance_info", "redis_start_time_seconds", "redis_uptime_in_seconds"}, Message: "Redis instance metadata, start time, and uptime metrics."},
			{Name: "clients", Any: []string{"redis_connected_clients", "redis_config_maxclients", "redis_blocked_clients", "redis_rejected_connections_total"}, Message: "Connected clients, maxclients pressure, blocked/rejected clients."},
			{Name: "client_list", Prefixes: []string{"redis_connected_client_"}, Message: "Optional CLIENT LIST per-client info, memory, buffer, subscription, idle, and watch metrics."},
			{Name: "client_buffers", Any: []string{"redis_client_recent_max_input_buffer_bytes", "redis_client_recent_max_output_buffer_bytes", "redis_client_output_buffer_limit_disconnections_total", "redis_client_query_buffer_limit_disconnections_total"}, Message: "Client buffer pressure and buffer limit disconnects."},
			{Name: "memory", Any: []string{"redis_memory_used_bytes", "redis_memory_max_bytes", "redis_mem_fragmentation_ratio"}, Message: "Memory usage, maxmemory pressure, fragmentation ratio."},
			{Name: "allocator_fragmentation", Any: []string{"redis_allocator_frag_ratio", "redis_allocator_rss_ratio", "redis_allocator_allocated_bytes", "redis_allocator_resident_bytes"}, Message: "Allocator-level fragmentation and RSS overhead."},
			{Name: "throughput", Any: []string{"redis_commands_processed_total", "redis_commands_total"}, Message: "Command processing rate from INFO stats or commandstats."},
			{Name: "command_stats", Any: []string{"redis_commands_total", "redis_commands_duration_seconds_total"}, Prefixes: []string{"redis_commands_"}, Message: "Per-command calls, duration, failed calls, rejected calls, and latency histograms."},
			{Name: "command_latency", Any: []string{"redis_commands_duration_seconds_total", "redis_commands_latencies_usec", "redis_latency_percentiles_usec"}, Message: "Command duration and latency percentile metrics."},
			{Name: "command_errors", Any: []string{"redis_commands_failed_calls_total", "redis_commands_rejected_calls_total", "redis_total_error_replies", "redis_unexpected_error_replies"}, Message: "Command failures, rejections, and error replies."},
			{Name: "network_io", Any: []string{"redis_net_input_bytes_total", "redis_net_output_bytes_total", "redis_total_reads_processed", "redis_total_writes_processed"}, Message: "Network bytes and read/write event counters."},
			{Name: "cache_hit", Any: []string{"redis_keyspace_hits_total", "redis_keyspace_misses_total"}, Message: "Keyspace hit/miss rates and hit ratio."},
			{Name: "object_inventory", Any: []string{"redis_db_keys", "redis_db_keys_expiring", "redis_keys_count"}, Message: "Logical DB count and key counts from INFO keyspace or optional key counting collectors. Missing means DB/key counts cannot be answered from metrics."},
			{Name: "keyspace", Any: []string{"redis_db_keys", "redis_db_keys_expiring"}, Message: "Key counts and expiring key counts by DB label."},
			{Name: "key_value_metrics", Any: []string{"redis_key_size", "redis_key_memory_usage_bytes", "redis_key_value", "redis_key_value_as_string", "redis_keys_count"}, Prefixes: []string{"redis_key_", "redis_keys_"}, Message: "Optional check-keys/check-single-keys/count-keys metrics for key size, value, memory usage, and key counts."},
			{Name: "key_groups", Any: []string{"redis_number_of_distinct_key_groups", "redis_last_key_groups_scrape_duration_milliseconds"}, Prefixes: []string{"redis_key_group_"}, Message: "Optional key group memory/size metrics from check-key-groups."},
			{Name: "evictions_expiry", Any: []string{"redis_evicted_keys_total", "redis_expired_keys_total"}, Message: "Evicted/expired key growth."},
			{Name: "persistence_rdb", Any: []string{"redis_rdb_changes_since_last_save", "redis_rdb_bgsave_in_progress", "redis_rdb_last_bgsave_status", "redis_rdb_last_save_timestamp_seconds", "redis_latest_fork_seconds"}, Message: "RDB persistence state, bgsave status, and fork duration."},
			{Name: "rdb_file_size", Any: []string{"redis_rdb_current_size_bytes"}, Message: "Optional local RDB file size metric when exporter can read the RDB file."},
			{Name: "persistence_aof", Any: []string{"redis_aof_enabled", "redis_aof_rewrite_in_progress", "redis_aof_last_bgrewrite_status", "redis_aof_last_write_status", "redis_aof_last_cow_size_bytes"}, Message: "AOF state, rewrite status, write status, and COW size."},
			{Name: "replication", Any: []string{"redis_connected_slaves", "redis_master_repl_offset", "redis_repl_backlog_is_active"}, Message: "Replica count and replication offsets/backlog state."},
			{Name: "replication_backlog", Any: []string{"redis_replication_backlog_bytes", "redis_repl_backlog_history_bytes", "redis_repl_backlog_first_byte_offset", "redis_replica_partial_resync_accepted", "redis_replica_partial_resync_denied"}, Message: "Replication backlog size/history and partial resync outcomes."},
			{Name: "slowlog_blocking", Any: []string{"redis_slowlog_length", "redis_last_slow_execution_duration_seconds", "redis_blocked_clients"}, Message: "Slowlog length, last slow execution, blocked clients."},
			{Name: "latency_latest", Prefixes: []string{"redis_latency_spike_"}, Message: "Optional LATENCY LATEST spike timestamp and duration metrics."},
			{Name: "latency_histogram", Any: []string{"redis_commands_latencies_usec"}, Message: "Optional LATENCY HISTOGRAM per-command latency buckets."},
			{Name: "pubsub", Any: []string{"redis_pubsub_clients", "redis_pubsub_channels", "redis_pubsub_patterns", "redis_pubsubshard_channels"}, Message: "Pub/sub clients and channel counts."},
			{Name: "cluster", Any: []string{"redis_cluster_enabled", "redis_cluster_connections"}, Message: "Redis Cluster mode and cluster connection state."},
			{Name: "cluster_nodes", Prefixes: []string{"redis_cluster_node_"}, Message: "Cluster node role/state/slot/link metrics when cluster collection is enabled."},
			{Name: "streams", Prefixes: []string{"redis_stream_"}, Message: "Optional stream, consumer group, pending, lag, and consumer idle metrics."},
			{Name: "lua_scripts", Any: []string{"redis_script_result", "redis_script_values"}, Message: "Optional custom Lua script result/value metrics."},
			{Name: "functions_scripts_modules", Any: []string{"redis_number_of_functions", "redis_number_of_libraries", "redis_number_of_cached_scripts", "redis_module_fork_in_progress"}, Message: "Redis functions, libraries, Lua scripts, and module fork state."},
			{Name: "module_info", Any: []string{"redis_module_info"}, Prefixes: []string{"redis_module_"}, Message: "Optional Redis Modules metrics from INFO MODULES."},
			{Name: "search_indexes", Prefixes: []string{"redis_search_index_"}, Message: "Optional RedisSearch index document, memory, indexing, and usage metrics."},
			{Name: "config_metrics", Any: []string{"redis_config_key_value", "redis_config_value", "redis_config_client_output_buffer_limit_bytes"}, Prefixes: []string{"redis_config_"}, Message: "Optional CONFIG metrics, including output buffer limits and selected numeric config values."},
			{Name: "system_metrics", Any: []string{"redis_total_system_memory_bytes", "redis_os", "redis_arch_bits", "redis_multiplexing_api"}, Message: "Optional system/host info emitted from Redis INFO when include-system-metrics is enabled."},
			{Name: "sentinel", Prefixes: []string{"redis_sentinel_"}, Message: "Sentinel master status, quorum, peer info, config, and ok-sentinel/ok-slave metrics."},
			{Name: "tile38", Prefixes: []string{"redis_tile38_"}, Message: "Optional Tile38 INFO metrics when the exporter runs in Tile38 mode."},
			{Name: "acl_security", Any: []string{"redis_acl_access_denied_auth_total", "redis_acl_access_denied_cmd_total", "redis_acl_access_denied_key_total", "redis_acl_access_denied_channel_total"}, Message: "ACL denied auth/command/key/channel counters."},
			{Name: "eventloop", Any: []string{"redis_eventloop_cycles_total", "redis_eventloop_duration_sum_usec_total", "redis_instantaneous_eventloop_duration_usec", "redis_instantaneous_eventloop_cycles_per_sec"}, Message: "Redis event loop activity and latency."},
			{Name: "tracking_watch", Any: []string{"redis_tracking_clients", "redis_tracking_total_keys", "redis_tracking_total_items", "redis_watching_clients", "redis_total_watched_keys"}, Message: "Client tracking and WATCH usage."},
		}
	case "mongodb":
		return []databaseCapabilitySpec{
			{Name: "liveness", All: []string{"mongodb_up"}, Message: "Exporter connectivity and scrape health."},
			{Name: "exporter_health", Any: []string{"collector_scrape_time_ms", "mongodb_up", "mongodb_exporter_up"}, Message: "MongoDB exporter scrape duration and up state."},
			{Name: "general_info", Any: []string{"mongodb_version_info", "mongodb_mongod_storage_engine"}, Message: "MongoDB version, edition/vendor, and storage engine metadata."},
			{Name: "feature_compatibility", Any: []string{"mongodb_fcv_feature_compatibility_version"}, Message: "Feature Compatibility Version collector output."},
			{Name: "diagnosticdata", Prefixes: []string{"mongodb_ss_", "mongodb_sys_", "mongodb_oplog_stats_", "mongodb_mongod_"}, Message: "getDiagnosticData-derived metrics, including serverStatus, systemMetrics, oplog stats, and mongod metadata."},
			{Name: "server_status", Prefixes: []string{"mongodb_ss_"}, Message: "serverStatus metric family generated from diagnosticData."},
			{Name: "connections", Any: []string{"mongodb_ss_connections", "mongodb_connections"}, Message: "Current/available connections when serverStatus connection metrics are exposed."},
			{Name: "connection_rate_limits", Any: []string{"mongodb_ss_connections_establishmentRateLimit_rejected", "mongodb_ss_connections_establishmentRateLimit_interruptedDueToClientDisconnect", "mongodb_ss_connections_establishmentRateLimit_exempted"}, Message: "MongoDB connection establishment rate limiting counters."},
			{Name: "operations", Any: []string{"mongodb_ss_opcounters", "mongodb_op_counters_total"}, Message: "Operation counters and operation rate when serverStatus opcounters are exposed."},
			{Name: "command_counters", Any: []string{"mongodb_ss_metrics_commands", "mongodb_ss_metrics_commands_find_total", "mongodb_ss_metrics_commands_insert_total", "mongodb_ss_metrics_commands_update_total", "mongodb_ss_metrics_commands_delete_total"}, Prefixes: []string{"mongodb_ss_metrics_commands_"}, Message: "Per-command total counters from serverStatus metrics.commands."},
			{Name: "command_failures", Any: []string{"mongodb_ss_metrics_commands_find_failed", "mongodb_ss_metrics_commands_insert_failed", "mongodb_ss_metrics_commands_update_failed", "mongodb_ss_metrics_commands_delete_failed"}, Prefixes: []string{"mongodb_ss_metrics_commands_"}, Message: "Per-command failed counters from serverStatus metrics.commands."},
			{Name: "op_latency", Any: []string{"mongodb_ss_opLatencies_latency", "mongodb_ss_opLatencies_ops"}, Message: "Operation latency and operation count metrics by operation type."},
			{Name: "query_planner", Any: []string{"mongodb_ss_metrics_queryExecutor_collectionScans_total", "mongodb_ss_metrics_queryExecutor_scanned", "mongodb_ss_metrics_queryExecutor_scannedObjects"}, Message: "Collection scan and scanned document/key counters."},
			{Name: "query_spills", Any: []string{"mongodb_ss_metrics_query_sort_spillToDisk", "mongodb_ss_metrics_query_sort_spillToDiskBytes", "mongodb_ss_metrics_query_group_spills", "mongodb_ss_metrics_query_lookup_hashLookupSpillToDisk"}, Prefixes: []string{"mongodb_ss_metrics_query_"}, Message: "Sort/group/lookup disk spill counters and bytes."},
			{Name: "cursors", Any: []string{"mongodb_ss_metrics_cursor_open", "mongodb_ss_metrics_cursor_timedOut", "mongodb_ss_metrics_cursor_totalOpened"}, Prefixes: []string{"mongodb_ss_metrics_cursor_"}, Message: "Open, timed out, and opened cursor counters."},
			{Name: "document_activity", Any: []string{"mongodb_ss_metrics_document", "mongodb_ss_metrics_ttl_deletedDocuments", "mongodb_ss_metrics_ttl_passes"}, Message: "Document activity and TTL delete/pass counters."},
			{Name: "errors_asserts", Any: []string{"mongodb_ss_asserts", "mongodb_asserts_total"}, Message: "MongoDB assert/error growth when serverStatus asserts are exposed."},
			{Name: "memory", Any: []string{"mongodb_ss_mem_resident", "mongodb_mongod_mem_resident_megabytes"}, Message: "Resident/virtual memory when memory collectors are exposed."},
			{Name: "tcmalloc", Any: []string{"mongodb_ss_tcmalloc_generic_current_allocated_bytes", "mongodb_ss_tcmalloc_generic_heap_size", "mongodb_ss_tcmalloc_tcmalloc_total_free_bytes"}, Prefixes: []string{"mongodb_ss_tcmalloc_"}, Message: "MongoDB tcmalloc allocation and heap metrics."},
			{Name: "page_faults", Any: []string{"mongodb_ss_extra_info_page_faults", "mongodb_ss_extra_info_page_faults_total"}, Message: "Page fault counters when diagnosticData extra_info metrics are exposed."},
			{Name: "storage_engine", Any: []string{"mongodb_ss_storageEngine_persistent", "mongodb_ss_storageEngine_readOnly", "mongodb_ss_storageEngine_backupCursorOpen"}, Message: "MongoDB storage engine state."},
			{Name: "wiredtiger_cache", Any: []string{"mongodb_ss_wt_cache_bytes_currently_in_the_cache", "mongodb_ss_wt_cache_maximum_bytes_configured", "mongodb_ss_wt_cache_tracked_dirty_bytes_in_the_cache", "mongodb_ss_wt_cache_operations_timed_out_waiting_for_space_in_cache"}, Prefixes: []string{"mongodb_ss_wt_cache_"}, Message: "WiredTiger cache size, dirty bytes, and cache pressure metrics."},
			{Name: "wiredtiger_io", Any: []string{"mongodb_ss_wt_block_manager_bytes_read", "mongodb_ss_wt_block_manager_bytes_written", "mongodb_ss_wt_cache_bytes_read_into_cache", "mongodb_ss_wt_cache_bytes_written_from_cache"}, Prefixes: []string{"mongodb_ss_wt_block_manager_"}, Message: "WiredTiger block/cache read and write bytes."},
			{Name: "transactions", Any: []string{"mongodb_ss_transactions_currentActive", "mongodb_ss_transactions_currentOpen", "mongodb_ss_transactions_totalAborted", "mongodb_ss_transactions_totalCommitted"}, Message: "Transaction current state and total outcomes."},
			{Name: "global_locks", Any: []string{"mongodb_ss_globalLock_currentQueue", "mongodb_ss_globalLock_activeClients_total", "mongodb_ss_globalLock_activeClients_readers", "mongodb_ss_globalLock_activeClients_writers"}, Message: "Global lock queue and active clients."},
			{Name: "lock_acquisition", Any: []string{"mongodb_ss_locks_acquireCount", "mongodb_ss_locks_Global_acquireCount_w", "mongodb_ss_locks_Database_acquireCount_W", "mongodb_ss_locks_Collection_acquireCount_w"}, Prefixes: []string{"mongodb_ss_locks_"}, Message: "MongoDB lock acquire counters by scope and mode."},
			{Name: "flow_control", Any: []string{"mongodb_ss_flowControl_enabled", "mongodb_ss_flowControl_isLagged", "mongodb_ss_flowControl_timeAcquiringMicros", "mongodb_ss_flowControl_targetRateLimit"}, Message: "Replica-set flow control state and throttling metrics."},
			{Name: "logical_sessions", Any: []string{"mongodb_ss_logicalSessionRecordCache_activeSessionsCount", "mongodb_ss_logicalSessionRecordCache_sessionCatalogSize", "mongodb_ss_logicalSessionRecordCache_sessionsCollectionJobCount"}, Message: "Logical session cache and cleanup job state."},
			{Name: "replication", Any: []string{"mongodb_mongod_replset_member_replication_lag", "mongodb_replset_member_replication_lag", "mongodb_ss_metrics_repl_network_bytes", "mongodb_ss_metrics_repl_apply_ops"}, Prefixes: []string{"mongodb_mongod_replset_", "mongodb_replset_", "mongodb_ss_metrics_repl_"}, Message: "Replica lag and replication activity when replSet collectors or serverStatus repl metrics are exposed."},
			{Name: "replset_status", Prefixes: []string{"mongodb_rs_"}, Message: "replSetGetStatus collector metrics for member state, optimes, elections, heartbeats, and sync source state."},
			{Name: "replset_config", Prefixes: []string{"mongodb_rs_cfg_"}, Message: "Replica set config metrics when collect-all/replset config collection is enabled."},
			{Name: "oplog_stats", Prefixes: []string{"mongodb_oplog_stats_"}, Message: "Oplog storage and WiredTiger metrics from diagnosticData local.oplog.rs.stats."},
			{Name: "network", Any: []string{"mongodb_ss_network_bytesIn", "mongodb_ss_network_bytesOut", "mongodb_ss_network_numRequests", "mongodb_network_bytes_total"}, Message: "MongoDB network bytes and request counters."},
			{Name: "transport_security", Any: []string{"mongodb_ss_transportSecurity_1_0", "mongodb_ss_transportSecurity_1_1", "mongodb_ss_transportSecurity_1_2", "mongodb_ss_transportSecurity_1_3", "mongodb_transportLayerStats_connsDiscardedDueToClientDisconnect"}, Prefixes: []string{"mongodb_ss_transportSecurity_", "mongodb_transportLayerStats_"}, Message: "TLS protocol counters and transport layer stats."},
			{Name: "system_cpu", Any: []string{"mongodb_sys_cpu_user_ms", "mongodb_sys_cpu_system_ms", "mongodb_sys_cpu_iowait_ms", "mongodb_sys_cpu_num_logical_cores"}, Prefixes: []string{"mongodb_sys_cpu_"}, Message: "Host CPU metrics emitted by MongoDB diagnosticData."},
			{Name: "system_memory", Any: []string{"mongodb_sys_memory_MemTotal_kb", "mongodb_sys_memory_MemAvailable_kb", "mongodb_sys_memory_Dirty_kb"}, Prefixes: []string{"mongodb_sys_memory_"}, Message: "Host memory metrics emitted by MongoDB diagnosticData."},
			{Name: "system_disk", Any: []string{"mongodb_sys_mounts_capacity", "mongodb_sys_mounts_available", "mongodb_sys_disks_vda_io_time_ms", "mongodb_sys_disks_vda_read_sectors", "mongodb_sys_disks_vda_write_sectors"}, Prefixes: []string{"mongodb_sys_mounts_", "mongodb_sys_disks_"}, Message: "Host disk/mount metrics emitted by MongoDB diagnosticData."},
			{Name: "object_inventory", Any: []string{"mongodb_dbstats_dataSize", "mongodb_dbstats_objects", "mongodb_collstats_storageStats_count"}, Prefixes: []string{"mongodb_dbstats_", "mongodb_collstats_", "mongodb_mongod_collstats_", "mongodb_mongos_collstats_"}, Message: "Database, collection, and object counts from dbStats/collStats collectors. Missing means database/collection counts cannot be answered from metrics."},
			{Name: "dbstats", Any: []string{"mongodb_dbstats_dataSize", "mongodb_dbstats_storageSize", "mongodb_dbstats_indexSize", "mongodb_dbstats_objects"}, Prefixes: []string{"mongodb_dbstats_"}, Message: "Optional dbStats collector metrics by database."},
			{Name: "dbstats_free_storage", Any: []string{"mongodb_dbstats_freeStorageSize", "mongodb_dbstats_totalFreeStorageSize", "mongodb_dbstats_indexFreeStorageSize"}, Prefixes: []string{"mongodb_dbstats_free", "mongodb_dbstats_totalFree", "mongodb_dbstats_indexFree"}, Message: "Optional dbStats free-storage metrics when dbstatsfreestorage is enabled."},
			{Name: "topmetrics", Any: []string{"mongodb_top_total_count", "mongodb_top_queries_count", "mongodb_top_insert_count", "mongodb_top_update_count", "mongodb_top_remove_count"}, Prefixes: []string{"mongodb_top_"}, Message: "Optional top collector per-namespace operation counters and time."},
			{Name: "current_ops", Any: []string{"mongodb_currentop_fsync_lock_state", "mongodb_currentop_query_uptime"}, Prefixes: []string{"mongodb_currentop_"}, Message: "Optional currentOp collector state and slow operation uptime."},
			{Name: "indexstats", Any: []string{"mongodb_indexstats_accesses_ops", "mongodb_mongod_db_index_size_bytes", "mongodb_mongos_db_index_size_bytes"}, Prefixes: []string{"mongodb_indexstats_", "mongodb_mongod_db_index_", "mongodb_mongos_db_index_"}, Message: "Optional indexStats/index size metrics; collection filters or discovery mode may be required."},
			{Name: "collstats", Any: []string{"mongodb_collstats_storageStats_count", "mongodb_collstats_storageStats_size", "mongodb_collstats_storageStats_storageSize", "mongodb_collstats_storageStats_totalIndexSize"}, Prefixes: []string{"mongodb_collstats_", "mongodb_mongod_collstats_", "mongodb_mongos_collstats_"}, Message: "Optional collStats collector metrics; collection filters or discovery mode may be required."},
			{Name: "profile", Any: []string{"mongodb_profile_slow_query_count"}, Message: "Optional profile collector slow query counts."},
			{Name: "sharding", Any: []string{"mongodb_shards_collection_chunks_count", "mongodb_shards_collection_chunks_is_balanced", "mongodb_shards_collection_chunks_total"}, Prefixes: []string{"mongodb_shards_", "mongodb_mongos_sharding_"}, Message: "Optional sharding collector chunk, balance, and mongos sharding metrics."},
			{Name: "backup_pbm", Any: []string{"mongodb_pbm_cluster_backup_configured", "mongodb_pbm_cluster_pitr_backup_enabled", "mongodb_pbm_backup_last_transition_ts"}, Prefixes: []string{"mongodb_pbm_"}, Message: "Optional Percona Backup for MongoDB collector metrics."},
			{Name: "collect_all", Any: []string{"mongodb_rs_cfg_protocolVersion", "mongodb_collstats_storageStats_count", "mongodb_indexstats_accesses_ops"}, Prefixes: []string{"mongodb_rs_cfg_", "mongodb_collstats_", "mongodb_indexstats_"}, Message: "Collector coverage that appears only when --collect-all or the matching optional collectors are enabled."},
			{Name: "compatible_mode", Any: []string{"mongodb_mongod_wiredtiger_log_bytes_total", "mongodb_mongod_op_counters_total", "mongodb_mongos_op_counters_total", "mongodb_mongod_replset_my_state"}, Prefixes: []string{"mongodb_mongod_", "mongodb_mongos_"}, Message: "Old mongodb_exporter compatible metric names emitted when --compatible-mode is enabled."},
		}
	default:
		return nil
	}
}

func evaluateDatabaseCapability(spec databaseCapabilitySpec, discovered map[string]struct{}) DatabaseCapability {
	presentSet := map[string]struct{}{}
	missing := make([]string, 0, len(spec.All)+len(spec.Any)+len(spec.Prefixes))
	requiredMissing := missingRequired(spec.All, discovered)
	for _, name := range spec.All {
		if _, ok := discovered[name]; ok {
			presentSet[name] = struct{}{}
		} else {
			missing = append(missing, name)
		}
	}
	anyPresent := 0
	for _, name := range spec.Any {
		if _, ok := discovered[name]; ok {
			presentSet[name] = struct{}{}
			anyPresent++
		} else {
			missing = append(missing, name)
		}
	}
	prefixPresent := 0
	for _, prefix := range spec.Prefixes {
		matches := metricNamesWithPrefix(discovered, prefix)
		if len(matches) > 0 {
			for _, name := range matches {
				presentSet[name] = struct{}{}
			}
			prefixPresent++
		} else {
			missing = append(missing, prefix+"*")
		}
	}
	present := sortedMetricNames(presentSet)
	status := "available"
	switch {
	case len(spec.All) > 0 && len(requiredMissing) > 0:
		if len(present) > 0 {
			status = "partial"
		} else {
			status = "unavailable"
		}
	case len(spec.All) == 0 && (len(spec.Any) > 0 || len(spec.Prefixes) > 0) && anyPresent+prefixPresent == 0:
		status = "unavailable"
	}
	if status == "available" {
		missing = nil
	}
	return DatabaseCapability{
		Name:           spec.Name,
		Status:         status,
		MetricCount:    len(present),
		Metrics:        limitStrings(present, maxDatabaseCapabilityMetricNames),
		MissingMetrics: missing,
		Message:        spec.Message,
	}
}

func hasMetricPrefix(discovered map[string]struct{}, prefix string) bool {
	return len(metricNamesWithPrefix(discovered, prefix)) > 0
}

func sortedMetricNames(items map[string]struct{}) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func metricNamesWithPrefix(discovered map[string]struct{}, prefix string) []string {
	matches := []string{}
	for name := range discovered {
		if strings.HasPrefix(name, prefix) {
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)
	return matches
}

func limitStrings(items []string, limit int) []string {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return append([]string(nil), items[:limit]...)
}

func unmappedDatabaseMetricNames(dbType string, discovered map[string]struct{}) []string {
	specs := databaseCapabilitySpecs(dbType)
	out := []string{}
	for name := range discovered {
		mapped := false
		for _, spec := range specs {
			if metricMatchesCapabilitySpec(name, spec) {
				mapped = true
				break
			}
		}
		if !mapped {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func metricMatchesCapabilitySpec(metric string, spec databaseCapabilitySpec) bool {
	for _, name := range spec.All {
		if metric == name {
			return true
		}
	}
	for _, name := range spec.Any {
		if metric == name {
			return true
		}
	}
	for _, prefix := range spec.Prefixes {
		if strings.HasPrefix(metric, prefix) {
			return true
		}
	}
	return false
}

func missingRequired(names []string, discovered map[string]struct{}) []string {
	missing := []string{}
	for _, name := range names {
		if _, ok := discovered[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

func (r databaseStatusRunner) analyzeDatabaseObjectInventory(ctx context.Context, dbType, selector string, discovered map[string]struct{}, row *DatabaseStatusSource) {
	recorded := false
	switch dbType {
	case "mysql":
		if hasAnyMetrics(discovered, []string{"mysql_info_schema_table_size_data_length", "mysql_info_schema_table_size_index_length"}) {
			metric := "mysql_info_schema_table_size_data_length"
			if _, ok := discovered[metric]; !ok {
				metric = "mysql_info_schema_table_size_index_length"
			}
			r.recordScalarQuery(ctx, discovered, row, "schema_count",
				fmt.Sprintf("count(count by (schema) (%s{%s}))", metric, selector),
				[]string{metric})
			r.recordScalarQuery(ctx, discovered, row, "table_count",
				fmt.Sprintf("count(count by (schema, table) (%s{%s}))", metric, selector),
				[]string{metric})
			recorded = true
		}
	case "postgresql":
		if hasRequiredMetrics(discovered, []string{"pg_database_size_bytes"}) {
			r.recordScalarQuery(ctx, discovered, row, "database_count",
				fmt.Sprintf("count(count by (datname) (pg_database_size_bytes{%s}))", selector),
				[]string{"pg_database_size_bytes"})
			recorded = true
		}
		if hasRequiredMetrics(discovered, []string{"pg_stat_user_tables_n_live_tup"}) {
			r.recordScalarQuery(ctx, discovered, row, "table_count",
				fmt.Sprintf("count(count by (schemaname, relname) (pg_stat_user_tables_n_live_tup{%s}))", selector),
				[]string{"pg_stat_user_tables_n_live_tup"})
			recorded = true
		}
	case "redis":
		if hasRequiredMetrics(discovered, []string{"redis_db_keys"}) {
			r.recordScalarQuery(ctx, discovered, row, "logical_database_count",
				fmt.Sprintf("count(count by (db) (redis_db_keys{%s}))", selector),
				[]string{"redis_db_keys"})
			r.recordScalarQuery(ctx, discovered, row, "key_count",
				fmt.Sprintf("sum(redis_db_keys{%s})", selector),
				[]string{"redis_db_keys"})
			recorded = true
		}
	case "mongodb":
		if hasRequiredMetrics(discovered, []string{"mongodb_dbstats_dataSize"}) {
			r.recordScalarQuery(ctx, discovered, row, "database_count",
				fmt.Sprintf("count(count by (database) (mongodb_dbstats_dataSize{%s}))", selector),
				[]string{"mongodb_dbstats_dataSize"})
			recorded = true
		}
		if hasRequiredMetrics(discovered, []string{"mongodb_collstats_storageStats_count"}) {
			r.recordScalarQuery(ctx, discovered, row, "collection_count",
				fmt.Sprintf("count(count by (database, collection) (mongodb_collstats_storageStats_count{%s}))", selector),
				[]string{"mongodb_collstats_storageStats_count"})
			recorded = true
		}
		if hasRequiredMetrics(discovered, []string{"mongodb_dbstats_objects"}) {
			r.recordScalarQuery(ctx, discovered, row, "object_count",
				fmt.Sprintf("sum(mongodb_dbstats_objects{%s})", selector),
				[]string{"mongodb_dbstats_objects"})
			recorded = true
		}
	}
	if !recorded {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "info",
			Code:     "database_object_inventory_missing",
			Title:    "未采集数据库对象清单指标",
			Message:  "当前 exporter 指标不能直接回答库/表/集合/key 数量；查看 capabilities 中 object_inventory 的 missing_metrics。",
		})
	}
}

func (r databaseStatusRunner) analyzeMySQL(ctx context.Context, selector string, discovered map[string]struct{}, row *DatabaseStatusSource) {
	r.checkUp(ctx, "mysql_up", selector, discovered, row)
	r.recordScalarMetric(ctx, discovered, row, "current_connections", "mysql_global_status_threads_connected",
		fmt.Sprintf("max(mysql_global_status_threads_connected{%s})", selector))
	r.recordScalarMetric(ctx, discovered, row, "max_connections", "mysql_global_variables_max_connections",
		fmt.Sprintf("max(mysql_global_variables_max_connections{%s})", selector))
	r.recordScalarMetric(ctx, discovered, row, "threads_running", "mysql_global_status_threads_running",
		fmt.Sprintf("max(mysql_global_status_threads_running{%s})", selector))
	r.recordScalarMetric(ctx, discovered, row, "max_used_connections", "mysql_global_status_max_used_connections",
		fmt.Sprintf("max(mysql_global_status_max_used_connections{%s})", selector))
	r.recordScalarQuery(ctx, discovered, row, "queries_per_second",
		fmt.Sprintf("sum(rate(mysql_global_status_questions{%s}[5m]))", selector),
		[]string{"mysql_global_status_questions"})
	r.recordScalarQuery(ctx, discovered, row, "network_received_bytes_per_second",
		fmt.Sprintf("sum(rate(mysql_global_status_bytes_received{%s}[5m]))", selector),
		[]string{"mysql_global_status_bytes_received"})
	r.recordScalarQuery(ctx, discovered, row, "network_sent_bytes_per_second",
		fmt.Sprintf("sum(rate(mysql_global_status_bytes_sent{%s}[5m]))", selector),
		[]string{"mysql_global_status_bytes_sent"})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "connection_usage_pct",
		Code:      "mysql_connection_pressure",
		Title:     "MySQL 连接使用率偏高",
		Expr:      fmt.Sprintf("100 * max(mysql_global_status_threads_connected{%s}) / clamp_min(max(mysql_global_variables_max_connections{%s}), 1)", selector, selector),
		Required:  []string{"mysql_global_status_threads_connected", "mysql_global_variables_max_connections"},
		WarnAbove: floatPtr(75),
		CritAbove: floatPtr(90),
		Unit:      "%",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "slow_queries_per_second",
		Code:      "mysql_slow_queries",
		Title:     "MySQL 慢查询增长",
		Expr:      fmt.Sprintf("sum(rate(mysql_global_status_slow_queries{%s}[5m]))", selector),
		Required:  []string{"mysql_global_status_slow_queries"},
		WarnAbove: floatPtr(0.1),
		CritAbove: floatPtr(1),
		Unit:      "/s",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "aborted_connects_15m",
		Code:      "mysql_aborted_connects",
		Title:     "MySQL 连接失败增长",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_aborted_connects{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_aborted_connects"},
		WarnAbove: floatPtr(0),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "aborted_clients_15m",
		Code:      "mysql_aborted_clients",
		Title:     "MySQL 客户端异常断开增长",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_aborted_clients{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_aborted_clients"},
		WarnAbove: floatPtr(10),
		CritAbove: floatPtr(100),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "connection_errors_15m",
		Code:      "mysql_connection_errors",
		Title:     "MySQL connection error 增长",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_connection_errors_total{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_connection_errors_total"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(10),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "tmp_disk_tables_15m",
		Code:      "mysql_tmp_disk_tables",
		Title:     "MySQL 磁盘临时表增长偏多",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_created_tmp_disk_tables{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_created_tmp_disk_tables"},
		WarnAbove: floatPtr(100),
		CritAbove: floatPtr(1000),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "innodb_buffer_pool_hit_pct",
		Code:      "mysql_innodb_buffer_pool_hit_low",
		Title:     "MySQL InnoDB buffer pool hit ratio 偏低",
		Expr: fmt.Sprintf(
			"100 * (1 - sum(rate(mysql_global_status_innodb_buffer_pool_reads{%[1]s}[5m])) / clamp_min(sum(rate(mysql_global_status_innodb_buffer_pool_read_requests{%[1]s}[5m])), 1))",
			selector,
		),
		Required:  []string{"mysql_global_status_innodb_buffer_pool_reads", "mysql_global_status_innodb_buffer_pool_read_requests"},
		WarnBelow: floatPtr(95),
		CritBelow: floatPtr(90),
		Unit:      "%",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "innodb_row_lock_current_waits",
		Code:      "mysql_row_lock_current_waits",
		Title:     "MySQL 当前存在 InnoDB 行锁等待",
		Expr:      fmt.Sprintf("max(mysql_global_status_innodb_row_lock_current_waits{%s})", selector),
		Required:  []string{"mysql_global_status_innodb_row_lock_current_waits"},
		WarnAbove: floatPtr(0),
		Unit:      "waits",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "innodb_row_lock_waits_15m",
		Code:      "mysql_row_lock_waits",
		Title:     "MySQL InnoDB 行锁等待增长偏多",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_innodb_row_lock_waits{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_innodb_row_lock_waits"},
		WarnAbove: floatPtr(10),
		CritAbove: floatPtr(100),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "open_files_usage_pct",
		Code:      "mysql_open_files_pressure",
		Title:     "MySQL open files 使用率偏高",
		Expr:      fmt.Sprintf("100 * max(mysql_global_status_open_files{%s}) / clamp_min(max(mysql_global_variables_open_files_limit{%s}), 1)", selector, selector),
		Required:  []string{"mysql_global_status_open_files", "mysql_global_variables_open_files_limit"},
		WarnAbove: floatPtr(75),
		CritAbove: floatPtr(90),
		Unit:      "%",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "opened_tables_15m",
		Code:      "mysql_opened_tables_growth",
		Title:     "MySQL opened tables 增长偏多",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_opened_tables{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_opened_tables"},
		WarnAbove: floatPtr(1000),
		CritAbove: floatPtr(10000),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "table_open_cache_overflows_15m",
		Code:      "mysql_table_open_cache_overflow",
		Title:     "MySQL table open cache overflow 增长",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_table_open_cache_overflows{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_table_open_cache_overflows"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(10),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "table_locks_waited_15m",
		Code:      "mysql_table_locks_waited",
		Title:     "MySQL table locks waited 增长",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_table_locks_waited{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_table_locks_waited"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(10),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "select_full_join_15m",
		Code:      "mysql_select_full_join",
		Title:     "MySQL full join 增长",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_select_full_join{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_select_full_join"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(100),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "sort_merge_passes_15m",
		Code:      "mysql_sort_merge_passes",
		Title:     "MySQL sort merge passes 增长",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_sort_merge_passes{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_sort_merge_passes"},
		WarnAbove: floatPtr(100),
		CritAbove: floatPtr(1000),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "innodb_log_waits_15m",
		Code:      "mysql_innodb_log_waits",
		Title:     "MySQL InnoDB log waits 增长",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_innodb_log_waits{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_innodb_log_waits"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(10),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "binlog_cache_disk_use_15m",
		Code:      "mysql_binlog_cache_disk_use",
		Title:     "MySQL binlog cache 使用磁盘临时文件",
		Expr:      fmt.Sprintf("sum(increase(mysql_global_status_binlog_cache_disk_use{%s}[15m]))", selector),
		Required:  []string{"mysql_global_status_binlog_cache_disk_use"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(10),
		Unit:      "15m",
	})
}

func (r databaseStatusRunner) recordScalarMetric(ctx context.Context, discovered map[string]struct{}, row *DatabaseStatusSource, metricKey, metricName, expr string) {
	r.recordScalarQuery(ctx, discovered, row, metricKey, expr, []string{metricName})
}

func (r databaseStatusRunner) recordScalarQuery(ctx context.Context, discovered map[string]struct{}, row *DatabaseStatusSource, metricKey, expr string, required []string) {
	if !hasRequiredMetrics(discovered, required) {
		return
	}
	v, ok, err := r.queryScalar(ctx, expr)
	if err != nil {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "unknown",
			Code:     "prom_query_error",
			Title:    "Prometheus 查询失败",
			PromQL:   expr,
			Message:  err.Error(),
		})
		return
	}
	if ok {
		row.Metrics[metricKey] = v
	}
}

func (r databaseStatusRunner) analyzePostgreSQL(ctx context.Context, selector string, discovered map[string]struct{}, row *DatabaseStatusSource) {
	r.checkUp(ctx, "pg_up", selector, discovered, row)
	r.recordScalarQuery(ctx, discovered, row, "active_connections",
		fmt.Sprintf("sum(pg_stat_activity_count{%s})", selector),
		[]string{"pg_stat_activity_count"})
	r.recordScalarMetric(ctx, discovered, row, "max_connections", "pg_settings_max_connections",
		fmt.Sprintf("max(pg_settings_max_connections{%s})", selector))
	r.recordScalarMetric(ctx, discovered, row, "database_size_bytes", "pg_database_size_bytes",
		fmt.Sprintf("sum(pg_database_size_bytes{%s})", selector))
	r.recordScalarMetric(ctx, discovered, row, "locks_count", "pg_locks_count",
		fmt.Sprintf("sum(pg_locks_count{%s})", selector))
	r.recordScalarMetric(ctx, discovered, row, "exporter_last_scrape_error", "pg_exporter_last_scrape_error",
		fmt.Sprintf("max(pg_exporter_last_scrape_error{%s})", selector))
	r.recordScalarQuery(ctx, discovered, row, "tuples_changed_per_second",
		fmt.Sprintf("sum(rate(pg_stat_database_tup_inserted{%[1]s}[5m])) + sum(rate(pg_stat_database_tup_updated{%[1]s}[5m])) + sum(rate(pg_stat_database_tup_deleted{%[1]s}[5m]))", selector),
		[]string{"pg_stat_database_tup_inserted", "pg_stat_database_tup_updated", "pg_stat_database_tup_deleted"})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "connection_usage_pct",
		Code:      "postgresql_connection_pressure",
		Title:     "PostgreSQL 连接使用率偏高",
		Expr:      fmt.Sprintf("100 * sum(pg_stat_activity_count{%s}) / clamp_min(max(pg_settings_max_connections{%s}), 1)", selector, selector),
		Required:  []string{"pg_stat_activity_count", "pg_settings_max_connections"},
		WarnAbove: floatPtr(75),
		CritAbove: floatPtr(90),
		Unit:      "%",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "deadlocks_15m",
		Code:      "postgresql_deadlocks",
		Title:     "PostgreSQL deadlock 增长",
		Expr:      fmt.Sprintf("sum(increase(pg_stat_database_deadlocks{%s}[15m]))", selector),
		Required:  []string{"pg_stat_database_deadlocks"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(3),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "rollback_ratio_pct",
		Code:      "postgresql_rollback_ratio",
		Title:     "PostgreSQL rollback 比例偏高",
		Expr: fmt.Sprintf(
			"100 * sum(rate(pg_stat_database_xact_rollback{%[1]s}[5m])) / clamp_min(sum(rate(pg_stat_database_xact_commit{%[1]s}[5m])) + sum(rate(pg_stat_database_xact_rollback{%[1]s}[5m])), 1)",
			selector,
		),
		Required:  []string{"pg_stat_database_xact_rollback", "pg_stat_database_xact_commit"},
		WarnAbove: floatPtr(10),
		CritAbove: floatPtr(30),
		Unit:      "%",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "cache_hit_ratio_pct",
		Code:      "postgresql_cache_hit_low",
		Title:     "PostgreSQL cache hit ratio 偏低",
		Expr: fmt.Sprintf(
			"100 * sum(rate(pg_stat_database_blks_hit{%[1]s}[5m])) / clamp_min(sum(rate(pg_stat_database_blks_hit{%[1]s}[5m])) + sum(rate(pg_stat_database_blks_read{%[1]s}[5m])), 1)",
			selector,
		),
		Required:  []string{"pg_stat_database_blks_hit", "pg_stat_database_blks_read"},
		WarnBelow: floatPtr(95),
		CritBelow: floatPtr(90),
		Unit:      "%",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "temp_bytes_15m",
		Code:      "postgresql_temp_bytes",
		Title:     "PostgreSQL 临时文件写入偏多",
		Expr:      fmt.Sprintf("sum(increase(pg_stat_database_temp_bytes{%s}[15m]))", selector),
		Required:  []string{"pg_stat_database_temp_bytes"},
		WarnAbove: floatPtr(1024 * 1024 * 1024),
		CritAbove: floatPtr(10 * 1024 * 1024 * 1024),
		Unit:      "bytes/15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "conflicts_15m",
		Code:      "postgresql_conflicts",
		Title:     "PostgreSQL conflict 增长",
		Expr:      fmt.Sprintf("sum(increase(pg_stat_database_conflicts{%s}[15m]))", selector),
		Required:  []string{"pg_stat_database_conflicts"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(10),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "replication_lag_seconds",
		Code:      "postgresql_replication_lag",
		Title:     "PostgreSQL 复制延迟偏高",
		Expr:      fmt.Sprintf("max(pg_replication_lag_seconds{%s})", selector),
		Required:  []string{"pg_replication_lag_seconds"},
		WarnAbove: floatPtr(30),
		CritAbove: floatPtr(300),
		Unit:      "s",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "exporter_last_scrape_error",
		Code:      "postgresql_exporter_scrape_error",
		Title:     "PostgreSQL exporter 最近一次 scrape 失败",
		Expr:      fmt.Sprintf("max(pg_exporter_last_scrape_error{%s})", selector),
		Required:  []string{"pg_exporter_last_scrape_error"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(0),
		Unit:      "",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "max_tx_duration_seconds",
		Code:      "postgresql_long_transaction",
		Title:     "PostgreSQL 存在长事务",
		Expr:      fmt.Sprintf("max(pg_stat_activity_max_tx_duration{%s})", selector),
		Required:  []string{"pg_stat_activity_max_tx_duration"},
		WarnAbove: floatPtr(300),
		CritAbove: floatPtr(1800),
		Unit:      "s",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "archiver_failures_15m",
		Code:      "postgresql_archiver_failures",
		Title:     "PostgreSQL WAL archiver 失败增长",
		Expr:      fmt.Sprintf("sum(increase(pg_stat_archiver_failed_count{%s}[15m]))", selector),
		Required:  []string{"pg_stat_archiver_failed_count"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(3),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "requested_checkpoints_15m",
		Code:      "postgresql_requested_checkpoints",
		Title:     "PostgreSQL requested checkpoints 增长",
		Expr:      fmt.Sprintf("sum(increase(pg_stat_bgwriter_checkpoints_req_total{%s}[15m]))", selector),
		Required:  []string{"pg_stat_bgwriter_checkpoints_req_total"},
		WarnAbove: floatPtr(0),
		Unit:      "15m",
	})
}

func (r databaseStatusRunner) analyzeRedis(ctx context.Context, selector string, discovered map[string]struct{}, row *DatabaseStatusSource) {
	r.checkUp(ctx, "redis_up", selector, discovered, row)
	r.recordScalarMetric(ctx, discovered, row, "connected_clients", "redis_connected_clients",
		fmt.Sprintf("max(redis_connected_clients{%s})", selector))
	r.recordScalarMetric(ctx, discovered, row, "max_clients", "redis_config_maxclients",
		fmt.Sprintf("max(redis_config_maxclients{%s})", selector))
	r.recordScalarQuery(ctx, discovered, row, "ops_per_second",
		fmt.Sprintf("sum(rate(redis_commands_processed_total{%s}[5m]))", selector),
		[]string{"redis_commands_processed_total"})
	r.recordScalarQuery(ctx, discovered, row, "network_input_bytes_per_second",
		fmt.Sprintf("sum(rate(redis_net_input_bytes_total{%s}[5m]))", selector),
		[]string{"redis_net_input_bytes_total"})
	r.recordScalarQuery(ctx, discovered, row, "network_output_bytes_per_second",
		fmt.Sprintf("sum(rate(redis_net_output_bytes_total{%s}[5m]))", selector),
		[]string{"redis_net_output_bytes_total"})
	r.recordScalarMetric(ctx, discovered, row, "connected_slaves", "redis_connected_slaves",
		fmt.Sprintf("max(redis_connected_slaves{%s})", selector))
	r.recordScalarMetric(ctx, discovered, row, "rdb_changes_since_last_save", "redis_rdb_changes_since_last_save",
		fmt.Sprintf("max(redis_rdb_changes_since_last_save{%s})", selector))
	r.checkRedisMemory(ctx, selector, discovered, row)
	r.checkRedisMemoryFragmentation(ctx, selector, discovered, row)
	r.checkRedisKeyspaceHitRatio(ctx, selector, discovered, row)
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "client_usage_pct",
		Code:      "redis_client_pressure",
		Title:     "Redis 客户端连接使用率偏高",
		Expr:      fmt.Sprintf("100 * max(redis_connected_clients{%s}) / clamp_min(max(redis_config_maxclients{%s}), 1)", selector, selector),
		Required:  []string{"redis_connected_clients", "redis_config_maxclients"},
		WarnAbove: floatPtr(75),
		CritAbove: floatPtr(90),
		Unit:      "%",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "evicted_keys_15m",
		Code:      "redis_evictions",
		Title:     "Redis 发生 key 淘汰",
		Expr:      fmt.Sprintf("sum(increase(redis_evicted_keys_total{%s}[15m]))", selector),
		Required:  []string{"redis_evicted_keys_total"},
		WarnAbove: floatPtr(0),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "rejected_connections_15m",
		Code:      "redis_rejected_connections",
		Title:     "Redis 拒绝连接增长",
		Expr:      fmt.Sprintf("sum(increase(redis_rejected_connections_total{%s}[15m]))", selector),
		Required:  []string{"redis_rejected_connections_total"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(0),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "expired_keys_15m",
		Code:      "redis_expired_keys",
		Title:     "Redis key 过期增长",
		Expr:      fmt.Sprintf("sum(increase(redis_expired_keys_total{%s}[15m]))", selector),
		Required:  []string{"redis_expired_keys_total"},
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "blocked_clients",
		Code:      "redis_blocked_clients",
		Title:     "Redis blocked clients 非零",
		Expr:      fmt.Sprintf("max(redis_blocked_clients{%s})", selector),
		Required:  []string{"redis_blocked_clients"},
		WarnAbove: floatPtr(0),
		Unit:      "clients",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "slowlog_length",
		Code:      "redis_slowlog",
		Title:     "Redis slowlog 非空",
		Expr:      fmt.Sprintf("max(redis_slowlog_length{%s})", selector),
		Required:  []string{"redis_slowlog_length"},
		WarnAbove: floatPtr(0),
		Unit:      "entries",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "exporter_last_scrape_error",
		Code:      "redis_exporter_scrape_error",
		Title:     "Redis exporter 最近一次 scrape 失败",
		Expr:      fmt.Sprintf("max(redis_exporter_last_scrape_error{%s})", selector),
		Required:  []string{"redis_exporter_last_scrape_error"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(0),
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "error_replies_15m",
		Code:      "redis_error_replies",
		Title:     "Redis error replies 增长",
		Expr:      fmt.Sprintf("sum(increase(redis_total_error_replies{%s}[15m]))", selector),
		Required:  []string{"redis_total_error_replies"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(100),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "command_failures_15m",
		Code:      "redis_command_failures",
		Title:     "Redis command failed calls 增长",
		Expr:      fmt.Sprintf("sum(increase(redis_commands_failed_calls_total{%s}[15m]))", selector),
		Required:  []string{"redis_commands_failed_calls_total"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(100),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "latest_fork_seconds",
		Code:      "redis_fork_slow",
		Title:     "Redis 最近一次 fork 耗时偏高",
		Expr:      fmt.Sprintf("max(redis_latest_fork_seconds{%s})", selector),
		Required:  []string{"redis_latest_fork_seconds"},
		WarnAbove: floatPtr(1),
		CritAbove: floatPtr(5),
		Unit:      "s",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "max_latency_usec",
		Code:      "redis_latency_high",
		Title:     "Redis latency percentile 偏高",
		Expr:      fmt.Sprintf("max(redis_latency_percentiles_usec{%s})", selector),
		Required:  []string{"redis_latency_percentiles_usec"},
		WarnAbove: floatPtr(100000),
		CritAbove: floatPtr(1000000),
		Unit:      "usec",
	})
}

func (r databaseStatusRunner) checkRedisMemory(ctx context.Context, selector string, discovered map[string]struct{}, row *DatabaseStatusSource) {
	if !hasRequiredMetrics(discovered, []string{"redis_memory_used_bytes", "redis_memory_max_bytes"}) {
		return
	}
	usedExpr := fmt.Sprintf("max(redis_memory_used_bytes{%s})", selector)
	maxExpr := fmt.Sprintf("max(redis_memory_max_bytes{%s})", selector)
	used, usedOK, usedErr := r.queryScalar(ctx, usedExpr)
	if usedErr != nil {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "unknown",
			Code:     "prom_query_error",
			Title:    "Prometheus 查询失败",
			PromQL:   usedExpr,
			Message:  usedErr.Error(),
		})
		return
	}
	maxBytes, maxOK, maxErr := r.queryScalar(ctx, maxExpr)
	if maxErr != nil {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "unknown",
			Code:     "prom_query_error",
			Title:    "Prometheus 查询失败",
			PromQL:   maxExpr,
			Message:  maxErr.Error(),
		})
		return
	}
	if usedOK {
		row.Metrics["memory_used_bytes"] = used
	}
	if maxOK {
		row.Metrics["memory_max_bytes"] = maxBytes
	}
	if !usedOK || !maxOK || maxBytes <= 0 {
		return
	}
	ratio := 100 * used / maxBytes
	row.Metrics["memory_usage_pct"] = ratio
	expr := fmt.Sprintf("100 * max(redis_memory_used_bytes{%[1]s}) / clamp_min(max(redis_memory_max_bytes{%[1]s}), 1)", selector)
	switch {
	case ratio > 90:
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity:  "critical",
			Code:      "redis_memory_pressure",
			Title:     "Redis 内存使用率偏高",
			Value:     ratio,
			Threshold: "> 90%",
			PromQL:    expr,
		})
	case ratio > 80:
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity:  "warning",
			Code:      "redis_memory_pressure",
			Title:     "Redis 内存使用率偏高",
			Value:     ratio,
			Threshold: "> 80%",
			PromQL:    expr,
		})
	}
}

func (r databaseStatusRunner) checkRedisMemoryFragmentation(ctx context.Context, selector string, discovered map[string]struct{}, row *DatabaseStatusSource) {
	if !hasRequiredMetrics(discovered, []string{"redis_mem_fragmentation_ratio"}) {
		return
	}
	expr := fmt.Sprintf("max(redis_mem_fragmentation_ratio{%s})", selector)
	ratio, ok, err := r.queryScalar(ctx, expr)
	if err != nil {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "unknown",
			Code:     "prom_query_error",
			Title:    "Prometheus 查询失败",
			PromQL:   expr,
			Message:  err.Error(),
		})
		return
	}
	if !ok {
		return
	}
	row.Metrics["mem_fragmentation_ratio"] = ratio
	if hasRequiredMetrics(discovered, []string{"redis_memory_used_bytes"}) {
		used, usedOK, usedErr := r.queryScalar(ctx, fmt.Sprintf("max(redis_memory_used_bytes{%s})", selector))
		if usedErr != nil {
			row.Findings = append(row.Findings, DatabaseStatusFinding{
				Severity: "unknown",
				Code:     "prom_query_error",
				Title:    "Prometheus 查询失败",
				PromQL:   fmt.Sprintf("max(redis_memory_used_bytes{%s})", selector),
				Message:  usedErr.Error(),
			})
			return
		}
		if usedOK && used < 64*1024*1024 {
			return
		}
	}
	switch {
	case ratio > 2:
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity:  "critical",
			Code:      "redis_memory_fragmentation",
			Title:     "Redis 内存碎片率偏高",
			Value:     ratio,
			Threshold: "> 2",
			PromQL:    expr,
		})
	case ratio > 1.5:
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity:  "warning",
			Code:      "redis_memory_fragmentation",
			Title:     "Redis 内存碎片率偏高",
			Value:     ratio,
			Threshold: "> 1.5",
			PromQL:    expr,
		})
	}
}

func (r databaseStatusRunner) checkRedisKeyspaceHitRatio(ctx context.Context, selector string, discovered map[string]struct{}, row *DatabaseStatusSource) {
	if !hasRequiredMetrics(discovered, []string{"redis_keyspace_hits_total", "redis_keyspace_misses_total"}) {
		return
	}
	hitsExpr := fmt.Sprintf("sum(rate(redis_keyspace_hits_total{%s}[5m]))", selector)
	missesExpr := fmt.Sprintf("sum(rate(redis_keyspace_misses_total{%s}[5m]))", selector)
	hits, hitsOK, hitsErr := r.queryScalar(ctx, hitsExpr)
	if hitsErr != nil {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "unknown",
			Code:     "prom_query_error",
			Title:    "Prometheus 查询失败",
			PromQL:   hitsExpr,
			Message:  hitsErr.Error(),
		})
		return
	}
	misses, missesOK, missesErr := r.queryScalar(ctx, missesExpr)
	if missesErr != nil {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "unknown",
			Code:     "prom_query_error",
			Title:    "Prometheus 查询失败",
			PromQL:   missesExpr,
			Message:  missesErr.Error(),
		})
		return
	}
	if hitsOK {
		row.Metrics["keyspace_hits_per_second"] = hits
	}
	if missesOK {
		row.Metrics["keyspace_misses_per_second"] = misses
	}
	if !hitsOK || !missesOK {
		return
	}
	total := hits + misses
	if total <= 0 {
		return
	}
	ratio := 100 * hits / total
	row.Metrics["keyspace_hit_ratio_pct"] = ratio
	expr := fmt.Sprintf("100 * sum(rate(redis_keyspace_hits_total{%[1]s}[5m])) / clamp_min(sum(rate(redis_keyspace_hits_total{%[1]s}[5m])) + sum(rate(redis_keyspace_misses_total{%[1]s}[5m])), 1)", selector)
	switch {
	case ratio < 50:
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity:  "critical",
			Code:      "redis_keyspace_hit_ratio_low",
			Title:     "Redis keyspace hit ratio 偏低",
			Value:     ratio,
			Threshold: "< 50%",
			PromQL:    expr,
		})
	case ratio < 80:
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity:  "warning",
			Code:      "redis_keyspace_hit_ratio_low",
			Title:     "Redis keyspace hit ratio 偏低",
			Value:     ratio,
			Threshold: "< 80%",
			PromQL:    expr,
		})
	}
}

func (r databaseStatusRunner) analyzeMongoDB(ctx context.Context, selector string, discovered map[string]struct{}, row *DatabaseStatusSource) {
	r.checkFirstUp(ctx, []string{"mongodb_up", "mongodb_exporter_up"}, selector, discovered, row)
	if !hasAnyMetrics(discovered, []string{"mongodb_ss_connections", "mongodb_connections", "mongodb_ss_opcounters", "mongodb_op_counters_total", "mongodb_ss_asserts", "mongodb_asserts_total", "mongodb_ss_mem_resident"}) {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "info",
			Code:     "mongodb_limited_metrics",
			Title:    "MongoDB 仅暴露基础连通性指标",
			Message:  "当前 source 未暴露 MongoDB serverStatus 连接数、操作计数、asserts 或内存指标；本工具只能判断 exporter/up 与采集链路。",
		})
	}
	r.checkFirstThreshold(ctx, selector, discovered, row, []numericCheck{
		{
			MetricKey: "connections_current",
			Code:      "mongodb_connections_current",
			Title:     "MongoDB 当前连接数偏高",
			Expr:      fmt.Sprintf("max(mongodb_ss_connections{%s,conn_type=\"current\"})", selector),
			Required:  []string{"mongodb_ss_connections"},
			WarnAbove: floatPtr(500),
			CritAbove: floatPtr(1000),
			Unit:      "connections",
		},
		{
			MetricKey: "connections_current",
			Code:      "mongodb_connections_current",
			Title:     "MongoDB 当前连接数偏高",
			Expr:      fmt.Sprintf("max(mongodb_connections{%s,state=\"current\"})", selector),
			Required:  []string{"mongodb_connections"},
			WarnAbove: floatPtr(500),
			CritAbove: floatPtr(1000),
			Unit:      "connections",
		},
	})
	r.checkFirstThreshold(ctx, selector, discovered, row, []numericCheck{
		{
			MetricKey: "connections_available",
			Code:      "mongodb_connections_available",
			Title:     "MongoDB 可用连接数",
			Expr:      fmt.Sprintf("max(mongodb_ss_connections{%s,conn_type=\"available\"})", selector),
			Required:  []string{"mongodb_ss_connections"},
			Unit:      "connections",
		},
		{
			MetricKey: "connections_available",
			Code:      "mongodb_connections_available",
			Title:     "MongoDB 可用连接数",
			Expr:      fmt.Sprintf("max(mongodb_connections{%s,state=\"available\"})", selector),
			Required:  []string{"mongodb_connections"},
			Unit:      "connections",
		},
	})
	r.checkFirstThreshold(ctx, selector, discovered, row, []numericCheck{
		{
			MetricKey: "operations_per_second",
			Code:      "mongodb_operations",
			Title:     "MongoDB 操作速率",
			Expr:      fmt.Sprintf("sum(rate(mongodb_ss_opcounters{%s}[5m]))", selector),
			Required:  []string{"mongodb_ss_opcounters"},
			Unit:      "/s",
		},
		{
			MetricKey: "operations_per_second",
			Code:      "mongodb_operations",
			Title:     "MongoDB 操作速率",
			Expr:      fmt.Sprintf("sum(rate(mongodb_op_counters_total{%s}[5m]))", selector),
			Required:  []string{"mongodb_op_counters_total"},
			Unit:      "/s",
		},
	})
	r.checkFirstThreshold(ctx, selector, discovered, row, []numericCheck{
		{
			MetricKey: "asserts_15m",
			Code:      "mongodb_asserts",
			Title:     "MongoDB asserts 增长",
			Expr:      fmt.Sprintf("sum(increase(mongodb_ss_asserts{%s}[15m]))", selector),
			Required:  []string{"mongodb_ss_asserts"},
			WarnAbove: floatPtr(0),
			Unit:      "15m",
		},
		{
			MetricKey: "asserts_15m",
			Code:      "mongodb_asserts",
			Title:     "MongoDB asserts 增长",
			Expr:      fmt.Sprintf("sum(increase(mongodb_asserts_total{%s}[15m]))", selector),
			Required:  []string{"mongodb_asserts_total"},
			WarnAbove: floatPtr(0),
			Unit:      "15m",
		},
	})
	r.checkFirstThreshold(ctx, selector, discovered, row, []numericCheck{
		{
			MetricKey: "page_faults_15m",
			Code:      "mongodb_page_faults",
			Title:     "MongoDB page faults 增长",
			Expr:      fmt.Sprintf("sum(increase(mongodb_ss_extra_info_page_faults{%s}[15m]))", selector),
			Required:  []string{"mongodb_ss_extra_info_page_faults"},
			WarnAbove: floatPtr(0),
			Unit:      "15m",
		},
		{
			MetricKey: "page_faults_15m",
			Code:      "mongodb_page_faults",
			Title:     "MongoDB page faults 增长",
			Expr:      fmt.Sprintf("sum(increase(mongodb_ss_extra_info_page_faults_total{%s}[15m]))", selector),
			Required:  []string{"mongodb_ss_extra_info_page_faults_total"},
			WarnAbove: floatPtr(0),
			Unit:      "15m",
		},
	})
	r.checkFirstThreshold(ctx, selector, discovered, row, []numericCheck{
		{
			MetricKey: "resident_memory_bytes",
			Code:      "mongodb_resident_memory",
			Title:     "MongoDB resident memory",
			Expr:      fmt.Sprintf("max(mongodb_ss_mem_resident{%s}) * 1024 * 1024", selector),
			Required:  []string{"mongodb_ss_mem_resident"},
			Unit:      "bytes",
		},
		{
			MetricKey: "resident_memory_bytes",
			Code:      "mongodb_resident_memory",
			Title:     "MongoDB resident memory",
			Expr:      fmt.Sprintf("max(mongodb_mongod_mem_resident_megabytes{%s}) * 1024 * 1024", selector),
			Required:  []string{"mongodb_mongod_mem_resident_megabytes"},
			Unit:      "bytes",
		},
	})
	r.recordScalarMetric(ctx, discovered, row, "feature_compatibility_version", "mongodb_fcv_feature_compatibility_version",
		fmt.Sprintf("max(mongodb_fcv_feature_compatibility_version{%s})", selector))
	r.recordScalarQuery(ctx, discovered, row, "network_input_bytes_per_second",
		fmt.Sprintf("sum(rate(mongodb_ss_network_bytesIn{%s}[5m]))", selector),
		[]string{"mongodb_ss_network_bytesIn"})
	r.recordScalarQuery(ctx, discovered, row, "network_output_bytes_per_second",
		fmt.Sprintf("sum(rate(mongodb_ss_network_bytesOut{%s}[5m]))", selector),
		[]string{"mongodb_ss_network_bytesOut"})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "connection_rate_limit_rejected_15m",
		Code:      "mongodb_connection_rate_limit_rejected",
		Title:     "MongoDB connection establishment 被限流拒绝",
		Expr:      fmt.Sprintf("sum(increase(mongodb_ss_connections_establishmentRateLimit_rejected{%s}[15m]))", selector),
		Required:  []string{"mongodb_ss_connections_establishmentRateLimit_rejected"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(10),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "global_lock_queue",
		Code:      "mongodb_global_lock_queue",
		Title:     "MongoDB global lock queue 非零",
		Expr:      fmt.Sprintf("max(mongodb_ss_globalLock_currentQueue{%s})", selector),
		Required:  []string{"mongodb_ss_globalLock_currentQueue"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(10),
		Unit:      "queued",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "flow_control_lagged",
		Code:      "mongodb_flow_control_lagged",
		Title:     "MongoDB flow control lagged",
		Expr:      fmt.Sprintf("max(mongodb_ss_flowControl_isLagged{%s})", selector),
		Required:  []string{"mongodb_ss_flowControl_isLagged"},
		WarnAbove: floatPtr(0),
		Unit:      "",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "wiredtiger_cache_usage_pct",
		Code:      "mongodb_wiredtiger_cache_pressure",
		Title:     "MongoDB WiredTiger cache 使用率偏高",
		Expr:      fmt.Sprintf("100 * max(mongodb_ss_wt_cache_bytes_currently_in_the_cache{%s}) / clamp_min(max(mongodb_ss_wt_cache_maximum_bytes_configured{%s}), 1)", selector, selector),
		Required:  []string{"mongodb_ss_wt_cache_bytes_currently_in_the_cache", "mongodb_ss_wt_cache_maximum_bytes_configured"},
		WarnAbove: floatPtr(80),
		CritAbove: floatPtr(95),
		Unit:      "%",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "wiredtiger_dirty_cache_pct",
		Code:      "mongodb_wiredtiger_dirty_cache_pressure",
		Title:     "MongoDB WiredTiger dirty cache 比例偏高",
		Expr:      fmt.Sprintf("100 * max(mongodb_ss_wt_cache_tracked_dirty_bytes_in_the_cache{%s}) / clamp_min(max(mongodb_ss_wt_cache_maximum_bytes_configured{%s}), 1)", selector, selector),
		Required:  []string{"mongodb_ss_wt_cache_tracked_dirty_bytes_in_the_cache", "mongodb_ss_wt_cache_maximum_bytes_configured"},
		WarnAbove: floatPtr(10),
		CritAbove: floatPtr(20),
		Unit:      "%",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "wiredtiger_cache_timeouts_15m",
		Code:      "mongodb_wiredtiger_cache_timeouts",
		Title:     "MongoDB WiredTiger cache 等待空间超时",
		Expr:      fmt.Sprintf("sum(increase(mongodb_ss_wt_cache_operations_timed_out_waiting_for_space_in_cache{%s}[15m]))", selector),
		Required:  []string{"mongodb_ss_wt_cache_operations_timed_out_waiting_for_space_in_cache"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(10),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "operation_latency_ms",
		Code:      "mongodb_operation_latency_high",
		Title:     "MongoDB 平均操作延迟偏高",
		Expr:      fmt.Sprintf("sum(rate(mongodb_ss_opLatencies_latency{%[1]s}[5m])) / clamp_min(sum(rate(mongodb_ss_opLatencies_ops{%[1]s}[5m])), 1) / 1000", selector),
		Required:  []string{"mongodb_ss_opLatencies_latency", "mongodb_ss_opLatencies_ops"},
		WarnAbove: floatPtr(50),
		CritAbove: floatPtr(200),
		Unit:      "ms",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "collection_scans_15m",
		Code:      "mongodb_collection_scans",
		Title:     "MongoDB collection scans 增长",
		Expr:      fmt.Sprintf("sum(increase(mongodb_ss_metrics_queryExecutor_collectionScans_total{%s}[15m]))", selector),
		Required:  []string{"mongodb_ss_metrics_queryExecutor_collectionScans_total"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(100),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "query_sort_spills_15m",
		Code:      "mongodb_query_sort_spills",
		Title:     "MongoDB query sort spill to disk 增长",
		Expr:      fmt.Sprintf("sum(increase(mongodb_ss_metrics_query_sort_spillToDisk{%s}[15m]))", selector),
		Required:  []string{"mongodb_ss_metrics_query_sort_spillToDisk"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(10),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "cursor_timeouts_15m",
		Code:      "mongodb_cursor_timeouts",
		Title:     "MongoDB cursor timeout 增长",
		Expr:      fmt.Sprintf("sum(increase(mongodb_ss_metrics_cursor_timedOut{%s}[15m]))", selector),
		Required:  []string{"mongodb_ss_metrics_cursor_timedOut"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(100),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "transactions_current_open",
		Code:      "mongodb_transactions_open",
		Title:     "MongoDB 当前 open transactions",
		Expr:      fmt.Sprintf("max(mongodb_ss_transactions_currentOpen{%s})", selector),
		Required:  []string{"mongodb_ss_transactions_currentOpen"},
		Unit:      "transactions",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "dbstats_data_size_bytes",
		Code:      "mongodb_dbstats_data_size",
		Title:     "MongoDB dbStats data size",
		Expr:      fmt.Sprintf("sum(mongodb_dbstats_dataSize{%s})", selector),
		Required:  []string{"mongodb_dbstats_dataSize"},
		Unit:      "bytes",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "dbstats_free_storage_bytes",
		Code:      "mongodb_dbstats_free_storage",
		Title:     "MongoDB dbStats free storage",
		Expr:      fmt.Sprintf("sum(mongodb_dbstats_freeStorageSize{%s})", selector),
		Required:  []string{"mongodb_dbstats_freeStorageSize"},
		Unit:      "bytes",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "top_total_ops_per_second",
		Code:      "mongodb_top_total_ops",
		Title:     "MongoDB top total operation rate",
		Expr:      fmt.Sprintf("sum(rate(mongodb_top_total_count{%s}[5m]))", selector),
		Required:  []string{"mongodb_top_total_count"},
		Unit:      "/s",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "currentop_fsync_lock_state",
		Code:      "mongodb_currentop_fsync_lock",
		Title:     "MongoDB currentOp fsync lock state",
		Expr:      fmt.Sprintf("max(mongodb_currentop_fsync_lock_state{%s})", selector),
		Required:  []string{"mongodb_currentop_fsync_lock_state"},
		WarnAbove: floatPtr(0),
		Unit:      "",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "profile_slow_queries_15m",
		Code:      "mongodb_profile_slow_queries",
		Title:     "MongoDB profile slow query 增长",
		Expr:      fmt.Sprintf("sum(increase(mongodb_profile_slow_query_count{%s}[15m]))", selector),
		Required:  []string{"mongodb_profile_slow_query_count"},
		WarnAbove: floatPtr(0),
		CritAbove: floatPtr(100),
		Unit:      "15m",
	})
	r.checkThreshold(ctx, selector, discovered, row, numericCheck{
		MetricKey: "pbm_backup_configured",
		Code:      "mongodb_pbm_backup_configured",
		Title:     "MongoDB PBM backup configured",
		Expr:      fmt.Sprintf("max(mongodb_pbm_cluster_backup_configured{%s})", selector),
		Required:  []string{"mongodb_pbm_cluster_backup_configured"},
		Unit:      "",
	})
}

func (r databaseStatusRunner) checkUp(ctx context.Context, metric, selector string, discovered map[string]struct{}, row *DatabaseStatusSource) {
	r.checkFirstUp(ctx, []string{metric}, selector, discovered, row)
}

func (r databaseStatusRunner) checkFirstUp(ctx context.Context, metrics []string, selector string, discovered map[string]struct{}, row *DatabaseStatusSource) {
	for _, metric := range metrics {
		if _, ok := discovered[metric]; !ok {
			continue
		}
		expr := fmt.Sprintf("min(%s{%s})", metric, selector)
		v, ok, err := r.queryScalar(ctx, expr)
		if err != nil {
			row.Findings = append(row.Findings, DatabaseStatusFinding{
				Severity: "unknown",
				Code:     "prom_query_error",
				Title:    "Prometheus 查询失败",
				PromQL:   expr,
				Message:  err.Error(),
			})
			return
		}
		if !ok {
			return
		}
		row.Metrics[metric] = v
		if v <= 0 {
			row.Findings = append(row.Findings, DatabaseStatusFinding{
				Severity:  "critical",
				Code:      metric + "_down",
				Title:     metric + " 为 0",
				Value:     v,
				Threshold: "> 0",
				PromQL:    expr,
			})
		}
		return
	}
}

type numericCheck struct {
	MetricKey string
	Code      string
	Title     string
	Expr      string
	Required  []string
	WarnAbove *float64
	CritAbove *float64
	WarnBelow *float64
	CritBelow *float64
	Unit      string
}

func (r databaseStatusRunner) checkFirstThreshold(ctx context.Context, selector string, discovered map[string]struct{}, row *DatabaseStatusSource, checks []numericCheck) {
	for _, check := range checks {
		if hasRequiredMetrics(discovered, check.Required) {
			r.checkThreshold(ctx, selector, discovered, row, check)
			return
		}
	}
}

func (r databaseStatusRunner) checkThreshold(ctx context.Context, _ string, discovered map[string]struct{}, row *DatabaseStatusSource, check numericCheck) {
	if !hasRequiredMetrics(discovered, check.Required) {
		return
	}
	v, ok, err := r.queryScalar(ctx, check.Expr)
	if err != nil {
		row.Findings = append(row.Findings, DatabaseStatusFinding{
			Severity: "unknown",
			Code:     "prom_query_error",
			Title:    "Prometheus 查询失败",
			PromQL:   check.Expr,
			Message:  err.Error(),
		})
		return
	}
	if !ok {
		return
	}
	if check.MetricKey != "" {
		row.Metrics[check.MetricKey] = v
	}
	if check.CritAbove != nil && v > *check.CritAbove {
		row.Findings = append(row.Findings, thresholdFinding("critical", check, v, "> "+formatThreshold(*check.CritAbove, check.Unit)))
		return
	}
	if check.CritBelow != nil && v < *check.CritBelow {
		row.Findings = append(row.Findings, thresholdFinding("critical", check, v, "< "+formatThreshold(*check.CritBelow, check.Unit)))
		return
	}
	if check.WarnAbove != nil && v > *check.WarnAbove {
		row.Findings = append(row.Findings, thresholdFinding("warning", check, v, "> "+formatThreshold(*check.WarnAbove, check.Unit)))
		return
	}
	if check.WarnBelow != nil && v < *check.WarnBelow {
		row.Findings = append(row.Findings, thresholdFinding("warning", check, v, "< "+formatThreshold(*check.WarnBelow, check.Unit)))
	}
}

func thresholdFinding(severity string, check numericCheck, value float64, threshold string) DatabaseStatusFinding {
	return DatabaseStatusFinding{
		Severity:  severity,
		Code:      check.Code,
		Title:     check.Title,
		Value:     value,
		Threshold: threshold,
		PromQL:    check.Expr,
	}
}

func (r databaseStatusRunner) discoverMetricNames(ctx context.Context, selector string) (map[string]struct{}, error) {
	expr := fmt.Sprintf("count by (__name__) ({%s})", selector)
	res, err := r.promQuery.Query(ctx, expr, time.Now())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", expr, err)
	}
	vals := instantValues(res)
	out := make(map[string]struct{}, len(vals))
	for _, v := range vals {
		if name := v.Metric["__name__"]; name != "" {
			out[name] = struct{}{}
		}
	}
	return out, nil
}

func (r databaseStatusRunner) queryScalar(ctx context.Context, expr string) (float64, bool, error) {
	res, err := r.promQuery.Query(ctx, expr, time.Now())
	if err != nil {
		return 0, false, fmt.Errorf("%s: %w", expr, err)
	}
	vals := instantValues(res)
	if len(vals) == 0 {
		return 0, false, nil
	}
	if math.IsNaN(vals[0].Value) || math.IsInf(vals[0].Value, 0) {
		return 0, false, fmt.Errorf("non-finite Prometheus value %v", vals[0].Value)
	}
	return vals[0].Value, true, nil
}

type promInstantValue struct {
	Metric map[string]string
	Value  float64
}

func instantValues(res *promquery.InstantResult) []promInstantValue {
	if res == nil || len(res.Result) == 0 {
		return nil
	}
	switch res.ResultType {
	case "vector", "":
		var rows []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"`
		}
		if err := json.Unmarshal(res.Result, &rows); err != nil {
			return nil
		}
		out := make([]promInstantValue, 0, len(rows))
		for _, row := range rows {
			v, ok := parsePromValue(row.Value)
			if !ok {
				continue
			}
			out = append(out, promInstantValue{Metric: row.Metric, Value: v})
		}
		return out
	case "scalar":
		var row []interface{}
		if err := json.Unmarshal(res.Result, &row); err != nil {
			return nil
		}
		v, ok := parsePromValue(row)
		if !ok {
			return nil
		}
		return []promInstantValue{{Value: v}}
	default:
		return nil
	}
}

func parsePromValue(raw []interface{}) (float64, bool) {
	if len(raw) < 2 {
		return 0, false
	}
	switch v := raw[1].(type) {
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	case float64:
		return v, true
	default:
		return 0, false
	}
}

func labelSelector(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, escapePromLabelValue(labels[k])))
	}
	return strings.Join(parts, ",")
}

func escapePromLabelValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

func hasRequiredMetrics(discovered map[string]struct{}, names []string) bool {
	for _, name := range names {
		if _, ok := discovered[name]; !ok {
			return false
		}
	}
	return true
}

func hasAnyMetrics(discovered map[string]struct{}, names []string) bool {
	for _, name := range names {
		if _, ok := discovered[name]; ok {
			return true
		}
	}
	return false
}

func normalizeDBTypeSet(items []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range items {
		v := normalizeDBType(item)
		if isSupportedDatabaseType(v) {
			out[v] = struct{}{}
		}
	}
	return out
}

func normalizeDBType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "postgres", "pg":
		return "postgresql"
	case "mongo":
		return "mongodb"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

func normalizeDatabaseResourceCategory(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return ""
	case "database":
		return "database"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

func isSupportedDatabaseType(v string) bool {
	switch v {
	case "mysql", "postgresql", "redis", "mongodb":
		return true
	default:
		return false
	}
}

func stringSet(items []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out[item] = struct{}{}
		}
	}
	return out
}

func mapFromMap(m map[string]interface{}, key string) map[string]interface{} {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	v, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	return v
}

func stringFromMap(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	raw, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func boolFromMap(m map[string]interface{}, key string, def bool) bool {
	if m == nil {
		return def
	}
	raw, ok := m[key]
	if !ok {
		return def
	}
	b, ok := raw.(bool)
	if !ok {
		return def
	}
	return b
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func floatPtr(v float64) *float64 { return &v }

func formatThreshold(v float64, unit string) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if unit != "" {
		return s + unit
	}
	return s
}

func aggregateDatabaseStatus(rows []DatabaseStatusSource) string {
	status := "ok"
	for _, row := range rows {
		status = maxStatus(status, row.Status)
	}
	return status
}

func statusFromFindings(findings []DatabaseStatusFinding) string {
	status := "ok"
	for _, f := range findings {
		status = maxStatus(status, f.Severity)
	}
	return status
}

func maxStatus(cur, next string) string {
	if statusRank(next) > statusRank(cur) {
		return normalizeStatus(next)
	}
	return normalizeStatus(cur)
}

func statusRank(v string) int {
	switch normalizeStatus(v) {
	case "critical":
		return 4
	case "warning":
		return 3
	case "unknown":
		return 2
	case "ok":
		return 1
	default:
		return 0
	}
}

func normalizeStatus(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "critical", "warning", "unknown", "ok":
		return strings.ToLower(strings.TrimSpace(v))
	case "info":
		return "ok"
	default:
		return "unknown"
	}
}
