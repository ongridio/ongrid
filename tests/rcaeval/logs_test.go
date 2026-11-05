package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

func TestLogReplayFiltersExistingObservations(t *testing.T) {
	for _, c := range testCases(t) {
		if c.ID != "memory_oom" && c.ID != "query_timeout" && c.ID != "memory_unproven" {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			for _, tc := range []struct {
				name, query string
				extra       map[string]any
				want        string
			}{
				{"broad", `{}`, nil, "oom_logs"},
				{"regex", `{device_id="42"} |~ "(?i)(oom|out of memory|killed process)"`, nil, "oom_logs"},
				{"direction_limit", `{}`, map[string]any{"direction": "forward", "limit": 1}, "oom_logs"},
				{"case_sensitive", `{} |= "oom"`, nil, "filtered_logs"},
				{"time_filtered", `{}`, map[string]any{"end": "2026-11-04T06:18:00Z"}, "filtered_logs"},
				{"label_filtered", `{unit="application"}`, nil, "filtered_logs"},
				{"wrong_device", `{}`, map[string]any{"device_id": 43}, "fixture_coverage_gap"},
				{"selector_conflict", `{device_id="43"}`, nil, "fixture_coverage_gap"},
				{"missing_device", `{}`, map[string]any{"device_id": nil}, "fixture_coverage_gap"},
				{"relative_time", `{}`, map[string]any{"end": "now"}, "fixture_coverage_gap"},
				{"reverse_time", `{}`, map[string]any{"end": "2026-11-04T05:00:00Z"}, "fixture_coverage_gap"},
				{"unsupported", `{unit="application"} | json`, nil, "fixture_coverage_gap"},
				{"invalid_regex", `{unit="application",other=~"["}`, nil, "fixture_coverage_gap"},
				{"invalid_selector", `{unit="application" device_id="42"}`, nil, "fixture_coverage_gap"},
				{"invalid_limit", `{}`, map[string]any{"limit": -1}, "fixture_coverage_gap"},
				{"unknown_field", `{}`, map[string]any{"unrecognized": true}, "fixture_coverage_gap"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r, err := newReplay(c, 1)
					if err != nil {
						t.Fatal(err)
					}
					args := map[string]any{"query": tc.query, "device_id": 42, "start": "2026-11-04T05:57:00Z", "end": "2026-11-04T06:31:00Z", "limit": 51}
					for k, v := range tc.extra {
						args[k] = v
					}
					raw, _ := json.Marshal(args)
					tool := &replayTool{info: &basetool.ToolInfo{Name: "query_logql"}, state: r}
					output, err := tool.InvokableRun(context.Background(), string(raw))
					want := tc.want
					if c.ID == "memory_unproven" {
						want = "fixture_coverage_gap"
					} else if c.ID == "query_timeout" && want != "fixture_coverage_gap" {
						want = "timeout_logs"
					}
					if len(r.calls) != 1 {
						t.Fatal("call count", r.calls)
					}
					call := r.calls[0]
					switch want {
					case "fixture_coverage_gap":
						if err == nil || call.Error != want || output != "" {
							t.Fatalf("output=%s err=%v call=%+v", output, err, call)
						}
					case "timeout_logs":
						if !errors.Is(err, context.DeadlineExceeded) || call.Error != "timeout" || call.Fixture != want {
							t.Fatalf("err=%v call=%+v", err, call)
						}
					default:
						if err != nil || call.Fixture != want {
							t.Fatalf("output=%s err=%v call=%+v", output, err, call)
						}
						if strings.Contains(output, "Killed process 901") != (want == "oom_logs") {
							t.Fatal("incorrect log filtering", output)
						}
					}
					if _, err := tool.InvokableRun(context.Background(), string(raw)); !errors.Is(err, errBudget) {
						t.Fatal("fallback bypassed budget", err)
					}
				})
			}
		})
	}
}
