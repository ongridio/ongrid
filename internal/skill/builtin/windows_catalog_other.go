//go:build !windows

// windows_catalog_other.go 在非 Windows 平台（manager 运行的 Linux）注册
// Windows host skill 的元数据存根（metadata-only stub）。
// #18-3 catalog metadata 单一真相源
// 问题（重构前）：3 个 Windows skill 的 metadata 定义在两处：
//   - windows/<skill>.go（//go:build windows）— Windows 端真实 skill
//   - 此文件（//go:build !windows）— Linux 端 stub
// 新增/修改 skill 时必须手动同步两处，catalog metadata 偏差 = 静默生产 bug。
// 解决方案：metadata 是纯数据（skill.Metadata struct），提取到 metadata.go
// （无 build tag，跨平台编译）的导出函数中。此文件调用 windows.XxxMetadata()
// 获取与 Windows 端完全相同的 metadata，消除重复定义。
// ⚠️ 新增 Windows skill 时：
//   1. 在 metadata.go 加 XxxMetadata() 函数
//   2. 在 windows/<skill>.go 的 Metadata() 委托调用
//   3. 在此文件 init() 加一行 skill.Register(...) 调用 windows.XxxMetadata()
// metadata 本身无需手动同步。
package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ongridio/ongrid/internal/skill"
	"github.com/ongridio/ongrid/internal/skill/builtin/windows"
)

func init() {
	// 每个 stub 的 metadata 直接调用 windows.XxxMetadata() — 与 Windows 端同一函数。
	skill.Register(&remoteHostSkill{meta: windows.EventLogMetadata()})
	skill.Register(&remoteHostSkill{meta: windows.ServicesMetadata()})
	skill.Register(&remoteHostSkill{meta: windows.ProcessesMetadata()})
	skill.Register(&remoteHostSkill{meta: windows.NetworkMetadata()})
	skill.Register(&remoteHostSkill{meta: windows.HotfixMetadata()})
}

// remoteHostSkill 是 Windows host skill 在非 Windows 平台的存根。
// manager 只用 Metadata() 做路由 + 权限校验，Execute() 不会被调用
// （ScopeHost skill 通过 tunnel RPC 转发到 edge agent 执行）。
type remoteHostSkill struct {
	meta skill.Metadata
}

func (s *remoteHostSkill) Metadata() skill.Metadata { return s.meta }

func (s *remoteHostSkill) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("skill %q is a host skill; execute via edge agent (tunnel RPC)", s.meta.Key)
}
