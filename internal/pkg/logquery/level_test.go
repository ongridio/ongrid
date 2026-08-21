package logquery

import "testing"

func TestDetectLevel(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{name: "json level wins over message text", message: `{"level":"INFO","msg":"error already resolved"}`, want: "info"},
		{name: "json severity", message: `{"severity":"warning","message":"slow"}`, want: "warn"},
		{name: "klog info", message: `I0821 13:51:44.571438 320 scope.go:122] "RemoveContainer"`, want: "info"},
		{name: "klog warning", message: `W0821 05:46:04.063545 1 watcher.go:331] compacted`, want: "warn"},
		{name: "klog error", message: `E0821 13:51:44.572761 320 pod_workers.go:1324] "Error syncing pod"`, want: "error"},
		{name: "klog fatal", message: `F0821 13:51:44.572761 320 server.go:1] stopped`, want: "fatal"},
		{name: "timestamped info", message: `2026/08/21 13:51:48 INFO github.com/example writePkt`, want: "info"},
		{name: "key value warning", message: `time="2026-08-21" level=warning msg="slow"`, want: "warn"},
		{name: "panic", message: `panic: runtime failure`, want: "panic"},
		{name: "unclassified", message: `ongrid-log-probe-abc123`, want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectLevel(tt.message); got != tt.want {
				t.Fatalf("detectLevel(%q) = %q, want %q", tt.message, got, tt.want)
			}
		})
	}
}

func TestSeverityNumberForLevel(t *testing.T) {
	tests := map[string]int32{"trace": 1, "DEBUG": 5, "notice": 9, "warning": 13, "err": 17, "critical": 21, "unknown": 0}
	for level, want := range tests {
		if got := severityNumberForLevel(level); got != want {
			t.Errorf("severityNumberForLevel(%q) = %d, want %d", level, got, want)
		}
	}
}
