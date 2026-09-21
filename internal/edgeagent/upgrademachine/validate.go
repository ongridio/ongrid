// validate.go 实现 manifest 条目的路径/权限白名单校验 + kill 双防线。
//
// 威胁模型：被篡改的 bundle / 部分被控的 manager。
// 三重 Dest 校验把「写什么」和「杀谁」同时锁死：
//  1. 文件名段字符正则 — 拒 ADS 冒号、尾点、尾空格、8.3 短名（~）、路径分隔符变体
//  2. 文件名 ∈ 构建期已知精确集合（EqualFold，NTFS 大小写不敏感语义）
//  3. 父目录 canonical（EvalSymlinks）+ 受管根前缀断言 — 拒 symlink/junction 逃逸与
//     兄弟目录前缀碰撞（尾随分隔符断言）
//
// 纯 Go（无 Windows 专属依赖），Linux CI 可跑；EqualFold 对大小写敏感文件系统无害
// （canonical 路径源自同一根字符串派生，大小写一致）。

package upgrademachine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// destBase 取 Dest/镜像路径的 basename。Dest 是 supervisor 产出的 Windows
// 路径（反斜杠分隔）；filepath.Base 在非 Windows 平台不认 '\'，跨平台测试
// （Linux CI）会把整串当 basename 导致白名单误拒，故对两种分隔符取最后一段。
func destBase(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// destBaseWhitelist 是 Dest 文件名的精确白名单。
//
// 单一真相源镜像：dist/build-edge-bundle-windows.sh 的 ENTRIES（构建侧固定清单）。
// 插件文件名是构建期已知的，不是运行期用户输入 —
// 新增插件 = 同步改构建脚本 + 这里（升级白名单与 bundle 产物同步演进）。
// KillManifestExes 复用同一集合：kill 目标 ⊆ swap 目标（kill 不存在独立的合法面）。
var destBaseWhitelist = []string{
	WorkerBinaryName,
	SupervisorBinaryName,
	"windows_exporter.exe",
	"promtail.exe",
	"otelcol-contrib.exe",
}

// destBasePattern 是文件名段字符白名单（一律拒绝，不做宽容清洗）。
// 字符集排除冒号（ADS）、波浪号（8.3 短名）、空格；`.exe$` 锚点排除尾点；
// (?i) 对齐 NTFS 大小写不敏感语义（与白名单 EqualFold 一致）。
var destBasePattern = regexp.MustCompile(`(?i)^[A-Za-z0-9._-]+\.exe$`)

// protectedSystemImages 是 KillByImage 实现层的系统关键镜像黑名单。
//
// 异源第二道防线：kill 路径不经 Dest 写校验是独立旁路，黑名单
// 与白名单不同代码路径、不同失败模式。杀这些镜像 = 系统失稳（lsass 触发强制重启、
// MsMpEng 是 Defender 自身）。explorer.exe 等用户会话进程刻意不入名单（YAGNI）。
var protectedSystemImages = []string{
	"smss.exe",
	"csrss.exe",
	"wininit.exe",
	"winlogon.exe",
	"services.exe",
	"lsass.exe",
	"svchost.exe",
	"MsMpEng.exe",
}

// IsProtectedSystemImage 报告 name（取 basename，大小写不敏感）是否属于系统关键镜像
// 黑名单。KillByImage 实现层调用：命中即拒绝执行（返回 error 而非静默跳过 —
// 显式失败让攻击与配置错误都可见）。
func IsProtectedSystemImage(name string) bool {
	base := destBase(name)
	for _, p := range protectedSystemImages {
		if strings.EqualFold(base, p) {
			return true
		}
	}
	return false
}

// ValidateAllEntries 校验全部 manifest 条目（all-or-nothing，遇首错即返回）。
// Machine.Apply 在 ParseManifest 之后、任何 kill/swap 之前调用 — 校验失败时
// 零磁盘变动、零进程终止。
//
// context 约定：入口处 ctx.Err guard（对称 ApplyBundle
// 的入口取消检查）；进入逐条校验后不中断（与后续 swap 的原子语义衔接）。
func ValidateAllEntries(ctx context.Context, binDir, incomingDir string, entries []ManifestEntry) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, e := range entries {
		if err := validateMode(e.Mode); err != nil {
			return fmt.Errorf("entry %s: %w", e.Src, err)
		}
		if err := validateSrc(incomingDir, e.Src); err != nil {
			return fmt.Errorf("entry src %s: %w", e.Src, err)
		}
		if err := validateDest(binDir, e.Dest); err != nil {
			return fmt.Errorf("entry dest %s: %w", e.Dest, err)
		}
	}
	return nil
}

