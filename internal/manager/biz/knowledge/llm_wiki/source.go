package llm_wiki

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
)

// Mirror stores one source file and records the new version. changed reports
// whether the content differed from the stored version, so callers can skip a
// recompilation for an unchanged upload.
func (u *Usecase) Mirror(ctx context.Context, input MirrorInput) (*model.Source, bool, error) {
	mirrored, err := u.files.MirrorSource(ctx, input)
	if err != nil {
		return nil, false, err
	}
	source := &model.Source{
		TenantID:      input.TenantID,
		SourceKey:     input.SourceKey,
		SourceType:    input.SourceType,
		RawPath:       mirrored.RawPath,
		ContentSHA256: mirrored.SHA256,
		Status:        model.SourcePending,
	}
	version := &model.SourceVersion{
		TenantID:      input.TenantID,
		SHA256:        mirrored.SHA256,
		SizeBytes:     mirrored.Size,
		SnapshotPath:  mirrored.SnapshotPath,
		SchemaVersion: SchemaVersion,
	}
	stored, _, changed, err := u.repo.UpsertSourceVersion(ctx, source, version)
	if err != nil {
		return nil, false, fmt.Errorf("llmwiki: save mirror metadata: %w", err)
	}
	return stored, changed, nil
}

type OrganizationSource struct {
	ID                   uint64
	Title, Path, Content string
}
type SyncResult struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Deleted   int `json:"deleted"`
	Total     int `json:"total"`
}

// sourcePageSize is the page size listAllSources walks with. It matches the
// store's default so a page never gets clamped mid-walk.
const sourcePageSize = 200

// sourceLister is the narrow read surface needed to walk a tenant's sources.
type sourceLister interface {
	ListSourcesAfter(ctx context.Context, tenantID uint64, status string, afterID uint64, limit int) ([]*model.Source, error)
}

// listAllSources returns every source of a tenant. ListSources answers with one
// page, so a tenant with more sources than a page holds would silently lose the
// remainder — which for sync means deleted documents never get cleaned up, and
// for a compile means a corpus that quietly omits sources.
func listAllSources(ctx context.Context, repo sourceLister, tenantID uint64) ([]*model.Source, error) {
	all := make([]*model.Source, 0, sourcePageSize)
	var afterID uint64
	for {
		page, err := repo.ListSourcesAfter(ctx, tenantID, "", afterID, sourcePageSize)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return all, nil
		}
		all = append(all, page...)
		afterID = page[len(page)-1].ID
		if len(page) < sourcePageSize {
			return all, nil
		}
	}
}

// SyncOrganizationSources adds or refreshes the organization knowledge base in
// the Wiki raw tree while retaining its folder hierarchy. It deliberately does
// not remove sources missing from this snapshot: repository re-indexing clears
// and repopulates Qdrant, so absence can be transient and must not invalidate
// the active Wiki build. Explicit Raw deletion remains the destructive path.
func (u *Usecase) SyncOrganizationSources(ctx context.Context, docs []OrganizationSource) (*SyncResult, error) {
	if err := u.ensureNoActiveCompile(ctx, DefaultTenantID); err != nil {
		return nil, err
	}

	unlock := u.files.LockArtifacts()
	defer unlock()
	existing, err := listAllSources(ctx, u.repo, DefaultTenantID)
	if err != nil {
		return nil, fmt.Errorf("llmwiki: list sources for sync: %w", err)
	}
	byKey := make(map[string]*model.Source)
	for _, source := range existing {
		if source.SourceType == "organization" {
			byKey[source.SourceKey] = source
		}
	}
	result := &SyncResult{Total: len(docs)}
	for _, doc := range docs {
		key := organizationSourcePrefix + strconv.FormatUint(doc.ID, 10)
		name := strings.TrimSpace(doc.Title)
		if name == "" {
			name = fmt.Sprintf("document-%d", doc.ID)
		}
		relative := filepath.ToSlash(filepath.Join(doc.Path, name+".md"))
		old := byKey[key]
		stored, changed, mirrorErr := u.Mirror(ctx, MirrorInput{TenantID: DefaultTenantID, SourceKey: key, SourceType: "organization", Name: name, Content: []byte(doc.Content), RelativePath: relative})
		if mirrorErr != nil {
			return nil, mirrorErr
		}
		if old == nil {
			result.Created++
		} else if changed || old.RawPath != stored.RawPath {
			result.Updated++
		} else {
			result.Unchanged++
		}
		if old != nil && old.RawPath != stored.RawPath {
			if err := u.files.RemoveSourceFile(ctx, old.RawPath); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

// organizationSourcePrefix marks a source mirrored from one organization
// knowledge-base document, whose id follows the prefix.
const organizationSourcePrefix = "organization:"

// ListSources lists the mirrored sources of a tenant, optionally filtered by
// status.
func (u *Usecase) ListSources(ctx context.Context, tenantID uint64, status string, limit int) ([]*model.Source, int64, error) {
	return u.repo.ListSources(ctx, tenantID, status, limit)
}
