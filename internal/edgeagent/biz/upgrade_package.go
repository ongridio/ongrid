// upgrade_package.go — edge handler. Receives a `fetch_package`
// RPC from the manager, downloads the entire release bundle (agent +
// plugins + apply script), verifies the outer SHA256 + each manifest
// entry's SHA, and lays it out at the path the install-side
// apply-pending-upgrade.sh script reads on next systemd start. The
// `apply_package` handler acks then triggers Run() exit so systemd
// restarts the agent and the apply script swaps everything in.
package biz

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// bundleDownloadClient skips TLS verification because the private-deployment
// manager fronts /edge/ with a self-signed nginx cert. SHA256 of the bundle
// (verified by downloadAndVerify after the body lands) is the trust anchor —
// any tampering, MITM, or proxy substitution fails the hash and we delete
// the staged file. trust model".
var bundleDownloadClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// bundleDirName is the subdir under UpgradeStageDir where the extracted
// bundle lives — matches the path apply-pending-upgrade.sh reads from.
const bundleDirName = "incoming"

// maxBundleBytes caps the tarball we'll accept (decompressed + on the
// wire). otelcol-contrib is the largest component, plus node_exporter,
// process_exporter, database exporters and ongrid-edge. The 1 GB cap
// keeps headroom for Collector/exporter growth
// without re-tuning every release.
const maxBundleBytes = 1024 * 1024 * 1024

// handleFetchPackage implements MethodFetchPackage. Downloads the
// tarball, verifies its outer SHA256, extracts into a staging dir,
// then re-verifies each file in MANIFEST.txt. Leaves the bundle ready
// for the next apply_package call; does NOT restart the agent.
func (a *Agent) handleFetchPackage(ctx context.Context, req tunnel.FetchPackageRequest) (tunnel.FetchPackageResponse, error) {
	dir := strings.TrimSpace(a.cfg.UpgradeStageDir)
	if dir == "" {
		return tunnel.FetchPackageResponse{}, fmt.Errorf("fetch_package: stage dir not configured")
	}
	expectedSHA := strings.ToLower(strings.TrimSpace(req.SHA256))
	if len(expectedSHA) != 64 {
		return tunnel.FetchPackageResponse{}, fmt.Errorf("fetch_package: sha256 must be 64 hex chars (got %d)", len(expectedSHA))
	}
	for _, c := range expectedSHA {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return tunnel.FetchPackageResponse{}, fmt.Errorf("fetch_package: sha256 not lower-hex")
		}
	}
	url := strings.TrimSpace(req.URL)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return tunnel.FetchPackageResponse{}, fmt.Errorf("fetch_package: url must be http(s)")
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return tunnel.FetchPackageResponse{}, fmt.Errorf("fetch_package: mkdir stage: %w", err)
	}
	tarball := filepath.Join(dir, "incoming.tar.gz")
	stage := filepath.Join(dir, bundleDirName)

	// Wipe any leftover from a previous failed attempt — we own the
	// stage dir, no concurrent writers.
	_ = os.Remove(tarball)
	_ = os.RemoveAll(stage)

	a.log.Info("fetch_package: starting download",
		slog.String("url", url),
		slog.String("sha256", expectedSHA),
		slog.String("stage", stage),
	)

	bytesDL, err := downloadAndVerify(ctx, url, expectedSHA, tarball)
	if err != nil {
		_ = os.Remove(tarball)
		return tunnel.FetchPackageResponse{}, err
	}

	// Extract to stage. We limit total bytes written + reject paths that
	// escape the stage dir (zip-slip-style attack mitigation, even
	// though our bundle is built by our own pipeline).
	if err := extractTarGz(tarball, stage); err != nil {
		_ = os.Remove(tarball)
		_ = os.RemoveAll(stage)
		return tunnel.FetchPackageResponse{}, fmt.Errorf("fetch_package: extract: %w", err)
	}
	// Tarball was just a transport — discard now that we have the tree.
	_ = os.Remove(tarball)

	// Walk MANIFEST.txt, verify per-file sha. We do this AFTER extract
	// because the per-file gates would otherwise need a streaming
	// tar reader that buffers each entry (complicated, error-prone);
	// the disk hop costs ~1s and gives us a clean two-phase check.
	manifestPath := filepath.Join(stage, "MANIFEST.txt")
	fileCount, err := verifyManifest(manifestPath, stage)
	if err != nil {
		_ = os.RemoveAll(stage)
		return tunnel.FetchPackageResponse{}, fmt.Errorf("fetch_package: manifest: %w", err)
	}
	// If the bundle carries a VERSION file, leave it in place; the
	// apply script copies it into last_upgrade_ver on swap.

	a.log.Info("fetch_package: staged",
		slog.String("path", stage),
		slog.Int64("bytes", bytesDL),
		slog.Int("files", fileCount),
	)
	return tunnel.FetchPackageResponse{
		StagedPath:    stage,
		Bytes:         bytesDL,
		ManifestFiles: fileCount,
		Version:       req.Version,
	}, nil
}

