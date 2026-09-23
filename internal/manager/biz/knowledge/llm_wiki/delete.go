package llm_wiki

import (
	"context"
	"errors"
	"fmt"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// DeleteNode deletes one mirrored raw source. Generated Wiki pages belong to an
// immutable build and cannot be deleted individually.
func (u *Usecase) DeleteNode(ctx context.Context, id string) error {
	layer, relative, err := decodeNodeID(id)
	if err != nil {
		return errors.Join(errs.ErrInvalid, errors.New("invalid wiki file"))
	}
	if layer != "raw" {
		return errors.Join(errs.ErrInvalid, errors.New("only raw source files can be deleted"))
	}
	if err := u.ensureNoActiveCompile(ctx, DefaultTenantID); err != nil {
		return err
	}

	unlock := u.files.LockArtifacts()
	defer unlock()

	return u.deleteSource(ctx, relative)
}

// deleteSource removes the mirrored source stored at one relative path.
func (u *Usecase) deleteSource(ctx context.Context, relative string) error {
	sources, err := listAllSources(ctx, u.repo, DefaultTenantID)
	if err != nil {
		return fmt.Errorf("llmwiki: list sources for delete: %w", err)
	}
	for _, candidate := range sources {
		if candidate.RawPath == relative {
			return u.removeSource(ctx, candidate)
		}
	}
	return errs.ErrNotFound
}

// removeSource removes one known source, the pages generated from it and its
// index entries.
func (u *Usecase) removeSource(ctx context.Context, source *model.Source) error {
	activeBuild, activeErr := u.repo.GetActiveBuild(ctx, DefaultTenantID)
	if activeErr != nil && !errors.Is(activeErr, errs.ErrNotFound) {
		return activeErr
	}
	if err := u.repo.DeleteSource(ctx, DefaultTenantID, source.ID); err != nil {
		return err
	}
	var cleanupErr error
	if err := u.files.RemoveSourceFile(ctx, source.RawPath); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove raw file: %w", err))
	}
	if activeBuild != nil {
		if err := newArtifactStore(u.files).deleteBuild(ctx, activeBuild.ID); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		if err := u.repo.DeleteBuild(ctx, DefaultTenantID, activeBuild.ID); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if u.indexer != nil {
		if err := u.indexer.Clear(ctx, DefaultTenantID); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if cleanupErr != nil {
		return fmt.Errorf("llmwiki: source %q deleted but derived cleanup failed: %w", source.RawPath, cleanupErr)
	}
	return nil
}
