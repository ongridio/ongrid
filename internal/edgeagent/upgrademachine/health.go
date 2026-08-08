// health.go 管理升级版本元数据文件 + 状态检测函数（ADR-033 U3，issue #23）。
//
// 所有文件名常量引用本包 ipc.go（单一真相源）。
//
// 健康判定：last_upgrade_ver == healthy_marker → 升级成功
// rollback 触发：last_upgrade_ver 存在但 healthy_marker 不匹配 → 回滚
//
// AGENTS.md context.Context 例外：WriteUpgradeMeta / ClearPending / WriteRollbackDone
// 操作本地文件系统（毫秒级），WriteUpgradeMeta 删 healthy_marker 是关键步骤
// （不删 → watchdog 永不触发 rollback）。取消检查由编排层 Machine 入口完成。

package upgrademachine

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// IsPending 检查是否有 pending upgrade（incoming/MANIFEST.txt 存在）。
func IsPending(stageDir string) bool {
	_, err := os.Stat(ManifestPath(stageDir))
	return err == nil
}

// IsUpgradeHealthy 判断最近一次 upgrade 是否被确认为健康。
// 条件：last_upgrade_ver 存在 AND healthy_marker 内容匹配。
func IsUpgradeHealthy(stageDir string) bool {
	lastVer := readTrimmed(LastUpgradeVerPath(stageDir))
	if lastVer == "" {
		return false // 从未升级过
	}
	healthyVer := readTrimmed(HealthyMarkerPath(stageDir))
	return lastVer == healthyVer
}

// HasLastUpgrade 报告是否发生过至少一次 upgrade swap（last_upgrade_ver 存在）。
// supervisor 启动时用此区分"从未升级"（无需 rollback）和"升级了但不健康"（需 rollback）。
func HasLastUpgrade(stageDir string) bool {
	return readTrimmed(LastUpgradeVerPath(stageDir)) != ""
}

// WriteUpgradeMeta 在 swap 完成后写入版本元数据，并删除旧 healthy_marker
// 重新武装健康检查（对称 Linux apply_bundle 完成后 `rm -f "$HEALTHY_MARKER"`）。
//
// 删 healthy_marker 是关键步骤：如果不删，旧 marker 仍在 → IsUpgradeHealthy
// 立刻返回 true → watchdog 永不触发 rollback。
// 新 worker register_edge 成功后会写新的 healthy_marker。
func WriteUpgradeMeta(stageDir, version string) error {
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		return fmt.Errorf("mkdir stage: %w", err)
	}
	// last_upgrade_ver
	if err := os.WriteFile(LastUpgradeVerPath(stageDir), []byte(version+"\n"), 0o640); err != nil {
		return fmt.Errorf("write last_upgrade_ver: %w", err)
	}
	// last_upgrade_at（ISO8601 UTC）
	ts := time.Now().UTC().Format(time.RFC3339)
	if err := os.WriteFile(LastUpgradeAtPath(stageDir), []byte(ts+"\n"), 0o640); err != nil {
		return fmt.Errorf("write last_upgrade_at: %w", err)
	}
	// 删 healthy_marker（不存在时 Remove 报错，忽略 — 可能是首次升级）
	markerPath := HealthyMarkerPath(stageDir)
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove old healthy_marker: %w", err)
	}
	return nil
}

// ReadStagedVersion 从 incoming/VERSION 读取版本号。
// 文件不存在时返回空字符串（不报错 — 旧版 bundle 可能无 VERSION）。
func ReadStagedVersion(stageDir string) (string, error) {
	verPath := StagedVersionPath(stageDir)
	if _, err := os.Stat(verPath); err != nil {
		return "", nil // 不存在 = 无版本信息
	}
	return readTrimmed(verPath), nil
}

// ClearPending 删除 incoming/ 目录（bundle 已 apply 或 rollback 后清理）。
func ClearPending(stageDir string) error {
	incoming := IncomingDir(stageDir)
	if err := os.RemoveAll(incoming); err != nil {
		return fmt.Errorf("remove incoming/: %w", err)
	}
	return nil
}

// RollbackDoneExists 报告 rollback.done 哨兵文件是否存在。
// superviseWorker 每轮启动前检查 → 存在则跳过 upgrade watch（避免死循环）。
func RollbackDoneExists(stageDir string) bool {
	_, err := os.Stat(RollbackDonePath(stageDir))
	return err == nil
}

// WriteRollbackDone 写 rollback.done 哨兵文件。
func WriteRollbackDone(stageDir string) error {
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		return fmt.Errorf("mkdir stage: %w", err)
	}
	return os.WriteFile(RollbackDonePath(stageDir), []byte("done\n"), 0o640)
}

// readTrimmed 读取文件并 trim 空白。文件不存在或读失败时返回空字符串。
func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
