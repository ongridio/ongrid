package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// toolMemo is a per-run cache of identical (read-tool, args) calls. The
// graph — and therefore the wrapped tool set — is rebuilt per request, so
// one memo per WrapBaseTools call is naturally scoped to a single ReAct
// run. Within that run an identical call (same tool, byte-identical args,
// seconds apart) cannot yield new information and is almost always a ReAct
// loop artifact; returning the prior result skips re-executing an expensive
// tool (SSH probe, PromQL, LLM-backed query_translate) and keeps an
// identical-call loop from burning the iteration budget on real work. Only
// Class=="read" tools are memoized — write/destructive tools never touch
// this path, so the review/mutation flow is unaffected.
type toolMemo struct {
	mu     sync.Mutex
	m      map[string]string // (tool\x00args) -> result, identical-call cache
	counts map[string]int    // tool name -> distinct executions this run
}

func newToolMemo() *toolMemo {
	return &toolMemo{m: make(map[string]string), counts: make(map[string]int)}
}

func (t *toolMemo) get(k string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.m[k]
	return v, ok
}

func (t *toolMemo) put(k, v string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[k] = v
}

// count returns how many real executions of the named tool have happened
// this run; bump records one more.
func (t *toolMemo) count(name string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[name]
}

func (t *toolMemo) bump(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts[name]++
}

// maxToolCallsPerRun caps how many times any one tool may EXECUTE within a
// single agent run. Identical-arg repeats are served from the memo and don't
// count; this catches the other failure mode — the model calling the same
// tool many times with slightly different args (e.g. query_promql across a
// dozen metrics, query_alert_rules over and over) without converging. Past
// the cap the tool returns a "synthesize now" directive instead of running,
// which forces the agent to answer from what it already gathered. Generous
// enough that normal multi-step investigation isn't clipped.
const maxToolCallsPerRun = 8

func maxCallsForTool(name string) int {
	switch name {
	case "draft_config_change", "list_metric_catalog":
		return 1
	default:
		return maxToolCallsPerRun
	}
}

func countFailedToolCall(name string) bool {
	switch name {
	case "draft_config_change":
		return false
	default:
		return true
	}
}

// toolBudgetExceeded is the synthetic tool result returned once a tool hits
// maxToolCallsPerRun. Shaped like a normal JSON tool result so the LLM reads
// it as data and (re)directs to answering.
func toolBudgetExceeded(name string, n int) string {
	b, _ := json.Marshal(map[string]any{
		"status":      "call_budget_exceeded",
		"tool":        name,
		"calls":       n,
		"instruction": fmt.Sprintf("You have already called %q %d times this turn — that is the per-tool limit. Do NOT call it again. Answer the user from the results you already gathered; if they're insufficient, state specifically what is missing and ask the user.", name, n),
	})
	return string(b)
}

// WrapBaseTool adapts an ongrid basetool.BaseTool to eino's
// components/tool.BaseTool + InvokableTool surface so the eino ToolsNode
// can dispatch to it. PR-3's basetool was deliberately mirror-shaped
// against eino (see basetool.go header comment), so this adapter is
// thin: Info is a 1-1 field copy and InvokableRun forwards the args
// JSON verbatim.
//
// graph 执行层 ToolsNode 接收的是 eino
// tool.BaseTool；本 adapter 是仓库自家 BaseTool 与 eino 之间唯一胶水点。
//
// Per-call options (tenant / user / device id) ride on
// `basetool.InvokeOption` slots; eino's `tool.Option` system carries an
// impl-specific bag for them — see WithInvokeOpts. If the caller does
// not pass any impl-specific options the inner tool runs with its
// decorator-resolved defaults (the typical path).
func WrapBaseTool(t basetool.BaseTool) einotool.InvokableTool {
	if t == nil {
		return nil
	}
	return &einoToolAdapter{inner: t}
}

// einoInvokeOptKey is the internal carrier for ongrid InvokeOptions
// passed through eino's `tool.Option` slot. Unexported so callers
// route through WithInvokeOpts.
type einoInvokeOptKey struct {
	opts []basetool.InvokeOption
}

