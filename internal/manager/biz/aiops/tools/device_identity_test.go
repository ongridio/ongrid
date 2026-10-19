package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

func identityEdges() *edgebiz.Usecase {
	d42, d99 := uint64(42), uint64(99)
	return edgebiz.NewUsecase(newFakeEdgeRepo(
		&edgemodel.Edge{ID: 7, Name: "host-a", DeviceID: &d42},
		&edgemodel.Edge{ID: 42, Name: "host-b", DeviceID: &d99},
	), nil, nil, nil)
}

func TestDeviceIdentity_RankAndOutliers(t *testing.T) {
	for _, path := range []string{"base", "catalog"} {
		for _, toolName := range []string{ToolNameRankEdges, ToolNameFindOutlierEdges} {
			for _, labels := range []string{`"device_id":"42"`, `"edge_id":"42"`, `"device_id":"42","edge_id":"42"`} {
				t.Run(path+"/"+toolName+"/"+labels, func(t *testing.T) {
					pq := &fakePromQuerier{resp: &promquery.InstantResult{ResultType: "matrix",
						Result: json.RawMessage(fmt.Sprintf(`[{"metric":{%s},"values":[[1,"3.5"]]}]`, labels))}}
					edges := identityEdges()
					args, key := `{"by":"mem"}`, "results"
					if toolName == ToolNameFindOutlierEdges {
						args, key = `{"metric":"mem","sigma":2}`, "outliers"
					}
					var out string
					var err error
					if path == "catalog" {
						reg := NewRegistry(nil, edges, nil, pq, nil, nil, nil, nil)
						out, err = baseToolByName(t, reg.BuildBaseTools(), toolName).InvokableRun(context.Background(), args)
					} else if toolName == ToolNameRankEdges {
						out, err = NewRankEdgesTool(pq, edges, nil).InvokableRun(context.Background(), args)
					} else {
						out, err = NewFindOutlierEdgesTool(pq, edges, nil).InvokableRun(context.Background(), args)
					}
					if err != nil {
						t.Fatal(err)
					}
					t.Log(out)
					var got map[string]json.RawMessage
					if err := json.Unmarshal([]byte(out), &got); err != nil {
						t.Fatal(err)
					}
					var rows []RankEdgeRow
					if err := json.Unmarshal(got[key], &rows); err != nil || len(rows) != 1 {
						t.Fatalf("rows=%s err=%v", got[key], err)
					}
					wantDevice, wantEdge, wantName := uint64(0), uint64(42), "host-b"
					if strings.Contains(labels, "device_id") {
						wantDevice, wantEdge, wantName = 42, 7, "host-a"
					}
					if row := rows[0]; row.DeviceID != wantDevice || row.EdgeID != wantEdge || row.EdgeName != wantName {
						t.Errorf("identity=%+v; want device=%d edge=%d name=%s", row, wantDevice, wantEdge, wantName)
					}
				})
			}
		}
	}
}

type identityDeviceRepo struct{ devicebiz.Repo }

func (identityDeviceRepo) Get(_ context.Context, id uint64) (*devicemodel.Device, error) {
	if id == 42 || id == 99 {
		return &devicemodel.Device{ID: id}, nil
	}
	return nil, errs.ErrNotFound
}

type identityLinks struct {
	devicebiz.EdgeDeviceRepo
	edgeID uint64
	err    error
}

func (l identityLinks) LookupEdgeForDevice(_ context.Context, id uint64, relation devicemodel.EdgeDeviceRelationType) (uint64, error) {
	if id != 42 || relation != devicemodel.EdgeDeviceRelationHost {
		return 0, fmt.Errorf("unexpected device/relation: %d/%v", id, relation)
	}
	return l.edgeID, l.err
}

type identityProm struct {
	fakePromQuerier
	queries []string
}

func (p *identityProm) Query(_ context.Context, expr string, _ time.Time) (*promquery.InstantResult, error) {
	p.queries = append(p.queries, expr)
	return &promquery.InstantResult{ResultType: "vector", Result: json.RawMessage(`[{"metric":{"device_id":"42"},"value":[1,"91"]}]`)}, nil
}

func TestDeviceIdentity_CorrelateIncident(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		links *identityLinks
	}{
		{"linked", &identityLinks{edgeID: 7}},
		{"unlinked", &identityLinks{}},
		{"lookup-error", &identityLinks{err: errors.New("database unavailable")}},
		{"missing-edge", &identityLinks{edgeID: 1000}},
		{"no-device-service", nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			deviceID, now := uint64(42), time.Now()
			alerts := &fakeAlertUC{incidentByID: map[uint64]*alertmodel.Incident{1: {
				ID: 1, DeviceID: &deviceID, Rule: "cpu_pct", FirstFiredAt: now,
				LastFiredAt: now, LabelsJSON: "{}", AnnotationsJSON: "{}",
			}}}
			var devices *devicebiz.Usecase
			if scenario.links != nil {
				devices = devicebiz.NewUsecase(identityDeviceRepo{}, scenario.links, nil)
			}
			pq, edges := &identityProm{}, identityEdges()
			var bundle *correlateIncidentBundle

			out, err := NewCorrelateIncidentTool(alerts, pq, nil, nil, edges, devices, nil).
				InvokableRun(context.Background(), `{"incident_ids":[1]}`)
			if err != nil {
				t.Fatal(err)
			}
			var response CorrelateIncidentBatchResponse
			if err := json.Unmarshal([]byte(out), &response); err != nil || response.SuccessCount != 1 {
				t.Fatalf("response=%s err=%v", out, err)
			}
			bundle = response.Results[0].Bundle
			t.Logf("edge=%+v skipped=%v", bundle.Edge, bundle.Skipped)
			if scenario.name != "linked" {
				if bundle.Edge != nil || bundle.Skipped["edge"] == "" {
					t.Errorf("unresolved device must omit edge and explain why, got %+v", bundle)
				}
				return
			}
			if bundle.Edge == nil || bundle.Edge.ID != 7 || bundle.Edge.Name != "host-a" {
				t.Errorf("wrong edge for device 42: %+v", bundle.Edge)
			}
			if alerts.lastIncidentFilter.DeviceID == nil || *alerts.lastIncidentFilter.DeviceID != 42 {
				t.Errorf("recent incidents must filter device 42: %+v", alerts.lastIncidentFilter)
			}
			if len(pq.queries) != 3 {
				t.Errorf("want cpu/mem/up probes, got %v", pq.queries)
			}
			for _, expr := range append(pq.queries, pq.gotExpr) {
				if !strings.Contains(expr, `device_id="42"`) || strings.Contains(expr, "edge_id") {
					t.Errorf("metrics must use device 42: %s", expr)
				}
			}
		})
	}
}

func TestDeviceIdentity_MetricExpressions(t *testing.T) {
	deviceID := uint64(42)
	for _, by := range []string{"cpu", "composite"} {
		expr, _, _ := rankMetricExpr(by)
		t.Logf("rank_%s=%s", by, expr)
		if !strings.Contains(expr, "device_id") || !strings.Contains(expr, "edge_id") {
			t.Errorf("rank must preserve device and legacy edge identities: %s", expr)
		}
	}
	for _, metric := range []string{"cpu_pct", "mem_pct", "net_rx_bps", "net_tx_bps"} {
		expr, ok := promExprForIncident(&alertmodel.Incident{Rule: metric, DeviceID: &deviceID})
		t.Logf("incident_%s=%s", metric, expr)
		if !ok || !strings.Contains(expr, `device_id="42"`) || strings.Contains(expr, "edge_id") {
			t.Errorf("incident must use device identity: %s", expr)
		}
	}
}
