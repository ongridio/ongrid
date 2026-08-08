// rename_other.go 提供 renameWithAVRetry 的非 Windows stub（issue #21 W3）。
//
// Linux 上 bundle swap 无 AV 干扰（对称 dist/build-edge-bundle.sh Linux 版本），
// isAVRetryable 永远 false，renameWithAVRetry 等价于单次 os.Rename。
// 这让 machine.go 的 SupervisorSelfSwap 逻辑在 Linux CI 可测试（正常路径）。

//go:build !windows

package upgrademachine

import (
	"log/slog"
	"os"
)

// isAVRetryable 非 Windows 永远 false（无 AV 干扰）。
func isAVRetryable(err error) bool { return false }

// renameWithAVRetry 非 Windows 等价于单次 os.Rename（无 AV retry 需求）。
func renameWithAVRetry(src, dst string, log *slog.Logger) error {
	return os.Rename(src, dst)
}
