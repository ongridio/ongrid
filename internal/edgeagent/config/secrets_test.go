//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/dpapi"
)

// TestLoadSecrets_ValidFile 验证合法 secrets.enc 能正确解密出 token。
func TestLoadSecrets_ValidFile(t *testing.T) {
	token := "ed_bk_test_token_abc123"
	encrypted, err := dpapi.Protect([]byte(token))
	if err != nil {
		t.Fatalf("dpapi.Protect failed: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")
	if err := os.WriteFile(path, encrypted, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	got, err := LoadSecrets(path)
	if err != nil {
		t.Fatalf("LoadSecrets failed: %v", err)
	}
	if got != token {
		t.Errorf("token mismatch:\n  want %q\n  got  %q", token, got)
	}
}

// TestLoadSecrets_FileNotFound 验证文件不存在时返回可识别错误。
func TestLoadSecrets_FileNotFound(t *testing.T) {
	_, err := LoadSecrets(filepath.Join(t.TempDir(), "nonexistent.enc"))
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

// TestLoadSecrets_InvalidPayload 验证损坏文件返回错误。
func TestLoadSecrets_InvalidPayload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.enc")
	if err := os.WriteFile(path, []byte("not a valid dpapi blob"), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := LoadSecrets(path)
	if err == nil {
		t.Fatal("expected error for corrupt file, got nil")
	}
}

// TestLoadSecrets_EmptyFile 验证空文件返回错误。
func TestLoadSecrets_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.enc")
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := LoadSecrets(path)
	if err == nil {
		t.Fatal("expected error for empty file, got nil")
	}
}

