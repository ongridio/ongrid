//go:build windows

package logs

import (
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// TestRenderWindows_HappyPath_ProducesWindowsEventsSource 验证默认配置
// 渲染出含 windows_events source 的 promtail config（替代 Linux 的 journald）。
// 对称 Linux TestRenderHappyPath，断言 Windows 特有字段。
func TestRenderWindows_HappyPath_ProducesWindowsEventsSource(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   42,
		Endpoint: "https://manager.example.com/loki/api/v1/push",
		AuthUser: "ak-edge42",
		AuthPass: "sk-secret",
		Spec: map[string]interface{}{
			"event_sources": []interface{}{"Application", "System"},
			"event_type":    "warning",
			"extra_labels":  map[string]interface{}{"service": "edge", "env": "test"},
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)

	for _, want := range []string{
		`url: "https://manager.example.com/loki/api/v1/push"`,
		"basic_auth:",
		`username: "ak-edge42"`,
		`password: "sk-secret"`,
		`device_id: "42"`,
		`service: "edge"`,
		`env: "test"`,
		"job_name: windows_events",
		"windows_events:",
		"event_type: warning",
		`- "Application"`,
		`- "System"`,
		`ongrid_source: "windows_events"`,
		"bookmark", // bookmark path 必须出现在 config 中
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered config missing %q\n--- full body ---\n%s", want, body)
		}
	}

	// Windows config 绝不能出现 journald source
	if strings.Contains(body, "journal") {
		t.Errorf("windows render must NOT emit journald source:\n%s", body)
	}
}

// TestRenderWindows_RejectsMissingEndpoint 对称 Linux TestRenderRejectsMissingEndpoint。
func TestRenderWindows_RejectsMissingEndpoint(t *testing.T) {
	cfg := plugins.PluginConfig{Enabled: true, EdgeID: 1}
	if _, err := render(cfg); err == nil {
		t.Errorf("render must reject missing endpoint")
	}
}

// TestRenderWindows_RejectsMissingEdgeID 对称 Linux TestRenderRejectsMissingEdgeID。
func TestRenderWindows_RejectsMissingEdgeID(t *testing.T) {
	cfg := plugins.PluginConfig{Enabled: true, Endpoint: "https://x/loki/api/v1/push"}
	if _, err := render(cfg); err == nil {
		t.Errorf("render must reject missing edge_id")
	}
}

// TestRenderWindows_DefaultEventType 验证 event_type 未设时默认 information
// （promtail windows_events 的最低事件级别 — 采集 information+warning+error）。
func TestRenderWindows_DefaultEventType(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://x/loki/api/v1/push",
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(out), "event_type: information") {
		t.Errorf("default event_type should be 'information':\n%s", string(out))
	}
}

