// validate_test.go 测试 manifest 路径校验 + kill 双防线。
//
// 校验语义：
//   - Dest：文件名段字符正则（拒 ADS 冒号/尾点/尾空格/8.3 短名）+ 精确白名单
//     （构建期已知集，EqualFold）+ 父目录 canonical 前缀（拒 junction/symlink 逃逸）
//   - Src：canonical 前缀 + 常规文件（拒 symlink/管道/目录）+ 无空格
//   - Mode：仅 0755/0644/空
//   - KillManifestExes：basename 套同一白名单（拒任意镜像名旁路）
//   - KillByImage 实现层：系统关键镜像黑名单兜底
package upgrademachine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// --- 白名单集合本身 ---

func TestDestBaseWhitelist_Membership(t *testing.T) {
	// 构建期完整已知集（dist/build-edge-bundle-windows.sh ENTRIES 的 edge 侧镜像）
	want := []string{
		WorkerBinaryName, SupervisorBinaryName,
		"windows_exporter.exe", "promtail.exe", "otelcol-contrib.exe",
	}
	if len(destBaseWhitelist) != len(want) {
		t.Fatalf("白名单成员数 = %d, want %d: %v", len(destBaseWhitelist), len(want), destBaseWhitelist)
	}
	for _, w := range want {
		found := false
		for _, g := range destBaseWhitelist {
			if strings.EqualFold(g, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("白名单缺少 %q", w)
		}
	}
}

// --- Dest 校验 ---

func TestValidateEntry_DestWhitelist(t *testing.T) {
	binDir := t.TempDir()

	cases := []struct {
		name    string
		dest    string
		wantErr bool
	}{
		{"worker 合法", filepath.Join(binDir, WorkerBinaryName), false},
		{"supervisor 合法", filepath.Join(binDir, SupervisorBinaryName), false},
		{"plugin 合法", filepath.Join(binDir, "windows_exporter.exe"), false},
		{"系统镜像被拒", filepath.Join(binDir, "svchost.exe"), true},
		{"任意文件名被拒", filepath.Join(binDir, "evil.exe"), true},
		{"受管根外被拒", filepath.Join(t.TempDir(), WorkerBinaryName), true},
		{"父目录穿越被拒", filepath.Join(binDir, "..", "..", "Windows", "system32", "svchost.exe"), true},
		{"ADS 冒号被拒", filepath.Join(binDir, WorkerBinaryName+":hidden"), true},
		{"尾点被拒（Win32 落盘剥离后变合法名）", filepath.Join(binDir, WorkerBinaryName+"."), true},
		{"尾空格被拒（Win32 落盘剥离后变合法名）", filepath.Join(binDir, WorkerBinaryName+" "), true},
		{"8.3 短名被拒", filepath.Join(binDir, "WORKER~1.EXE"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDest(binDir, tc.dest)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateDest(%q) err=%v, wantErr=%v", tc.dest, err, tc.wantErr)
			}
		})
	}
	t.Run("大小写变体合法（NTFS 不区分大小写）", func(t *testing.T) {
		requireCaseInsensitiveDir(t, binDir)
		dest := filepath.Join(strings.ToUpper(binDir), strings.ToUpper(WorkerBinaryName))
		if err := validateDest(binDir, dest); err != nil {
			t.Fatalf("validateDest(%q): %v", dest, err)
		}
	})
}

// 只跳过不支持大小写变体的用例，其他白名单与越界检查仍在 Linux 执行。
func requireCaseInsensitiveDir(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(strings.ToUpper(dir)); err != nil {
		if runtime.GOOS == "windows" {
			t.Fatalf("Windows 回归测试需要大小写不敏感目录: %v", err)
		}
		t.Skipf("文件系统不支持大小写变体: %v", err)
	}
}

func TestEnsureWithinDir_CaseInsensitivePaths(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "managed")
	child := filepath.Join(root, "plugins")
	sibling := filepath.Join(parent, "managed-evil")
	for _, dir := range []string{child, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	requireCaseInsensitiveDir(t, root)
	for _, tc := range []struct {
		name    string
		root    string
		path    string
		wantErr bool
	}{
		{"same directory with upper-case path", root, strings.ToUpper(root), false},
		{"same directory with upper-case root", strings.ToUpper(root), root, false},
		{"child with upper-case path", root, strings.ToUpper(child), false},
		{"sibling prefix still rejected", root, strings.ToUpper(sibling), true},
		{"parent still rejected", root, strings.ToUpper(parent), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ensureWithinDir(tc.root, tc.path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ensureWithinDir(%q, %q) = %v, wantErr=%v", tc.root, tc.path, err, tc.wantErr)
			}
		})
	}
}

