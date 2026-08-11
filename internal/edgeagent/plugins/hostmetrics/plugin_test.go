package hostmetrics

import "testing"

// TestName 验证 plugin 名称常量。
func TestName(t *testing.T) {
	if Name != "hostmetrics" {
		t.Fatalf("Name = %q, want %q", Name, "hostmetrics")
	}
}