// TestRenderWindows_DisableWindowsEvents 对称 Linux TestRenderEnableJournaldFalse。
// enable_windows_events=false 时 windows_events block 必须消失。
func TestRenderWindows_DisableWindowsEvents(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://x/loki/api/v1/push",
		Spec: map[string]interface{}{
			"enable_windows_events": false,
			"file_paths":            []interface{}{`C:\logs\app.log`},
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	if strings.Contains(body, "job_name: windows_events") {
		t.Errorf("windows_events block should be omitted when enable_windows_events=false:\n%s", body)
	}
	if !strings.Contains(body, `C:\logs\app.log`) {
		t.Errorf("file path missing from rendered config")
	}
}

// TestRenderWindows_FilePathsWorkAlongsideWindowsEvents 验证 windows_events
// 与 file_paths 可以共存（file tail 是跨平台功能，不依赖 OS source）。
func TestRenderWindows_FilePathsWorkAlongsideWindowsEvents(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://x/loki/api/v1/push",
		Spec: map[string]interface{}{
			"file_paths": []interface{}{`C:\var\log\app.log`},
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	if !strings.Contains(body, "job_name: windows_events") {
		t.Errorf("windows_events should be on by default:\n%s", body)
	}
	if !strings.Contains(body, "job_name: 'file-C--var-log-app-log'") {
		// Windows 路径 C:\var\... → jobNameSafe 把 ':' 和 '\' 各转成 '-'，
		// 序列 ':\' 产生 '--'（双 dash，区别于 Linux /var/log 的单 dash）。
		t.Errorf("file path job missing from rendered config:\n%s", body)
	}
}

// TestRenderWindows_SingleClient 验证只有一个 clients[] entry（对称 Linux）。
func TestRenderWindows_SingleClient(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://manager.example.com/loki/api/v1/push",
		AuthUser: "ak",
		AuthPass: "sk",
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	if strings.Count(body, "- url: ") != 1 {
		t.Errorf("expected exactly one clients[] entry, got body:\n%s", body)
	}
}

// TestRenderWindows_Adversarial_LabelValueInjection 验证 extra_labels 的
// value 含双引号 + 换行时不能突破 YAML 双引号字符串（防止注入新 YAML key）。
// payload: `evil"\n  malicious: "injected` — 尝试关闭引号后注入新字段。
func TestRenderWindows_Adversarial_LabelValueInjection(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://x/loki/api/v1/push",
		Spec: map[string]interface{}{
			"extra_labels": map[string]interface{}{
				"service": "evil\"\n  malicious: \"injected",
			},
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)

	// payload 的双引号必须被转义（\"），不能裸出现
	// 若 value 突破了双引号边界，body 会含未转义的 `malicious: "injected"` 作为新 YAML key
	if strings.Contains(body, `malicious: "injected"`) {
		t.Errorf("ADVERSARIAL: label value injection succeeded — YAML key 'malicious' was injected:\n%s", body)
	}
}

// TestRenderWindows_Adversarial_EndpointInjection 验证 endpoint URL 含
// 双引号 / 换行时不能突破 YAML 结构。yamlDoubleQuote 会将 \n 转义为字面
// 反斜杠-n，使整个 URL 保持在一行内的双引号字符串中。
func TestRenderWindows_Adversarial_EndpointInjection(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://x/loki/api/v1/push\"\n  malicious_field: bad",
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)

	// 逐行扫描：malicious_field 只能出现在 url 行内（双引号字符串内），
	// 不能作为独立 YAML key 出现在行首。
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "malicious_field") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "- url:") {
				t.Errorf("ADVERSARIAL: endpoint injection created standalone YAML key on line %q:\n%s", trimmed, body)
			}
		}
	}
}

// TestRenderWindows_RejectsInvalidEventType 验证 event_type 白名单验证。
// promtail 仅接受 information / warning / error，非法值会被拒绝（防注入）。
func TestRenderWindows_RejectsInvalidEventType(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://x/loki/api/v1/push",
		Spec: map[string]interface{}{
			"event_type": "information\nmalicious: injected",
		},
	}
	if _, err := render(cfg); err == nil {
		t.Errorf("render must reject invalid event_type (potential YAML injection)")
	}
}

// TestRenderWindows_Adversarial_EventSourceInjection 验证 event_sources
// 元素含 YAML 特殊字符时不会破坏 config 结构（sources 列表项注入）。
// yamlDoubleQuote 将 \n 转义为字面反斜杠-n，使恶意 payload 留在双引号字符串内。
func TestRenderWindows_Adversarial_EventSourceInjection(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://x/loki/api/v1/push",
		Spec: map[string]interface{}{
			"event_sources": []interface{}{
				"Application\nmalicious: injected",
			},
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)

	// 逐行扫描：malicious 只能出现在 sources 列表项行内（双引号字符串内），
	// 不能作为独立 YAML key 出现在行首。
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "malicious") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "- ") {
				t.Errorf("ADVERSARIAL: event_source injection created standalone YAML key on line %q:\n%s", trimmed, body)
			}
		}
	}
}
