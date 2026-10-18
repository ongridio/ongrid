//go:build e2e

// Catalog: T2 — get_host_summary "all edges" (v0.9.0). A flow tool node calling
// get_host_summary with NO device_ids must succeed (summarize all edges, capped)
// rather than fail with "device_ids: must contain at least 1 element". Run with
// an empty fleet → empty envelope, node succeeds. This guards the regression a
// NL-generated workflow hit (the node had no device_ids wired).
package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

func TestFlow_GetHostSummaryAllEdges_T2(t *testing.T) {
	env := testenv.Start(t)
	tok := env.LoginAdmin().AccessToken

	graph := map[string]any{
		"nodes": []any{
			map[string]any{"id": "t", "type": "trigger.manual", "name": "手动触发", "config": map[string]any{}},
			map[string]any{"id": "s", "type": "tool", "name": "全边端摘要", "config": map[string]any{
				"tool": "get_host_summary",
				"args": map[string]any{}, // no device_ids → "all edges" mode
			}},
		},
		"edges": []any{map[string]any{"id": "e1", "source": "t", "target": "s"}},
	}
	status, body, err := env.DoJSON("POST", "/api/v1/flows", map[string]any{
		"name":  "e2e-get-edge-summary-all",
		"graph": graph,
	}, tok)
	if err != nil || status != 201 {
		t.Fatalf("create flow: status=%d err=%v body=%v", status, err, body)
	}
	flowID := numToStr(body["id"])

	status, body, _ = env.DoJSON("POST", fmt.Sprintf("/api/v1/flows/%s/run", flowID), map[string]any{"input": map[string]any{}}, tok)
	if status != 200 && status != 201 && status != 202 {
		t.Fatalf("run flow: status=%d body=%v", status, body)
	}
	runID, _ := body["id"].(string)
	if runID == "" {
		if r, ok := body["run"].(map[string]any); ok {
			runID, _ = r["id"].(string)
		}
	}
	if runID == "" {
		t.Fatalf("no run id in %v", body)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, body, _ := env.DoJSON("GET", "/api/v1/flow-runs/"+runID, nil, tok)
		if status != 200 {
			time.Sleep(400 * time.Millisecond)
			continue
		}
		run, _ := body["run"].(map[string]any)
		st, _ := run["status"].(string)
		if st == "running" || st == "pending" {
			time.Sleep(400 * time.Millisecond)
			continue
		}
		// terminal — the summary node must have SUCCEEDED (empty device_ids is
		// valid now), not failed on the old "must contain at least 1" validation.
		nodes, _ := body["nodes"].([]any)
		ns := flowNodeStatus(nodes, "s")
		if ns != "succeeded" {
			t.Fatalf("get_host_summary node status=%q (want succeeded), run.status=%q, run.error=%v", ns, st, run["error"])
		}
		return
	}
	t.Fatalf("flow run did not reach terminal state in 30s")
}

func flowNodeStatus(nodes []any, nodeID string) string {
	for _, it := range nodes {
		if m, ok := it.(map[string]any); ok {
			if id, _ := m["node_id"].(string); id == nodeID {
				s, _ := m["status"].(string)
				return s
			}
		}
	}
	return ""
}