// symlink/junction 逃逸：binDir 内的子目录被符号链接指向受管根外 → dest 解析后逃逸被拒。
func TestValidateEntry_DestSymlinkEscape(t *testing.T) {
	if testing.Short() {
		t.Skip("symlink 需要真实文件系统")
	}
	binDir := t.TempDir()
	outside := t.TempDir()

	link := filepath.Join(binDir, "plugins")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("此环境不支持 symlink: %v", err)
	}

	// 字符串前缀合法（plugins 在 binDir 下），canonical 解析后指向 outside → 必须被拒
	err := validateDest(binDir, filepath.Join(link, "promtail.exe"))
	if err == nil {
		t.Errorf("symlink 逃逸必须被拒: dest=%s → %s", filepath.Join(link, "promtail.exe"), outside)
	}
}

// 兄弟目录前缀碰撞：upgrade-evil 不是 upgrade 的子目录，必须被拒（尾随分隔符断言）。
func TestValidateEntry_DestSiblingPrefixCollision(t *testing.T) {
	parent := t.TempDir()
	binDir := filepath.Join(parent, "upgrade")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	evilDir := filepath.Join(parent, "upgrade-evil")
	if err := os.MkdirAll(evilDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := validateDest(binDir, filepath.Join(evilDir, WorkerBinaryName)); err == nil {
		t.Errorf("兄弟目录前缀碰撞必须被拒")
	}
}

// --- Src 校验 ---

func TestValidateEntry_Src(t *testing.T) {
	incoming := t.TempDir()
	goodSrc := filepath.Join(incoming, "ongrid-edge-worker.exe")
	writeTestFile(t, goodSrc, "bin")

	cases := []struct {
		name    string
		src     string
		wantErr bool
	}{
		{"直接子文件合法", "ongrid-edge-worker.exe", false},
		{"incoming 内子目录合法", filepath.Join("sub", "file.exe"), false}, // 子目录由 fixture 创建
		{"绝对路径被拒", goodSrc, true},
		{"父目录穿越被拒", filepath.Join("..", "etc", "passwd"), true},
		{"空格被拒（M4：src 永不支持空格）", "my file.exe", true},
		{"不存在被拒", "missing.exe", true},
	}
	// 子目录 fixture
	writeTestFile(t, filepath.Join(incoming, "sub", "file.exe"), "x")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSrc(incoming, tc.src)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateSrc(%q) err=%v, wantErr=%v", tc.src, err, tc.wantErr)
			}
		})
	}
}

// src 是 symlink → 不是常规文件，必须被拒。
func TestValidateEntry_SrcSymlinkRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("symlink 需要真实文件系统")
	}
	incoming := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "evil.exe")
	writeTestFile(t, target, "evil")

	link := filepath.Join(incoming, "link.exe")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("此环境不支持 symlink: %v", err)
	}
	if err := validateSrc(incoming, "link.exe"); err == nil {
		t.Errorf("symlink src 必须被拒")
	}
}

// src 是目录 → 必须被拒。
func TestValidateEntry_SrcDirectoryRejected(t *testing.T) {
	incoming := t.TempDir()
	if err := os.MkdirAll(filepath.Join(incoming, "dir.exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateSrc(incoming, "dir.exe"); err == nil {
		t.Errorf("目录 src 必须被拒")
	}
}

// --- Mode 白名单 ---

func TestValidateEntry_ModeWhitelist(t *testing.T) {
	cases := []struct {
		mode    string
		wantErr bool
	}{
		{"", false},
		{"0755", false},
		{"0644", false},
		{"0444", true},    // read-only 注入 → 下轮升级 rename 失败 brick
		{"777", true},     // 非 bundle 产物权限
		{"0444abc", true}, // parseMode 静默截断防护
	}
	for _, tc := range cases {
		err := validateMode(tc.mode)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateMode(%q) err=%v, wantErr=%v", tc.mode, err, tc.wantErr)
		}
	}
}

// --- ValidateAllEntries：Machine.Apply 的单点门卫 ---

func TestValidateAllEntries_RejectsBadManifest(t *testing.T) {
	binDir := t.TempDir()
	incoming := t.TempDir()
	writeTestFile(t, filepath.Join(incoming, "x.exe"), "x")

	entries := []ManifestEntry{
		{SHA256: "00", Mode: "0755", Src: "x.exe", Dest: filepath.Join(binDir, WorkerBinaryName)},
		{SHA256: "00", Mode: "0755", Src: "x.exe", Dest: `C:\Windows\system32\svchost.exe`},
	}
	if err := ValidateAllEntries(context.Background(), binDir, incoming, entries); err == nil {
		t.Fatalf("含系统镜像 dest 的 manifest 必须整体被拒")
	}
}

