package flow

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	"github.com/prometheus/client_golang/prometheus"
)

type lateTool struct{ name string }

func (tool lateTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: tool.name, Class: "read"}, nil
}

func (lateTool) InvokableRun(ctx context.Context, args string, _ ...basetool.InvokeOption) (string, error) {
	return `{"web_allowed":` + strconv.FormatBool(basetool.WebSearchAllowed(ctx)) + `,"source":"` + basetool.ArtifactSourceFromContext(ctx) + `","args":` + args + `}`, nil
}

func TestToolInvokerMergesLateToolsOnceAndPreservesWorkflowContext(t *testing.T) {
	invoker := &ToolInvoker{
		tools: map[string]basetool.BaseTool{},
		deps:  decorators.Deps{Registerer: prometheus.NewRegistry()},
	}
	if _, err := invoker.InvokeTool(context.Background(), "late_tool", nil); err == nil {
		t.Fatal("unknown tool was accepted")
	}
	bag := aiopstools.NewToolBag([]basetool.BaseTool{
		lateTool{"late_tool"}, lateTool{aiopstools.ToolNameExecuteK8sAction},
	}, 0)
	invoker.MergeBag(nil)
	invoker.MergeBag(bag)
	invoker.MergeBag(bag) // Duplicate metrics registration would panic.
	out, err := invoker.InvokeTool(context.Background(), "late_tool", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		WebAllowed bool            `json:"web_allowed"`
		Source     string          `json:"source"`
		Args       json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	if !result.WebAllowed || result.Source != "workflow" || string(result.Args) != "{}" {
		t.Fatalf("tool result = %s", out)
	}
	if _, err := invoker.InvokeTool(context.Background(), aiopstools.ToolNameExecuteK8sAction, nil); err == nil {
		t.Fatal("a tool requiring ReviewGate was admitted to workflow execution")
	}
}