// WithInvokeOpts is the eino-side option helper that carries
// basetool.InvokeOption into a ToolsNode call. The graph wiring layer
// (PR-N chatruntime) will use this to thread per-request tenant / user
// id through the graph runtime down to each tool's InvokableRun call.
//
// Usage from a graph client:
//
//	runnable.Invoke(ctx, in, compose.WithToolsNodeOption(
//	    compose.WithToolOption(graph.WithInvokeOpts(
//	        basetool.WithUserID(uid),
//	        basetool.WithTenant(tenantID),
//	),
//	)
func WithInvokeOpts(opts ...basetool.InvokeOption) einotool.Option {
	return einotool.WrapImplSpecificOptFn(func(k *einoInvokeOptKey) {
		k.opts = append(k.opts, opts...)
	})
}

// einoToolAdapter wraps a basetool.BaseTool to satisfy eino's
// InvokableTool interface. The struct is intentionally trivial — all
// real behaviour (tenant/audit/timeout/ratelimit/metric) lives in the
// PR-3 decorator chain wrapped *around* the inner tool *before* it
// reaches this adapter.
type einoToolAdapter struct {
	inner basetool.BaseTool
	// memo is the per-run identical-call cache (nil = memoization off, e.g.
	// the single-tool WrapBaseTool path used by tests). Shared across all
	// adapters built by one WrapBaseTools call.
	memo *toolMemo
	// Info() is resolved once (name + read-ness) for the memo key + gate.
	infoOnce  sync.Once
	cacheName string
	cacheable bool // Class == "read"
}

// resolveInfo lazily caches the tool's name + whether it's a pure-read tool
// (the only class we memoize). Info() is otherwise called by eino at build
// time; caching here avoids a call per dispatch.
func (a *einoToolAdapter) resolveInfo(ctx context.Context) {
	a.infoOnce.Do(func() {
		if info, err := a.inner.Info(ctx); err == nil && info != nil {
			a.cacheName = info.Name
			a.cacheable = info.Class == "read"
		}
	})
}

// Info returns the eino schema.ToolInfo for this tool. WhenToUse from
// our extended ToolInfo is appended to the description (with a
// "When to use:" prefix) so the LLM sees both halves through the
// standard schema field. — Tool 层 description vs
// when_to_use 拆分。
func (a *einoToolAdapter) Info(ctx context.Context) (*schema.ToolInfo, error) {
	if a == nil || a.inner == nil {
		return nil, fmt.Errorf("graph: tool adapter has nil inner tool")
	}
	info, err := a.inner.Info(ctx)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("graph: tool returned nil ToolInfo")
	}
	desc := info.Description
	if info.WhenToUse != "" {
		if desc != "" {
			desc = desc + "\n\nWhen to use: " + info.WhenToUse
		} else {
			desc = "When to use: " + info.WhenToUse
		}
	}
	out := &schema.ToolInfo{
		Name: info.Name,
		Desc: desc,
	}
	if len(info.Parameters) > 0 {
		// Preserve the existing JSON-Schema bytes verbatim by re-parsing
		// into eino's jsonschema.Schema. PR-3's basetool.ToolInfo carries
		// the schema as raw JSON; eino's ParamsOneOf wants a typed
		// *jsonschema.Schema, so we deserialize. A failure here means the
		// upstream tool produced invalid JSON Schema — bubble it as an
		// error so the graph build refuses to compile.
		js := &jsonschema.Schema{}
		if err := json.Unmarshal(info.Parameters, js); err != nil {
			return nil, fmt.Errorf("graph: tool %q: parse parameters JSON Schema: %w", info.Name, err)
		}
		out.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(js)
	}
	return out, nil
}

