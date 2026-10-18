package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	aiopstoolsbase "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopstoolsdec "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	managerbizflow "github.com/ongridio/ongrid/internal/manager/biz/flow"
	"github.com/prometheus/client_golang/prometheus"
)

type ToolInvoker struct {
	Registry *aiopstools.Registry
	deps     aiopstoolsdec.Deps
	tools    map[string]aiopstoolsbase.BaseTool
	// mcp dispatches mcp__ tool nodes LIVE (resolve the current server/tool +
	// run directly, NO human approval). Wired post-construction once mcpUC
	// exists. nil → mcp tool nodes error cleanly ("unknown tool").
	MCP *MCPSource
}

func NewToolInvoker(reg *aiopstools.Registry, registerer prometheus.Registerer) *ToolInvoker {
	inv := &ToolInvoker{
		Registry: reg,
		deps: aiopstoolsdec.Deps{
			Limiter:    aiopstoolsdec.NewTokenBucketLimiter(0),
			Registerer: registerer,
		},
		tools: map[string]aiopstoolsbase.BaseTool{},
	}
	inv.MergeBag(reg.BuildBaseTools())
	return inv
}

// MergeBag adds every tool in bag not already in the invoker map. Some tools
// register late (cloud_bash once its proposer is wired; host-files tools) —
// after the invoker was first built — so without a re-merge the flow `tool`
// node reports "unknown tool" for them. Only NEW names are Wrap'd: re-wrapping
// an existing tool would double-register its prometheus metric and panic.
func (s *ToolInvoker) MergeBag(bag *aiopstools.ToolBag) {
	if bag == nil {
		return
	}
	for _, t := range bag.AllTools() {
		if t == nil {
			continue
		}
		info, err := t.Info(context.Background())
		if err != nil || info == nil || info.Name == "" {
			continue
		}
		if isFlowRuntimeUnsupportedTool(info.Name) {
			continue
		}
		if _, exists := s.tools[info.Name]; exists {
			continue
		}
		s.tools[info.Name] = aiopstoolsdec.Wrap(t, s.deps)
	}
}

func (s *ToolInvoker) InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	// Tag any artifact a tool node produces (serve_page) as workflow-sourced so
	// the operations UI's 生成来源 column distinguishes it from chat-generated pages.
	ctx = aiopstoolsbase.WithArtifactSource(ctx, aiopstoolsbase.ArtifactSourceWorkflow)
	// A published tool node explicitly authorizes its web-search operation.
	ctx = aiopstoolsbase.WithWebSearchAllowed(ctx, true)
	// MCP tool nodes dispatch LIVE through the mcp source (resolve the current
	// server/tool, run directly without an approval card — placing the node in
	// a published flow IS the human authorization; a per-run inbox approval is
	// the wrong model for a deterministic, human-authored step). A removed
	// server/tool surfaces as a clean error → the node's error port.
	if s.MCP != nil && strings.HasPrefix(name, aiopstools.MCPToolNamePrefix) {
		return s.MCP.call(ctx, name, args)
	}
	name = aiopstoolsbase.CanonicalName(name)
	t, ok := s.tools[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	argsStr := string(args)
	if argsStr == "" {
		argsStr = "{}"
	}
	// Schema-aware coercion: a {{ref}} can resolve to a value whose type
	// doesn't match the param (a scalar wired into an array field, a numeric
	// string into a number field, etc.). Rather than fail with an opaque
	// unmarshal error, coerce toward the declared type (the n8n / Dify way).
	if info, ierr := t.Info(ctx); ierr == nil && len(info.Parameters) > 0 {
		argsStr = managerbizflow.CoerceArgsToSchema(argsStr, info.Parameters)
	}
	out, err := t.InvokableRun(ctx, argsStr)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
