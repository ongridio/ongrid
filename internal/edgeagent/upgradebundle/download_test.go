package upgradebundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeBundle 构造一个合法的 .tar.gz bundle 字节流。
//
// 文件映射：name → content。自动生成对应的 MANIFEST.txt（每行
// `<sha256> <mode> <src> <dest>` 格式）。
func makeBundle(t *testing.T, files map[string]string, includeManifest bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	var manifestLines []string

	// 先写普通文件
	for name, content := range files {
		h := sha256.Sum256([]byte(content))
		sha := hex.EncodeToString(h[:])
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		// manifest 行：sha / mode / src(incoming 相对) / dest(绝对路径占位)
		manifestLines = append(manifestLines, fmt.Sprintf("%s 0755 %s /usr/local/bin/%s", sha, name, name))
	}

	if includeManifest {
		manifest := strings.Join(manifestLines, "\n") + "\n"
		if err := tw.WriteHeader(&tar.Header{
			Name: "MANIFEST.txt",
			Mode: 0644,
			Size: int64(len(manifest)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(manifest)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bundleSHA256 计算 bundle 字节流的 sha256（供 DownloadBundle 的 expectedSHA 参数用）。
func bundleSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TestDownloadBundle_Success 验证完整正常路径：
// 下载 → sha256 校验 → 解压 → manifest 验证 → incoming/MANIFEST.txt 就绪。
func TestDownloadBundle_Success(t *testing.T) {
	bundleData := makeBundle(t, map[string]string{
		"worker": "fake binary content",
	}, true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bundleData)
	}))
	defer server.Close()

	dir := t.TempDir()
	expectedSHA := bundleSHA256(bundleData)

	n, _, err := DownloadBundle(context.Background(), server.Client(), server.URL, expectedSHA, dir)
	if err != nil {
		t.Fatalf("DownloadBundle: %v", err)
	}
	if n != int64(len(bundleData)) {
		t.Errorf("bytes = %d, want %d", n, len(bundleData))
	}

	// 触发器就绪：incoming/MANIFEST.txt 存在
	manifestPath := filepath.Join(dir, "incoming", "MANIFEST.txt")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("MANIFEST.txt should exist at %s: %v", manifestPath, err)
	}

	// tarball 应被删除（只保留解压后的文件）
	if _, err := os.Stat(filepath.Join(dir, "incoming.tar.gz")); !os.IsNotExist(err) {
		t.Error("incoming.tar.gz should be removed after extract")
	}
}

// TestDownloadBundle_SHA256Mismatch 验证 sha256 不匹配时报错 + 清理。
func TestDownloadBundle_SHA256Mismatch(t *testing.T) {
	bundleData := makeBundle(t, map[string]string{"worker": "x"}, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bundleData)
	}))
	defer server.Close()

	dir := t.TempDir()

	_, _, err := DownloadBundle(context.Background(), server.Client(), server.URL,
		"0000000000000000000000000000000000000000000000000000000000000000", dir)
	if err == nil {
		t.Fatal("expected sha mismatch error")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("error should mention sha256 mismatch, got: %v", err)
	}

	// tarball 应被删除
	if _, err := os.Stat(filepath.Join(dir, "incoming.tar.gz")); !os.IsNotExist(err) {
		t.Error("tarball should be removed on sha mismatch")
	}
	// incoming/ 不应存在（sha 校验在解压前）
	if _, err := os.Stat(filepath.Join(dir, "incoming")); !os.IsNotExist(err) {
		t.Error("incoming/ should not exist when sha fails before extract")
	}
}

// TestDownloadBundle_BadURL 验证非 http(s) URL 被拒绝。
func TestDownloadBundle_BadURL(t *testing.T) {
	dir := t.TempDir()
	// 传合法 sha256 格式，确保报错来自 URL 校验而非 sha
	validSHA := hex.EncodeToString(make([]byte, 32))
	_, _, err := DownloadBundle(context.Background(), http.DefaultClient, "ftp://example.com/bundle.tar.gz",
		validSHA, dir)
	if err == nil || !strings.Contains(err.Error(), "must be http(s)") {
		t.Errorf("expected URL protocol error, got: %v", err)
	}
}

// TestDownloadBundle_BadSHAFormat 验证 sha256 格式校验。
func TestDownloadBundle_BadSHAFormat(t *testing.T) {
	dir := t.TempDir()
	_, _, err := DownloadBundle(context.Background(), http.DefaultClient, "http://example.com/x",
		"tooshort", dir)
	if err == nil || !strings.Contains(err.Error(), "sha256 must be 64") {
		t.Errorf("expected sha format error, got: %v", err)
	}
}

