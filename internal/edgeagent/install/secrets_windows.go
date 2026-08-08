//go:build windows

package install

import (
	"bytes"
	"fmt"
	"os"

	"github.com/ongridio/ongrid/internal/edgeagent/dpapi"
)

// WindowsSecretStore 是 SecretStore 的 Windows 实现，
// 使用 DPAPI CryptProtectData 加密凭证（ADR-037 A2 CR4）。
type WindowsSecretStore struct {
	path string
}

// NewSecretStore 创建 Windows DPAPI SecretStore。
// secretsPath 是 secrets.enc 的完整路径。
func NewSecretStore(secretsPath string) SecretStore {
	return &WindowsSecretStore{path: secretsPath}
}

// Install 加密 token 并写入 secrets.enc，含 round-trip 验证。
//
// 流程：
//  1. dpapi.Protect(token) → 加密
//  2. os.WriteFile(path, encrypted, 0600)
//  3. 读回 + dpapi.Unprotect → 验证 round-trip
//  4. 验证失败时删除文件（保持调用前状态）
//
// token 的 []byte 副本清零由调用方负责（defer zeroBytes）。
func (s *WindowsSecretStore) Install(token []byte) error {
	encrypted, err := dpapi.Protect(token)
	if err != nil {
		return fmt.Errorf("dpapi encrypt token: %w", err)
	}
	if err := os.WriteFile(s.path, encrypted, 0600); err != nil {
		return fmt.Errorf("write secrets.enc: %w", err)
	}
	// round-trip 验证
	data, err := os.ReadFile(s.path)
	if err != nil {
		_ = os.Remove(s.path)
		return fmt.Errorf("read secrets.enc for verify: %w", err)
	}
	decrypted, err := dpapi.Unprotect(data)
	if err != nil {
		_ = os.Remove(s.path)
		return fmt.Errorf("decrypt secrets.enc for verify: %w", err)
	}
	if !bytes.Equal(decrypted, token) {
		_ = os.Remove(s.path)
		return fmt.Errorf("secrets.enc round-trip mismatch")
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

// Rotate 原子地替换 secrets.enc 中的 token（#16 D4 tmp→rename）。
//
// 流程：
//  1. dpapi.Protect(newToken) → 加密
//  2. 写入 tmp 文件（path + ".tmp"）
//  3. round-trip 验证 tmp 文件
//  4. os.Rename(tmp, path) — Windows 上 ReplaceFile/os.Rename 是原子操作
//  5. 验证失败或任何错误 → 删除 tmp，旧文件不受影响
//
// 轮转过程中进程崩溃：tmp 文件残留，secrets.enc 保持旧内容不受影响。
func (s *WindowsSecretStore) Rotate(token []byte) error {
	encrypted, err := dpapi.Protect(token)
	if err != nil {
		return fmt.Errorf("dpapi encrypt token: %w", err)
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, encrypted, 0600); err != nil {
		return fmt.Errorf("write tmp secrets: %w", err)
	}
	// round-trip 验证 tmp 文件
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath) // best-effort: 清理 tmp，错误由 return 暴露
		return fmt.Errorf("read tmp secrets for verify: %w", err)
	}
	decrypted, err := dpapi.Unprotect(data)
	if err != nil {
		_ = os.Remove(tmpPath) // best-effort: 清理 tmp，错误由 return 暴露
		return fmt.Errorf("decrypt tmp secrets for verify: %w", err)
	}
	if !bytes.Equal(decrypted, token) {
		_ = os.Remove(tmpPath) // best-effort: 清理 tmp，错误由 return 暴露
		return fmt.Errorf("tmp secrets round-trip mismatch")
	}
	// 原子替换：os.Rename 在 Windows 上等价于 MoveFileEx(MOVEFILE_REPLACE_EXISTING)
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath) // best-effort: 清理 tmp，错误由 return 暴露
		return fmt.Errorf("rename tmp to secrets.enc: %w", err)
	}
	return nil
}
