package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managerbizflow "github.com/ongridio/ongrid/internal/manager/biz/flow"
	managerbizmcp "github.com/ongridio/ongrid/internal/manager/biz/mcp"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
)

// MCPSource live-queries the registered MCP servers (HLD-018) so the flow
// tool palette and the `tool` node always reflect the CURRENT tool universe —
// add/remove a server and it shows up / drops out without a restart (n8n's
// McpClient live-listSearch pattern). MCP tools carry a JSON inputSchema, so
// they are first-class deterministic nodes (skills, lacking a schema, are not).
type MCPSource struct {
	Usecase *managerbizmcp.Usecase
	Logger  *slog.Logger
}

// mcpEntry is one live MCP tool: its wire name + the (server, bareTool) needed
// to dispatch it, plus the schema for the node's param form.
type mcpEntry struct {
	wire   string
	server string
	bare   string
	desc   string
	schema json.RawMessage
}

// enumerate connects to every enabled server and lists its tools. Best-effort:
// an unreachable server is logged and skipped (degradation, not a hard fail).
func (m *MCPSource) enumerate(ctx context.Context) []mcpEntry {
	if m == nil || m.Usecase == nil {
		return nil
	}
	servers, err := m.Usecase.ListEnabled(ctx)
	if err != nil {
		return nil
	}
	var out []mcpEntry
	for _, srv := range servers {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cli, berr := m.Usecase.BuildClient(cctx, srv)
		if berr == nil {
			berr = cli.Initialize(cctx)
		}
		var tools []mcpclient.Tool
		if berr == nil {
			tools, berr = cli.ListTools(cctx)
		}
		cancel()
		if berr != nil {
			if m.Logger != nil {
				m.Logger.Warn("flow mcp: list failed, skipping server", slog.String("server", srv.Name), slog.Any("err", berr))
			}
			continue
		}
		for _, t := range tools {
			out = append(out, mcpEntry{
				wire:   aiopstools.MCPToolName(srv.Name, t.Name),
				server: srv.Name,
				bare:   t.Name,
				desc:   t.Description,
				schema: t.InputSchema,
			})
		}
	}
	return out
}

// metas returns the live MCP tools as flow palette entries.
func (m *MCPSource) metas(ctx context.Context) []managerbizflow.ToolMeta {
	entries := m.enumerate(ctx)
	out := make([]managerbizflow.ToolMeta, 0, len(entries))
	for _, e := range entries {
		out = append(out, managerbizflow.ToolMeta{
			Name:        e.wire,
			DisplayZh:   e.bare,
			Description: e.desc,
			// Infer read vs destructive from the tool name so read-only MCP
			// tools (k8s list/get/log/...) are single-node test-runnable;
			// mutating/unknown ones stay gated to full-flow runs.
			Class:      aiopstools.MCPToolClass(e.bare),
			Category:   "integration",
			Parameters: e.schema,
		})
	}
	return out
}

// call resolves a wire name to its current (server, bareTool) and dispatches
// directly via the usecase (NO approval). Narrows by server prefix first so it
// only lists the one matching server's tools, not all of them.
func (m *MCPSource) call(ctx context.Context, wire string, args json.RawMessage) (json.RawMessage, error) {
	if m == nil || m.Usecase == nil {
		return nil, fmt.Errorf("unknown tool %q", wire)
	}
	servers, err := m.Usecase.ListEnabled(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp: list servers: %w", err)
	}
	for _, srv := range servers {
		if !strings.HasPrefix(wire, aiopstools.MCPToolName(srv.Name, "")) {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cli, berr := m.Usecase.BuildClient(cctx, srv)
		if berr == nil {
			berr = cli.Initialize(cctx)
		}
		var tools []mcpclient.Tool
		if berr == nil {
			tools, berr = cli.ListTools(cctx)
		}
		cancel()
		if berr != nil {
			return nil, fmt.Errorf("mcp %q: server %q unreachable: %w", wire, srv.Name, berr)
		}
		for _, t := range tools {
			if aiopstools.MCPToolName(srv.Name, t.Name) != wire {
				continue
			}
			var argMap map[string]any
			if len(args) > 0 {
				if uerr := json.Unmarshal(args, &argMap); uerr != nil {
					return nil, fmt.Errorf("mcp %q: bad args: %w", wire, uerr)
				}
			}
			res, cerr := m.Usecase.CallTool(ctx, srv.Name, t.Name, argMap)
			if cerr != nil {
				return nil, fmt.Errorf("mcp %q: %w", wire, cerr)
			}
			return json.RawMessage(res), nil
		}
		return nil, fmt.Errorf("mcp %q: tool no longer exists on server %q", wire, srv.Name)
	}
	return nil, fmt.Errorf("mcp %q: no enabled server matches", wire)
}
