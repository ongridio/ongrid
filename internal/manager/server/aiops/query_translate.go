// query_translate.go — POST /v1/aiops/query-translate, the
// natural-language → LogQL/TraceQL/PromQL helper.
//
// UX contract: this is a *helper*, not a hard dependency. The SPA's
// query pages stay fully usable when this endpoint is unavailable;
// translate failures don't block the user. Front-end conventions:
//   - Show a ✨ button next to the main query input
//   - Click → popover, user types Chinese / English, hits 翻译
//   - Result populates the main query box (does NOT auto-submit)
//   - User reviews and edits before running
//
// Backend protections:
//   - 120-second timeout — project-wide unification floor for any LLM
//     call (see internal/pkg/llm/client.go::defaultTimeout). Originally
//     6 s tuned for a Haiku-class default; bumped to 20 s once the
//     cluster default moved to DeepSeek; finally unified at 120 s with
//     the rest of the LLM-call sites so a slow reasoning model can
//     finish a tool-rich turn. Still short enough that a stuck request
//     gives up on a human-grade timescale.
//   - Force JSON-only output via system prompt + parse with leniency
//     (strip ```json fences, trim whitespace) so a chatty model still
//     produces something usable.
//   - Whitelist dialect; no fallthrough to arbitrary LLM chat.
package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

const queryTranslateTimeout = 120 * time.Second

type queryTranslateReq struct {
	// Dialect: "logql" | "traceql" | "promql".
	Dialect string `json:"dialect"`
	// Prompt: user's natural-language query.
	Prompt string `json:"prompt"`
	// Optional context the SPA may attach (e.g. selected device, time
	// window) so the model produces a more concrete query. Free-form
	// JSON; the system prompt rewrites it as a hint block.
	Context map[string]any `json:"context,omitempty"`
}

type queryTranslateResp struct {
	Query       string `json:"query"`
	Explanation string `json:"explanation,omitempty"`
	Dialect     string `json:"dialect"`
}

// dialectGuide carries the per-dialect prompt scaffolding. Keeping it
// in code (not a settings table) is intentional: shipping dialect
// rules with the binary means the helper works on a fresh deploy
// without admin intervention.
var dialectGuide = map[string]string{
	"logql": `LogQL（Loki 查询语言）。规则：
- 必须以 stream selector 开头：{label="value"} 或 {label=~"regex"}
- ongrid 标签：device_id (string数字), service, level, unit (systemd unit), filename, host
- 行过滤：|= "substring" 或 |~ "regex"
- 反向过滤：!= 或 !~
- 多个匹配用 | 串联
- 严禁随意编造标签，只用上面列出的
示例：
"dev-host-3 最近 error 日志" → {device_id="3"} |~ "(?i)error"
"sshd 服务的失败登录" → {unit="sshd.service"} |~ "(Failed|invalid)"
"OOM 杀掉的进程" → {} |~ "(Out of memory|OOM|invoked oom-killer)"`,

	"traceql": `TraceQL（Tempo 查询语言）。规则：
- 必须用 {} 包裹，过滤 span 属性
- 支持 resource.service.name / span.http.status_code / span.http.method / span.name / status
- duration 比较：duration > 1s / duration < 100ms
- 多过滤逻辑：{ a && b }
- 严禁编造没有 ongrid 实际有的标签
示例：
"超过 1 秒的 API 调用" → {span.http.status_code != 0 && duration > 1s}
"出错的 trace" → {status=error}
"k8s 服务里慢的请求" → {resource.service.name="k8s" && duration > 500ms}`,

	"promql": `PromQL（Prometheus 查询语言）。规则：
- 用 ongrid 已有的 metric：node_cpu_seconds_total, node_memory_MemAvailable_bytes,
  node_filesystem_avail_bytes, node_load1, node_network_*_bytes_total,
  node_disk_*_bytes_total, host_cpu_pct, host_mem_pct, host_disk_used_pct, ongrid_edge_status
- by/without 聚合：sum by (device_id) / avg by (host)
- 速率：rate(metric[5m])
- 严禁编造 metric 名
- device_id 是字符串数字（"3" 不是 3）
示例：
"主机 3 的 CPU 使用率" → 100 - (avg by(device_id) (rate(node_cpu_seconds_total{mode="idle",device_id="3"}[5m])) * 100)
"内存使用率最高的 5 台" → topk(5, host_mem_pct)
"磁盘使用率超过 80% 的设备" → host_disk_used_pct > 80`,
}

func (h *Handler) queryTranslate(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	if h.llmClient == nil {
		// 503 with a clean message — SPA falls back to "AI 不可用，
		// 直接打字" without a popup error.
		http.Error(w, "llm client not configured", http.StatusServiceUnavailable)
		return
	}
	var req queryTranslateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	dialect := strings.TrimSpace(strings.ToLower(req.Dialect))
	guide, ok := dialectGuide[dialect]
	if !ok {
		writeErr(w, fmt.Errorf("%w: unsupported dialect %q (want logql|traceql|promql)", errs.ErrInvalid, req.Dialect))
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeErr(w, fmt.Errorf("%w: prompt required", errs.ErrInvalid))
		return
	}

	systemPrompt := "你是一个查询语言专家，把用户的自然语言转成精确的 " + strings.ToUpper(dialect) +
		"。\n\n" + guide + `

输出 **严格 JSON**，不要 markdown 代码块，shape：
{
  "query": "<具体查询>",
  "explanation": "<≤30 字中文一句话说明>"
}

只输出 JSON，不要别的文字。`

	userPrompt := "用户需求：" + prompt
	if len(req.Context) > 0 {
		ctxBlob, _ := json.Marshal(req.Context)
		userPrompt += "\n\n上下文：" + string(ctxBlob)
	}

	ctx, cancel := context.WithTimeout(r.Context(), queryTranslateTimeout)
	defer cancel()

	resp, err := h.llmClient.Chat(ctx, llm.ChatReq{
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.1, // deterministic-ish; we want a precise query
	})
	if err != nil {
		http.Error(w, "llm: "+err.Error(), http.StatusBadGateway)
		return
	}
	parsed, perr := parseTranslateOutput(resp.Assistant.Content)
	if perr != nil {
		// Wrap raw output in the response so the caller can decide
		// whether to surface it; the SPA shows a "翻译失败：..." hint
		// and lets the user click 重试 or just type.
		http.Error(w, "parse: "+perr.Error()+" raw="+truncate(resp.Assistant.Content, 200), http.StatusBadGateway)
		return
	}
	parsed.Dialect = dialect
	writeJSON(w, http.StatusOK, parsed)
}

// parseTranslateOutput strips ```json fences and JSON-decodes, with
// best-effort tolerance for chatty models that wrap the JSON in
// commentary.
var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

func parseTranslateOutput(raw string) (*queryTranslateResp, error) {
	raw = strings.TrimSpace(raw)
	// Strip ```json fences if present.
	if m := fenceRe.FindStringSubmatch(raw); len(m) > 1 {
		raw = m[1]
	}
	// Crop everything before the first { and after the last }.
	if i := strings.IndexByte(raw, '{'); i > 0 {
		raw = raw[i:]
	}
	if j := strings.LastIndexByte(raw, '}'); j >= 0 && j+1 < len(raw) {
		raw = raw[:j+1]
	}
	var out queryTranslateResp
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	out.Query = strings.TrimSpace(out.Query)
	if out.Query == "" {
		return nil, errors.New("empty query in response")
	}
	return &out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
