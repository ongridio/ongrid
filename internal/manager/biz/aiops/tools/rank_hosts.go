package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
)

// ToolNameRankHosts is the stable wire name the LLM sees.
const ToolNameRankHosts = "rank_hosts"

// RankHostsDescription pushes the model toward this tool whenever the
// question is "which top/bottom N machines by a host metric".
const RankHostsDescription = "Rank hosts by a closed-set host metric (cpu, mem, disk, load, composite) and return top-N or bottom-N. " +
	"Use this for 'find the most/least loaded N machines' style questions. " +
	"Composite is the unweighted mean of cpu_pct + mem_pct + disk_used_pct."

// RankHostsSchema is the JSON Schema of the tool's argument object.
var RankHostsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "by": {
      "type": "string",
      "enum": ["cpu", "mem", "disk", "load", "composite"],
      "description": "Which metric to rank on. cpu/mem/disk are the same closed-set the alert evaluator uses."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 50,
      "description": "How many hosts to return (default 5)."
    },
    "direction": {
      "type": "string",
      "enum": ["top", "bottom"],
      "description": "top = highest values; bottom = lowest values (default top)."
    }
  },
  "required": ["by"]
}`)

// RankHostsArgs is the typed form of RankHostsSchema.
type RankHostsArgs struct {
	By        string `json:"by"`
	Limit     int    `json:"limit,omitempty"`
	Direction string `json:"direction,omitempty"`
}

// hostMetricRow contains only a durable Host identity and its measurement.
type hostMetricRow struct {
	DeviceID uint64  `json:"device_id"`
	Value    float64 `json:"value"`
	Metric   string  `json:"metric"`
}

const rankHostsCallTimeout = 60 * time.Second

// hostRankMetricExpr groups measurements by durable host identity.
func hostRankMetricExpr(by string) (string, string, bool) {
	cpu := `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle",device_id!=""}[5m])))`
	mem := `avg by (device_id) (100 * (1 - node_memory_MemAvailable_bytes{device_id!=""} / node_memory_MemTotal_bytes{device_id!=""}))`
	disk := `avg by (device_id) (100 * (1 - node_filesystem_avail_bytes{mountpoint="/",device_id!=""} / node_filesystem_size_bytes{mountpoint="/",device_id!=""}))`
	switch by {
	case "cpu":
		return cpu, "cpu_pct", true
	case "mem":
		return mem, "mem_pct", true
	case "disk":
		return disk, "disk_used_pct", true
	case "load":
		return `avg by (device_id) (node_load1{device_id!=""})`, "load1", true
	case "composite":
		return "((" + cpu + ") + (" + mem + ") + (" + disk + ")) / 3", "composite_pct", true
	}
	return "", "", false
}

// promRangeSeries is the per-series envelope inside a Prom matrix
// response. Decoded only as much as we need.
type promRangeSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][2]any          `json:"values"`
}

// decodeRankSeries reads Host-labelled measurements. Edge-only series are excluded.
func decodeRankSeries(res interface { /* satisfied by *promquery.InstantResult */
}, metricLabel string) ([]hostMetricRow, error) {
	type irShape struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	}
	// The PromQuerier returns a *promquery.InstantResult; rather than
	// hard-binding to that concrete type here (which would create a
	// dependency cycle issue if the package layout shifts), re-encode
	// and re-decode through json. This costs one extra marshal; the
	// payload is small (<= 50 rows).
	blob, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	var ir irShape
	if err := json.Unmarshal(blob, &ir); err != nil {
		return nil, err
	}
	switch ir.ResultType {
	case "matrix":
		var series []promRangeSeries
		if err := json.Unmarshal(ir.Result, &series); err != nil {
			return nil, err
		}
		rows := make([]hostMetricRow, 0, len(series))
		for _, s := range series {
			deviceID, ok := numericLabel(s.Metric, "device_id")
			if !ok {
				continue
			}
			val, ok := lastSampleValue(s.Values)
			if !ok {
				continue
			}
			rows = append(rows, hostMetricRow{DeviceID: deviceID, Value: val, Metric: metricLabel})
		}
		return rows, nil
	case "vector":
		var series []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}
		if err := json.Unmarshal(ir.Result, &series); err != nil {
			return nil, err
		}
		rows := make([]hostMetricRow, 0, len(series))
		for _, s := range series {
			deviceID, ok := numericLabel(s.Metric, "device_id")
			if !ok {
				continue
			}
			val, ok := promSampleFloat(s.Value[1])
			if !ok {
				continue
			}
			rows = append(rows, hostMetricRow{DeviceID: deviceID, Value: val, Metric: metricLabel})
		}
		return rows, nil
	}
	return nil, nil
}

