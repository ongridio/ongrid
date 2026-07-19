// download.go 实现 worker 侧的 bundle 下载 + sha256 校验 + 解压（ADR-033 U3 Phase 3）。
//
// 对称 Linux 侧 biz/handleFetchPackage，但作为独立纯函数 API（不绑定 Agent 结构体），
// 可被 Windows worker / 测试 / Phase 4 RPC handler 直接调用。
//
// 流程（all-or-nothing，任何步骤失败都清理残留）：
//  1. 校验输入（URL 协议、sha256 格式）
//  2. 清理旧残留（incoming/ + incoming.tar.gz）
//  3. 流式下载 → incoming.tar.gz（边下边算 sha256）
//  4. sha256 校验（不信任下载内容）
//  5. 解压 → incoming/（拒绝路径逃逸、symlink、超限）
//  6. 删 tarball（只保留解压后的树）
//  7. 验证 incoming/MANIFEST.txt（per-file sha256 all-or-nothing）
//
// 成功后 incoming/MANIFEST.txt 存在 = 触发器就绪，supervisor 检测到此文件 → swap。

package upgradebundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/upgrademachine"
)

// maxBundleBytes 限制接受的最大 bundle 大小（解压后）。
// 对称 biz/upgrade_package.go 的 maxBundleBytes（1 GB）。
const maxBundleBytes = 1024 * 1024 * 1024

// downloadTimeout 是单次下载的最大时长。
const downloadTimeout = 45 * time.Minute

// bundleTarballName 是下载暂存的 tarball 文件名（解压后删除）。
const bundleTarballName = "incoming.tar.gz"

// DownloadBundle 从 downloadURL 下载 .tar.gz bundle，校验 sha256，解压到
// stageDir/incoming/，并验证 MANIFEST.txt 中每个文件的 sha256。
//
// 返回 (下载字节数, manifest 文件数, error)。
// 成功后 stageDir/incoming/MANIFEST.txt 存在（= pending upgrade 触发器）。
// 任何步骤失败时清理所有残留文件（tarball + incoming/），不留半成品。
//
// ctx 用于请求级取消（RPC 超时、客户端断开）。内部用 downloadTimeout
// 作为上限封顶，避免慢网络无限挂起。ctx.Done() 触发时 HTTP 请求中止。
//
// client 为 nil 时使用 http.DefaultClient。调用方可注入跳过 TLS 验证的
// client（私有部署自签名 nginx 场景，对称 biz.bundleDownloadClient）。
func DownloadBundle(ctx context.Context, client *http.Client, downloadURL, expectedSHA, stageDir string) (int64, int, error) {
	if client == nil {
		client = http.DefaultClient
	}

	// 1. 校验输入。
	expectedSHA = strings.ToLower(strings.TrimSpace(expectedSHA))
	if len(expectedSHA) != 64 {
		return 0, 0, fmt.Errorf("download_bundle: sha256 must be 64 hex chars (got %d)", len(expectedSHA))
	}
	for _, c := range expectedSHA {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return 0, 0, fmt.Errorf("download_bundle: sha256 not lower-hex")
		}
	}
	url := strings.TrimSpace(downloadURL)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return 0, 0, fmt.Errorf("download_bundle: url must be http(s)")
	}

	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		return 0, 0, fmt.Errorf("download_bundle: mkdir stage: %w", err)
	}

	tarballPath := filepath.Join(stageDir, bundleTarballName)
	incomingPath := filepath.Join(stageDir, upgrademachine.IncomingDirName)

	// 2. 清理旧残留（幂等）。
	_ = os.Remove(tarballPath)
	_ = os.RemoveAll(incomingPath)

	// 3-4. 下载 + sha256 校验。
	n, err := downloadAndVerify(ctx, client, url, expectedSHA, tarballPath)
	if err != nil {
		_ = os.Remove(tarballPath)
		return 0, 0, fmt.Errorf("download_bundle: %w", err)
	}

	// 5. 解压 → incoming/。
	if err := extractTarGz(tarballPath, incomingPath); err != nil {
		_ = os.Remove(tarballPath)
		_ = os.RemoveAll(incomingPath)
		return 0, 0, fmt.Errorf("download_bundle: extract: %w", err)
	}

	// 6. 删 tarball（只保留解压后的树）。
	_ = os.Remove(tarballPath)

	// 7. 验证 MANIFEST.txt（per-file sha256 all-or-nothing）。
	manifestPath := filepath.Join(incomingPath, upgrademachine.ManifestFileName)
	entries, err := upgrademachine.ParseManifest(manifestPath)
	if err != nil {
		_ = os.RemoveAll(incomingPath)
		return 0, 0, fmt.Errorf("download_bundle: manifest parse: %w", err)
	}
	if err := upgrademachine.VerifyAll(incomingPath, entries); err != nil {
		_ = os.RemoveAll(incomingPath)
		return 0, 0, fmt.Errorf("download_bundle: manifest verify: %w", err)
	}

	return n, len(entries), nil
}

