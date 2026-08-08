// ipc.go 定义升级状态机的所有文件系统 IPC 常量（单一真相源）。
// 这些常量消除原先 upgradeapply / upgradebundle / cmd 三包各自的私有/导出定义 drift。
// 任何包需要引用文件名时，import 本包的导出常量，而非重新定义。
//  supervisor 自升级 sentinel 常量也在此定义（保持单一真相源）。

package upgrademachine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StageDir 下的 IPC 文件名常量。
const (
	// HealthyMarkerFile 是 worker register_edge 成功后写的版本号文件名。
	// supervisor 的 IsUpgradeHealthy 对比此文件内容与 last_upgrade_ver 判定升级是否健康。
	HealthyMarkerFile = "healthy_marker"

	// RollbackDoneFile 是 rollback 完成后的哨兵文件名。
	// supervisor 检测到此文件 → 跳过 rollback 检查（避免死循环）。
	// manager 推新 bundle 前必须删除此文件（upgradebundle.DeleteRollbackSentinel）。
	RollbackDoneFile = "rollback.done"

	// LastUpgradeVerFile 是 supervisor swap 后写的当前版本号文件名。
	LastUpgradeVerFile = "last_upgrade_ver"

	// LastUpgradeAtFile 是 supervisor swap 后写的 ISO8601 时间戳文件名。
	LastUpgradeAtFile = "last_upgrade_at"

	// IncomingDirName 是 StageDir 下存放解压 bundle 的子目录名。
	IncomingDirName = "incoming"

	// ManifestFileName 是 swap 指令清单文件名（也是 pending 触发器）。
	ManifestFileName = "MANIFEST.txt"

	// VersionFileName 是 bundle 内携带的版本号文件名。
	VersionFileName = "VERSION"

	// PreviousSuffix 是 .previous 备份文件的统一后缀。
	PreviousSuffix = ".previous"

	// SupervisorUpgradePendingFile 是 supervisor 自升级哨兵 — applyOne 检测到
	// supervisor.exe dest 时写入此文件，跳过原子 rename（深模块不能 rename 运行中的自身）。
	// Machine.BootCheck / superviseWorker 检测到此文件 → 触发 SupervisorSelfSwap。
	SupervisorUpgradePendingFile = "supervisor_upgrade.pending"

	// SupervisorUpgradeAppliedFile 是 supervisor 自升级完成哨兵 —
	// SupervisorSelfSwap 成功后写入，BootCheck 检测到后清理 .old 备份 + 删此哨兵。
	SupervisorUpgradeAppliedFile = "supervisor_upgrade.applied"
)

// 二进制文件名常量（对称 edgedirs.WorkerBinary，本包自包含以避免 cmd → edgedirs 反向依赖）。
const (
	// SupervisorBinaryName 是 supervisor.exe 文件名（MANIFEST dest 匹配键）。
	SupervisorBinaryName = "ongrid-edge-supervisor.exe"

	// WorkerBinaryName 是 worker.exe 文件名（BootCheck KillByImage 清理 orphan worker 用）。
	WorkerBinaryName = "ongrid-edge-worker.exe"
)

// IncomingDir 返回 StageDir 下的 incoming/ 子目录路径。
func IncomingDir(stageDir string) string {
	return filepath.Join(stageDir, IncomingDirName)
}

// ManifestPath 返回 incoming/MANIFEST.txt 的完整路径。
func ManifestPath(stageDir string) string {
	return filepath.Join(stageDir, IncomingDirName, ManifestFileName)
}

// HealthyMarkerPath 返回 healthy_marker 的完整路径。
func HealthyMarkerPath(stageDir string) string {
	return filepath.Join(stageDir, HealthyMarkerFile)
}

// RollbackDonePath 返回 rollback.done 的完整路径。
func RollbackDonePath(stageDir string) string {
	return filepath.Join(stageDir, RollbackDoneFile)
}

// LastUpgradeVerPath 返回 last_upgrade_ver 的完整路径。
func LastUpgradeVerPath(stageDir string) string {
	return filepath.Join(stageDir, LastUpgradeVerFile)
}

// LastUpgradeAtPath 返回 last_upgrade_at 的完整路径。
func LastUpgradeAtPath(stageDir string) string {
	return filepath.Join(stageDir, LastUpgradeAtFile)
}

// StagedVersionPath 返回 incoming/VERSION 的完整路径。
func StagedVersionPath(stageDir string) string {
	return filepath.Join(stageDir, IncomingDirName, VersionFileName)
}

// SupervisorUpgradePendingPath 返回 supervisor_upgrade.pending 的完整路径。
func SupervisorUpgradePendingPath(stageDir string) string {
	return filepath.Join(stageDir, SupervisorUpgradePendingFile)
}

// SupervisorUpgradeAppliedPath 返回 supervisor_upgrade.applied 的完整路径。
func SupervisorUpgradeAppliedPath(stageDir string) string {
	return filepath.Join(stageDir, SupervisorUpgradeAppliedFile)
}

// IsSupervisorUpgradePending 报告是否存在 supervisor 自升级 pending 哨兵。
// 存在 = applyOne 已 stage supervisor.exe.new，等待 SupervisorSelfSwap 执行 rename-aside。
func IsSupervisorUpgradePending(stageDir string) bool {
	_, err := os.Stat(SupervisorUpgradePendingPath(stageDir))
	return err == nil
}

// WriteSupervisorUpgradePending 写 supervisor_upgrade.pending 哨兵。
// applyOne 检测到 supervisor dest 时调用，内容是 supervisor.exe.new 的预期版本（可空）。
func WriteSupervisorUpgradePending(stageDir, version string) error {
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		return fmt.Errorf("mkdir stage: %w", err)
	}
	body := version
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return os.WriteFile(SupervisorUpgradePendingPath(stageDir), []byte(body), 0o640)
}

// IsSupervisorUpgradeApplied 报告是否存在 supervisor 自升级 applied 哨兵。
// 存在 = SupervisorSelfSwap 已完成 rename-aside，BootCheck 应清理 .old 并删此哨兵。
func IsSupervisorUpgradeApplied(stageDir string) bool {
	_, err := os.Stat(SupervisorUpgradeAppliedPath(stageDir))
	return err == nil
}

// WriteSupervisorUpgradeApplied 写 supervisor_upgrade.applied 哨兵。
// SupervisorSelfSwap 成功后调用，内容是新版本号（可空）。
func WriteSupervisorUpgradeApplied(stageDir, version string) error {
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		return fmt.Errorf("mkdir stage: %w", err)
	}
	body := version
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return os.WriteFile(SupervisorUpgradeAppliedPath(stageDir), []byte(body), 0o640)
}