// numericLabel pulls a uint64-ish label out of the Prom metric map. A
// non-numeric or missing label returns ok=false.
func numericLabel(m map[string]string, key string) (uint64, bool) {
	v, ok := m[key]
	if !ok || v == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// lastSampleValue returns the last numeric sample of a Prom matrix series.
// Each sample is [unix_ts, "<value>"]; the value comes back as a string
// inside JSON.
func lastSampleValue(values [][2]any) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	last := values[len(values)-1]
	return promSampleFloat(last[1])
}

func promSampleFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return x, true
	}
	return 0, false
}

type hostRankDevices interface {
	Get(context.Context, uint64) (*devicemodel.Device, error)
}

type HostRankRow struct {
	DeviceID   uint64  `json:"device_id"`
	DeviceName string  `json:"device_name"`
	Value      float64 `json:"value"`
	Metric     string  `json:"metric"`
}

// RankHostsTool is the BaseTool form of rank_hosts. Mirrors
// measurements are grouped by durable Host identity.
type RankHostsTool struct {
	promQuery PromQuerier
	devices   hostRankDevices
	log       *slog.Logger
}

// NewRankHostsTool builds the BaseTool variant.
func NewRankHostsTool(promQuery PromQuerier, devices hostRankDevices, log *slog.Logger) *RankHostsTool {
	if log == nil {
		log = slog.Default()
	}
	return &RankHostsTool{promQuery: promQuery, devices: devices, log: log}
}

// rankHostsWhenToUse — top/bottom-N pivot for closed-set host metrics.
const rankHostsWhenToUse = "When the user wants the TOP-N or BOTTOM-N hosts ranked by a closed-set metric " +
	"(cpu / mem / disk / load / composite). Example: '帮我找出最忙的 5 台机器'. " +
	"NOT for outlier detection / who's anomalously deviating (use find_outlier_hosts). " +
	"NOT for general PromQL ranking on free-form metrics (use query_promql with topk/bottomk). " +
	"NOT for a single host's stats (use get_host_load)."

// Info returns metadata. Class=read.
func (t *RankHostsTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameRankHosts,
		Description: RankHostsDescription,
		WhenToUse:   rankHostsWhenToUse,
		Parameters:  RankHostsSchema,
		Class:       "read",
	}, nil
}

// InvokableRun runs topk/bottomk PromQL and decorates with host names.
func (t *RankHostsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.promQuery == nil {
		return "", fmt.Errorf("rank_hosts: prom query client not configured")
	}
	if t.devices == nil {
		return "", fmt.Errorf("rank_hosts: device usecase not configured")
	}

	var in RankHostsArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("rank_hosts: bad args: %w", err)
	}
	if in.By == "" {
		return "", fmt.Errorf("rank_hosts: by required")
	}
	base, label, ok := hostRankMetricExpr(in.By)
	if !ok {
		return "", fmt.Errorf("rank_hosts: unsupported by=%q", in.By)
	}
	if in.Limit <= 0 {
		in.Limit = 5
	}
	if in.Limit > 50 {
		in.Limit = 50
	}
	op := "topk"
	switch in.Direction {
	case "", "top":
		op = "topk"
	case "bottom":
		op = "bottomk"
	default:
		return "", fmt.Errorf("rank_hosts: direction must be top or bottom (got %q)", in.Direction)
	}
	expr := fmt.Sprintf(`%s(%d, %s)`, op, in.Limit, base)

	end := time.Now()

	callCtx, cancel := context.WithTimeout(ctx, rankHostsCallTimeout)
	defer cancel()
	res, err := t.promQuery.Query(callCtx, expr, end)
	if err != nil {
		return "", fmt.Errorf("rank_hosts: dispatch: %w", err)
	}

	decoded, err := decodeRankSeries(res, label)
	if err != nil {
		return "", fmt.Errorf("rank_hosts: decode: %w", err)
	}

	rows := make([]HostRankRow, 0, len(decoded))
	for _, row := range decoded {
		if row.DeviceID == 0 {
			continue
		} // Edge-only samples cannot identify a host.
		device, err := t.devices.Get(callCtx, row.DeviceID)
		if err != nil {
			return "", fmt.Errorf("rank_hosts: lookup device %d: %w", row.DeviceID, err)
		}
		if device == nil {
			return "", fmt.Errorf("rank_hosts: device %d not found", row.DeviceID)
		}
		rows = append(rows, HostRankRow{DeviceID: device.ID, DeviceName: device.Name, Value: row.Value, Metric: row.Metric})
	}

	out, err := json.Marshal(map[string]any{
		"results":   rows,
		"metric":    label,
		"by":        in.By,
		"direction": op,
	})
	if err != nil {
		return "", fmt.Errorf("rank_hosts: marshal: %w", err)
	}
	return string(out), nil
}
