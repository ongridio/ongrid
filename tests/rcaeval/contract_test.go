package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	toolkit "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	alertbiz "github.com/ongridio/ongrid/internal/manager/biz/alert"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	auditmodel "github.com/ongridio/ongrid/internal/manager/model/audit"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type metricRow struct {
	Metric map[string]string    `json:"metric"`
	Values [][2]json.RawMessage `json:"values"`
}

func fixtureByID(c evalCase, id string) fixture {
	for _, f := range c.Fixtures {
		if f.ID == id {
			return f
		}
	}
	return fixture{}
}

func (b corpusBackend) GetIncident(_ context.Context, id uint64) (*alertmodel.Incident, error) {
	if id != b.c.Incident.ID {
		return nil, fmt.Errorf("unknown incident %d", id)
	}
	return b.c.Incident.model(), nil
}
func (b corpusBackend) ListIncidents(_ context.Context, f alertbiz.IncidentFilter) ([]*alertmodel.Incident, error) {
	if f.DeviceID == nil || *f.DeviceID != b.c.Incident.DeviceID {
		return nil, errors.New("unexpected incident device")
	}
	return []*alertmodel.Incident{b.c.Incident.model()}, nil
}
func (b corpusBackend) ListEvents(context.Context, uint64, int) ([]*alertmodel.Event, error) {
	return nil, nil
}
func (b corpusBackend) ListRules(context.Context, string) ([]*alertmodel.Rule, error) {
	return nil, errors.New("unexpected rule list")
}
func (b corpusBackend) ListChanges(context.Context, time.Time, time.Time, string, string, int) ([]auditmodel.Log, error) {
	return nil, nil
}
func (b corpusBackend) Query(_ context.Context, expr string, _ time.Time) (*promquery.InstantResult, error) {
	metric := b.c.Incident.Labels["metric"]
	if metric == "cpu_pct" && strings.Contains(expr, "node_cpu_seconds_total") || metric == "mem_pct" && strings.Contains(expr, "node_memory_MemAvailable_bytes") {
		var data struct {
			Result []metricRow `json:"result"`
		}
		if err := json.Unmarshal(fixtureByID(b.c, "metrics").Result, &data); err != nil {
			return nil, err
		}
		s := data.Result[0]
		raw, err := json.Marshal([]any{map[string]any{"metric": s.Metric, "value": s.Values[len(s.Values)-1]}})
		return &promquery.InstantResult{ResultType: "vector", Result: raw}, err
	}
	return &promquery.InstantResult{ResultType: "vector", Result: json.RawMessage(`[]`)}, nil
}

type corpusDevices struct {
	devicebiz.Repo
	c evalCase
}

func (d corpusDevices) Get(_ context.Context, id uint64) (*devicemodel.Device, error) {
	if id != d.c.Incident.DeviceID {
		return nil, fmt.Errorf("unknown device %d", id)
	}
	edgeID := uint64(1042)
	return &devicemodel.Device{ID: id, Name: "eval-host-42", Online: true, LastSeenAt: &d.c.Incident.At, CurrentEdgeID: &edgeID, Roles: devicemodel.EncodeRoles([]string{"server"})}, nil
}
func (b corpusBackend) QueryRange(_ context.Context, expr string, start, end time.Time, _ time.Duration) (*promquery.InstantResult, error) {
	f := fixtureByID(b.c, "metrics")
	var result struct {
		ResultType string      `json:"resultType"`
		Result     []metricRow `json:"result"`
	}
	if err := decodeStrict(f.Result, &result); err != nil {
		return nil, err
	}
	var base struct {
		Expr string `json:"expr"`
	}
	if err := json.Unmarshal(f.Args[0], &base); err != nil {
		return nil, err
	}
	if expr != base.Expr && !(strings.Contains(expr, `and on(device_id)`) && strings.Contains(expr, `device_id="42"`)) {
		return nil, fmt.Errorf("unexpected metric expression %s", expr)
	}
	rows := []metricRow{}
	for _, s := range result.Result {
		if expr != base.Expr && s.Metric["device_id"] != "42" {
			continue
		}
		values := [][2]json.RawMessage{}
		for _, p := range s.Values {
			var at int64
			if err := json.Unmarshal(p[0], &at); err != nil {
				return nil, err
			}
			if at >= start.Unix() && at <= end.Unix() {
				values = append(values, p)
			}
		}
		if len(values) > 0 {
			s.Values = values
			rows = append(rows, s)
		}
	}
	data, err := json.Marshal(rows)
	return &promquery.InstantResult{ResultType: "matrix", Result: data}, err
}