// downloadAndVerify 流式下载 url 到 out，边下边算 sha256，校验后返回字节数。
// 失败时删除 out 文件。ctx 用上层 RPC 的 context（可取消），内部用
// downloadTimeout 封顶防止慢网络无限挂起。
func downloadAndVerify(ctx context.Context, client *http.Client, url, expectedSHA, out string) (int64, error) {
	// downloadTimeout 作为上限封顶；ctx 自身的 deadline 可能更短（RPC 超时）。
	dlCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(dlCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build req: %w", err)
	}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("get: %w", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("get: status %d", httpResp.StatusCode)
	}

	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, fmt.Errorf("open: %w", err)
	}
	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hasher), io.LimitReader(httpResp.Body, maxBundleBytes+1))
	if err != nil {
		_ = f.Close()
		_ = os.Remove(out)
		return 0, fmt.Errorf("stream: %w", err)
	}
	if n > maxBundleBytes {
		_ = f.Close()
		_ = os.Remove(out)
		return 0, fmt.Errorf("bundle too large (%d > %d)", n, maxBundleBytes)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(out)
		return 0, fmt.Errorf("sync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(out)
		return 0, fmt.Errorf("close: %w", err)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != expectedSHA {
		_ = os.Remove(out)
		return 0, fmt.Errorf("sha256 mismatch (got %s, want %s)", got, expectedSHA)
	}
	return n, nil
}

// extractTarGz 解压 src（.tar.gz）到 dst。
// 安全措施：拒绝路径逃逸（zip-slip）、symlink/hardlink、超限总大小。
func extractTarGz(src, dst string) error {
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return fmt.Errorf("mkdir dst: %w", err)
	}
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var totalBytes int64
	absDst, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("abs dst: %w", err)
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		// 只接受普通文件和目录（拒绝 symlink/hardlink/device）。
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		default:
			return fmt.Errorf("disallowed tar entry type %v in %q", hdr.Typeflag, hdr.Name)
		}
		cleaned := filepath.Clean(hdr.Name)
		if strings.HasPrefix(cleaned, "/") || strings.Contains(cleaned, "..") {
			return fmt.Errorf("disallowed tar path %q (escapes archive root)", hdr.Name)
		}
		target := filepath.Join(absDst, cleaned)
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return fmt.Errorf("abs target: %w", err)
		}
		if !strings.HasPrefix(absTarget, absDst+string(os.PathSeparator)) && absTarget != absDst {
			return fmt.Errorf("target %q escapes %q", absTarget, absDst)
		}
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir parent of %s: %w", target, err)
		}
		w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC,
			os.FileMode(hdr.Mode)&0o755)
		if err != nil {
			return fmt.Errorf("open out %s: %w", target, err)
		}
		n, err := io.Copy(w, io.LimitReader(tr, maxBundleBytes-totalBytes+1))
		if err != nil {
			_ = w.Close()
			return fmt.Errorf("write %s: %w", target, err)
		}
		if err := w.Close(); err != nil {
			return fmt.Errorf("close %s: %w", target, err)
		}
		totalBytes += n
		if totalBytes > maxBundleBytes {
			return fmt.Errorf("extracted size exceeded %d bytes", maxBundleBytes)
		}
	}
	return nil
}
