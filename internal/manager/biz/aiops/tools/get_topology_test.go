package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
)

func TestGetTopologyTool_Info(t *testing.T) {
	tool := NewGetTopologyTool(nil, nil, TopologyInfo{}, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameGetTopology {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard")
	}
}

func TestGetTopologyTool_BasicCounts(t *testing.T) {
	e1 := &edgemodel.Edge{ID: 1, Name: "a", Status: edgemodel.StatusOnline}
	e2 := &edgemodel.Edge{ID: 2, Name: "b", Status: edgemodel.StatusOffline}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(e1, e2), nil, nil, slog.Default())
	topo := TopologyInfo{ManagerVersion: "v0.7.33", ConfiguredPromURL: "http://prom:9090"}
	tool := NewGetTopologyTool(uc, nil, topo, nil)

	out, err := tool.InvokableRun(context.Background(), "")
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["manager_version"] != "v0.7.33" {
		t.Errorf("manager_version = %v", got["manager_version"])
	}
	if got["edge_count"].(float64) != 2 {
		t.Errorf("edge_count = %v", got["edge_count"])
	}
	if got["online_count"].(float64) != 1 {
		t.Errorf("online_count = %v", got["online_count"])
	}
}

func TestGetTopologyTool_BadEmptyArgsAccepted(t *testing.T) {
	// get_topology accepts any (or empty) args. Confirm ignoring works.
	tool := NewGetTopologyTool(nil, nil, TopologyInfo{}, nil)
	if _, err := tool.InvokableRun(context.Background(), `{}`); err != nil {
		t.Errorf("empty args should be fine: %v", err)
	}
	if _, err := tool.InvokableRun(context.Background(), ``); err != nil {
		t.Errorf("empty string args should be fine: %v", err)
	}
}
