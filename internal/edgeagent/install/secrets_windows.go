//go:build windows

package install

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ongridio/ongrid/internal/edgeagent/dpapi"
)

// WindowsSecretStore 是 SecretStore 的 Windows 实现，
// 使用 DPAPI CryptProtectData 加密凭证。
type WindowsSecretStore struct {
	path string
}

// 包级 var 间接调用 ACL 函数，便于测试 mock（生产环境 supervisor 以 SYSTEM 身份
// 运行，普通用户测试无法通过 ApplySecretACL 后的 WriteFile；用 var 间接让测试
// 替换为 noop，ACL 行为在 acl_windows_test.go 单独验证）。
var (
	ensureSecureDirFn  = EnsureSecureDir
	applySecretACLFn  = ApplySecretACL
	verifySecretACLFn = VerifySecretACL
)

// NewSecretStore 创建 Windows DPAPI SecretStore。
// secretsPath 是 secrets.enc 的完整路径。
func NewSecretStore(secretsPath string) SecretStore {
	return &WindowsSecretStore{path: secretsPath}
}

// Install 加密 token 并写入 secrets.enc，含 ACL 应用 + round-trip 验证。
//
// 流程：
//  1. EnsureSecureDir(parent) — 父目录受限为 SYSTEM+Admins，子文件自动继承
//  2. dpapi.Protect(token) → 加密
//  3. os.WriteFile — 文件继承父目录 ACL（出生即受限，无明文窗口）
//  4. ApplySecretACL — 显式 /inheritance:r 防父目录被改后影响
//  5. VerifySecretACL — 读回 ACL 验证
//  6. ReadFile + dpapi.Unprotect → round-trip 验证
//  7. 失败时清理（暴露 Remove 错误，不静默吞掉）
//
// token 的 []byte 副本清零由调用方负责。
func (s *WindowsSecretStore) Install(token []byte) error {
	dir := filepath.Dir(s.path)
	if err := ensureSecureDirFn(dir); err != nil {
		return fmt.Errorf("secure parent dir: %w", err)
	}
	encrypted, err := dpapi.Protect(token)
	if err != nil {
		return fmt.Errorf("dpapi encrypt token: %w", err)
	}
	if err := os.WriteFile(s.path, encrypted, 0o600); err != nil {
		return fmt.Errorf("write secrets.enc: %w", err)
	}
	// 立即 ApplySecretACL 缩小明文窗口：文件继承父目录 ACL 后即使没这步也受限，
	// 这步是 belt-and-suspenders（防 GPO 在 EnsureSecureDir 后改父目录）。
	if err := applySecretACLFn(s.path); err != nil {
		cleanupErr := os.Remove(s.path)
		return fmt.Errorf("apply ACL: %w (cleanup: %v)", err, cleanupErr)
	}
	if err := verifySecretACLFn(s.path); err != nil {
		cleanupErr := os.Remove(s.path)
		return fmt.Errorf("verify ACL: %w (cleanup: %v)", err, cleanupErr)
	}
	// round-trip 验证（ACL 已限制，普通用户即使能 round-trip 也需要 SYSTEM 身份）
	data, err := os.ReadFile(s.path)
	if err != nil {
		cleanupErr := os.Remove(s.path)
		return fmt.Errorf("read secrets.enc for verify: %w (cleanup: %v)", err, cleanupErr)
	}
	decrypted, err := dpapi.Unprotect(data)
	if err != nil {
		cleanupErr := os.Remove(s.path)
		return fmt.Errorf("decrypt secrets.enc for verify: %w (cleanup: %v)", err, cleanupErr)
	}
	if !bytes.Equal(decrypted, token) {
		cleanupErr := os.Remove(s.path)
		return fmt.Errorf("secrets.enc round-trip mismatch (cleanup: %v)", cleanupErr)
	}
	return nil
}

// Remove 删除 secrets.enc。不存在时不报错。
func (s *WindowsSecretStore) Remove() error {
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove secrets.enc: %w", err)
	}
	return nil
}

// Rotate 原子地替换 secrets.enc 中的 token。
//
// 流程：
//  1. EnsureSecureDir(parent) — 父目录受限（idempotent，首次后幂等）
//  2. dpapi.Protect(token) → 加密
//  3. os.CreateTemp(dir, "secrets-*.tmp") — 随机 tmp 名（防并发竞态），
//     继承父目录 ACL（无明文窗口）
//  4. Write encrypted to tmp + Close
//  5. ApplySecretACL(tmpPath) — 显式 /inheritance:r
//  6. ReadFile + dpapi.Unprotect → round-trip 验证
//  7. os.Rename(tmpPath, s.path) — 原子替换（MoveFileEx REPLACE_EXISTING）
//  8. VerifySecretACL(s.path) — 失败时仅 warn（rename 后新 token 已生效，
//     返回 error 会让调用方误以为可重试，实际旧 token 已被覆盖）
//
// 轮转过程中进程崩溃：tmp 文件残留，secrets.enc 保持旧内容不受影响。
func (s *WindowsSecretStore) Rotate(token []byte) error {
	dir := filepath.Dir(s.path)
	if err := ensureSecureDirFn(dir); err != nil {
		return fmt.Errorf("secure parent dir: %w", err)
	}
	encrypted, err := dpapi.Protect(token)
	if err != nil {
		return fmt.Errorf("dpapi encrypt token: %w", err)
	}
	// 随机 tmp 名（防并发竞态）+ 继承父目录 ACL（无明文窗口）
	f, err := os.CreateTemp(dir, "secrets-*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp secrets: %w", err)
	}
	tmpPath := f.Name()
	defer func() {
		// 兜底清理：如果函数在 Rename 之前 return，tmp 文件应被删除
		if _, statErr := os.Stat(tmpPath); statErr == nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := f.Write(encrypted); err != nil {
		_ = f.Close()
		return fmt.Errorf("write tmp secrets: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp secrets: %w", err)
	}
	// 显式 ApplySecretACL（防 GPO 改父目录后影响）
	if err := applySecretACLFn(tmpPath); err != nil {
		return fmt.Errorf("apply ACL on tmp: %w", err)
	}
	// round-trip 验证
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return fmt.Errorf("read tmp secrets for verify: %w", err)
	}
	decrypted, err := dpapi.Unprotect(data)
	if err != nil {
		return fmt.Errorf("decrypt tmp secrets for verify: %w", err)
	}
	if !bytes.Equal(decrypted, token) {
		return fmt.Errorf("tmp secrets round-trip mismatch")
	}
	// 原子替换：os.Rename 在 Windows 上等价于 MoveFileEx(MOVEFILE_REPLACE_EXISTING)
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("rename tmp to secrets.enc: %w", err)
	}
	// rename 后 tmpPath 已不存在，清空 defer 兜底
	tmpPath = ""
	// rename 后 verify（防 GPO / AV 在 rename 后修改 ACL）
	// 注意：失败时仅 warn，因为新 token 已经生效。返回 error 会让调用方误以为
	// 可重试 Rotate，实际旧 token 已被覆盖，重试会再生成新密文。
	if err := verifySecretACLFn(s.path); err != nil {
		slog.Warn("verify ACL after rotate failed (new token already in effect, manual remediation needed)",
			"path", s.path, "err", err)
	}
	return nil
}
