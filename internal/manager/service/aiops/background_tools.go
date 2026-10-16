package aiops

import (
	"context"
	"encoding/json"
	"fmt"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	aiopstoolsbase "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// BackgroundToolInvoker 让确定性的告警初查复用统一工具目录，不依赖旧循环注册表。
// tools 是启动时快照，后台任务不读取后续装配仍在修改的 Registry。
type BackgroundToolInvoker struct{ tools []aiopstoolsbase.BaseTool }

func NewBackgroundToolInvoker(tools []aiopstoolsbase.BaseTool) BackgroundToolInvoker {
	return BackgroundToolInvoker{tools: append([]aiopstoolsbase.BaseTool(nil), tools...)}
}

func (i BackgroundToolInvoker) Invoke(ctx context.Context, name string, args json.RawMessage) (aiopstools.ExecuteResult, error) {
	for _, t := range i.tools {
		info, err := t.Info(ctx)
		if err != nil {
			return aiopstools.ExecuteResult{}, err
		}
		if info == nil || info.Name != name {
			continue
		}
		if info.Class != "read" {
			return aiopstools.ExecuteResult{}, fmt.Errorf("background tool %q is not read-only", name)
		}
		result, err := t.InvokableRun(ctx, string(args))
		if err != nil {
			return aiopstools.ExecuteResult{}, err
		}
		return aiopstools.ExecuteResult{ResultJSON: json.RawMessage(result)}, nil
	}
	return aiopstools.ExecuteResult{}, fmt.Errorf("background tool %q unavailable", name)
}
