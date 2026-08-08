// plugin_windows_test.go 是 Windows 专属测试 — 验证 collector 白名单、
// 端口、CLI 参数等 Windows 平台常量。仅在 Windows + L2 CI 上运行。

//go:build windows

package hostmetrics

import (
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// TestCollectorWhitelist_ExactlySevenCollectors 验证 hardcode 的 collector
// 白名单包含预期的 7 个 collector（cs 已在 windows_exporter 0.27+ 移除）。
func TestCollectorWhitelist_ExactlySevenCollectors(t *testing.T) {
	collectors := strings.Split(collectorWhitelist, ",")
	if len(collectors) != 7 {
		t.Fatalf("expected 7 collectors, got %d: %v", len(collectors), collectors)
	}
	expected := map[string]bool{
		"cpu": true, "logical_disk": true, "net": true,
		"os": true, "service": true, "system": true, "process": true,
	}
	for _, c := range collectors {
		c = strings.TrimSpace(c)
		if !expected[c] {
			t.Errorf("unexpected collector: %q", c)
		}
	}
}

// TestDefaultListenAddress_LocalhostOnly 验证监听地址固定 127.0.0.1:9182。
func TestDefaultListenAddress_LocalhostOnly(t *testing.T) {
	if !strings.HasPrefix(defaultListenAddress, "127.0.0.1:") {
		t.Fatalf("listen address must be localhost only, got %q", defaultListenAddress)
	}
	if !strings.HasSuffix(defaultListenAddress, ":9182") {
		t.Fatalf("port must be 9182, got %q", defaultListenAddress)
	}
}

// TestNew_ReturnsNonNil 验证 New 返回有效的 Plugin。
func TestNew_ReturnsNonNil(t *testing.T) {
	p := New("C:\\test\\bin", "C:\\test\\work", nil)
	if p == nil {
		t.Fatal("New() returned nil")
	}
	if p.Name() != Name {
		t.Fatalf("Name() = %q, want %q", p.Name(), Name)
	}
}

// TestNew_HealthInitiallyStopped 验证新 plugin 状态为 stopped。
func TestNew_HealthInitiallyStopped(t *testing.T) {
	p := New("C:\\test\\bin", "C:\\test\\work", nil)
	h := p.HealthSnapshot()
	if h.State != plugins.StateStopped {
		t.Fatalf("initial state = %q, want %q", h.State, plugins.StateStopped)
	}
}
