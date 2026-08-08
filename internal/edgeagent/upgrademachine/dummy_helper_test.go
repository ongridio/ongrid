// dummy_helper_test.go 提供 testdata/dummy_main.go 的跨平台编译 helper。
//
// buildDummy 被 Step 0 Windows 文件锁语义测试（running_exe_rename_windows_test.go）
// 和 Step 4 SupervisorSelfSwap 测试（supervisor_selfswap_test.go）共用。
//
// dummy.exe 支持：
//   - 默认：sleep 60s（模拟运行中 supervisor.exe）
//   - --version：输出 "ongrid-dummy v0.0.1" + exit 0（smokeTestVersion 验证用）
//   - --self-rename <path>：自 rename + JSON 报告（Step 0 场景 2 用）

package upgrademachine

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var (
	dummyOnce sync.Once
	dummyPath string
	dummyErr  error
)

// buildDummy 编译 dummy_main.go → dummy.exe，所有测试共用。
// 首次调用 ~3-5s，后续 0s（cache 在 os.TempDir 固定路径）。
func buildDummy(t *testing.T) string {
	t.Helper()
	dummyOnce.Do(func() {
		out := filepath.Join(os.TempDir(), "ongrid-dummy.exe")
		src := filepath.Join("testdata", "dummy_main.go")
		cmd := exec.Command("go", "build", "-o", out, src)
		if out, err := cmd.CombinedOutput(); err != nil {
			dummyErr = fmt.Errorf("go build dummy: %w\noutput: %s", err, out)
			return
		}
		dummyPath = out
	})
	if dummyErr != nil {
		t.Fatal(dummyErr)
	}
	return dummyPath
}

// copyFileExe 复制 src → dst（用于铺地板，保留 src 的文件权限）。
// 跨平台 — Step 0 / Step 4 测试共用。
func copyFileExe(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open src %s: %v", src, err)
	}
	defer in.Close()
	info, _ := in.Stat()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		t.Fatalf("create dst %s: %v", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy: %v", err)
	}
}

// appendMarker 在文件末尾追加 marker 字节，让 src 和 dest 内容不同（测试可区分版本）。
func appendMarker(t *testing.T, path, marker string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open for append %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(marker); err != nil {
		t.Fatalf("append: %v", err)
	}
}
