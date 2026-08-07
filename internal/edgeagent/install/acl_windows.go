//go:build windows

package install

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ACL 验证：DPAPI CRYPTPROTECT_LOCAL_MACHINE scope 绑定到机器级 SystemCredential，
// 同机器上任何 LocalSystem / NetworkService 进程都能解密。这意味着 DPAPI 本身
// 不替代文件 ACL——必须配合文件系统 ACL 限制非 System/Administrators 身份的读访问。
//
// 目标 ACL（secrets.enc + 父目录）：
//   - NT AUTHORITY\SYSTEM:(F)        — supervisor / worker 跑在 LocalSystem，需要 Read + Write + Delete（rotate）
//   - BUILTIN\Administrators:(F)     — 管理员运维访问（rotate / uninstall）
//   - 移除 BUILTIN\Users / Everyone / Authenticated Users 的所有 ACE
//
// 用 icacls.exe（Windows 内置命令）而非 raw syscall：
//   - 标准做法，可审计（icacls 输出人类可读）
//   - 错误信息清晰
//   - 避免 SECURITY_DESCRIPTOR 手工构造的复杂度

// ApplySecretACL 应用 secrets.enc 的 ACL：仅 SYSTEM + Administrators 有 Full Control。
//
// 操作：
//  1. /inheritance:r  — 移除从父目录继承的 ACE
//  2. /grant:r         — 替换为指定 ACE（非合并）
//
// 注意：(F) Full Control 是必要的——supervisor（SYSTEM 身份）需要 Write + Delete
// 来执行 Rotate（rename tmp → secrets.enc 会用 Delete 旧文件）。
func ApplySecretACL(path string) error {
	// icacls 在路径参数上接受正斜杠或反斜杠，但路径含空格时需要 quote。
	// 用 filepath.Clean 规范化（去 trailing slash 等）。
	clean := filepath.Clean(path)
	cmd := exec.Command("icacls", clean,
		"/inheritance:r",
		"/grant:r",
		"NT AUTHORITY\\SYSTEM:(F)",
		"BUILTIN\\Administrators:(F)",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("icacls apply ACL on %s: %w (output: %s)", clean, err, out)
	}
	return nil
}

// VerifySecretACL 验证 secrets.enc 的 ACL 符合预期。
//
// 读 icacls 输出，确认：
//   - SYSTEM 有 Full Control 权限（含或不含 inherit 标记）
//   - Administrators 有 Full Control 权限
//   - 不含 Users / Everyone / Authenticated Users 组（防普通用户读）
//
// 注意：这是"独立检查点"——即使 ApplySecretACL 成功，也要读回来验证。
// 防止 GPO / AV / 其他工具在 apply 后修改 ACL。
//
// 匹配策略：按行扫描，每行检查是否同时含 principal 关键字 + (F)。
// 这样兼容 icacls 各种 inherit 标记格式（(I)(F) / (OI)(CI)(F) / (F)）。
func VerifySecretACL(path string) error {
	clean := filepath.Clean(path)
	out, err := exec.Command("icacls", clean).CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls verify on %s: %w (output: %s)", clean, err, out)
	}
	output := string(out)

	// 按行检查 SYSTEM + Administrators 是否有 (F)
	if !hasAceWithPerm(output, "NT AUTHORITY\\SYSTEM:", "(F)") {
		return fmt.Errorf("verify ACL: SYSTEM:(F) missing on %s (output: %s)", clean, output)
	}
	if !hasAceWithPerm(output, "BUILTIN\\Administrators:", "(F)") {
		return fmt.Errorf("verify ACL: Administrators:(F) missing on %s (output: %s)", clean, output)
	}
	// 必须不出现 Users / Everyone / Authenticated Users（任何权限位）
	for _, forbidden := range []string{"BUILTIN\\Users:", "Everyone:", "Authenticated Users:"} {
		if strings.Contains(output, forbidden) {
			return fmt.Errorf("verify ACL: forbidden ACE %q present on %s (output: %s)", forbidden, clean, output)
		}
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
