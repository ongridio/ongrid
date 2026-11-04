package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	toolkit "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
)

func checkObservation(c evalCase, f fixture) error {
	if f.Error != "" {
		return nil
	}
	var envelope struct {
		Success int `json:"success_count"`
		Errors  int `json:"error_count"`
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(f.Result, &envelope); err != nil {
		return err
	}
	if envelope.Results != nil {
		failed := 0
		for _, r := range envelope.Results {
			if r.Error != "" {
				failed++
			}
		}
		if envelope.Success != len(envelope.Results)-failed || envelope.Errors != failed {
			return fmt.Errorf("%s batch counts contradict entries", f.ID)
		}
	}
	switch f.Tool {
	case "query_promql":
		var result struct {
			Result []metricRow `json:"result"`
		}
		if err := json.Unmarshal(f.Result, &result); err != nil {
			return err
		}
		// Only labels preserved by these expressions are checked here; mode is
		// intentionally removed by the CPU aggregation.
		matcher := regexp.MustCompile(`\b(device_id|mountpoint|service_name)\s*(=~|=)\s*("(?:\\.|[^"\\])*")`)
		for _, raw := range f.Args {
			var a toolkit.QueryPromQLArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return err
			}
			for _, s := range result.Result {
				for _, m := range matcher.FindAllString(a.Expr, -1) {
					ok, err := selectorMatches(m, s.Metric)
					if err != nil {
						return err
					}
					if !ok {
						return fmt.Errorf("%s metric labels do not match %s", f.ID, m)
					}
				}
				if name := s.Metric["__name__"]; name != "" && !strings.HasPrefix(a.Expr, name+"{") {
					return fmt.Errorf("%s derived expression cannot retain metric name %s", f.ID, name)
				}
			}
		}
	case "query_logql":
		var result struct {
			Result []logStream `json:"result"`
		}
		if err := json.Unmarshal(f.Result, &result); err != nil {
			return err
		}
		stamp := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z`)
		for _, s := range result.Result {
			for _, v := range s.Values {
				if date := stamp.FindString(v[1]); date != "" {
					at, err := time.Parse(time.RFC3339, date)
					if err != nil {
						return err
					}
					ns, err := strconv.ParseInt(v[0], 10, 64)
					if err != nil {
						return err
					}
					if at.UnixNano() != ns {
						return fmt.Errorf("%s log event time disagrees with record timestamp", f.ID)
					}
				}
			}
		}
	case "get_host_processes":
		var result toolkit.ProcessListBatchResponse
		if err := json.Unmarshal(f.Result, &result); err != nil {
			return err
		}
		for _, raw := range f.Args {
			var a toolkit.GetProcessListBatchArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return err
			}
			if len(a.DeviceIDs) != len(result.Results) {
				return fmt.Errorf("%s device count mismatch", f.ID)
			}
			limit := a.TopN
			if limit == 0 {
				limit = 10
			}
			for i, r := range result.Results {
				if r.DeviceID != a.DeviceIDs[i] || (r.ProcessList == nil) == (r.Error == "") {
					return fmt.Errorf("%s device/result mismatch", f.ID)
				}
				if r.ProcessList == nil {
					continue
				}
				p := r.ProcessList
				if p.SampledAt != c.Incident.At.Unix() || p.CPUBasis != "host_total" || p.CPUWindowSeconds <= 0 || len(p.Processes) > int(limit) {
					return fmt.Errorf("%s invalid process sampling or limit", f.ID)
				}
				previous := math.Inf(1)
				for _, row := range p.Processes {
					value := row.CPUPct
					if a.SortBy == "mem" {
						value = row.MemPct
					}
					if row.PID <= 0 || row.CPUPct < 0 || row.CPUPct > 100 || row.MemPct < 0 || row.MemPct > 100 || row.CPUSampleStatus != "valid" || value > previous {
						return fmt.Errorf("%s invalid process values/order", f.ID)
					}
					previous = value
				}
			}
		}
	}
	return nil
}

func TestScenarioFacts(t *testing.T) {
	cases := testCases(t)
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			for _, f := range c.Fixtures {
				if err := checkObservation(c, f); err != nil {
					t.Fatal(err)
				}
			}
			switch c.ID {
			case "cpu_process", "mixed_devices":
				id := "processes"
				if c.ID == "mixed_devices" {
					id = "batch_processes"
				}
				var batch toolkit.ProcessListBatchResponse
				if err := json.Unmarshal(fixtureByID(c, id).Result, &batch); err != nil {
					t.Fatal(err)
				}
				p := batch.Results[0].ProcessList.Processes[0]
				if p.PID != 812 || p.Name != "pigz" || p.Cmdline != "/usr/bin/pigz -p 8 /data/archive/events-20261104.tar" || p.CPUPct != 91 {
					t.Fatal("CPU observation changed; review its expected conclusion")
				}
				if c.ID == "cpu_process" {
					var loads toolkit.HostLoadBatchResponse
					if err := json.Unmarshal(fixtureByID(c, "host_load").Result, &loads); err != nil {
						t.Fatal(err)
					}
					load := loads.Results[0].HostLoad
					if load.CPUPct != 96 || load.CPUPct < p.CPUPct || load.SampledAt != batch.Results[0].ProcessList.SampledAt {
						t.Fatal("host and process samples disagree")
					}
				} else if len(batch.Results) != 2 || batch.Results[1].DeviceID != 43 || batch.Results[1].Error == "" {
					t.Fatal("mixed device failure missing")
				}
			case "memory_oom":
				log := fixtureByID(c, "oom_logs")
				if !strings.Contains(string(log.Result), "Killed process 901 (checkout-api)") {
					t.Fatal("OOM victim observation missing")
				}
			case "memory_unproven", "stale_logs":
				id := "empty_logs"
				if c.ID == "stale_logs" {
					id = "current_logs"
				}
				var logs struct {
					Result []logStream `json:"result"`
				}
				f := fixtureByID(c, id)
				if err := json.Unmarshal(f.Result, &logs); err != nil {
					t.Fatal(err)
				}
				if f.Error != "" || len(logs.Result) != 0 {
					t.Fatal("empty query must be successful")
				}
				if c.ID == "stale_logs" {
					if err := json.Unmarshal(fixtureByID(c, "old_logs").Result, &logs); err != nil {
						t.Fatal(err)
					}
					ns, err := strconv.ParseInt(logs.Result[0].Values[0][0], 10, 64)
					if err != nil {
						t.Fatal(err)
					}
					if ns != c.Incident.At.Add(-24*time.Hour).UnixNano() {
						t.Fatal("historical log must remain on previous day")
					}
				}
			case "query_timeout":
				if f := fixtureByID(c, "timeout_logs"); f.Error != "timeout" || len(f.Result) != 0 {
					t.Fatal("timeout cannot have a successful empty result")
				}
			case "disk_log_growth":
				checkDiskFacts(t, c)
			case "downstream_dependency":
				checkTraceFacts(t, c)
			}
		})
	}
}

func checkDiskFacts(t *testing.T, c evalCase) {
	t.Helper()
	var logs struct {
		Result []logStream `json:"result"`
	}
	if err := json.Unmarshal(fixtureByID(c, "file_growth").Result, &logs); err != nil {
		t.Fatal(err)
	}
	values := logs.Result[0].Values
	bytesRE := regexp.MustCompile(`\ballocated_bytes=(\d+)`)
	sizes := []int64{}
	for _, v := range values {
		match := bytesRE.FindStringSubmatch(v[1])
		if len(match) != 2 {
			t.Fatal("file allocation sample missing")
		}
		n, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, n)
	}
	if len(sizes) != 2 || sizes[0] != 93<<30 || sizes[1] != 68<<30 || sizes[0]-sizes[1] != 25<<30 {
		t.Fatal("disk growth evidence changed")
	}
	var usage struct {
		Filesystems []struct {
			Mount string `json:"mountpoint"`
			Used  int64  `json:"used_bytes"`
			Size  int64  `json:"size_bytes"`
		} `json:"filesystems"`
		Results []struct {
			Total    int64 `json:"total_size_bytes"`
			Subpaths []struct {
				Path string `json:"subpath"`
				Size int64  `json:"size_bytes"`
			} `json:"subpaths"`
		} `json:"results"`
	}
	if err := json.Unmarshal(fixtureByID(c, "disk_usage").Result, &usage); err != nil {
		t.Fatal(err)
	}
	fs := usage.Filesystems[0]
	r := usage.Results[0]
	if fs.Mount != "/var" || fs.Size != 100<<30 || fs.Used != 100<<30 || r.Total != fs.Used || r.Subpaths[0].Path != "/var/log" || r.Subpaths[0].Size != sizes[0] {
		t.Fatal("file, directory, and filesystem capacity disagree")
	}
	var metrics struct {
		Result []metricRow `json:"result"`
	}
	if err := json.Unmarshal(fixtureByID(c, "metrics").Result, &metrics); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := json.Unmarshal(metrics.Result[0].Values[0][1], &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(metrics.Result[0].Values[2][1], &after); err != nil {
		t.Fatal(err)
	}
	if before != "75" || after != "100" {
		t.Fatal("filesystem trend does not match the 25 GiB growth")
	}
}

func checkTraceFacts(t *testing.T, c evalCase) {
	t.Helper()
	var trace struct {
		TraceID   string `json:"trace_id"`
		Total     int    `json:"total_spans"`
		Truncated bool   `json:"truncated"`
		Spans     []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						String string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			Span struct {
				TraceID string `json:"traceId"`
				ID      string `json:"spanId"`
				Parent  string `json:"parentSpanId"`
				Start   string `json:"startTimeUnixNano"`
				End     string `json:"endTimeUnixNano"`
				Status  struct {
					Code int `json:"code"`
				} `json:"status"`
				Events []struct {
					At string `json:"timeUnixNano"`
				} `json:"events"`
			} `json:"span"`
		} `json:"spans"`
	}
	if err := json.Unmarshal(fixtureByID(c, "trace").Result, &trace); err != nil {
		t.Fatal(err)
	}
	if trace.Total != 2 || len(trace.Spans) != 2 || trace.Truncated {
		t.Fatal("expected one complete two-span trace")
	}
	for i, entry := range trace.Spans {
		span := entry.Span
		if span.TraceID != trace.TraceID || span.Status.Code != 2 {
			t.Fatal("trace ID/status mismatch")
		}
		start, err := strconv.ParseInt(span.Start, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		end, err := strconv.ParseInt(span.End, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if end < start || end > c.Incident.At.UnixNano() || start < c.Incident.At.Add(-20*time.Minute).UnixNano() {
			t.Fatal("span outside incident window")
		}
		attrs := map[string]string{}
		for _, a := range entry.Resource.Attributes {
			attrs[a.Key] = a.Value.String
		}
		service := []string{"checkout-api", "inventory-api"}[i]
		if attrs["device_id"] != "42" || attrs["service.name"] != service {
			t.Fatal("trace resource identity mismatch")
		}
		if i == 1 {
			parent := trace.Spans[0].Span
			if span.Parent != parent.ID || span.Start < parent.Start || span.End > parent.End || end-start != 2080*int64(time.Millisecond) {
				t.Fatal("invalid dependency span relationship")
			}
			for _, event := range span.Events {
				at, err := strconv.ParseInt(event.At, 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				if at < start || at > end {
					t.Fatal("span event outside span")
				}
			}
			var logs struct {
				Result []logStream `json:"result"`
			}
			if err := json.Unmarshal(fixtureByID(c, "dependency_logs").Result, &logs); err != nil {
				t.Fatal(err)
			}
			if logs.Result[0].Stream["trace_id"] != trace.TraceID || logs.Result[0].Values[0][0] != span.End {
				t.Fatal("dependency log does not match the sampled trace")
			}
		}
	}
}

func assertScenarioReferences(t *testing.T, c evalCase, check alertmodel.EvidenceCheck) {
	t.Helper()
	expected := []string{"correlate_incident", "query_promql"}
	switch c.ID {
	case "cpu_process":
		expected = append(expected, "get_host_processes", "get_host_load")
	case "disk_log_growth":
		expected = append(expected, "host_find_large_files", "query_logql", "host_du_summary")
	case "downstream_dependency":
		expected = append(expected, "query_traceql", "AgentTool", "query_logql")
	case "mixed_devices":
		expected = append(expected, "get_host_processes", "get_host_processes")
	case "stale_logs":
		expected = append(expected, "query_logql", "query_logql")
	default:
		expected = append(expected, "query_logql")
	}
	if len(check.Sources) != len(expected) {
		t.Fatalf("%s sources=%d want %d", c.ID, len(check.Sources), len(expected))
	}
	for i, s := range check.Sources {
		if s.ID != fmt.Sprintf("E%d", i+1) || s.Tool != expected[i] {
			t.Fatalf("%s reference %s points to %s, want %s", c.ID, s.ID, s.Tool, expected[i])
		}
	}
	for _, id := range check.RootCause.SourceIDs {
		if id == "E1" || id == "E4" && c.ID == "downstream_dependency" {
			t.Fatal("root cause cites orchestration/context instead of observations")
		}
	}
	if c.ID == "mixed_devices" {
		if check.Sources[2].Target != "device_id=42" || check.Sources[3].Target != "device_id=43" || check.Sources[3].State != "error" || !slices.Contains(check.RootCause.SourceIDs, "E4") || check.RootCause.Status != "partial" {
			t.Fatal("mixed-device missing evidence was not cited distinctly")
		}
	}
	if c.ID == "stale_logs" && (!strings.HasPrefix(check.Sources[3].DataStart, "2026-11-03") || !slices.Contains(check.RootCause.SourceIDs, "E4")) {
		t.Fatal("historical observation lost its date or citation")
	}
}
