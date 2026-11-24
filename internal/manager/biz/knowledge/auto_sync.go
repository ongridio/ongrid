package knowledge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
)

// A new registration or a changed indexing ref gets one initial sync.
func repoSyncDue(r *model.Repository) bool {
	if IsBuiltinVaultURL(r.URL) {
		return false
	}
	if r.LastSyncedAt == nil {
		return true
	}
	return r.AutoSyncEnabled && r.AutoSyncIntervalMinutes > 0 &&
		int64(time.Since(*r.LastSyncedAt)/time.Minute) >= int64(r.AutoSyncIntervalMinutes)
}

// RunAutoSync uses one bounded worker for startup, newly added repos and refreshes.
func (u *Usecase) RunAutoSync(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			u.log.Error("knowledge: auto sync panic", "panic", p)
		}
	}()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		u.syncDueRepos(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-u.syncWake:
		}
	}
}

func (u *Usecase) syncDueRepos(ctx context.Context) {
	rows, err := u.repo.ListRepos(ctx)
	if err != nil {
		u.log.Warn("knowledge: auto sync list", "err", err)
		return
	}
	// ponytail: sequential repository sync; add bounded concurrency if queue latency matters.
	for _, r := range rows {
		if ctx.Err() != nil {
			return
		}
		if !repoSyncDue(r) {
			continue
		}
		if _, busy := u.active.Load(r.ID); busy {
			continue
		}
		if synced, err := u.syncRepo(ctx, r.ID, true); err != nil {
			u.log.Warn("knowledge: auto sync failed", "repo_id", r.ID, "err", err)
		} else if synced != nil {
			u.log.Info("knowledge: auto sync complete", "repo_id", r.ID)
		}
	}
}

func readSourceStats(ctx context.Context, dir string, repo *model.Repository) error {
	commits, err := runGit(ctx, dir, nil, "rev-list", "--count", "--all")
	if err != nil {
		return fmt.Errorf("count commits: %w", err)
	}
	repo.CommitCount, err = strconv.Atoi(strings.TrimSpace(commits))
	if err != nil {
		return fmt.Errorf("parse commit count: %w", err)
	}
	tags, err := runGit(ctx, dir, nil, "for-each-ref", "--format=%(refname)", "refs/tags/")
	if err != nil {
		return fmt.Errorf("count tags: %w", err)
	}
	repo.TagCount = len(strings.Fields(tags))
	branches, err := runGit(ctx, dir, nil, "for-each-ref", "--format=%(refname) %(symref)", "refs/remotes/origin/")
	if err != nil {
		return fmt.Errorf("count branches: %w", err)
	}
	repo.BranchCount = 0
	for _, line := range strings.Split(branches, "\n") {
		if len(strings.Fields(line)) == 1 {
			repo.BranchCount++
		}
	}
	shallow, err := runGit(ctx, dir, nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return fmt.Errorf("read history depth: %w", err)
	}
	repo.HistoryComplete = strings.TrimSpace(shallow) == "false"
	now := time.Now().UTC()
	repo.SourceSyncedAt = &now
	return nil
}
