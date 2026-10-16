package aiops

import (
	"context"
	"encoding/json"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	mcpmodel "github.com/ongridio/ongrid/internal/manager/model/mcp"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
	"github.com/prometheus/client_golang/prometheus"
	"log/slog"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestMCPRefreshKeepsRuntimeAndSearchCatalogInSync(t *testing.T) {
	sessions, _ := newSeededSessions(7)
	runtime, err := chatruntime.NewRuntime(chatruntime.Config{Sessions: sessions, ChatModel: &mcpReviewModel{}})
	if err != nil {
		t.Fatal(err)
	}
	catalog := tools.NewToolBag(nil, 16)
	runtime.SetToolBag(catalog)
	refresher := NewMCPToolRefresher(runtime, decorators.Deps{Timeout: 90 * time.Second, Registerer: prometheus.NewRegistry()}, MCPCaller{}, MCPProposer{}, slog.Default())
	ctx := context.Background()
	discovered := func(name string) []mcpclient.Tool {
		return []mcpclient.Tool{{Name: name, Description: "Tool " + name, InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)}}
	}
	check := func(want ...string) {
		t.Helper()
		slices.Sort(want)
		got := runtime.ToolNames(ctx)
		slices.Sort(got)
		var raw []string
		for _, tool := range catalog.AllTools() {
			info, err := tool.Info(ctx)
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, info.Name)
		}
		slices.Sort(raw)
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(raw, want) {
			t.Fatalf("runtime %v / catalog %v, want %v", got, raw, want)
		}
	}
	alpha := &mcpmodel.Server{Name: "alpha", Enabled: true, Trusted: false}
	beta := &mcpmodel.Server{Name: "beta", Enabled: true, Trusted: true}
	refresher.Refresh(ctx, alpha.Name, alpha, discovered("old"))
	refresher.Refresh(ctx, beta.Name, beta, discovered("keep"))
	refresher.Refresh(ctx, alpha.Name, alpha, discovered("new"))
	refresher.Refresh(ctx, alpha.Name, alpha, discovered("new"))
	check(tools.MCPToolName("alpha", "new"), tools.MCPToolName("beta", "keep"))
	alpha.Enabled = false
	refresher.Refresh(ctx, alpha.Name, alpha, discovered("ignored"))
	check(tools.MCPToolName("beta", "keep"))
}
