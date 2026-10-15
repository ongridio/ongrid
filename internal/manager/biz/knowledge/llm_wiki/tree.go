package llm_wiki

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/docextract"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// ListTree returns the children of one Wiki tree node. layer selects the tree
// ("raw" or "wiki") and an empty parentID starts at its root.
func (u *Usecase) ListTree(ctx context.Context, layer, parentID string) ([]TreeNode, error) {
	unlock := u.files.LockArtifacts()
	defer unlock()
	if layer != "raw" && layer != "wiki" {
		return nil, errors.Join(errs.ErrInvalid, errors.New("invalid layer"))
	}
	relative := ""
	if parentID != "" {
		decodedLayer, decodedRelative, err := decodeNodeID(parentID)
		if err != nil || decodedLayer != layer {
			return nil, errors.Join(errs.ErrInvalid, errors.New("invalid parent"))
		}
		relative = decodedRelative
	}
	return u.listTreeFromMetadata(ctx, layer, relative, parentID)
}

// listTreeFromMetadata builds the complete flat tree from one repository
// snapshot. The filesystem is consulted only to discard stale rows and to obtain
// a fallback timestamp; folder counts and hierarchy are derived from durable
// metadata, so expanding a directory never triggers another directory scan.
func (u *Usecase) listTreeFromMetadata(ctx context.Context, layer, parentRelative, parentID string) ([]TreeNode, error) {
	type fileMetadata struct {
		source    *model.Source
		buildPage *model.WikiBuildPage
	}
	files := make(map[string]fileMetadata)
	if layer == "raw" {
		sources, err := listAllSources(ctx, u.repo, DefaultTenantID)
		if err != nil {
			return nil, err
		}
		for _, source := range sources {
			if source == nil || strings.TrimSpace(source.RawPath) == "" {
				continue
			}
			files[filepath.ToSlash(filepath.Clean(source.RawPath))] = fileMetadata{source: source}
		}
	}
	if layer == "wiki" {
		buildPages, active, err := u.activeBuildPages(ctx)
		if err != nil {
			return nil, err
		}
		if active {
			for _, page := range buildPages {
				if page == nil || strings.TrimSpace(page.PageID) == "" || strings.TrimSpace(page.BodyPath) == "" {
					continue
				}
				relative, err := wikiBuildPagePath(page)
				if err != nil {
					return nil, err
				}
				files[relative] = fileMetadata{buildPage: page}
			}
		}
	}

	tree := make(map[string]*TreeNode)
	for relative, metadata := range files {
		storedRelative := relative
		if metadata.buildPage != nil {
			storedRelative = metadata.buildPage.BodyPath
		}
		path, err := u.files.resolve(layer, storedRelative)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			continue
		}
		fileNode := TreeNode{
			ID: encodeNodeID(layer, relative), Layer: layer, Kind: "file",
			Name: filepath.Base(relative), RelativePath: relative, DocumentCount: 1,
		}
		updated := info.ModTime()
		if metadata.source != nil {
			fileNode.SourceID = strconv.FormatUint(metadata.source.ID, 10)
			fileNode.Status = metadata.source.Status
			if !metadata.source.UpdatedAt.IsZero() {
				updated = metadata.source.UpdatedAt
			}
		}
		if metadata.buildPage != nil {
			fileNode.PageID = metadata.buildPage.PageID
			fileNode.PageType = metadata.buildPage.PageType
			fileNode.Name = metadata.buildPage.Title
			fileNode.Status = "ready"
			if !metadata.buildPage.CreatedAt.IsZero() {
				updated = metadata.buildPage.CreatedAt
			}
		}
		fileNode.UpdatedAt = &updated
		tree[relative] = &fileNode
		for parent := filepath.ToSlash(filepath.Dir(relative)); parent != "." && parent != ""; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if _, exists := tree[parent]; !exists {
				tree[parent] = &TreeNode{ID: encodeNodeID(layer, parent), Layer: layer, Kind: "folder", Name: filepath.Base(parent), RelativePath: parent}
			}
		}
	}

	rollUpFolderStats(tree)
	return selectTreeLevel(tree, layer, parentRelative, parentID), nil
}

