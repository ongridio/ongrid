//go:build windows

package logs

import (
	"fmt"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// render_windows.go 是 Windows 平台的 promtail config 渲染实现。
// 使用 windows_events source 采集 Windows Event Log（替代 Linux 的 journald）。
// 决策依据： PL4 分层对等（第一批 promtail + windows_exporter）。
// promtail Windows 版的 windows_events source 直接订阅 Windows Event Log，
// 通过 bookmark 文件记录读取位置，重启后从断点续读。

// bookmarkFileName 是 promtail windows_events source 的 bookmark 检查点文件。
// 相对路径 —— promtail 的 CWD = SubprocessPlugin.workDir（见 subprocess.go:232
// `cmd.Dir = s.workDir`），等价于 <pluginWorkDir>/windows_events.bookmark。
// 与 positions.yaml（promtail CLI -positions.file 指定）同目录。
const bookmarkFileName = "./windows_events.bookmark"

// defaultEventType 是默认采集的最低 Windows 事件级别。
// information = 采集 information + warning + error（最全）。
// 可通过 spec.event_type 覆盖为 "warning" 或 "error"（更高级别 = 更少日志量）。
const defaultEventType = "information"

// windowsScrapeSection 是 Windows 专属的 windows_events scrape 段落。
// 与 promtailBaseSection + promtailFilePathsSection 拼接成完整 config。
const windowsScrapeSection = `scrape_configs:
{{- if .EnableWindowsEvents }}
  - job_name: windows_events
    windows_events:
      path: {{ .BookmarkPath | yamlDoubleQuote }}
      event_type: {{ .EventType }}
      {{- if .EventSources }}
      sources:
        {{- range .EventSources }}
        - {{ . | yamlDoubleQuote }}
        {{- end }}
      {{- end }}
      labels:
        ongrid_source: "windows_events"
{{- end }}
` + promtailFilePathsSection

// windowsConfigData 嵌入共享基类 + Windows 专属字段。
type windowsConfigData struct {
	promtailConfigData
	EnableWindowsEvents bool
	EventType           string
	EventSources        []string
	BookmarkPath        string
}

// render builds promtail.yaml bytes from a PluginConfig on Windows hosts.
// Spec keys:
//	enable_windows_events : bool (default TRUE — Windows Event Log 是 Windows
//	                                 主机的通用系统日志源，对称 Linux 的 journald)
//	event_type : string (default "information"；可选 "warning" / "error"
//	                    控制最低采集级别，越高级别日志量越少)
//	event_sources : []string (default empty = all sources；常见值 Application /
//	                        System / Security / Setup / ForwardedEvents)
//	file_paths : []string (default empty; add app-specific log files here)
//	extra_labels : map[string]string (allow-list policed by manager)
func render(cfg plugins.PluginConfig) ([]byte, error) {
	common, err := buildCommonConfigData(cfg)
	if err != nil {
		return nil, err
	}

	// windows_events 是 Windows 的通用默认 source —— 对称 Linux journald。
	// Windows Event Log 在所有 Windows Server 版本（Desktop Experience +
	// Server Core）都完整可用，是 RCA 的第二证据源（仅次于实时指标）。
	enableWindowsEvents := true
	if v, ok := cfg.Spec["enable_windows_events"]; ok {
		if b, ok := v.(bool); ok {
			enableWindowsEvents = b
		}
	}

	// Fallback：当 windows_events 被显式关闭且无 file_paths 时，tail 通用
	// Windows 日志文件（对称 Linux 的 /var/log/syslog fallback 分支）。
	filePaths := common.FilePaths
	if len(filePaths) == 0 && !enableWindowsEvents {
		filePaths = []string{`C:\Windows\System32\LogFiles\`}
	}

	eventType := defaultEventType
	if v, ok := cfg.Spec["event_type"].(string); ok && v != "" {
		if !isValidEventType(v) {
			return nil, fmt.Errorf("logs plugin: invalid event_type %q (allowed: information, warning, error)", v)
		}
		eventType = v
	}

	data := windowsConfigData{
		promtailConfigData:  common,
		EnableWindowsEvents: enableWindowsEvents,
		EventType:           eventType,
		EventSources:        stringSlice(cfg.Spec, "event_sources"),
		BookmarkPath:        bookmarkFileName,
	}
	data.FilePaths = filePaths

	return executeTemplate("promtail-windows", promtailBaseSection+windowsScrapeSection, data)
}

// isValidEventType 校验 promtail windows_events.event_type 白名单。
// promtail 仅接受这三个值（对应 Windows 事件级别最低阈值）。
// 白名单验证比转义更强 —— 直接拒绝非法输入，防止 event_type 字段注入。
func isValidEventType(s string) bool {
	switch s {
	case "information", "warning", "error":
		return true
	}
	return false
}