func (b corpusBackend) GetTrace(_ context.Context, id string) (*tracequery.TraceResult, error) {
	var f struct {
		TraceID string `json:"trace_id"`
		Spans   []struct {
			Resource json.RawMessage `json:"resource"`
			Span     json.RawMessage `json:"span"`
		} `json:"spans"`
	}
	if err := json.Unmarshal(fixtureByID(b.c, "trace").Result, &f); err != nil {
		return nil, err
	}
	if id != f.TraceID {
		return nil, errors.New("wrong trace ID")
	}
	groups := []any{}
	for _, s := range f.Spans {
		groups = append(groups, map[string]any{"resource": s.Resource, "scopeSpans": []any{map[string]any{"spans": []json.RawMessage{s.Span}}}})
	}
	raw, err := json.Marshal(map[string]any{"resourceSpans": groups})
	return &tracequery.TraceResult{Body: raw}, err
}
func (b corpusBackend) SearchTraces(_ context.Context, opts tracequery.SearchOptions) (*tracequery.SearchResult, error) {
	if b.c.ID != "downstream_dependency" || !strings.Contains(opts.Query, `resource.device_id = "42"`) {
		return nil, fmt.Errorf("unexpected trace search: %+v", opts)
	}
	var detail struct {
		TraceID string `json:"trace_id"`
		Spans   []struct {
			Span struct {
				Name  string `json:"name"`
				Start string `json:"startTimeUnixNano"`
				End   string `json:"endTimeUnixNano"`
			} `json:"span"`
		} `json:"spans"`
	}
	if err := json.Unmarshal(fixtureByID(b.c, "trace").Result, &detail); err != nil {
		return nil, err
	}
	if len(detail.Spans) == 0 {
		return nil, errors.New("missing trace root")
	}
	root := detail.Spans[0].Span
	start, err := strconv.ParseInt(root.Start, 10, 64)
	if err != nil {
		return nil, err
	}
	end, err := strconv.ParseInt(root.End, 10, 64)
	if err != nil {
		return nil, err
	}
	if start < opts.Start.UnixNano() || start > opts.End.UnixNano() {
		return &tracequery.SearchResult{Traces: json.RawMessage(`[]`)}, nil
	}
	raw, err := json.Marshal([]any{map[string]any{"traceID": detail.TraceID, "rootServiceName": b.c.Incident.Labels["service"], "rootTraceName": root.Name, "durationMs": (end - start) / int64(time.Millisecond), "startTimeUnixNano": root.Start}})
	return &tracequery.SearchResult{Traces: raw}, err
}

func productionFixture(c evalCase, f fixture, args json.RawMessage) (string, error) {
	b := corpusBackend{c}
	var tool basetool.BaseTool
	switch f.Tool {
	case "get_incident_detail":
		tool = toolkit.NewGetIncidentDetailTool(b, nil)
	case "correlate_incident":
		devices := devicebiz.NewUsecase(corpusDevices{c: c}, nil, nil)
		tool = toolkit.NewCorrelateIncidentTool(b, b, b, b, nil, devices, nil)
	case "query_promql":
		tool = toolkit.NewQueryPromQLTool(b, nil)
	case "query_logql":
		tool = toolkit.NewQueryLogQLTool(b)
	case "query_change_events":
		tool = toolkit.NewQueryChangeEventsTool(b, nil)
	case "query_traceql":
		tool = toolkit.NewQueryTraceQLTool(b, nil)
	default:
		// Host executor injection is private to tools. Its exported wire DTOs
		// still reject unknown fields and enforce the complete serialized shape.
		var target any
		switch f.Tool {
		case "get_host_processes":
			target = &toolkit.ProcessListBatchResponse{}
		case "get_host_load":
			target = &toolkit.HostLoadBatchResponse{}
		case "host_find_large_files":
			target = &struct {
				DeviceID     uint64                             `json:"device_id"`
				SuccessCount int                                `json:"success_count"`
				ErrorCount   int                                `json:"error_count"`
				Results      []tunnel.FindLargeFilesResultEntry `json:"results"`
			}{}
		case "host_du_summary":
			target = &struct {
				DeviceID     uint64                        `json:"device_id"`
				SuccessCount int                           `json:"success_count"`
				ErrorCount   int                           `json:"error_count"`
				Results      []tunnel.DuSummaryResultEntry `json:"results"`
				Filesystems  []tunnel.HostFilesystem       `json:"filesystems,omitempty"`
			}{}
		default:
			return "", fmt.Errorf("missing contract for %s", f.Tool)
		}
		if err := decodeStrict(f.Result, target); err != nil {
			return "", err
		}
		out, err := json.Marshal(target)
		return string(out), err
	}
	ctx := basetool.WithObservationTime(context.Background(), c.Incident.At)
	return basetool.WithValidation(tool).InvokableRun(ctx, string(args))
}

