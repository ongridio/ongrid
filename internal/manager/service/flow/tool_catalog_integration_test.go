package flow

import (
	"context"
	"log/slog"
	"testing"

	tools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
)

type catalogCaller struct{}

func (catalogCaller) Call(context.Context, uint64, string, []byte) ([]byte, error) {
	return []byte(`{}`), nil
}

func TestWorkflowPaletteAndInvokerIncludeHostFiles(t *testing.T) {
	reg := tools.NewRegistry(catalogCaller{}, edgebiz.NewUsecase(nil, nil, nil, slog.Default()), &devicebiz.Usecase{}, nil, nil, nil, nil, slog.Default())
	catalog := ToolCatalog{Registry: reg}
	palette := map[string]bool{}
	for _, entry := range catalog.ListTools() {
		palette[entry.Name] = true
	}
	invoker := NewToolInvoker(reg, nil)
	for _, name := range []string{"host_find_large_files", "host_du_summary", "host_stat_file"} {
		if !palette[name] {
			t.Errorf("missing palette tool %s", name)
		}
		if invoker.tools[name] == nil {
			t.Errorf("missing executable %s", name)
		}
	}
	if palette[tools.ToolNameExecuteK8sAction] {
		t.Fatal("workflow exposes unsupported approval action")
	}
}