// rollUpFolderStats counts the documents and the newest update time of every
// folder node.
func rollUpFolderStats(tree map[string]*TreeNode) {
	for relative, node := range tree {
		if node.Kind != "file" {
			continue
		}
		for parent := filepath.ToSlash(filepath.Dir(relative)); parent != "." && parent != ""; parent = filepath.ToSlash(filepath.Dir(parent)) {
			folder := tree[parent]
			if folder == nil {
				continue
			}
			folder.DocumentCount++
			if node.UpdatedAt != nil && (folder.UpdatedAt == nil || node.UpdatedAt.After(*folder.UpdatedAt)) {
				updated := *node.UpdatedAt
				folder.UpdatedAt = &updated
			}
		}
	}
	for relative, node := range tree {
		if node.Kind == "file" {
			continue
		}
		for childRelative := range tree {
			if filepath.ToSlash(filepath.Dir(childRelative)) != relative {
				continue
			}
			node.ChildCount++
			node.HasChildren = true
		}
	}
}

// selectTreeLevel returns the nodes directly below parentRelative, folders first
// and then by name.
func selectTreeLevel(tree map[string]*TreeNode, layer, parentRelative, parentID string) []TreeNode {
	items := make([]TreeNode, 0, len(tree))
	for relative, node := range tree {
		parent := filepath.ToSlash(filepath.Dir(relative))
		if parent == "." {
			parent = ""
		}
		if parentID != "" && parent != parentRelative {
			continue
		}
		if parentID != "" {
			node.ParentID = parentID
		} else if parent != "" {
			node.ParentID = encodeNodeID(layer, parent)
		}
		items = append(items, *node)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind == "folder"
		}
		left := strings.ToLower(items[i].Name)
		right := strings.ToLower(items[j].Name)
		if left != right {
			return left < right
		}
		return items[i].RelativePath < items[j].RelativePath
	})
	return items
}

// GetNode returns one tree node with its content and metadata.
func (u *Usecase) GetNode(ctx context.Context, id string) (*NodeDetail, error) {
	unlock := u.files.LockArtifacts()
	defer unlock()

	layer, relative, err := decodeNodeID(id)
	if err != nil {
		return nil, err
	}
	if layer != "raw" && layer != "wiki" {
		return nil, errs.ErrInvalid
	}
	storedRelative := relative
	var buildPage *model.WikiBuildPage
	if layer == "wiki" {
		buildPage, err = u.findActiveBuildPage(ctx, relative)
		if err != nil {
			return nil, err
		}
		if buildPage != nil {
			storedRelative = buildPage.BodyPath
		} else {
			return nil, errs.ErrNotFound
		}
	}
	path, err := u.files.resolve(layer, storedRelative)
	if err != nil {
		return nil, err
	}
	storeRelative := filepath.ToSlash(filepath.Join(layer, storedRelative))
	maxBytes := int64(MaxPageBytes)
	if layer == "raw" {
		maxBytes = MaxSourceBytes
	}
	body, err := u.files.Read(ctx, storeRelative, maxBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errs.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.Join(errs.ErrInvalid, errors.New("folder has no content"))
	}

	updated := info.ModTime()
	displayBody := body
	if layer == "raw" && binaryPreviewFormat(relative) != "" {
		displayBody = nil
	}
	detail := &NodeDetail{
		TreeNode: TreeNode{ID: id, Layer: layer, Kind: "file", Name: filepath.Base(relative), RelativePath: relative, UpdatedAt: &updated},
		Content:  string(displayBody),
		Metadata: map[string]any{"sha256": contentSHA256(body)},
	}
	if buildPage != nil {
		detail.Name = buildPage.Title
		detail.PageID = buildPage.PageID
		detail.PageType = buildPage.PageType
		detail.Status = "ready"
	}
	if layer == "raw" {
		if err := u.attachSourceFiles(ctx, detail, relative); err != nil {
			return nil, err
		}
	}
	if layer == "wiki" {
		if buildPage != nil {
			if err := u.attachBuildPageMetadata(ctx, detail, buildPage); err != nil {
				return nil, err
			}
		}
	}
	return detail, nil
}

