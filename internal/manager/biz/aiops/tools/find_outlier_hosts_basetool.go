package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// FindOutlierHostsTool is the BaseTool form of find_outlier_hosts.
// Mirrors executeFindOutlierHosts in find_outlier_hosts.go.
type FindOutlierHostsTool struct {
	promQuery PromQuerier
	devices   hostRankDevices
	log       *slog.Logger
}

// NewFindOutlierHostsTool builds the BaseTool variant.
func NewFindOutlierHostsTool(promQuery PromQuerier, devices hostRankDevices, log *slog.Logger) *FindOutlierHostsTool {
	if log == nil {
		log = slog.Default()
	}
	return &FindOutlierHostsTool{promQuery: promQuery, devices: devices, log: log}
}

// findOutlierEdgesWhenToUse — anomaly-detection pivot. Reverse-guards
// against confusing this with simple ranking.
const findOutlierEdgesWhenToUse = "When the user asks who's an OUTLIER on cpu / mem / disk — i.e. who's deviating " +
	"more than N sigma from the fleet baseline. " +
	"NOT for a top-N ranking by raw value (use rank_hosts). " +
	"NOT for deviation on free-form metrics (use query_promql with stddev). " +
	"NOT for single-host details (use get_host_load)."

// Info returns metadata. Class=read.
func (t *FindOutlierHostsTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameFindOutlierHosts,
		Description: FindOutlierHostsDescription,
		WhenToUse:   findOutlierEdgesWhenToUse,
		Parameters:  FindOutlierHostsSchema,
		Class:       "read",
	}, nil
}

// InvokableRun builds the z-score PromQL and filters > sigma.
func (t *FindOutlierHostsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.promQuery == nil {
		return "", fmt.Errorf("find_outlier_hosts: prom query client not configured")
	}
	if t.devices == nil {
		return "", fmt.Errorf("find_outlier_hosts: device usecase not configured")
	}

	var in FindOutlierHostsArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("find_outlier_hosts: bad args: %w", err)
	}
	// Whitelist cpu/mem/disk explicitly. rankMetricExpr also accepts
	// load/composite (for rank_hosts), but z-score outlier detection on
	// those is not in scope — fail fast with a clear message instead of
	// relying on rankMetricExpr's ok-bool.
	switch in.Metric {
	case "cpu", "mem", "disk":
		// ok
	default:
		return "", fmt.Errorf("find_outlier_hosts: metric must be cpu, mem or disk; got %q", in.Metric)
	}
	base, label, _ := hostRankMetricExpr(in.Metric)
	if in.Sigma <= 0 {
		in.Sigma = 2
	}
	if in.Sigma > 10 {
		in.Sigma = 10
	}

	expr := fmt.Sprintf(
		`((%s) - on() group_left() avg(%s)) / on() group_left() stddev(%s) > %g`,
		base, base, base, in.Sigma,
	)

	end := time.Now()

	callCtx, cancel := context.WithTimeout(ctx, outlierCallTimeout)
	defer cancel()
	res, err := t.promQuery.Query(callCtx, expr, end)
	if err != nil {
		return "", fmt.Errorf("find_outlier_hosts: dispatch: %w", err)
	}

	rankRows, err := decodeRankSeries(res, label)
	if err != nil {
		return "", fmt.Errorf("find_outlier_hosts: decode: %w", err)
	}
	rows := make([]OutlierHostRow, 0, len(rankRows))
	for _, rr := range rankRows {
		if rr.DeviceID == 0 {
			continue
		}
		host, err := t.devices.Get(callCtx, rr.DeviceID)
		if err != nil {
			return "", fmt.Errorf("get host %d: %w", rr.DeviceID, err)
		}
		if host == nil {
			return "", fmt.Errorf("host %d not found", rr.DeviceID)
		}
		rows = append(rows, OutlierHostRow{DeviceID: host.ID, DeviceName: host.Name, ZScore: rr.Value, Metric: rr.Metric})
	}

	out, err := json.Marshal(map[string]any{
		"outliers": rows,
		"sigma":    in.Sigma,
		"metric":   in.Metric,
	})
	if err != nil {
		return "", fmt.Errorf("find_outlier_hosts: marshal: %w", err)
	}
	return string(out), nil
}
