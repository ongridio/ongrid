package llm_wiki

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
)

// unsafeNameRE matches everything that is not safe inside a generated file name.
var unsafeNameRE = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// rawRelativePath is where a source file is mirrored inside raw/: uploaded files
// keep their name, repo files keep their path below the source namespace, and
// non-default tenants get their own top-level directory.
func rawRelativePath(tenantID uint64, sourceType, sourceKey, name string) string {
	if sourceType == "upload" {
		fileName := safeUploadFileName(name)
		if tenantID == DefaultTenantID {
			return fileName
		}
		return filepath.ToSlash(filepath.Join(fmt.Sprintf("tenant-%d", tenantID), fileName))
	}
	parts := make([]string, 0, 3)
	if tenantID != DefaultTenantID {
		parts = append(parts, fmt.Sprintf("tenant-%d", tenantID))
	}
	parts = append(parts, sourceNamespaceDir(sourceType, sourceKey))
	switch sourceType {
	case "repo", "git":
		parts = append(parts, safeRelativePath(name))
	default:
		parts = append(parts, safeWikiFileName(name))
	}
	return filepath.ToSlash(filepath.Join(parts...))
}

// safeWikiFileName reduces a name to a single Markdown file name inside the Wiki
// tree.
func safeWikiFileName(name string) string {
	name = safeDirName(filepath.Base(strings.TrimSpace(name)))
	if name == "" {
		name = "source"
	}
	if filepath.Ext(name) == "" {
		name += ".md"
	}
	return name
}

// safeUploadFileName keeps the uploaded file name, including Unicode, spaces and
// its original extension. Only path separators and control characters are
// removed, so the raw tree does not silently rename documents such as
// "设计方案.pdf" to an unrelated markdown file name.
func safeUploadFileName(name string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(name), func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return "source"
	}
	name = strings.Map(func(r rune) rune {
		if r == 0 || unicode.IsControl(r) {
			return -1
		}
		return r
	}, parts[len(parts)-1])
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "source"
	}
	return name
}

func safeOrganizationPath(name string) string {
	parts := strings.FieldsFunc(filepath.ToSlash(name), func(r rune) bool { return r == '/' || r == '\\' })
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "." || part == ".." {
			continue
		}
		if safe := safeUploadFileName(part); safe != "source" || part == "source" {
			clean = append(clean, safe)
		}
	}
	if len(clean) == 0 {
		return "source.md"
	}
	clean[len(clean)-1] = strings.TrimSuffix(clean[len(clean)-1], filepath.Ext(clean[len(clean)-1])) + ".md"
	return filepath.ToSlash(filepath.Join(clean...))
}

// wikiPageRelativePath places a generated page below the directory of its
// primary source. SourceRefs are sorted by SourceID so the primary source is
// stable across rebuilds. Old pages without SourcePath keep the historical
// flat layout.
func wikiPageRelativePath(pageID string, refs []model.SourceRef) string {
	filename := safeWikiFileName(pageID)
	sorted := slices.Clone(refs)
	slices.SortFunc(sorted, func(left, right model.SourceRef) int {
		switch {
		case left.SourceID < right.SourceID:
			return -1
		case left.SourceID > right.SourceID:
			return 1
		default:
			return 0
		}
	})

	for _, ref := range sorted {
		sourcePath := strings.TrimSpace(ref.SourcePath)
		if sourcePath == "" {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(sourcePath)))
		if safeDir := safeRelativeDirectory(dir); safeDir != "" {
			return filepath.ToSlash(filepath.Join(safeDir, filename))
		}
		return filename
	}
	return filename
}

// safeRelativeDirectory sanitizes every directory segment while preserving the
// source-language folder names.
func safeRelativeDirectory(name string) string {
	parts := strings.FieldsFunc(filepath.ToSlash(name), func(r rune) bool { return r == '/' || r == '\\' })
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "." || part == ".." {
			continue
		}
		if safe := safeUploadFileName(part); safe != "source" || part == "source" {
			clean = append(clean, safe)
		}
	}
	return filepath.ToSlash(filepath.Join(clean...))
}

// sourceNamespaceDir is the directory that groups all files of one source, for
// example "repo-ongrid" for the source key "github:ongrid".
func sourceNamespaceDir(sourceType, sourceKey string) string {
	parts := strings.Split(sourceKey, ":")
	identifier := sourceKey
	if len(parts) > 1 {
		identifier = parts[1]
	}
	if identifier == "" {
		identifier = "source"
	}
	if name := safeDirName(sourceType + "-" + identifier); name != "" {
		return name
	}
	return "source"
}

// safeDirName reduces a value to a safe single path element.
func safeDirName(name string) string {
	name = unsafeNameRE.ReplaceAllString(strings.TrimSpace(name), "-")
	return strings.Trim(name, ".-")
}

// safeRelativePath keeps the directory structure of a repo-relative path while
// sanitising every element and forcing a Markdown extension on the last one.
func safeRelativePath(name string) string {
	parts := strings.FieldsFunc(filepath.ToSlash(name), func(r rune) bool { return r == '/' || r == '\\' })
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "." || part == ".." {
			continue
		}
		part = unsafeNameRE.ReplaceAllString(strings.Trim(part, ".-"), "-")
		if part == "" {
			continue
		}
		clean = append(clean, part)
	}
	if len(clean) == 0 {
		return "source.md"
	}
	if filepath.Ext(clean[len(clean)-1]) == "" {
		clean[len(clean)-1] += ".md"
	}
	return filepath.Join(clean...)
}
