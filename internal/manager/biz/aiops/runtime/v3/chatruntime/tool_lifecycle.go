package chatruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// runtimeTool 保留本轮 schema；撤销或替换后，旧引用不得开始新的调用。
// 已经进入执行的调用继续沿用原超时/取消机制，不在持锁期间执行外部 IO。
type runtimeTool struct {
	inner    basetool.BaseTool
	runtime  *Runtime
	name     string
	version  uint64
	info     *basetool.ToolInfo
	fullInfo *basetool.ToolInfo
}

func cloneToolInfo(info *basetool.ToolInfo) *basetool.ToolInfo {
	copy := *info
	copy.Parameters = append([]byte(nil), info.Parameters...)
	return &copy
}

func (rt *Runtime) versionedToolLocked(inner basetool.BaseTool, info, full *basetool.ToolInfo) basetool.BaseTool {
	rt.nextToolVersion++
	rt.toolVersions[info.Name] = rt.nextToolVersion
	return &runtimeTool{inner: inner, runtime: rt, name: info.Name, version: rt.nextToolVersion, info: cloneToolInfo(info), fullInfo: cloneToolInfo(full)}
}
func (t *runtimeTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return cloneToolInfo(t.info), nil
}

// FullInfo 保留此执行版本的完整 schema，避免旧回合读到新版本参数。
func (t *runtimeTool) FullInfo(context.Context) (*basetool.ToolInfo, error) {
	return cloneToolInfo(t.fullInfo), nil
}

// inspectToolBatch 在修改任何视图前校验整批元数据，错误不破坏现有工具。
type toolBatch struct {
	infos map[string]*basetool.ToolInfo
	names []string
}

func inspectToolBatch(tools []basetool.BaseTool, prefix string) (*toolBatch, error) {
	infos := make(map[string]*basetool.ToolInfo, len(tools))
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("nil tool in registration")
		}
		info, err := t.Info(context.Background())
		if err != nil {
			return nil, fmt.Errorf("read tool metadata: %w", err)
		}
		if info == nil || strings.TrimSpace(info.Name) == "" {
			return nil, fmt.Errorf("tool name required")
		}
		if !strings.HasPrefix(info.Name, prefix) {
			return nil, fmt.Errorf("tool %q is outside source %q", info.Name, prefix)
		}
		if infos[info.Name] != nil {
			return nil, fmt.Errorf("duplicate tool %q", info.Name)
		}
		infos[info.Name] = cloneToolInfo(info)
		names = append(names, info.Name)
	}
	return &toolBatch{infos: infos, names: names}, nil
}

// AppendToolBag 同步注册执行与搜索视图；同名工具替换，整批重复名称被拒绝。
func (rt *Runtime) AppendToolBag(tools []basetool.BaseTool) error {
	if rt == nil {
		return fmt.Errorf("runtime unavailable")
	}
	infos, err := inspectToolBatch(tools, "")
	if err != nil {
		return err
	}
	rt.toolMu.Lock()
	defer rt.toolMu.Unlock()
	if rt.toolVersions == nil {
		rt.toolVersions = map[string]uint64{}
	}
	index := map[string]int{}
	for i, t := range rt.cfg.ToolBag {
		index[t.(*runtimeTool).name] = i
	}
	for j, t := range tools {
		info := infos.infos[infos.names[j]]
		wrapped := rt.versionedToolLocked(t, info, info)
		if i, exists := index[info.Name]; exists {
			rt.cfg.ToolBag[i] = wrapped
		} else {
			rt.cfg.ToolBag = append(rt.cfg.ToolBag, wrapped)
		}
	}
	if bag, ok := rt.bag.(mutableToolBagProvider); ok {
		bag.UpsertTools(tools)
	}
	return nil
}

func (rt *Runtime) ReplaceToolsByNamePrefix(prefix string, replacements []basetool.BaseTool) error {
	return rt.ReplaceToolSource(prefix, replacements, replacements)
}

// ReplaceToolSource 校验来源边界与两份视图，成功后一起替换并撤销旧版本。
func (rt *Runtime) ReplaceToolSource(prefix string, raw, decorated []basetool.BaseTool) error {
	if rt == nil || strings.TrimSpace(prefix) == "" {
		return fmt.Errorf("runtime and tool source required")
	}
	full, err := inspectToolBatch(raw, prefix)
	if err != nil {
		return err
	}
	infos, err := inspectToolBatch(decorated, prefix)
	if err != nil {
		return err
	}
	if len(full.infos) != len(infos.infos) {
		return fmt.Errorf("tool source %q catalog/executor mismatch", prefix)
	}
	for name := range infos.infos {
		if full.infos[name] == nil {
			return fmt.Errorf("tool %q has no catalog entry", name)
		}
	}
	rt.toolMu.Lock()
	defer rt.toolMu.Unlock()
	if rt.toolVersions == nil {
		rt.toolVersions = map[string]uint64{}
	}
	next := make([]basetool.BaseTool, 0, len(rt.cfg.ToolBag)+len(decorated))
	for _, t := range rt.cfg.ToolBag {
		current := t.(*runtimeTool)
		if strings.HasPrefix(current.name, prefix) {
			delete(rt.toolVersions, current.name)
		} else {
			next = append(next, t)
		}
	}
	for j, t := range decorated {
		name := infos.names[j]
		next = append(next, rt.versionedToolLocked(t, infos.infos[name], full.infos[name]))
	}
	if bag, ok := rt.bag.(mutableToolBagProvider); ok {
		bag.ReplaceToolsByNamePrefix(prefix, raw)
	}
	rt.cfg.ToolBag = next
	return nil
}

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
