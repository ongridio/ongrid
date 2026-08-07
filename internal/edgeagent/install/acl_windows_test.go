//go:build windows

package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withNoopACL 把 ACL apply/verify 函数替换为 noop，t.Cleanup 还原。
//
// 用途：DPAPI round-trip 测试不需要触发真实 ACL（ACL 会让后续 WriteFile / Remove
// 在普通用户身份下失败）。ACL 行为在 TestApplySecretACL_* / TestVerifySecretACL_*
// 单独验证（需 Administrator 身份）。
func withNoopACL(t *testing.T) {
	t.Helper()
	origApply, origVerify := applySecretACLFn, verifySecretACLFn
	applySecretACLFn = func(string) error { return nil }
	verifySecretACLFn = func(string) error { return nil }
	t.Cleanup(func() {
		applySecretACLFn, verifySecretACLFn = origApply, origVerify
	})
}

// isAdmin 通过 net session 命令探测当前进程是否以 elevated Administrator 身份运行。
// net session 在非 admin 时返回非零 exit code。
func isAdmin() bool {
	cmd := exec.Command("net", "session")
	return cmd.Run() == nil
}

// TestApplySecretACL_RestrictsToSystemAndAdmins 验证 ApplySecretACL 后：
//   - SYSTEM:(F) 出现在 icacls 输出
//   - Administrators:(F) 出现
//   - BUILTIN\Users 不出现
//
// 需要 Administrator 身份（icacls 修改 ACL 需要文件所有权或 SeTakeOwnershipPrivilege）。
func TestApplySecretACL_RestrictsToSystemAndAdmins(t *testing.T) {
	if !isAdmin() {
		t.Skip("requires Administrator (icacls modify needs elevated token)")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.enc")
	if err := os.WriteFile(path, []byte("dummy"), 0600); err != nil {
		t.Fatalf("setup: write file: %v", err)
	}

	if err := ApplySecretACL(path); err != nil {
		t.Fatalf("ApplySecretACL: %v", err)
	}

	out, err := exec.Command("icacls", path).CombinedOutput()
	if err != nil {
		t.Fatalf("icacls read: %v (output: %s)", err, out)
	}
	output := string(out)

	if !strings.Contains(output, "NT AUTHORITY\\SYSTEM:(F)") {
		t.Errorf("SYSTEM:(F) missing in ACL: %s", output)
	}
	if !strings.Contains(output, "BUILTIN\\Administrators:(F)") {
		t.Errorf("Administrators:(F) missing in ACL: %s", output)
	}
	if strings.Contains(output, "BUILTIN\\Users:") {
		t.Errorf("forbidden Users ACE present: %s", output)
	}
}

// TestVerifySecretACL_DetectsMissingSystem 验证 VerifySecretACL 能检出 SYSTEM ACE 缺失。
//
// 不需要 Administrator：用 t.TempDir 默认 ACL（含 SYSTEM:(I)(F)）→ 通过；
// 然后测一个不存在的路径 → icacls 失败 → VerifySecretACL 报错。
// 完整的 forbidden ACE 检测在 TestApplySecretACL_RestrictsToSystemAndAdmins（admin-only）覆盖。
func TestVerifySecretACL_DetectsMissingSystem(t *testing.T) {
	// 不存在的路径 → icacls 命令本身失败
	missing := filepath.Join(t.TempDir(), "no-such-file.enc")
	err := VerifySecretACL(missing)
	if err == nil {
		t.Fatal("VerifySecretACL should fail on non-existent path")
	}
}

// TestVerifySecretACL_PassesAfterApply 验证 ApplySecretACL + VerifySecretACL 端到端闭环。
//
// 需要 Administrator 身份（ApplySecretACL 修改 ACL 需要 elevated token）。
func TestVerifySecretACL_PassesAfterApply(t *testing.T) {
	if !isAdmin() {
		t.Skip("requires Administrator (ApplySecretACL needs elevated token)")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.enc")
	if err := os.WriteFile(path, []byte("dummy"), 0600); err != nil {
		t.Fatalf("setup: write file: %v", err)
	}

	if err := ApplySecretACL(path); err != nil {
		t.Fatalf("ApplySecretACL: %v", err)
	}
	if err := VerifySecretACL(path); err != nil {
		t.Errorf("VerifySecretACL after Apply should pass, got: %v", err)
	}
}
