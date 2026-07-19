// manifest.go 实现 MANIFEST.txt 的解析 + per-file sha256 验证。
// 从 upgradeapply/manifest.go 移入，消除 upgradeapply 和 upgradebundle 的重复解析。
// upgradebundle/download.go 原有的 verifyManifest + fileSHA256 已删除，
// 改用本包的 ParseManifest + VerifyAll。

package upgrademachine

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ManifestEntry 是 MANIFEST.txt 中一行的结构化表示。
// 格式：`<sha256> <mode> <src> <dest>`
//   - SHA256：src 文件的预期 sha256（小写 hex，64 字符）
//   - Mode：文件权限（如 "0755"），apply 时设到新文件
//   - Src：incoming/ 目录内的相对路径
//   - Dest：swap 目标绝对路径
type ManifestEntry struct {
	SHA256 string
	Mode   string
	Src    string
	Dest   string
}

// ParseManifest 读取并解析 MANIFEST.txt。
// 跳过空行和 # 开头的注释行。字段少于 4 个的行视为格式错误。
// 返回零条目时报错（空 manifest = 打包错误）。
func ParseManifest(path string) ([]ManifestEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open manifest %s: %w", path, err)
	}
	defer f.Close()

	var entries []ManifestEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// dest 可能含空格（Windows: C:\Program Files\...），
		// sha/mode/src 不含空格，所以前 3 个字段严格分割，
		// 第 4 个字段起合并为 dest 路径。
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return nil, fmt.Errorf("malformed manifest line: %q", line)
		}
		entries = append(entries, ManifestEntry{
			SHA256: fields[0],
			Mode:   fields[1],
			Src:    fields[2],
			Dest:   strings.Join(fields[3:], " "),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan manifest: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("manifest declared zero files")
	}
	return entries, nil
}

// VerifyEntry 检查 incomingDir/<Src> 的 sha256 是否与 entry.SHA256 匹配。
func VerifyEntry(incomingDir string, entry ManifestEntry) error {
	srcPath := filepath.Join(incomingDir, entry.Src)
	got, err := fileSHA256(srcPath)
	if err != nil {
		return fmt.Errorf("sha %s: %w", entry.Src, err)
	}
	if !strings.EqualFold(got, entry.SHA256) {
		return fmt.Errorf("sha mismatch for %s (got %s want %s)", entry.Src, got, entry.SHA256)
	}
	return nil
}

// VerifyAll 验证所有条目。遇到第一个错误即返回（all-or-nothing 语义）。
func VerifyAll(incomingDir string, entries []ManifestEntry) error {
	for _, e := range entries {
		if err := VerifyEntry(incomingDir, e); err != nil {
			return err
		}
	}
	return nil
}

// fileSHA256 计算文件 sha256 并返回小写 hex。
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