// InvokableRun forwards to the inner basetool.BaseTool. Per-call
// InvokeOptions are extracted from the eino tool.Option bag if the
// caller used WithInvokeOpts.
//
// **Tool errors are converted to a JSON envelope, never returned as a
// Go error.** Eino's ToolsNode treats Go-level errors as graph-fatal
// (terminates the whole invoke + SSE stream); ongrid's invariant is
// "tool failures are facts the LLM can recover from" — the LLM should
// see the error text as a tool result and decide to retry / switch /
// ask the user, NOT have the conversation aborted. We mirror what the
// legacy agent.go for-loop did: marshal err into a result-shaped JSON
// like {"error": "..."} so the LLM consumes it as data.
//
// True nil-receiver / unrecoverable bugs (we wrote the wrong inner)
// still surface as Go error so eino can panic-loud, since those are
// not user-fixable.
func (a *einoToolAdapter) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...einotool.Option) (string, error) {
	if a == nil || a.inner == nil {
		return "", fmt.Errorf("graph: tool adapter has nil inner tool")
	}
	var memoKey string
	if a.memo != nil {
		a.resolveInfo(ctx)
		// 1. Identical-call memo (read tools only): a byte-identical repeat
		//    returns the prior result without re-executing.
		if a.cacheable && a.cacheName != "" {
			memoKey = a.cacheName + "\x00" + argumentsInJSON
			if cached, ok := a.memo.get(memoKey); ok {
				return cached, nil
			}
		}
		// 2. Per-tool execution cap (all tools): once a tool has run
		//    maxToolCallsPerRun times this run, stop executing it and hand
		//    back a "synthesize now" directive. Catches the distinct-args
		//    repeat loop (query_promql/query_alert_rules called over and over)
		//    that the memo can't.
		if a.cacheName != "" && a.memo.count(a.cacheName) >= maxCallsForTool(a.cacheName) {
			return toolBudgetExceeded(a.cacheName, a.memo.count(a.cacheName)), nil
		}
	}
	resolved := einotool.GetImplSpecificOptions(&einoInvokeOptKey{}, opts...)
	out, err := a.inner.InvokableRun(ctx, argumentsInJSON, resolved.opts...)
	if err != nil {
		// Count most failures toward the cap so a failing tool cannot be
		// hammered. draft_config_change is the exception: validation failures
		// are common while the model corrects structured config args, and only
		// a successful draft should consume the one-draft-per-turn budget.
		if a.memo != nil && a.cacheName != "" && countFailedToolCall(a.cacheName) {
			a.memo.bump(a.cacheName)
		}
		// Re-shape as a tool-result-style JSON so the LLM gets it as a
		// message instead of having the graph terminate. Truncate long
		// errors so we don't blow the context window with stack traces.
		msg := err.Error()
		const cap = 2048
		if len(msg) > cap {
			msg = msg[:cap] + "...(truncated)"
		}
		envelope, mErr := json.Marshal(map[string]any{
			"error":  msg,
			"status": "failed",
		})
		if mErr != nil {
			// Marshal of a string + status into a 2-key map should be
			// infallible; if it isn't, fall back to the original error.
			return "", err
		}
		return string(envelope), nil
	}
	// Count this successful real execution toward the per-tool cap.
	if a.memo != nil && a.cacheName != "" {
		a.memo.bump(a.cacheName)
	}
	// Cache successful read-tool results only — a failed call stays
	// retryable (a transient error shouldn't be pinned for the whole run).
	if memoKey != "" {
		a.memo.put(memoKey, out)
	}
	return out, nil
}

// WrapBaseTools is the slice-flavoured WrapBaseTool. Returns a slice of
// eino tool.BaseTool ready to feed into compose.ToolsNodeConfig.Tools.
// Nil entries in the input are skipped so callers can pass a sparse
// list (e.g. from a skill activation filter).
func WrapBaseTools(tools []basetool.BaseTool) []einotool.BaseTool {
	// One memo shared by every tool in this build = scoped to one run
	// (the graph is rebuilt per request). The single-tool WrapBaseTool
	// path deliberately leaves memo nil (tests / non-graph callers).
	memo := newToolMemo()
	out := make([]einotool.BaseTool, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		out = append(out, &einoToolAdapter{inner: t, memo: memo})
	}
	return out
}