// validateMode：Mode 字段白名单。
// 仅接受 bundle 产物实际使用的两个值 + 空串（空 = 不 chmod，applyOne 对空
// Mode 跳过权限设置 — 兼容无权限语义的 manifest 条目）；其余拒绝 —
// 防 read-only（0444）注入让下轮升级 rename 撞只读位永久 brick，
// 也防 parseMode 对非八进制字符静默截断。
// 空串放行属实施期偏差（设计原文两值），理由：拒绝空串会让
// 「不设权限」这一合法语义不可表达，且空串无攻击面（不进 chmod 路径）。
func validateMode(mode string) error {
	switch mode {
	case "", "0755", "0644":
		return nil
	}
	return fmt.Errorf("mode %q not in whitelist (allowed: 0755/0644/empty)", mode)
}

// validateDest 三重校验。
func validateDest(binDir, dest string) error {
	base := destBase(dest)
	if !destBasePattern.MatchString(base) {
		return fmt.Errorf("dest base %q fails filename pattern (rejects ADS colon/trailing dot/8.3 short name)", base)
	}
	if !destBaseAllowed(base) {
		return fmt.Errorf("dest base %q not in whitelist", base)
	}
	// 父目录 canonical（dest 文件可能不存在（首装），对父目录做 EvalSymlinks —
	// 父目录必须存在且解析后仍在受管根内，junction/symlink 逃逸在此被拒）
	if err := ensureWithinDir(binDir, filepath.Dir(dest)); err != nil {
		return fmt.Errorf("dest dir: %w", err)
	}
	return nil
}

// destBaseAllowed：basename ∈ 白名单（EqualFold 全链路统一比较语义，H3）。
func destBaseAllowed(base string) bool {
	for _, w := range destBaseWhitelist {
		if strings.EqualFold(base, w) {
			return true
		}
	}
	return false
}

// validateSrc：src 是 incoming 内的相对路径，必须是常规文件。
func validateSrc(incomingDir, src string) error {
	// src 永不支持空格 — 显式拒绝（解析格式固定，不做「兼容」）
	if strings.ContainsAny(src, " \t") {
		return fmt.Errorf("src %q contains whitespace (never supported)", src)
	}
	if filepath.IsAbs(src) {
		return fmt.Errorf("src %q must be relative to incoming dir", src)
	}
	srcPath := filepath.Join(incomingDir, src)
	// Lstat 不解引用 — symlink/管道/目录一律拒（解引用语义的 io.Copy 有
	// 内容污染与挂起面）
	if fi, err := os.Lstat(srcPath); err != nil {
		return fmt.Errorf("lstat src: %w", err)
	} else if !fi.Mode().IsRegular() {
		return fmt.Errorf("src %q is not a regular file (mode %s)", src, fi.Mode())
	}
	// canonical 前缀：src 所在目录解析后不得逃逸 incoming（防目录挂载点逃逸；
	// symlink 已被 Lstat 拒，此处是纵深第二层）
	if err := ensureWithinDir(incomingDir, filepath.Dir(srcPath)); err != nil {
		return fmt.Errorf("src dir: %w", err)
	}
	return nil
}

// ensureWithinDir 断言 canonical(path) 位于 canonical(root) 之内（含等于 root）。
//
// 比较语义：尾随分隔符断言防兄弟目录前缀碰撞（upgrade vs upgrade-evil）；
// EqualFold 对齐 NTFS 大小写不敏感语义（大小写敏感文件系统上 canonical 路径
// 同源派生、大小写一致，EqualFold 无害）。
func ensureWithinDir(rootDir, path string) error {
	rootCanon, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		return fmt.Errorf("canonical root %s: %w", rootDir, err)
	}
	pathCanon, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("canonical %s: %w", path, err)
	}
	if strings.EqualFold(pathCanon, rootCanon) {
		return nil
	}
	rootPrefix := rootCanon + string(filepath.Separator)
	if len(pathCanon) > len(rootPrefix) && strings.EqualFold(pathCanon[:len(rootPrefix)], rootPrefix) {
		return nil
	}
	return fmt.Errorf("canonical %s escapes managed root %s", pathCanon, rootCanon)
}
