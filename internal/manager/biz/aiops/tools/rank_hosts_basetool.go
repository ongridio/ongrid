package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
)

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
// executeRankHosts in rank_hosts.go.
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
