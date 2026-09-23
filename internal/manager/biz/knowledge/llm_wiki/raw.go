package llm_wiki

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MirrorInput describes one source file to mirror into the Wiki tree.
type MirrorInput struct {
	TenantID     uint64
	SourceKey    string
	SourceType   string
	Name         string
	Content      []byte
	RelativePath string
}

// MirroredFile is where a mirrored source file and its snapshot ended up.
type MirroredFile struct {
	RawPath      string
	SnapshotPath string
	SHA256       string
	Size         uint64
}

// MirrorSource writes a raw file plus an immutable, content-addressed snapshot.
// Compilation reads the snapshot, so replacing a raw file never changes a page
// that was generated from an earlier version.
func (s *FileStore) MirrorSource(ctx context.Context, input MirrorInput) (*MirroredFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.SourceKey) == "" {
		return nil, errors.New("llmwiki: source key is required")
	}
	if len(input.Content) > MaxSourceBytes {
		return nil, fmt.Errorf("llmwiki: source exceeds %d bytes", MaxSourceBytes)
	}

	digest := contentSHA256(input.Content)
	rawRelative := rawRelativePath(input.TenantID, input.SourceType, input.SourceKey, input.Name)
	if input.RelativePath != "" {
		rawRelative = safeOrganizationPath(input.RelativePath)
	}
	rawPath, err := s.resolve("raw", rawRelative)
	if err != nil {
		return nil, err
	}

	versionRelative := filepath.ToSlash(filepath.Join(
		".llm-wiki/versions",
		shortHash(fmt.Sprintf("%d:%s", input.TenantID, input.SourceKey)),
		digest+".md",
	))
	versionPath, err := s.resolve("", versionRelative)
	if err != nil {
		return nil, err
	}

	if err := atomicWrite(versionPath, input.Content, 0o640); err != nil {
		return nil, fmt.Errorf("llmwiki: write version: %w", err)
	}
	if err := atomicWrite(rawPath, input.Content, 0o640); err != nil {
		return nil, fmt.Errorf("llmwiki: write raw: %w", err)
	}

	return &MirroredFile{
		RawPath:      rawRelative,
		SnapshotPath: versionRelative,
		SHA256:       digest,
		Size:         uint64(len(input.Content)),
	}, nil
}

// RemoveSourceFile deletes one mirrored raw file. A missing file is not an error.
func (s *FileStore) RemoveSourceFile(ctx context.Context, relative string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.resolve("raw", relative)
	if err != nil {
		return err
	}
	if err := os.Remove(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("llmwiki: remove raw file: %w", err)
	}
	return nil
}
