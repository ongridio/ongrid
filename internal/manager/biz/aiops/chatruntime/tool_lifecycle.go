package chatruntime

import (
	"context"
	"fmt"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// runtimeTool 保留本轮 schema；撤销或替换后，旧引用不得开始新的调用。
// 已经进入执行的调用继续沿用原超时/取消机制，不在持锁期间执行外部 IO。
type runtimeTool struct {
	inner   basetool.BaseTool
	runtime *Runtime
	name    string
	version uint64
}

func (rt *Runtime) versionedToolLocked(name string, inner basetool.BaseTool) basetool.BaseTool {
	rt.nextToolVersion++
	rt.toolVersions[name] = rt.nextToolVersion
	return &runtimeTool{inner: inner, runtime: rt, name: name, version: rt.nextToolVersion}
}
func (t *runtimeTool) Info(ctx context.Context) (*basetool.ToolInfo, error) { return t.inner.Info(ctx) }
func (t *runtimeTool) InvokableRun(ctx context.Context, args string, opts ...basetool.InvokeOption) (string, error) {
	t.runtime.toolMu.RLock()
	current := t.runtime.toolVersions[t.name]
	t.runtime.toolMu.RUnlock()
	if current != t.version {
		return "", fmt.Errorf("tool %q was removed or replaced; start a new turn", t.name)
	}
	if t.name == basetool.ToolWebSearch && !basetool.WebSearchAllowed(ctx) {
		return "", fmt.Errorf("web_search disabled for this request")
	}
	return t.inner.InvokableRun(ctx, args, opts...)
}
