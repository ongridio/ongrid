// rename_windows.go 实现 renameWithAVRetry 的 Windows 专属逻辑（issue #21 W3）。
//
// Windows Defender / 第三方 AV 实时扫描未签名 PE 文件时持 FILE_SHARE_READ
// 句柄，期间 os.Rename 失败（ERROR_SHARING_VIOLATION = errno 0x20）。
// renameWithAVRetry 重试 5 次 × 200ms = 1s，覆盖 99% AV 扫描窗口。

//go:build windows

package upgrademachine

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"
)

// ERROR_SHARING_VIOLATION Windows errno 32 (0x20)。
const windowsSharingViolation = syscall.Errno(0x20)

// isAVRetryable 判定 rename 错误是否值得 AV retry。
// Windows 上只对 ERROR_SHARING_VIOLATION retry（AV 扫描瞬时持锁）。
func isAVRetryable(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == windowsSharingViolation
	}
	return false
}

// renameWithAVRetry 对 os.Rename 做 AV 扫描瞬时失败的 retry。
//
// 5 次 × 200ms = 1s 总等待。非共享冲突错误立即返回（不重试）。
func renameWithAVRetry(src, dst string, log *slog.Logger) error {
	const maxRetry = 5
	const retryDelay = 200 * time.Millisecond

	var lastErr error
	for i := 0; i < maxRetry; i++ {
		err := os.Rename(src, dst)
		if err == nil {
			if i > 0 {
				log.Info("rename succeeded after AV retry",
					"src", src, "dst", dst, "attempts", i+1)
			}
			return nil
		}
		lastErr = err
		if !isAVRetryable(err) {
			return err // 非共享冲突，不重试
		}
		log.Warn("rename blocked (likely AV scan); retrying",
			"src", src, "dst", dst, "attempt", i+1, "err", err)
		time.Sleep(retryDelay)
	}
	return fmt.Errorf("rename %s → %s: AV scan did not release after %d retries: %w",
		src, dst, maxRetry, lastErr)
}
