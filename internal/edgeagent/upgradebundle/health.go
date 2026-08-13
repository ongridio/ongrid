// health.go 实现 worker 侧的健康标记 + rollback sentinel 管理。
// 常量引用 upgrademachine 包（单一真相源），消除跨包 drift。

package upgradebundle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ongridio/ongrid/internal/edgeagent/upgrademachine"
)

// HealthyMarkerFile 是 register_edge 成功后写的版本号文件名。
// 引用 upgrademachine.HealthyMarkerFile（单一真相源）。
const HealthyMarkerFile = upgrademachine.HealthyMarkerFile

// RollbackDoneFile 是 rollback 完成后的 sentinel 文件名。
// 引用 upgrademachine.RollbackDoneFile（单一真相源）。
const RollbackDoneFile = upgrademachine.RollbackDoneFile

// WriteHealthyMarker 在 register_edge 成功后写 <stageDir>/healthy_marker。
// 内容 = version + "\n"。supervisor 的 Machine.HealthPoll 检测到此文件
// 内容匹配 last_upgrade_ver 时判定升级成功。
// 空 version 时跳过（不报错）— 适配 dev 模式或未配 AgentVersion 的场景。
// stageDir 不存在时自动创建。
func WriteHealthyMarker(stageDir, version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		return fmt.Errorf("mkdir stage: %w", err)
	}
	path := filepath.Join(stageDir, HealthyMarkerFile)
	if err := os.WriteFile(path, []byte(version+"\n"), 0o640); err != nil {
		return fmt.Errorf("write healthy_marker: %w", err)
	}
	return nil
}

// DeleteRollbackSentinel 删除 <stageDir>/rollback.done sentinel 文件。
// DownloadBundle 步骤 2（download.go 清理旧残留段）自动调用此函数，
// 保证新升级下载前清掉上次 rollback 留下的 sentinel —— 否则 superviseWorker
//（cmd/ongrid-edge-supervisor/worker.go:80）检测到 rollback.done → watchUpgrade=false
// → 跳过新升级的 health watch → 坏 bundle 不触发 rollback → brick。
// 设计说明：原  注释承诺 "manager 推送入口" 清理，但代码考古证明 manager
// 是远程进程（部署在 manager），不持有 edge 本地 stageDir → 物理上无法直接删文件。
// 真实架构是 manager 通过 fetch_package RPC 触发 edge 的 DownloadBundle，
// 因此清理责任归 edge 自治（与 incoming/ + tarball 清理同构）。
// 幂等：文件不存在时返回 nil（首次升级或已清理）。
func DeleteRollbackSentinel(stageDir string) error {
	path := filepath.Join(stageDir, RollbackDoneFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove rollback.done: %w", err)
	}
	return nil
}
