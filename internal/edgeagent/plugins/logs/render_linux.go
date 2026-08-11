//go:build !windows

package logs

import (
	"sort"
	"strings"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// render_linux.go 是 Linux 平台的 promtail config 渲染实现。
// 使用 journald source 作为 system log 采集（systemd 主机的通用日志源）。
// Windows 版本见 render_windows.go（windows_events source）。

// joinRegex builds an OR-regex of the journald unit names (anchored to
// match promtail's relabel_configs.regex semantics)。
// 仅 Linux render 使用（journald_units 过滤），不暴露给 Windows。
func joinRegex(units []string) string {
	if len(units) == 0 {
		return ""
	}
	sorted := append([]string(nil), units...)
	sort.Strings(sorted)
	escaped := make([]string, 0, len(sorted))
	for _, u := range sorted {
		escaped = append(escaped, regexEscape(u))
	}
	return strings.Join(escaped, "|")
}

// regexEscape 转义 promtail relabel_configs.regex 的正则元字符。
// 仅 joinRegex 调用，仅 Linux 编译。
func regexEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '.', '+', '*', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// linuxScrapeSection 是 Linux 专属的 journald scrape 段落。
// 与 promtailBaseSection + promtailFilePathsSection 拼接成完整 config。
const linuxScrapeSection = `scrape_configs:
{{- if .EnableJournald }}
  - job_name: journald
    journal:
      max_age: 12h
      labels:
        ongrid_source: "journald"
    relabel_configs:
      - source_labels: ['__journal__systemd_unit']
        target_label:  'unit'
      - source_labels: ['__journal_syslog_identifier']
        target_label:  'identifier'
{{- if .JournaldUnits }}
      - source_labels: ['__journal__systemd_unit']
        regex:         '{{ .JournaldUnitsRegex }}'
        action:        'keep'
{{- end }}
      - source_labels: ['__journal_priority']
        target_label:  'level'
{{- end }}
` + promtailFilePathsSection

// linuxConfigData 嵌入共享基类 + Linux 专属字段。
type linuxConfigData struct {
	promtailConfigData
	EnableJournald     bool
	JournaldUnits      []string
	JournaldUnitsRegex string
}

// render builds promtail.yaml bytes from a PluginConfig on Linux hosts.
// Spec keys:
//	enable_journald : bool (default TRUE — systemd-journald is universal on
//	                          systemd hosts and self-rotating; set false to
//	                          opt out, which falls back to syslog file tail)
//	journald_units : []string (default all units when journald enabled)
//	file_paths : []string (default empty; add app-specific log files here)
//	extra_labels : map[string]string (allow-list policed by manager)
func render(cfg plugins.PluginConfig) ([]byte, error) {
	common, err := buildCommonConfigData(cfg)
	if err != nil {
		return nil, err
	}

	// Journald is the universal default: systemd-journald is always running
	// on systemd hosts, whereas rsyslog / /var/log/syslog is NOT guaranteed
	// (absent on Arch, Alpine, minimal cloud images, containers). It
	// self-rotates (journald.conf SystemMaxUse) and tags every entry with
	// its systemd unit, so services are cleanly separable by the `unit`
	// label. Operators opt out with spec.enable_journald=false, or add
	// file_paths for app-specific log files (e.g. nginx access logs).
	enableJournald := true
	if v, ok := cfg.Spec["enable_journald"]; ok {
		if b, ok := v.(bool); ok {
			enableJournald = b
		}
	}

	units := stringSlice(cfg.Spec, "journald_units")
	filePaths := common.FilePaths
	// Fallback only when the operator explicitly turned journald OFF and
	// set no file paths: tail the universal syslog files so the plugin
	// still emits at least one scrape job (a config with zero jobs = silent
	// empty Loki, which reads as "RAG/logs broke"). With journald on by
	// default this branch is normally skipped.
	if len(filePaths) == 0 && !enableJournald {
		filePaths = []string{"/var/log/syslog", "/var/log/messages"}
	}

	data := linuxConfigData{
		promtailConfigData:  common,
		EnableJournald:      enableJournald,
		JournaldUnits:       units,
		JournaldUnitsRegex:  joinRegex(units),
	}
	data.FilePaths = filePaths

	return executeTemplate("promtail-linux", promtailBaseSection+linuxScrapeSection, data)
}
