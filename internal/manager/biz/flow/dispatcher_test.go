package flow

import (
	"encoding/json"
	"testing"
)

// TestAlertMatches covers the trigger.alert_fired matching gate: optional
// case-insensitive rule substring + optional min-severity rank.
func TestAlertMatches(t *testing.T) {
	cfg := func(rule, minSev string) json.RawMessage {
		b, _ := json.Marshal(alertTriggerConfig{Rule: rule, MinSeverity: minSev})
		return b
	}
	cases := []struct {
		name     string
		cfg      json.RawMessage
		ruleKey  string
		ruleName string
		severity string
		want     bool
	}{
		{"blank config matches anything", json.RawMessage(`{}`), "disk_full", "Disk Full", "warning", true},
		{"nil config matches anything", nil, "cpu_high", "CPU High", "info", true},
		{"rule key substring case-insensitive", cfg("DOWN_JOHN", ""), "redis_down_john", "Redis Down (John)", "warning", true},
		{"rule name substring case-insensitive", cfg("REDIS DOWN", ""), "redis_down_john", "Redis Down (John)", "critical", true},
		{"rule substring miss", cfg("network", ""), "disk_full", "Disk Full", "critical", false},
		{"min severity met", cfg("", "error"), "x", "X", "critical", true},
		{"min severity exact", cfg("", "error"), "x", "X", "error", true},
		{"min severity below", cfg("", "error"), "x", "X", "warning", false},
		{"unknown severity below gate", cfg("", "warning"), "x", "X", "bogus", false},
		{"both gates pass", cfg("disk_full", "error"), "disk_full", "Disk Full", "critical", true},
		{"rule passes severity fails", cfg("disk", "critical"), "disk_full", "Disk Full", "error", false},
	}
	for _, c := range cases {
		if got := alertMatches(c.cfg, c.ruleKey, c.ruleName, c.severity); got != c.want {
			t.Errorf("%s: alertMatches = %v, want %v", c.name, got, c.want)
		}
	}
}
