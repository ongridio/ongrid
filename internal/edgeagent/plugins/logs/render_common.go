package logs

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// 本文件包含 promtail config 渲染的跨平台共享逻辑（helpers + 模板片段 +
// 共享 data 结构 + 模板执行器）。平台特定的 render 函数见：
//   - render_linux.go   （//go:build !windows — journald source）
//   - render_windows.go （//go:build windows — windows_events source）
// DRY 原则：server / clients / external_labels / file_paths scrape 段落
// 在两个平台完全一致，提取为常量供两个 render 函数组合使用。

// promtailBaseSection 是 server + clients + external_labels 段落，
// 两个平台完全一致。platform render 函数将其与各自的 scrape_configs
// 段落拼接成完整 config。
// 安全：ExtraLabels 的 key 和 value、Endpoint、AuthUser、AuthPass 都
// 通过 yamlDoubleQuote 渲染，防止 YAML 注入（adversarial 测试覆盖）。
const promtailBaseSection = `# Rendered by ongrid-edge logs plugin.
# DO NOT EDIT — regenerated from manager-pushed PluginConfig on every reconcile.

server:
  disable: true   # No HTTP API needed; we only push.

clients:
  - url: {{ .Endpoint | yamlDoubleQuote }}
    {{- if .AuthUser }}
    basic_auth:
      username: {{ .AuthUser | yamlDoubleQuote }}
      password: {{ .AuthPass | yamlDoubleQuote }}
    {{- else if .AuthPass }}
    bearer_token: {{ .AuthPass | yamlDoubleQuote }}
    {{- end }}
    {{- if .TLSInsecureSkipVerify }}
    tls_config:
      # Self-signed cert. The standard install ships a self-signed
      # manager cert (deploy/install/upgrade.sh); operators with a real
      # cert can disable via spec.tls_insecure_skip_verify=false.
      insecure_skip_verify: true
    {{- end }}
    tenant_id: ongrid
    backoff_config:
      min_period: 500ms
      max_period: 1m
      max_retries: 10
    batchsize: 1048576
    batchwait: 1s
    external_labels:
      device_id: {{ .EdgeID | yamlDoubleQuote }}
      {{- range $k, $v := .ExtraLabels }}
      {{ $k | yamlKey }}: {{ $v | yamlDoubleQuote }}
      {{- end }}

`

// promtailFilePathsSection 是 file tail scrape 段落，两个平台一致。
// 与平台特定的 system source（journald / windows_events）组合使用。
const promtailFilePathsSection = `
{{- range .FilePaths }}
  - job_name: 'file-{{ . | jobNameSafe }}'
    static_configs:
      - targets: [localhost]
        labels:
          ongrid_source: 'file:{{ . }}'
          job:           'file'
          __path__:      '{{ . }}'
{{- end }}
`

// promtailConfigData 是共享的渲染数据基类。两个平台的 render 函数
// 将其嵌入各自的 platform-specific struct（journaldUnits / eventSources 等）。
type promtailConfigData struct {
	Endpoint              string
	AuthUser              string
	AuthPass              string
	EdgeID                uint64
	ExtraLabels           map[string]string
	TLSInsecureSkipVerify bool
	FilePaths             []string
}

// buildCommonConfigData 从 PluginConfig 提取跨平台共享字段，并执行
// 两平台都需要的前置验证（endpoint / device_id 非空）。
func buildCommonConfigData(cfg plugins.PluginConfig) (promtailConfigData, error) {
	if cfg.Endpoint == "" {
		return promtailConfigData{}, fmt.Errorf("logs plugin: endpoint required")
	}
	if cfg.EdgeID == 0 {
		return promtailConfigData{}, fmt.Errorf("logs plugin: device_id required (set ONGRID_EDGE_ID)")
	}

	// Default to skip-verify because the standard install ships a
	// self-signed manager cert (deploy/install/upgrade.sh). Operators
	// who plug in a real cert can set spec.tls_insecure_skip_verify=false.
	tlsInsecure := true
	if v, ok := cfg.Spec["tls_insecure_skip_verify"]; ok {
		if b, ok := v.(bool); ok {
			tlsInsecure = b
		}
	}

	return promtailConfigData{
		Endpoint:              cfg.Endpoint,
		AuthUser:              cfg.AuthUser,
		AuthPass:              cfg.AuthPass,
		EdgeID:                cfg.EdgeID,
		ExtraLabels:           stringMap(cfg.Spec, "extra_labels"),
		TLSInsecureSkipVerify: tlsInsecure,
		FilePaths:             stringSlice(cfg.Spec, "file_paths"),
	}, nil
}

// executeTemplate 解析 + 执行 Go text/template，统一 error wrapping。
func executeTemplate(name, text string, data any) ([]byte, error) {
	tmpl, err := template.New(name).Funcs(template.FuncMap{
		"jobNameSafe":     jobNameSafe,
		"yamlDoubleQuote": yamlDoubleQuote,
		"yamlKey":         yamlKey,
	}).Parse(text)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}

// yamlDoubleQuote 将任意值转为 YAML 双引号标量的安全表示（含外层引号）。
// 防止 label value / endpoint / auth 凭证中含 " 或控制字符导致 YAML 注入。
// YAML 双引号转义规范见 https://yaml.org/spec/1.2.2/#731-single-quoted。
func yamlDoubleQuote(v any) string {
	s := fmt.Sprintf("%v", v)
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// yamlKey 清理 YAML mapping key 中的危险字符（冒号 / 换行 / 引号）。
// YAML key 不适合用双引号包裹（会破坏 template 缩进结构），改为
// 拒绝含危险字符的 key —— 将其替换为下划线，注入测试覆盖此路径。
func yamlKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ':' || r == '\n' || r == '\r' || r == '"' || r == '\'':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// stringSlice extracts a []string from spec[key], tolerating either
// []string or []interface{} JSON-decoded shapes.
func stringSlice(spec map[string]interface{}, key string) []string {
	raw, ok := spec[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func stringMap(spec map[string]interface{}, key string) map[string]string {
	raw, ok := spec[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case map[string]string:
		return v
	case map[string]interface{}:
		out := make(map[string]string, len(v))
		for k, val := range v {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
		return out
	}
	return nil
}

// jobNameSafe turns a file path into a label-safe job name fragment.
func jobNameSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
