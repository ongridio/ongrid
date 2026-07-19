// apply.go 实现 bundle swap 算法。
// 对称 Linux apply-pending-upgrade.sh 的 bundle apply 模式：
//  1. 预检：验证所有 src 的 sha256（all-or-nothing，半换比不换更糟）
//  2. 对每条 entry：
//     a. 如果 dest 存在 → copy(dest, dest.previous)（备份）
//     b. copy(src, dest.new)（暂存到 dest 同目录，保证同卷原子 rename）
//     c. rename(dest.new, dest)（原子替换）
// Windows 特殊考虑：
//   - os.Rename 在同目录下是原子的（底层 MoveFileExW + MOVEFILE_REPLACE_EXISTING）
//   - copy 到 dest.new 而非直接 rename src→dest，因为 incoming/ 和 dest 可能跨卷
//   - 残留 dest.new（上次崩溃）在步骤 b 被覆盖，幂等
// AGENTS.md context.Context 例外：ApplyBundle 操作本地文件系统（秒级），
// 是原子事务语义（all-or-nothing），中途取消比跑完更危险（半 swap 状态）。
// 取消检查由编排层 Machine.Apply 在入口处完成（ctx.Err() guard），
// 进入 ApplyBundle 后操作不可取消。故不接收 context.Context 参数。

package upgrademachine

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ApplyResult 记录 swap 结果，供调用方决策（日志 + 是否触发 rollback）。
type ApplyResult struct {
	Swapped  []string // 成功 swap 的 dest 路径
	BackedUp []string // 创建的 .previous 备份路径
	Staged   []string // supervisor.exe.new 暂存路径（未 rename，等 SupervisorSelfSwap）
}

// ApplyBundle 执行 MANIFEST 中所有条目的 swap。
// 预检阶段（VerifyAll）失败时立即返回，不触碰任何 dest 文件。
// swap 阶段中单条 entry 失败时记录错误并继续（best-effort，对称 Linux 行为）。
// stageDir 用于写 supervisor_upgrade.pending sentinel。
// 当 entry.Dest 是 supervisor.exe 时，只 stage .new + 写 sentinel，不 rename。
func ApplyBundle(stageDir, incomingDir string, entries []ManifestEntry) (*ApplyResult, error) {
	// 预检：所有 src 的 sha 必须验证通过
	if err := VerifyAll(incomingDir, entries); err != nil {
		return nil, fmt.Errorf("pre-verify aborted: %w", err)
	}

	result := &ApplyResult{}
	for _, e := range entries {
		if err := applyOne(stageDir, incomingDir, e, result); err != nil {
			return result, fmt.Errorf("swap %s: %w", e.Src, err)
		}
	}
	return result, nil
}

// isSupervisorBinary 判断 dest 是否指向 supervisor.exe。
// applyOne 用此决定走 stage-only 路径还是标准 swap 路径。
func isSupervisorBinary(dest string) bool {
	return filepath.Base(dest) == SupervisorBinaryName
}

// applyOne 执行单条 entry 的 swap：backup → stage → atomic rename。
// supervisor.exe special-case：运行中的 supervisor.exe 无法被
// 原子 rename（image loader 持有 image section）。改为只 stage .new + 写
// pending sentinel，让 Machine.SupervisorSelfSwap 后续做 rename-aside。
func applyOne(stageDir, incomingDir string, e ManifestEntry, result *ApplyResult) error {
	srcPath := filepath.Join(incomingDir, e.Src)
	destDir := filepath.Dir(e.Dest)

	// 确保 dest 父目录存在（首次安装路径可能不存在）
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("mkdir dest dir %s: %w", destDir, err)
	}

	// supervisor.exe special-case：stage .new + 写 pending sentinel，不 backup / rename
	if isSupervisorBinary(e.Dest) {
		newPath := e.Dest + ".new"
		if err := copyFile(srcPath, newPath); err != nil {
			return fmt.Errorf("supervisor stage to %s: %w", newPath, err)
		}
		if e.Mode != "" {
			_ = os.Chmod(newPath, parseMode(e.Mode))
		}
		if err := WriteSupervisorUpgradePending(stageDir, ""); err != nil {
			return fmt.Errorf("write supervisor pending sentinel: %w", err)
		}
		result.Staged = append(result.Staged, newPath)
		return nil
	}

	// 备份：dest 存在 → copy(dest, dest.previous)
	if _, err := os.Stat(e.Dest); err == nil {
		prevPath := e.Dest + PreviousSuffix
		if err := copyFile(e.Dest, prevPath); err != nil {
			return fmt.Errorf("backup to %s: %w", prevPath, err)
		}
		result.BackedUp = append(result.BackedUp, prevPath)
	}

	// 暂存：copy(src, dest.new) — 同目录保证后续 rename 原子
	newPath := e.Dest + ".new"
	if err := copyFile(srcPath, newPath); err != nil {
		return fmt.Errorf("stage to %s: %w", newPath, err)
	}

	// 应用 mode（Windows 上仅 read-only bit 有效，但保持语义一致）
	if e.Mode != "" {
		_ = os.Chmod(newPath, parseMode(e.Mode)) // 失败不阻断（Windows 忽略）
	}

	// 原子替换：rename(dest.new, dest) — 同目录 = 同卷，原子
	if err := os.Rename(newPath, e.Dest); err != nil {
		_ = os.Remove(newPath) // best-effort 清理暂存文件；rename 已失败，清理失败也无处理手段
		return fmt.Errorf("rename %s → %s: %w", newPath, e.Dest, err)
	}

	result.Swapped = append(result.Swapped, e.Dest)
	return nil
}

// copyFile 是 src → dst 的完整拷贝（内容 + 权限）。
// 不用 os.Rename 是因为 src 和 dst 可能跨卷（incoming/ 在 ProgramData，
// dest 在 Program Files）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat src: %w", err)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return fmt.Errorf("create dst: %w", err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("close dst: %w", err)
	}
	return nil
}

// parseMode 将 "0755" 等字符串转为 os.FileMode。
func parseMode(s string) os.FileMode {
	var m uint32
	for _, c := range s {
		if c >= '0' && c <= '7' {
			m = m<<3 | uint32(c-'0')
		}
	}
	return os.FileMode(m)
}