// activeBuildPages returns the published build snapshot. The active build is
// the sole source of truth for generated Wiki pages.
func (u *Usecase) activeBuildPages(ctx context.Context) ([]*model.WikiBuildPage, bool, error) {
	return u.activeBuildPagesForTenant(ctx, DefaultTenantID)
}

func (u *Usecase) activeBuildPagesForTenant(ctx context.Context, tenantID uint64) ([]*model.WikiBuildPage, bool, error) {
	build, err := u.repo.GetActiveBuild(ctx, tenantID)
	if errors.Is(err, errs.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	pages, err := u.repo.ListPagesByBuild(ctx, tenantID, build.ID)
	return pages, true, err
}

func (u *Usecase) findActiveBuildPage(ctx context.Context, relative string) (*model.WikiBuildPage, error) {
	pages, active, err := u.activeBuildPages(ctx)
	if err != nil || !active {
		return nil, err
	}
	for _, page := range pages {
		virtualPath, err := wikiBuildPagePath(page)
		if err != nil {
			return nil, err
		}
		if virtualPath == relative {
			return page, nil
		}
	}
	return nil, nil
}

// wikiBuildPagePath derives the visible tree path of a generated page from its
// source provenance. Pages written before SourcePath existed stay flat.
func wikiBuildPagePath(page *model.WikiBuildPage) (string, error) {
	if page == nil {
		return "", nil
	}
	var refs []model.SourceRef
	if raw := strings.TrimSpace(page.SourceRefsJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &refs); err != nil {
			return "", fmt.Errorf("llmwiki: decode build page source refs for %s: %w", page.PageID, err)
		}
	}
	return wikiPageRelativePath(page.PageID, refs), nil
}

func (u *Usecase) attachBuildPageMetadata(ctx context.Context, detail *NodeDetail, page *model.WikiBuildPage) error {
	detail.Metadata["page_type"] = page.PageType
	var refs []model.SourceRef
	if err := json.Unmarshal([]byte(page.SourceRefsJSON), &refs); err != nil {
		return fmt.Errorf("llmwiki: decode build page source refs: %w", err)
	}
	type sourceReference struct {
		Path      string `json:"path"`
		NodeID    string `json:"node_id"`
		VersionID string `json:"version_id"`
	}
	references := make([]sourceReference, 0, len(refs))
	seen := make(map[uint64]bool, len(refs))
	for _, ref := range refs {
		if seen[ref.SourceID] {
			continue
		}
		seen[ref.SourceID] = true
		source, err := u.repo.GetSource(ctx, page.TenantID, ref.SourceID)
		if err != nil {
			return err
		}
		references = append(references, sourceReference{Path: source.RawPath, NodeID: encodeNodeID("raw", source.RawPath), VersionID: strconv.FormatUint(ref.SourceVersionID, 10)})
	}
	if len(references) > 0 {
		detail.Metadata["source_files"] = references
		detail.Metadata["source_file"] = references[0].Path
		detail.Metadata["source_node_id"] = references[0].NodeID
		detail.Metadata["source_version"] = references[0].VersionID
	}
	return nil
}

// attachSourceFiles adds the source row of one raw file to a node: its id, type,
// status and the Wiki pages generated from it.
func (u *Usecase) attachSourceFiles(ctx context.Context, detail *NodeDetail, relative string) error {
	sources, err := listAllSources(ctx, u.repo, DefaultTenantID)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if source.RawPath != relative {
			continue
		}
		detail.SourceID = strconv.FormatUint(source.ID, 10)
		detail.Status = source.Status
		detail.Metadata["source_type"] = source.SourceType
		if source.CurrentVersionID != nil {
			detail.Metadata["source_version"] = strconv.FormatUint(*source.CurrentVersionID, 10)
		}
		return u.attachWikiFiles(ctx, detail.Metadata, source.ID)
	}
	return nil
}

