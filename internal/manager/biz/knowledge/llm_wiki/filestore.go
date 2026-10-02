package llm_wiki

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileStore owns the on-disk Wiki tree:
//
//	raw/                    mirrored source files, as uploaded
//	wiki/builds/            immutable generated Wiki builds
//	.llm-wiki/versions/     content-addressed source snapshots
//
// Every path that enters or leaves the store goes through resolve, which keeps
// reads and writes inside the root and rejects symlinks.
type FileStore struct {
	root       string
	artifactMu sync.Mutex
}

// NewFileStore opens (but does not create) a Wiki tree at root.
func NewFileStore(root string) (*FileStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("llmwiki: root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("llmwiki: resolve root: %w", err)
	}
	return &FileStore{root: abs}, nil
}

// Root returns the absolute root of the Wiki tree.
func (s *FileStore) Root() string { return s.root }

// LockArtifacts serialises access to the Wiki tree and returns the unlock func.
func (s *FileStore) LockArtifacts() func() {
	s.artifactMu.Lock()
	return s.artifactMu.Unlock
}

// Ensure creates the current Wiki layout and removes obsolete catalog/manifest
// artifacts. Generated pages exist only below wiki/builds.
func (s *FileStore) Ensure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, legacy := range []string{
		"concepts",
		"entities",
		"wiki/concepts",
		"wiki/entities",
		"wiki/sources",
		"wiki/topics",
		"wiki/index.md",
		"wiki/log.md",
		".llm-wiki/staging",
		"schema.md",
	} {
		path, err := s.resolve("", legacy)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("llmwiki: remove legacy artifact %s: %w", legacy, err)
		}
	}
	for _, dir := range []string{"raw", "wiki/builds", ".llm-wiki/versions"} {
		path, err := s.resolve("", dir)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(path, 0o750); err != nil {
			return fmt.Errorf("llmwiki: create %s: %w", dir, err)
		}
	}
	return nil
}

// Read reads one file from the Wiki tree and rejects anything larger than max
// bytes, so a hostile or accidental huge file cannot exhaust memory.
func (s *FileStore) Read(ctx context.Context, relative string, max int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.resolve("", relative)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("llmwiki: open %s: %w", relative, err)
	}
	defer func() { _ = f.Close() /* read-only cleanup; the read result remains authoritative */ }()
	body, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("llmwiki: read %s: %w", relative, err)
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("llmwiki: file exceeds %d bytes", max)
	}
	return body, nil
}

// resolve maps a store-relative path to an absolute path inside the root. An
// empty layer addresses the root itself; otherwise layer is the first path
// element (raw, wiki, .llm-wiki).
func (s *FileStore) resolve(layer, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", errors.New("llmwiki: absolute path rejected")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("llmwiki: path traversal rejected")
	}
	base := s.root
	if layer != "" {
		base = filepath.Join(base, layer)
	}
	target := filepath.Join(base, clean)
	if target != base && !strings.HasPrefix(target, base+string(filepath.Separator)) {
		return "", errors.New("llmwiki: path escapes root")
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("llmwiki: symlink target rejected")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("llmwiki: inspect target: %w", err)
	}
	for current := filepath.Dir(target); strings.HasPrefix(current, s.root); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("llmwiki: symlink path rejected")
		}
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("llmwiki: inspect path: %w", err)
		}
		if current == s.root {
			break
		}
	}
	return target, nil
}

// atomicWrite writes body to path through a temporary file and renames it into
// place, so readers never observe a partially written Wiki file. Writing the
// same content again is a no-op.
func atomicWrite(path string, body []byte, mode fs.FileMode) error {
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(body) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".llmwiki-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpName) /* best-effort cleanup after the primary write error */
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close() /* cleanup after the primary chmod error */
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close() /* cleanup after the primary write error */
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close() /* cleanup after the primary sync error */
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	keep = true
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() /* Sync below reports the durability error */ }()
	return dir.Sync()
}

// contentSHA256 is the content hash used for sources, pages and build artifacts.
func contentSHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// shortHash derives a short stable directory name from arbitrary text.
func shortHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:12])
}