// handleApplyPackage implements MethodApplyPackage. Confirms a bundle
// is staged (otherwise apply would no-op and confuse the operator),
// then signals Run() to exit. systemd restarts the agent within a few
// seconds; the ExecStartPre apply-pending-upgrade.sh script does the
// real work.
func (a *Agent) handleApplyPackage(_ context.Context, _ tunnel.ApplyPackageRequest) (tunnel.ApplyPackageResponse, error) {
	dir := strings.TrimSpace(a.cfg.UpgradeStageDir)
	if dir == "" {
		return tunnel.ApplyPackageResponse{}, fmt.Errorf("apply_package: stage dir not configured")
	}
	manifest := filepath.Join(dir, bundleDirName, "MANIFEST.txt")
	if _, err := os.Stat(manifest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tunnel.ApplyPackageResponse{}, fmt.Errorf("apply_package: no staged bundle (run fetch_package first)")
		}
		return tunnel.ApplyPackageResponse{}, fmt.Errorf("apply_package: inspect staged bundle: %w", err)
	}
	a.log.Info("apply_package: signal exit", slog.String("manifest", manifest))
	// Same channel agent_upgrade uses — buffered(1), extra sends drop.
	select {
	case a.upgradeRequested <- struct{}{}:
	default:
	}
	return tunnel.ApplyPackageResponse{Accepted: true}, nil
}

// downloadAndVerify streams url into out, computing sha256 inline, and
// returns the byte count once verified. Errors leave `out` removed.
func downloadAndVerify(ctx context.Context, url, expectedSHA, out string) (int64, error) {
	dlCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(dlCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build req: %w", err)
	}
	httpResp, err := bundleDownloadClient.Do(httpReq)
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

// extractTarGz unpacks src (.tar.gz) into dst. dst is created fresh
// (caller already cleaned it). Reject paths that try to escape dst
// (zip-slip), oversize total writes, and symlinks (we don't need them
// and they're a footgun).
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
		// Reject anything that isn't a regular file or directory —
		// symlinks / hardlinks / devices have no business in our
		// bundle and they're how zip-slip variants usually escape.
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
		// Belt + suspenders: re-resolve target and verify it's under dst.
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
		w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o755)
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

// verifyManifest reads stage/MANIFEST.txt and sha256s every src under
// stage/. Returns the count of files verified. Lines that are blank
// or start with '#' are skipped.
//
// Manifest line shape:
//
//	<sha256> <mode> <src> <dest>
//
// fields are whitespace-separated; spaces in paths aren't supported
// (the bundle's our own and the artifact names are operator-friendly).
func verifyManifest(path, stage string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open manifest: %w", err)
	}
	defer f.Close()
	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return count, fmt.Errorf("malformed manifest line: %q", line)
		}
		sha, _, src, _ := fields[0], fields[1], fields[2], fields[3]
		srcPath := filepath.Join(stage, src)
		got, err := fileSHA256(srcPath)
		if err != nil {
			return count, fmt.Errorf("sha %s: %w", src, err)
		}
		if !strings.EqualFold(got, sha) {
			return count, fmt.Errorf("sha mismatch for %s (got %s want %s)", src, got, sha)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("scan: %w", err)
	}
	if count == 0 {
		return 0, fmt.Errorf("manifest declared zero files")
	}
	return count, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
