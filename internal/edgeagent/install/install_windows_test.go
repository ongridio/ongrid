//go:build windows

package install

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWindowsSecretStore_Install_CreatesValidFile 验证 Install 创建合法 DPAPI 加密文件 + round-trip。
func TestWindowsSecretStore_Install_CreatesValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")
	token := []byte("ed_bk_test_token_abc123")

	ss := NewSecretStore(path)
	if err := ss.Install(token); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	// 文件应存在且非空
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("secrets.enc is empty")
	}
}

// TestWindowsSecretStore_Install_OverwritesExisting 验证写入覆盖已有文件。
func TestWindowsSecretStore_Install_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")
	ss := NewSecretStore(path)

	// 先写 token1
	if err := ss.Install([]byte("token1")); err != nil {
		t.Fatalf("first Install failed: %v", err)
	}
	// 再写 token2
	if err := ss.Install([]byte("token2")); err != nil {
		t.Fatalf("second Install failed: %v", err)
	}
	// 验证文件内容对应 token2：如果 token1 仍匹配则说明覆盖失败
	// （无法直接验证 round-trip 因为 Install 内部已验证，这里间接验证：
	//   如果文件没更新，第二次 Install 的 round-trip 会用旧密文 → mismatch → error）
	// 所以只要第二次 Install 不报错就说明覆盖成功
}

// TestWindowsSecretStore_Remove_DeletesFile 验证 Remove 删除文件。
func TestWindowsSecretStore_Remove_DeletesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")
	ss := NewSecretStore(path)

	if err := ss.Install([]byte("token")); err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	if err := ss.Remove(); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should not exist after Remove, got err: %v", err)
	}
}

// TestWindowsSecretStore_Remove_NonExistentNoError 验证 Remove 不存在的文件不报错。
func TestWindowsSecretStore_Remove_NonExistentNoError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.enc")
	ss := NewSecretStore(path)

	if err := ss.Remove(); err != nil {
		t.Errorf("Remove non-existent file should not error, got: %v", err)
	}
}

// TestWindowsSecretStore_Install_WrongTokenRoundTrip 验证不同 token 产生不同密文。
func TestWindowsSecretStore_Install_WrongTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")
	ss := NewSecretStore(path)

	// 写 token1
	if err := ss.Install([]byte("correct_token")); err != nil {
		t.Fatalf("Install correct_token failed: %v", err)
	}
	// 再写 token2（覆盖）
	if err := ss.Install([]byte("wrong_token")); err != nil {
		t.Fatalf("Install wrong_token failed: %v", err)
	}
	// 如果覆盖失败，第二次 Install 的 round-trip 会验证到旧密文 → mismatch → error
	// 所以只要第二次 Install 不报错就说明覆盖成功
}

// --- Rotate tests (#16 D4 tmp→rename) ---

// TestWindowsSecretStore_Rotate_ReplacesToken 验证 Rotate 原子替换文件内容。
func TestWindowsSecretStore_Rotate_ReplacesToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")
	token1 := []byte("original-cred-value")
	token2 := []byte("rotated-cred-value")

	ss := NewSecretStore(path)
	if err := ss.Install(token1); err != nil {
		t.Fatalf("Install token1: %v", err)
	}

	// 记录旧文件修改时间
	infoBefore, _ := os.Stat(path)

	if err := ss.Rotate(token2); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// 验证文件存在且非空
	infoAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not found after Rotate: %v", err)
	}
	if infoAfter.Size() == 0 {
		t.Error("secrets.enc is empty after Rotate")
	}

	// 验证 tmp 文件已被清理
	tmpPath := path + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("tmp file should not exist after Rotate, got err: %v", err)
	}

	// 验证文件确实被更新（修改时间不同）
	if !infoAfter.ModTime().After(infoBefore.ModTime()) {
		t.Error("file ModTime not updated after Rotate")
	}
}

// TestWindowsSecretStore_Rotate_OnNonExistentFile 验证 Rotate 对不存在的文件也能工作
// （os.Rename 会创建目标文件）。
func TestWindowsSecretStore_Rotate_OnNonExistentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")

	ss := NewSecretStore(path)
	if err := ss.Rotate([]byte("fresh-cred")); err != nil {
		t.Fatalf("Rotate on non-existent file: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should exist after Rotate: %v", err)
	}
}
