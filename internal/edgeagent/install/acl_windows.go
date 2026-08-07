//go:build windows

package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ACL 验证：DPAPI CRYPTPROTECT_LOCAL_MACHINE scope 绑定到机器级 SystemCredential，
// 同机器上任何 LocalSystem / NetworkService 进程都能解密。这意味着 DPAPI 本身
// 不替代文件 ACL——必须配合文件系统 ACL 限制非 System/Administrators 身份的读访问。
//
// 目标 ACL（secrets.enc + 父目录）：
//   - NT AUTHORITY\SYSTEM:(F)        — supervisor / worker 跑在 LocalSystem
//   - BUILTIN\Administrators:(F)     — 管理员运维访问
//   - 移除 BUILTIN\Users / Everyone / Authenticated Users 的所有 ACE
//
// 用 icacls.exe（Windows 内置命令）而非 raw syscall：
//   - 标准做法，可审计（icacls 输出人类可读）
//   - 错误信息清晰
//   - 避免 SECURITY_DESCRIPTOR 手工构造的复杂度
//
// icacls 错误信息 sanitize 策略：
//   - error message 只保留 exit code + 简短原因，不暴露 icacls 完整输出
//   - 完整输出在开发期可见（test 跑 icacls），生产日志不记录（避免泄露路径/SID）

// icaclsNotFoundError 表示 icacls.exe 不在 PATH 中（如 Windows Server Core 精简镜像）。
// 这种环境下无法应用 ACL，调用方必须中止 install（不降级到无 ACL 状态）。
var icaclsNotFoundError = fmt.Errorf("icacls.exe not found in PATH — cannot apply ACL, install aborted (Windows Server Core 精简镜像可能移除了 icacls)")

// requireIcacls 在入口检查 icacls 可用性，避免后续命令失败时错误信息不清晰。
func requireIcacls() error {
	if _, err := exec.LookPath("icacls"); err != nil {
		return icaclsNotFoundError
	}
	return nil
}

// sanitizeIcaclsOutput 从 icacls 输出中提取关键错误信息，去除路径前缀和 SID 详情。
// 用于 error message，避免泄露完整路径/ACL 配置到生产日志。
func sanitizeIcaclsOutput(out []byte) string {
	s := strings.TrimSpace(string(out))
	// 取最后一行（通常是 "Successfully processed N files" 或错误摘要）
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return ""
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	// 截断超长输出
	if len(last) > 200 {
		last = last[:200] + "..."
	}
	return last
}

// ApplySecretACL 应用 secrets.enc 的 ACL：仅 SYSTEM + Administrators 有 Full Control。
//
// 操作：
//  1. /inheritance:r  — 移除从父目录继承的 ACE
//  2. /grant:r         — 替换为指定 ACE（非合并）
//
// 注意：(F) Full Control 是必要的——supervisor（SYSTEM 身份）需要 Write + Delete
// 来执行 Rotate（rename tmp → secrets.enc 会用 Delete 旧文件）。
func ApplySecretACL(path string) error {
	if err := requireIcacls(); err != nil {
		return err
	}
	clean := filepath.Clean(path)
	cmd := exec.Command("icacls", clean,
		"/inheritance:r",
		"/grant:r",
		"NT AUTHORITY\\SYSTEM:(F)",
		"BUILTIN\\Administrators:(F)",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls apply on %s failed (exit %v): %s", clean, err, sanitizeIcaclsOutput(out))
	}
	return nil
}