// Apply 集成：恶意 manifest 在 kill/swap 之前被拒（零磁盘变动、零进程终止）。
func TestMachine_Apply_RejectsMaliciousManifestBeforeKill(t *testing.T) {
	stageDir := t.TempDir()
	binDir := t.TempDir()  // 受管根
	evilDir := t.TempDir() // 受管根外
	writeTestFile(t, filepath.Join(binDir, WorkerBinaryName), "old")

	// manifest 一条合法 + 一条恶意（dest 指向受管根外的系统镜像名）
	buildMachineBundle(t, stageDir, binDir, "v1", []struct{ Src, Dest, Content string }{
		{"a.exe", WorkerBinaryName, "new"},
	})
	// 追加恶意行（buildMachineBundle 只写白名单文件名，这里手工追加）
	manifest := filepath.Join(stageDir, IncomingDirName, ManifestFileName)
	writeTestFile(t, filepath.Join(stageDir, IncomingDirName, "evil.exe"), "evil")
	f, err := os.OpenFile(manifest, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(sha256Of(t, "evil") + " 0755 evil.exe " + filepath.Join(evilDir, "svchost.exe") + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var pc mockProcessController
	m := NewMachine(stageDir, binDir, testLogger(), &pc)
	if err := m.Apply(context.Background(), 12345); err == nil {
		t.Fatalf("恶意 manifest 的 Apply 必须失败")
	}
	if pc.killImageCalls.Load() != 0 {
		t.Errorf("校验必须先于 kill：KillByImage 被调用了 %d 次", pc.killImageCalls.Load())
	}
	// 零磁盘变动：合法条目也不得被换
	got, _ := os.ReadFile(filepath.Join(binDir, WorkerBinaryName))
	if string(got) != "old" {
		t.Errorf("校验失败必须零磁盘变动, dest content = %q", got)
	}
}

// --- KillManifestExes 白名单过滤 ---

func TestKillManifestExes_WhitelistFilter(t *testing.T) {
	entries := []ManifestEntry{
		{Dest: `C:\bin\` + WorkerBinaryName},
		{Dest: `C:\bin\windows_exporter.exe`}, // 插件在白名单内 → 合法 kill 目标（文件锁场景）
		{Dest: `C:\bin\` + SupervisorBinaryName},
		{Dest: `C:\bin\ONGRID-EDGE-SUPERVISOR.EXE`}, // 大小写变体 → 同样跳过（EqualFold）
		{Dest: `C:\bin\svchost.exe`},                // 白名单外 → 不杀（C1 旁路封死）
		{Dest: `C:\temp\evil.exe`},                  // 白名单外 → 不杀
	}
	var pc mockProcessController
	m := NewMachine(t.TempDir(), t.TempDir(), testLogger(), &pc)

	m.KillManifestExes(entries)

	want := []string{WorkerBinaryName, "windows_exporter.exe"}
	if len(pc.killImageNames) != len(want) {
		t.Fatalf("KillByImage calls = %v, want %v", pc.killImageNames, want)
	}
	for i, w := range want {
		if !strings.EqualFold(pc.killImageNames[i], w) {
			t.Errorf("kill[%d] = %q, want %q", i, pc.killImageNames[i], w)
		}
	}
}

// --- 系统镜像黑名单（KillByImage 实现层兜底）---

func TestIsProtectedSystemImage(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"svchost.exe", true},
		{"SVCHOST.EXE", true}, // 大小写不敏感
		{"lsass.exe", true},
		{"csrss.exe", true},
		{"smss.exe", true},
		{"wininit.exe", true},
		{"winlogon.exe", true},
		{"services.exe", true},
		{"MsMpEng.exe", true},
		{WorkerBinaryName, false}, // 白名单内正常目标不受黑名单影响
		{"windows_exporter.exe", false},
		{"explorer.exe", false}, // 不在最小黑名单（YAGNI：用户会话进程，非系统关键）
	}
	for _, tc := range cases {
		if got := IsProtectedSystemImage(tc.name); got != tc.want {
			t.Errorf("IsProtectedSystemImage(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// sha256Of 计算内容 sha（测试 helper，恶意 manifest 行需要真实 sha 让校验走到 dest 检查）。
func sha256Of(t *testing.T, content string) string {
	t.Helper()
	// writeTestFile 返回值即内容 sha256
	return writeTestFile(t, filepath.Join(t.TempDir(), "sha_tmp"), content)
}

// TestDestBase 分隔符无关性回归：Dest 是 Windows 反斜杠路径，Linux CI 上
// filepath.Base 不认 '\' 曾导致白名单误拒（KillManifestExes 0 次 kill）。
func TestDestBase(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`C:\bin\` + WorkerBinaryName, WorkerBinaryName},
		{`C:\bin\sub\windows_exporter.exe`, "windows_exporter.exe"},
		{`C:/bin/promtail.exe`, "promtail.exe"}, // 正斜杠变体同拒同收
		{WorkerBinaryName, WorkerBinaryName},
	}
	for _, tc := range cases {
		if got := destBase(tc.in); got != tc.want {
			t.Errorf("destBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
