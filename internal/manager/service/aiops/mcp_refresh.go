package aiops

import (
	"context"
	"log/slog"
	"time"

	aiopschatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	aiopstoolsbase "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopstoolsdec "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	managerbizmcp "github.com/ongridio/ongrid/internal/manager/biz/mcp"
	managermodelmcp "github.com/ongridio/ongrid/internal/manager/model/mcp"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
)

// MCPToolRefresher keeps runtime tools and the unredacted search catalog in sync.
type MCPToolRefresher struct {
	runtime  *aiopschatruntime.Runtime
	deps     aiopstoolsdec.Deps
	caller   MCPCaller
	proposer MCPProposer
	log      *slog.Logger
}

func NewMCPToolRefresher(runtime *aiopschatruntime.Runtime, deps aiopstoolsdec.Deps, caller MCPCaller, proposer MCPProposer, log *slog.Logger) *MCPToolRefresher {
	return &MCPToolRefresher{runtime: runtime, deps: deps, caller: caller, proposer: proposer, log: log}
}

func (r *MCPToolRefresher) Refresh(ctx context.Context, serverName string, srv *managermodelmcp.Server, discovered []mcpclient.Tool) {
	prefix := aiopstools.MCPToolName(serverName, "")
	var rawTools []aiopstoolsbase.BaseTool
	var graphTools []aiopstoolsbase.BaseTool
	if srv != nil && srv.Enabled {
		rawTools = make([]aiopstoolsbase.BaseTool, 0, len(discovered))
		graphTools = make([]aiopstoolsbase.BaseTool, 0, len(discovered))
		for _, mt := range discovered {
			raw := aiopstools.NewMCPTool(srv.Name, mt.Name, mt.Description, mt.InputSchema, srv.Trusted, r.caller, r.proposer, r.log)
			rawTools = append(rawTools, raw)
			deps := r.deps
			if !srv.Trusted {
				deps.Timeout = ApprovalWaitTimeout + time.Minute
			}
			graphTools = append(graphTools, aiopstoolsdec.Wrap(raw, deps))
		}
	}
	if err := r.runtime.ReplaceToolSource(prefix, rawTools, graphTools); err != nil {
		r.log.Error("mcp tool refresh rejected", slog.String("server", serverName), slog.Any("err", err))
		return
	}
	r.log.Info("mcp tools refreshed in chat runtime",
		slog.String("server", serverName),
		slog.Int("tools", len(graphTools)),
		slog.Int("tool_count", r.runtime.ToolCount()))
}

// LoadEnabled discovers each enabled server with the existing per-server deadline.
func (r *MCPToolRefresher) LoadEnabled(ctx context.Context, mcpUC *managerbizmcp.Usecase) {
	if servers, err := mcpUC.ListEnabled(ctx); err == nil {
		for _, srv := range servers {
			connCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			cli, berr := mcpUC.BuildClient(connCtx, srv)
			if berr == nil {
				berr = cli.Initialize(connCtx)
			}
			var mtools []mcpclient.Tool
			if berr == nil {
				mtools, berr = cli.ListTools(connCtx)
			}
			cancel()
			if berr != nil {
				r.log.Warn("mcp: connect failed, skipping server", slog.String("server", srv.Name), slog.Any("err", berr))
				continue
			}
			r.Refresh(ctx, srv.Name, srv, mtools)
			r.log.Info("mcp: server connected", slog.String("server", srv.Name), slog.Int("tools", len(mtools)), slog.Bool("trusted", srv.Trusted))
		}
	} else {
		r.log.Warn("mcp: list enabled servers failed", slog.Any("err", err))
	}
}
