package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

func TestRankHostsTool_Info(t *testing.T) {
	uc := rankHostTestDevices{}
	tool := NewRankHostsTool(&fakePromQuerier{}, uc, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameRankHosts {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard")
	}
}

func TestRankHostsTool_TopK(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "matrix",
			Result:     json.RawMessage(`[{"metric":{"device_id":"1"},"values":[[1,"50"]]}]`),
		},
	}
	uc := rankHostTestDevices{}
	tool := NewRankHostsTool(pq, uc, nil)

	out, err := tool.InvokableRun(context.Background(), `{"by":"cpu","limit":3}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.HasPrefix(pq.gotExpr, "topk(3,") {
		t.Errorf("expected topk(3,...) expr, got %s", pq.gotExpr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["by"] != "cpu" {
		t.Errorf("by = %v", got["by"])
	}
	rows, ok := got["results"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("results = %#v", got["results"])
	}
	row, ok := rows[0].(map[string]any)
	if !ok || row["device_id"] != float64(1) {
		t.Fatalf("result row must preserve Prometheus device_id, got %#v", rows[0])
	}
}

func TestRankHostsTool_BadArgs(t *testing.T) {
	uc := rankHostTestDevices{}
	tool := NewRankHostsTool(&fakePromQuerier{}, uc, nil)
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected error for missing by")
	}
	if _, err := tool.InvokableRun(context.Background(), `{"by":"weird"}`); err == nil {
		t.Errorf("expected error for unsupported by")
	}
	if _, err := tool.InvokableRun(context.Background(), `{"by":"cpu","direction":"sideways"}`); err == nil {
		t.Errorf("expected error for invalid direction")
	}
}

func TestRankHostsTool_NilDeps(t *testing.T) {
	uc := rankHostTestDevices{}
	if _, err := NewRankHostsTool(nil, uc, nil).InvokableRun(context.Background(), `{"by":"cpu"}`); err == nil {
		t.Errorf("expected error when promQuery nil")
	}
	if _, err := NewRankHostsTool(&fakePromQuerier{}, nil, nil).InvokableRun(context.Background(), `{"by":"cpu"}`); err == nil {
		t.Errorf("expected error when edges nil")
	}
}

func TestRankHostsTool_DispatchError(t *testing.T) {
	uc := rankHostTestDevices{}
	tool := NewRankHostsTool(&fakePromQuerier{err: errors.New("prom 5xx")}, uc, nil)
	_, err := tool.InvokableRun(context.Background(), `{"by":"cpu"}`)
	if err == nil || !strings.Contains(err.Error(), "prom 5xx") {
		t.Errorf("expected wrapped dispatch error, got %v", err)
	}
}

// 同号 Edge 的旧指标不能成为 Host 身份。
type rankHostTestDevices struct{}

func (rankHostTestDevices) Get(_ context.Context, id uint64) (*devicemodel.Device, error) {
	return &devicemodel.Device{ID: id, Name: "host-name"}, nil
}

func TestRankHosts_DeviceIdentity(t *testing.T) {
	pq := &fakePromQuerier{resp: &promquery.InstantResult{ResultType: "vector", Result: json.RawMessage(`[
 {"metric":{"device_id":"7","edge_id":"99"},"value":[1,"80"]},
 {"metric":{"edge_id":"7"},"value":[1,"90"]}
 ]`)}}
	result, err := NewRankHostsTool(pq, rankHostTestDevices{}, nil).InvokableRun(context.Background(), `{"by":"cpu"}`)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Results []HostRankRow `json:"results"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 1 || got.Results[0].DeviceID != 7 || got.Results[0].DeviceName != "host-name" {
		t.Fatalf("identity mismatch: %s", result)
	}
	if strings.Contains(result, "edge_id") || strings.Contains(result, "edge_name") {
		t.Fatalf("edge identity leaked: %s", result)
	}
}

func TestRankHosts_MetricExpressionsKeepDeviceIdentity(t *testing.T) {
	for _, metric := range []string{"cpu", "mem", "disk", "load", "composite"} {
		t.Run(metric, func(t *testing.T) {
			expr, _, ok := hostRankMetricExpr(metric)
			if !ok || !strings.Contains(expr, "by (device_id)") || strings.Contains(expr, "edge_id") || !strings.Contains(expr, `device_id!=""`) {
				t.Fatalf("invalid host query: %s", expr)
			}
		})
	}
}