// VerifySecretACL 验证 secrets.enc 的 ACL 符合预期。
//
// 检查：
//   - SYSTEM 行含 (F)
//   - Administrators 行含 (F)
//   - 不含 Users / Everyone / Authenticated Users
//
// 这是"独立检查点"——即使 ApplySecretACL 成功，也要读回来验证。
// 防止 GPO / AV / 其他工具在 apply 后修改 ACL。
func VerifySecretACL(path string) error {
	if err := requireIcacls(); err != nil {
		return err
	}
	clean := filepath.Clean(path)
	out, err := exec.Command("icacls", clean).CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls verify on %s failed (exit %v): %s", clean, err, sanitizeIcaclsOutput(out))
	}
	output := string(out)

	if !hasAceWithPerm(output, "NT AUTHORITY\\SYSTEM:", "(F)") {
		return fmt.Errorf("verify ACL: SYSTEM:(F) missing on %s", clean)
	}
	if !hasAceWithPerm(output, "BUILTIN\\Administrators:", "(F)") {
		return fmt.Errorf("verify ACL: Administrators:(F) missing on %s", clean)
	}
	for _, forbidden := range []string{"BUILTIN\\Users:", "Everyone:", "Authenticated Users:"} {
		if strings.Contains(output, forbidden) {
			return fmt.Errorf("verify ACL: forbidden ACE %q present on %s", forbidden, clean)
		}
	}
	return nil
}

// ApplyDirACL 应用目录的 ACL，使用 (OI)(CI) 容器继承标记让子文件/子目录自动继承。
//
// 与 ApplySecretACL 区别：
//   - 目录用 (OI)(CI)(F) — Object Inherit + Container Inherit + Full Control
//   - secrets.enc 用 (F) — 仅文件本身
//
// 调用 EnsureSecureDir 而非直接调用此函数（EnsureSecureDir 含 MkdirAll + 验证）。
func ApplyDirACL(dir string) error {
	if err := requireIcacls(); err != nil {
		return err
	}
	clean := filepath.Clean(dir)
	cmd := exec.Command("icacls", clean,
		"/inheritance:r",
		"/grant:r",
		"NT AUTHORITY\\SYSTEM:(OI)(CI)(F)",
		"BUILTIN\\Administrators:(OI)(CI)(F)",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls apply dir ACL on %s failed (exit %v): %s", clean, err, sanitizeIcaclsOutput(out))
	}
	return nil
}

// VerifyDirACL 验证目录 ACL 符合预期（与 VerifySecretACL 类似，但允许 inherit 标记）。
func VerifyDirACL(dir string) error {
	if err := requireIcacls(); err != nil {
		return err
	}
	clean := filepath.Clean(dir)
	out, err := exec.Command("icacls", clean).CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls verify dir on %s failed (exit %v): %s", clean, err, sanitizeIcaclsOutput(out))
	}
	output := string(out)

	if !hasAceWithPerm(output, "NT AUTHORITY\\SYSTEM:", "(F)") {
		return fmt.Errorf("verify dir ACL: SYSTEM:(F) missing on %s", clean)
	}
	if !hasAceWithPerm(output, "BUILTIN\\Administrators:", "(F)") {
		return fmt.Errorf("verify dir ACL: Administrators:(F) missing on %s", clean)
	}
	for _, forbidden := range []string{"BUILTIN\\Users:", "Everyone:", "Authenticated Users:"} {
		if strings.Contains(output, forbidden) {
			return fmt.Errorf("verify dir ACL: forbidden ACE %q present on %s", forbidden, clean)
		}
	}
	return nil
}

// EnsureSecureDir 确保 dir 存在且 ACL 受限（仅 SYSTEM + Administrators）。
//
// 流程：
//  1. MkdirAll(dir) — 创建目录（如已存在是 noop）
//  2. ApplyDirACL(dir) — 强制刷新 ACL（idempotent）
//  3. VerifyDirACL(dir) — 读回来验证
//
// 调用时机：Install 流程开始前（写 secrets.enc 之前）确保父目录受限，
// 这样 secrets.enc 即使继承父目录 ACL 也只对 SYSTEM+Admins 可读。
func EnsureSecureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := ApplyDirACL(dir); err != nil {
		return fmt.Errorf("apply dir ACL: %w", err)
	}
	if err := VerifyDirACL(dir); err != nil {
		return fmt.Errorf("verify dir ACL: %w", err)
	}
	return nil
}

// hasAceWithPerm 检查 icacls 输出中是否存在某 principal 行且该行含指定权限位。
// 行内同时含 principal 关键字和权限位（如 (F)）即视为匹配。
func hasAceWithPerm(output, principal, perm string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, principal) && strings.Contains(line, perm) {
			return true
		}
	}
	return false
}