func TestFixturesMatchProductionContracts(t *testing.T) {
	for _, c := range testCases(t) {
		t.Run(c.ID, func(t *testing.T) {
			replay, err := newReplay(c, 1000)
			if err != nil {
				t.Fatal(err)
			}
			bag, err := replay.tools(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			validated := map[string]basetool.BaseTool{}
			for _, tool := range bag {
				info, err := tool.Info(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				validated[info.Name] = basetool.WithValidation(tool)
			}
			for _, f := range c.Fixtures {
				for _, args := range f.Args {
					if _, err := validated[f.Tool].InvokableRun(context.Background(), string(args)); err != nil && f.Error == "" {
						t.Fatalf("%s invalid declared args: %v", f.ID, err)
					}
					if err := checkFixtureContract(c, f, args); err != nil {
						t.Errorf("%s args=%s: %v", f.ID, args, err)
					}
				}
			}
		})
	}
}

func checkFixtureContract(c evalCase, f fixture, args json.RawMessage) error {
	if err := checkObservation(c, f); err != nil {
		return err
	}
	actual, err := productionFixture(c, f, args)
	if f.Error == "timeout" {
		if !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("want timeout, got %v", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	var got, want any
	if err := json.Unmarshal([]byte(actual), &got); err != nil {
		return err
	}
	if err := json.Unmarshal(f.Result, &want); err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("contract differs\nactual=%s\nfixture=%s", actual, f.Result)
	}
	return nil
}

func TestCorpusChecksRejectContradictoryObservations(t *testing.T) {
	for _, tc := range []struct {
		name, caseID, fixtureID string
		change                  func(map[string]any)
	}{
		{"incident_field", "cpu_process", "incident", func(v map[string]any) {
			v["results"].([]any)[0].(map[string]any)["incident"].(map[string]any)["rule"] = "other-rule"
		}},
		{"wrong_device", "cpu_process", "metrics", func(v map[string]any) {
			v["result"].([]any)[0].(map[string]any)["metric"].(map[string]any)["device_id"] = "43"
		}},
		{"missing_mount", "disk_log_growth", "metrics", func(v map[string]any) {
			delete(v["result"].([]any)[0].(map[string]any)["metric"].(map[string]any), "mountpoint")
		}},
		{"wrong_service", "downstream_dependency", "metrics", func(v map[string]any) {
			v["result"].([]any)[0].(map[string]any)["metric"].(map[string]any)["service_name"] = "inventory-api"
		}},
		{"old_log_in_current_window", "stale_logs", "correlation", func(v map[string]any) {
			v["results"].([]any)[0].(map[string]any)["bundle"].(map[string]any)["log_panel"] = []any{map[string]any{"ts": "2026-11-03T06:20:00Z", "line": "Out of memory"}}
		}},
		{"log_clock_mismatch", "disk_log_growth", "file_growth", func(v map[string]any) {
			v["result"].([]any)[0].(map[string]any)["values"].([]any)[0].([]any)[0] = "1793773140000000000"
		}},
		{"batch_partial_success", "mixed_devices", "batch_processes", func(v map[string]any) { v["success_count"] = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c evalCase
			for _, candidate := range testCases(t) {
				if candidate.ID == tc.caseID {
					c = candidate
				}
			}
			f := fixtureByID(c, tc.fixtureID)
			var result map[string]any
			if err := json.Unmarshal(f.Result, &result); err != nil {
				t.Fatal(err)
			}
			tc.change(result)
			var err error
			f.Result, err = json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkFixtureContract(c, f, f.Args[0]); err == nil {
				t.Fatal("contradictory fixture accepted")
			}
		})
	}
}