// TestDownloadBundle_HTTP404 验证 HTTP 错误状态码。
func TestDownloadBundle_HTTP404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dir := t.TempDir()
	_, _, err := DownloadBundle(context.Background(), server.Client(), server.URL,
		bundleSHA256([]byte("x")), dir)
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Errorf("expected HTTP 404 error, got: %v", err)
	}
}

// TestDownloadBundle_ManifestVerifyFail 验证 manifest per-file sha 不匹配时报错。
// 构造一个 MANIFEST.txt 声明的 sha 与实际文件不符的 bundle。
func TestDownloadBundle_ManifestVerifyFail(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// 写一个内容固定的文件
	content := []byte("real content")
	tw.WriteHeader(&tar.Header{Name: "worker", Mode: 0644, Size: int64(len(content))})
	tw.Write(content)

	// MANIFEST.txt 声明一个错误的 sha
	badManifest := fmt.Sprintf("%s 0755 worker /usr/local/bin/worker\n",
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	tw.WriteHeader(&tar.Header{Name: "MANIFEST.txt", Mode: 0644, Size: int64(len(badManifest))})
	tw.Write([]byte(badManifest))

	tw.Close()
	gz.Close()
	bundleData := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bundleData)
	}))
	defer server.Close()

	dir := t.TempDir()
	_, _, err := DownloadBundle(context.Background(), server.Client(), server.URL, bundleSHA256(bundleData), dir)
	if err == nil {
		t.Fatal("expected manifest verify error")
	}
	if !strings.Contains(err.Error(), "manifest") {
		t.Errorf("error should mention manifest, got: %v", err)
	}

	// 失败后 incoming/ 应被清理
	if _, err := os.Stat(filepath.Join(dir, "incoming")); !os.IsNotExist(err) {
		t.Error("incoming/ should be cleaned up after manifest verify failure")
	}
}

// TestDownloadBundle_CleansStaleIncoming 验证下载前清理旧残留（幂等）。
func TestDownloadBundle_CleansStaleIncoming(t *testing.T) {
	bundleData := makeBundle(t, map[string]string{"worker": "new"}, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bundleData)
	}))
	defer server.Close()

	dir := t.TempDir()
	// 放一个旧的 incoming/ 目录 + 旧 tarball
	oldIncoming := filepath.Join(dir, "incoming")
	os.MkdirAll(oldIncoming, 0o750)
	os.WriteFile(filepath.Join(oldIncoming, "MANIFEST.txt"), []byte("stale"), 0o640)
	os.WriteFile(filepath.Join(dir, "incoming.tar.gz"), []byte("stale"), 0o640)

	expectedSHA := bundleSHA256(bundleData)
	if _, _, err := DownloadBundle(context.Background(), server.Client(), server.URL, expectedSHA, dir); err != nil {
		t.Fatalf("DownloadBundle: %v", err)
	}

	// 旧 MANIFEST.txt 应被新内容覆盖
	got, _ := os.ReadFile(filepath.Join(oldIncoming, "MANIFEST.txt"))
	if strings.Contains(string(got), "stale") {
		t.Error("stale MANIFEST.txt should be replaced")
	}
}

// TestDownloadBundle_RejectsPathEscape 验证 tar 条目含 ../ 时解压被拒绝。
func TestDownloadBundle_RejectsPathEscape(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	tw.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0644, Size: 1})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	evilBundle := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(evilBundle)
	}))
	defer server.Close()

	dir := t.TempDir()
	_, _, err := DownloadBundle(context.Background(), server.Client(), server.URL,
		bundleSHA256(evilBundle), dir)
	if err == nil {
		t.Fatal("expected path escape rejection")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error should mention escape, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "incoming")); !os.IsNotExist(err) {
		t.Error("incoming/ should be cleaned after path escape rejection")
	}
}

// TestDownloadBundle_RejectsSymlink 验证 tar 条目含 symlink 时被拒绝。
func TestDownloadBundle_RejectsSymlink(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	tw.WriteHeader(&tar.Header{
		Name:     "evil",
		Linkname: "/etc/passwd",
		Typeflag: tar.TypeSymlink,
	})
	tw.Close()
	gz.Close()
	symlinkBundle := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(symlinkBundle)
	}))
	defer server.Close()

	dir := t.TempDir()
	_, _, err := DownloadBundle(context.Background(), server.Client(), server.URL,
		bundleSHA256(symlinkBundle), dir)
	if err == nil {
		t.Fatal("expected symlink rejection")
	}
	if !strings.Contains(err.Error(), "disallowed tar entry type") {
		t.Errorf("error should mention disallowed type, got: %v", err)
	}
}
