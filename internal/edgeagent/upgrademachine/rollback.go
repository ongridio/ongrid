// rollback.go 实现 upgrade 失败时的 .previous 恢复逻辑。
// 对称 Linux apply-pending-upgrade.sh 的 maybe_rollback() 函数：
//   - 遍历指定目录（递归），找 *.previous 文件
//   - rollback: rename(dest.previous, dest) 恢复旧版本
//   - cleanup: delete *.previous（upgrade 确认健康后）
// rollback 是 best-effort：单文件失败不阻断其他文件恢复。
// AGENTS.md context.Context 例外：Rollback / CleanupPrevious 操作本地文件系统，
// rollback 必须原子完成（半回滚 = 部分恢复 + 部分未恢复），中途取消不可接受。
// 取消检查由编排层 Machine.RollbackAndMark / HealthCheck 入口完成。

package upgrademachine

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Rollback 遍历 dirs（递归），将所有 *.previous 文件恢复到原路径。
// 例如：worker.exe.previous → worker.exe（原子 rename 替换当前版本）。
// 返回成功恢复的文件数。目录不存在或无 .previous 文件时返回 (0, nil)。
func Rollback(dirs []string) (int, error) {
	return walkPreviousDirs(dirs, "rollback", func(prevPath string) error {
		target := strings.TrimSuffix(prevPath, PreviousSuffix)
		return os.Rename(prevPath, target)
	})
}

// CleanupPrevious 删除指定目录（递归）下所有 *.previous 文件。
// 在 upgrade 确认健康后调用（healthy_marker 匹配 last_upgrade_ver）。
// 对称 Linux 的 `find ... -name '*.previous' -delete`。
func CleanupPrevious(dirs []string) (int, error) {
	return walkPreviousDirs(dirs, "cleanup", func(prevPath string) error {
		return os.Remove(prevPath)
	})
}

// walkPreviousDirs 对 dirs 中每个目录递归遍历 *.previous 文件，执行 op。
// op 返回 error 时视为该文件失败（best-effort，继续其他文件）。
// WalkDir 自身的错误（目录不可访问等）向上传播。
func walkPreviousDirs(dirs []string, opName string, op func(prevPath string) error) (int, error) {
	total := 0
	for _, dir := range dirs {
		n, err := forEachPrevious(dir, op)
		total += n
		if err != nil {
			return total, fmt.Errorf("%s %s: %w", opName, dir, err)
		}
	}
	return total, nil
}

// forEachPrevious 递归遍历 dir，对每个 *.previous 文件调用 fn。
// fn 失败时跳过该文件（best-effort），不中断遍历。
func forEachPrevious(dir string, fn func(prevPath string) error) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 忽略遍历错误（best-effort）
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), PreviousSuffix) {
			return nil
		}
		if err := fn(path); err != nil {
			return nil // 忽略单个操作失败（best-effort）
		}
		count++
		return nil
	})
	return count, err
}
