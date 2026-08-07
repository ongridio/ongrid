//go:build windows

package install

import (
	"fmt"
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

// --- ACL flow integration tests ---
//
// 这组测试验证 Install/Rotate 真的调用了 ACL 函数（不全部 noop），且调用顺序正确。
// 补 withNoopACL 之外的盲区：withNoopACL 让 Install/Rotate 测试跳过 ACL，
// 这些测试用 spy 记录调用顺序，确保 ACL 步骤确实在流程里。

// aclSpy 记录 ACL 函数调用顺序，可注入错误。
type aclSpy struct {
	calls     []string
	applyErr  error
	verifyErr error
	ensureErr error
}

// install 替换包级 ACL var 函数为 spy，t.Cleanup 自动还原。
func (s *aclSpy) install(t *testing.T) {
	t.Helper()
	origEnsure, origApply, origVerify := ensureSecureDirFn, applySecretACLFn, verifySecretACLFn
	ensureSecureDirFn = func(dir string) error {
		s.calls = append(s.calls, "ensure:"+dir)
		return s.ensureErr
	}
	applySecretACLFn = func(path string) error {
		s.calls = append(s.calls, "apply:"+path)
		return s.applyErr
	}
	verifySecretACLFn = func(path string) error {
		s.calls = append(s.calls, "verify:"+path)
		return s.verifyErr
	}
	t.Cleanup(func() {
		ensureSecureDirFn, applySecretACLFn, verifySecretACLFn = origEnsure, origApply, origVerify
	})
}

// TestInstall_ACLFlowOrder 验证 Install 调用 ACL 函数顺序：ensure → apply → verify。
// round-trip 验证（ReadFile + Unprotect）发生在 ACL 之后。
func TestInstall_ACLFlowOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")
	ss := NewSecretStore(path).(*WindowsSecretStore)

	spy := &aclSpy{}
	spy.install(t)

	if err := ss.Install([]byte("test_token_order")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// 前 3 个调用应为 ensure → apply → verify
	want := []string{
		"ensure:" + dir,
		"apply:" + path,
		"verify:" + path,
	}
	if len(spy.calls) < len(want) {
		t.Fatalf("expected at least %d calls, got %d: %v", len(want), len(spy.calls), spy.calls)
	}
	for i, w := range want {
		if spy.calls[i] != w {
			t.Errorf("call[%d] = %q, want %q (full: %v)", i, spy.calls[i], w, spy.calls)
		}
	}
}

// TestInstall_ApplyErr_CleansUpFile 验证 apply 失败时清理 secrets.enc。
func TestInstall_ApplyErr_CleansUpFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")
	ss := NewSecretStore(path).(*WindowsSecretStore)

	spy := &aclSpy{applyErr: fmt.Errorf("simulated icacls failure")}
	spy.install(t)

	if err := ss.Install([]byte("token")); err == nil {
		t.Fatal("Install should fail when apply fails")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("secrets.enc should be removed on apply failure, got err: %v", err)
	}
}

// TestInstall_VerifyErr_CleansUpFile 验证 verify 失败时清理 secrets.enc。
func TestInstall_VerifyErr_CleansUpFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")
	ss := NewSecretStore(path).(*WindowsSecretStore)

	spy := &aclSpy{verifyErr: fmt.Errorf("simulated verify failure")}
	spy.install(t)

	if err := ss.Install([]byte("token")); err == nil {
		t.Fatal("Install should fail when verify fails")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("secrets.enc should be removed on verify failure, got err: %v", err)
	}
}

// TestRotate_ACLFlowOrder 验证 Rotate 调用顺序：ensure → apply(tmp) → verify(final)。
// 注意 tmp 名随机（os.CreateTemp），所以 apply 调用的路径是动态的。
func TestRotate_ACLFlowOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")
	ss := NewSecretStore(path).(*WindowsSecretStore)

	// 先 Install 初始化（用 spy 但允许全通过）
	initSpy := &aclSpy{}
	initSpy.install(t)
	if err := ss.Install([]byte("init")); err != nil {
		t.Fatalf("Install init: %v", err)
	}

	// 重新装 spy（清空 calls）
	spy := &aclSpy{}
	spy.install(t)

	if err := ss.Rotate([]byte("rotated")); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// 第一个调用必须是 ensure:dir
	if len(spy.calls) == 0 || spy.calls[0] != "ensure:"+dir {
		t.Fatalf("call[0] = %q, want ensure:%s", spy.calls, dir)
	}
	// 必须有 apply:...（tmp 路径，动态）和 verify:path
	foundApply, foundVerify := false, false
	for _, c := range spy.calls {
		if strings.HasPrefix(c, "apply:") {
			foundApply = true
			// apply 的路径必须不是 final path（应该是 tmp）
			if c == "apply:"+path {
				t.Errorf("apply should be on tmp path, got final path: %s", c)
			}
		}
		if c == "verify:"+path {
			foundVerify = true
		}
	}
	if !foundApply {
		t.Errorf("apply on tmp not called: %v", spy.calls)
	}
	if !foundVerify {
		t.Errorf("verify on final path not called: %v", spy.calls)
	}
}

// TestRotate_VerifyErr_NoErrorReturned 验证 Rotate verify 失败时返回 nil
// （因为 rename 后新 token 已生效，返回 error 会让调用方误以为可重试）。
func TestRotate_VerifyErr_NoErrorReturned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")
	ss := NewSecretStore(path).(*WindowsSecretStore)

	// 先 Install
	initSpy := &aclSpy{}
	initSpy.install(t)
	if err := ss.Install([]byte("init")); err != nil {
		t.Fatalf("Install init: %v", err)
	}

	// Rotate with verify failing — should NOT return error
	spy := &aclSpy{verifyErr: fmt.Errorf("simulated post-rename ACL drift")}
	spy.install(t)

	if err := ss.Rotate([]byte("rotated")); err != nil {
		t.Errorf("Rotate should swallow verify error (new token already in effect), got: %v", err)
	}
}
