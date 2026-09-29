package tools

// mcp_basetool.go — adapts one tool of one external MCP server (HLD-018) into
// an ongrid BaseTool. Wire name: mcp__<server>__<tool>. The runtime bolts
// these onto the toolbag at boot after connecting each enabled server. A
// trusted server's tools run synchronously; otherwise the call is queued to
// the human approval inbox (same propose-confirm model as cloud_bash).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/compose"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// MCPCaller runs an MCP tool synchronously (trusted-server path).
type MCPCaller interface {
	CallMCPTool(ctx context.Context, server, tool string, args map[string]any) (string, error)
}

// MCPProposer waits for human approval and returns the executed tool result.
type MCPProposer interface {
	ProposeMCPCallAndAwait(ctx context.Context, server, tool string, args map[string]any, sessionID, toolCallID string, userID uint64) (string, error)
}

// MCPTool is the BaseTool wrapping one (server, tool) pair.
type MCPTool struct {
	server   string
	bareName string // the MCP tool's own name
	wireName string // mcp__<server>__<tool>
	desc     string
	schema   json.RawMessage
	trusted  bool
	caller   MCPCaller
	proposer MCPProposer
	log      *slog.Logger
}

// NewMCPTool builds the adapter. schema is the MCP tool's inputSchema (raw
// JSON Schema), passed straight to the LLM.
func NewMCPTool(server, bareName, desc string, schema json.RawMessage, trusted bool, caller MCPCaller, proposer MCPProposer, log *slog.Logger) *MCPTool {
	return &MCPTool{
		server:   server,
		bareName: bareName,
		wireName: MCPToolName(server, bareName),
		desc:     desc,
		schema:   schema,
		trusted:  trusted,
		caller:   caller,
		proposer: proposer,
		log:      log,
	}
}

// MCPToolNamePrefix is the wire-name prefix every MCP tool carries. Callers
// use it to recognise an MCP tool by name (e.g. the flow invoker routes these
// to the live MCP dispatch path).
const MCPToolNamePrefix = "mcp__"

// MCPToolName builds the LLM-facing wire name, sanitizing both segments.
func MCPToolName(server, tool string) string {
	return MCPToolNamePrefix + sanitizeMCPSeg(server) + "__" + sanitizeMCPSeg(tool)
}

func sanitizeMCPSeg(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// MCPToolClass infers an MCP tool's risk class from its name. MCP servers
// rarely set the readOnlyHint annotation, so we read the verb: pure-read
// queries (k8s list/get/log/top/stats/view/...) are "read" and can be
// single-node test-run; anything naming a mutating verb — or unknown — stays
// "destructive" so it is gated to full-flow runs / approval. Decoupled from the
// server's `trusted` flag, which governs only the sync-vs-approval run path.
func MCPToolClass(bareName string) string {
	n := strings.ToLower(bareName)
	for _, v := range []string{"delete", "remove", "create", "apply", "exec", "scale", "restart", "patch", "update", "drain", "cordon", "rollout", "evict", "kill", "destroy", "stop", "start", "attach", "write"} {
		if strings.Contains(n, v) {
			return "destructive"
		}
	}
	for _, v := range []string{"list", "get", "read", "view", "describe", "log", "top", "stat", "status", "summary", "event", "watch", "search", "info", "query", "show", "fetch", "inspect", "config", "cat", "tail", "head"} {
		if strings.Contains(n, v) {
			return "read"
		}
	}
	return "destructive"
}

func (t *MCPTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	class := MCPToolClass(t.bareName)
	desc := t.desc
	if desc == "" {
		desc = "MCP tool " + t.bareName + " from server " + t.server
	}
	schema := t.schema
	if len(strings.TrimSpace(string(schema))) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	return &basetool.ToolInfo{
		Name:         t.wireName,
		Description:  desc,
		WhenToUse:    "外部 MCP 服务「" + t.server + "」提供的能力。",
		Parameters:   schema,
		Class:        class,
		Confirmation: basetool.ConfirmationSelfManaged,
		Origin:       basetool.OriginMCP, // runtime-discovered → routed to specialists, not the coordinator
	}, nil
}

func (t *MCPTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	var args map[string]any
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("mcp %s: bad args: %w", t.wireName, err)
		}
	}
	if t.trusted {
		if t.caller == nil {
			return "", fmt.Errorf("mcp %s: caller not wired", t.wireName)
		}
		return t.caller.CallMCPTool(ctx, t.server, t.bareName, args)
	}
	if t.proposer == nil {
		return "", fmt.Errorf("mcp %s: approval not wired", t.wireName)
	}
	cfg := basetool.ResolveOptions(opts)
	toolCallID := compose.GetToolCallID(ctx)
	if toolCallID == "" {
		toolCallID = basetool.ToolCallIDFromContext(ctx)
	}
	result, err := t.proposer.ProposeMCPCallAndAwait(ctx, t.server, t.bareName, args, basetool.SessionIDFromContext(ctx), toolCallID, cfg.UserID)
	if err != nil {
		return "", fmt.Errorf("mcp %s: propose: %w", t.wireName, err)
	}
	return result, nil
}
