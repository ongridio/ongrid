package alert

import (
	"encoding/json"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
)

func TestCompileLogMatch_Defaults(t *testing.T) {
	spec, _ := json.Marshal(map[string]any{
		"stream_selector": `{job="varlogs"}`,
		"line_filter":     "(?i)error",
		"threshold":       50.0,
	})
	row := &model.Rule{ID: 1, RuleKey: "log_err", ConditionsJSON: string(spec)}
	r, err := compileLogMatchRule(row)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if r.Window != "5m" {
		t.Errorf("default Window = %q, want 5m", r.Window)
	}
	if r.Operator != ">=" {
		t.Errorf("default Operator = %q, want >=", r.Operator)
	}
	if r.Threshold != 50 {
		t.Errorf("Threshold = %v, want 50", r.Threshold)
	}
}

func TestCompileLogMatch_Errors(t *testing.T) {
	for _, c := range []struct {
		name, spec string
	}{
		{"empty selector", `{}`},
		{"bad operator", `{"stream_selector":"{a=\"b\"}","operator":"~~"}`},
	} {
		row := &model.Rule{RuleKey: "k", ConditionsJSON: c.spec}
		if _, err := compileLogMatchRule(row); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

func TestCompileLogVolume_PreservesLineFilter(t *testing.T) {
	spec, _ := json.Marshal(map[string]any{
		"stream_selector": `{ongrid_source=~"journald(:.*)?",level!="7"}`,
		"line_filter":     "(?i)(error|failed)",
		"window":          "10m",
		"ratio_op":        ">",
		"ratio_threshold": 3.0,
	})
	row := &model.Rule{ID: 2, RuleKey: "log_volume_err", ConditionsJSON: string(spec)}
	r, err := compileLogVolumeRule(row)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if r.LineFilter != "(?i)(error|failed)" {
		t.Fatalf("LineFilter = %q, want content regex", r.LineFilter)
	}
	if q := buildLogMatchQuery(r.StreamSelector, r.LineFilter, r.Window); !strings.Contains(q, `|~ "(?i)(error|failed)"`) {
		t.Fatalf("query = %q, want log volume filter applied", q)
	}
}

func TestCompileTraceLatency_BuildsPromExpr(t *testing.T) {
	spec, _ := json.Marshal(map[string]any{
		"service":      "ongrid-edge",
		"operation":    "tunnel.dial",
		"quantile":     "p99",
		"window":       "5m",
		"threshold_ms": 250.0,
	})
	row := &model.Rule{RuleKey: "edge_p99", ConditionsJSON: string(spec)}
	r, err := compileTraceLatencyRule(row)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	want := []string{
		"histogram_quantile(0.99",
		`service_name="ongrid-edge"`,
		`span_name="tunnel.dial"`,
		"[5m]",
		"* 1000 > 250",
	}
	for _, s := range want {
		if !strings.Contains(r.Expr, s) {
			t.Errorf("expr missing %q\nfull: %s", s, r.Expr)
		}
	}
}

func TestCompileTraceErrorRate_BuildsPromExpr(t *testing.T) {
	spec, _ := json.Marshal(map[string]any{
		"service":       "checkout",
		"window":        "10m",
		"operator":      ">",
		"threshold_pct": 1.5,
	})
	row := &model.Rule{RuleKey: "checkout_err", ConditionsJSON: string(spec)}
	r, err := compileTraceErrorRateRule(row)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, s := range []string{
		`service_name="checkout"`,
		`status_code="STATUS_CODE_ERROR"`,
		"traces_spanmetrics_calls_total",
		"[10m]",
		"> 1.5",
	} {
		if !strings.Contains(r.Expr, s) {
			t.Errorf("expr missing %q\nfull: %s", s, r.Expr)
		}
	}
}

func TestBuildLogMatchQuery(t *testing.T) {
	q := buildLogMatchQuery(`{job="x"}`, "", "")
	if q != `count_over_time({job="x"} [5m])` {
		t.Errorf("default window: %q", q)
	}
	q = buildLogMatchQuery(`{job="x"}`, "(?i)error", "10m")
	if q != `count_over_time({job="x"} |~ "(?i)error" [10m])` {
		t.Errorf("with filter: %q", q)
	}
}