// attachWikiFiles lists the Wiki pages generated from one source.
func (u *Usecase) attachWikiFiles(ctx context.Context, metadata map[string]any, sourceID uint64) error {
	pages, active, err := u.activeBuildPages(ctx)
	if err != nil {
		return err
	}
	type wikiReference struct {
		Title  string `json:"title"`
		Path   string `json:"path"`
		NodeID string `json:"node_id"`
		PageID string `json:"page_id"`
	}
	references := make([]wikiReference, 0, len(pages))
	if !active {
		metadata["wiki_files"] = references
		return nil
	}
	for _, page := range pages {
		var refs []model.SourceRef
		if err := json.Unmarshal([]byte(page.SourceRefsJSON), &refs); err != nil {
			return fmt.Errorf("llmwiki: decode build page source refs: %w", err)
		}
		matched := false
		for _, ref := range refs {
			if ref.SourceID == sourceID {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		path, err := wikiBuildPagePath(page)
		if err != nil {
			return err
		}
		references = append(references, wikiReference{
			Title:  page.Title,
			Path:   path,
			NodeID: encodeNodeID("wiki", path),
			PageID: page.PageID,
		})
	}
	metadata["wiki_files"] = references
	return nil
}

// PreviewNode returns the binary preview of one raw file. Only PDF and DOCX
// files have one: both are converted to a browser-friendly representation.
func (u *Usecase) PreviewNode(ctx context.Context, id string) (*NodePreview, error) {
	unlock := u.files.LockArtifacts()
	defer unlock()

	layer, relative, err := decodeNodeID(id)
	if err != nil || layer != "raw" {
		return nil, errors.Join(errs.ErrInvalid, errors.New("only raw files can be previewed"))
	}
	previewFormat := binaryPreviewFormat(relative)
	if previewFormat == "" {
		return nil, errors.Join(errs.ErrInvalid, errors.New("only PDF and DOCX files support binary preview"))
	}
	body, err := u.files.Read(ctx, filepath.ToSlash(filepath.Join("raw", relative)), MaxSourceBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errs.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	contentType := "application/pdf"
	if previewFormat == "docx" {
		text, extractErr := docextract.ExtractPlainText(relative, body)
		if extractErr != nil {
			return nil, fmt.Errorf("llmwiki: preview source %q: %w", relative, extractErr)
		}
		body = []byte(text)
		contentType = "text/plain; charset=utf-8"
	}
	return &NodePreview{Name: filepath.Base(relative), ContentType: contentType, Content: body}, nil
}

// binaryPreviewFormat reports the preview format of a raw file, or "" when the
// file has no binary preview.
func binaryPreviewFormat(relative string) string {
	switch strings.ToLower(filepath.Ext(relative)) {
	case ".pdf":
		return "pdf"
	case ".docx":
		return "docx"
	default:
		return ""
	}
}

// encodeNodeID encodes a tree position as an opaque, URL-safe node id.
func encodeNodeID(layer, relative string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(layer + ":" + relative))
}

// decodeNodeID splits a node id back into its layer and store-relative path.
func decodeNodeID(id string) (string, string, error) {
	body, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", "", errors.Join(errs.ErrInvalid, err)
	}
	parts := strings.SplitN(string(body), ":", 2)
	if len(parts) != 2 {
		return "", "", errs.ErrInvalid
	}
	relative := filepath.Clean(filepath.FromSlash(parts[1]))
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errs.ErrInvalid
	}
	return parts[0], filepath.ToSlash(relative), nil
}

// ParseID parses a numeric id from an API path parameter.
func ParseID(id string) (uint64, error) {
	value, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return 0, errors.Join(errs.ErrInvalid, err)
	}
	return value, nil
}
