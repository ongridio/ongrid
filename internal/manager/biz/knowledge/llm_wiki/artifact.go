package llm_wiki

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
)

// artifactStore keeps the Markdown bodies of one build in its own directory
// tree, so a failed or superseded build never touches the published Wiki.
type artifactStore struct {
	files *FileStore
}

// newArtifactStore creates a build artifact store rooted at the Wiki file store.
func newArtifactStore(files *FileStore) *artifactStore {
	return &artifactStore{files: files}
}

// buildDir returns the base directory for a build's artifacts.
func (s *artifactStore) buildDir(buildID uint64) string {
	return filepath.Join("builds", fmt.Sprintf("%d", buildID))
}

// pagePath returns the relative path of one page body inside a build.
func (s *artifactStore) pagePath(buildID uint64, pageID string) string {
	return filepath.Join(s.buildDir(buildID), "pages", pageID+".md")
}

// writePageBody writes a page body and returns its relative path and content hash.
func (s *artifactStore) writePageBody(ctx context.Context, buildID uint64, pageID, content string) (path, hash string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	relativePath := s.pagePath(buildID, pageID)
	absolutePath, err := s.files.resolve("wiki", relativePath)
	if err != nil {
		return "", "", fmt.Errorf("build artifacts: resolve path: %w", err)
	}
	body := []byte(content)
	if err := atomicWrite(absolutePath, body, 0o640); err != nil {
		return "", "", fmt.Errorf("build artifacts: write page: %w", err)
	}
	return relativePath, contentSHA256(body), nil
}

// readPageBody reads a page body back from the build artifact tree.
func (s *artifactStore) readPageBody(ctx context.Context, buildID uint64, pageID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	absolutePath, err := s.files.resolve("wiki", s.pagePath(buildID, pageID))
	if err != nil {
		return "", fmt.Errorf("build artifacts: resolve path: %w", err)
	}
	body, err := os.ReadFile(absolutePath)
	if err != nil {
		return "", fmt.Errorf("build artifacts: read page: %w", err)
	}
	return string(body), nil
}

// deleteBuild removes every artifact of one build.
func (s *artifactStore) deleteBuild(ctx context.Context, buildID uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.files.resolve("wiki", s.buildDir(buildID))
	if err != nil {
		return fmt.Errorf("build artifacts: resolve dir: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("build artifacts: delete build dir: %w", err)
	}
	return nil
}

// validateBuild reports every page whose body is missing or whose hash no longer
// matches the recorded one.
func (s *artifactStore) validateBuild(ctx context.Context, buildID uint64, pages []*model.WikiBuildPage) []string {
	var failures []string

	for _, page := range pages {
		if page.BuildID != buildID {
			failures = append(failures, fmt.Sprintf("page %s has mismatched build_id", page.PageID))
			continue
		}

		content, err := s.readPageBody(ctx, buildID, page.PageID)
		if err != nil {
			failures = append(failures, fmt.Sprintf("page %s: %v", page.PageID, err))
			continue
		}

		if actualHash := contentSHA256([]byte(content)); actualHash != page.BodySHA256 {
			failures = append(failures, fmt.Sprintf("page %s: hash mismatch (expected %s, got %s)",
				page.PageID, page.BodySHA256[:12], actualHash[:12]))
		}
	}

	return failures
}

// newIndexDocument converts one build page into a search index document.
func newIndexDocument(page *model.WikiBuildPage, content string) IndexDocument {
	return IndexDocument{
		TenantID: page.TenantID,
		PageID:   page.PageID,
		PageType: page.PageType,
		Title:    page.Title,
		Content:  content,
	}
}
