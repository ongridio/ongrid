package knowledge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
	"unicode"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// UpdateRepo preserves repository identity and all mirrored source history.
func (u *Usecase) UpdateRepo(ctx context.Context, id uint64, branch, description string) (*model.Repository, error) {
	if _, busy := u.active.LoadOrStore(id, true); busy {
		return nil, fmt.Errorf("%w: repository syncing; retry after sync completes", errs.ErrConflict)
	}
	defer func() {
		u.active.Delete(id)
		select {
		case u.syncWake <- struct{}{}:
		default:
		}
	}()
	repo, err := u.repo.GetRepo(ctx, id)
	if err != nil {
		return nil, err
	}
	branch, description = strings.TrimSpace(branch), strings.TrimSpace(description)
	if len(description) > 512 {
		return nil, fmt.Errorf("%w: description exceeds 512 bytes", errs.ErrInvalid)
	}
	if err := u.validateRepository(ctx, repo.URL, branch); err != nil {
		return nil, err
	}
	if err := u.repo.UpdateRepo(ctx, id, branch, description); err != nil {
		return nil, fmt.Errorf("knowledge: update repo: %w", err)
	}
	return u.repo.GetRepo(ctx, id)
}

// Validate with the same SSH identity and host-key policy as the actual fetch.
// ls-remote checks access and ref existence, not clone or indexing completion.
func (u *Usecase) validateRepository(ctx context.Context, repoURL, branch string) error {
	if err := validateRepositoryURL(repoURL); err != nil {
		return err
	}
	if branch == "" || len(branch) > 128 || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "refs/") {
		return fmt.Errorf("%w: enter a branch or tag name (without refs/ prefix)", errs.ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := runGit(ctx, "", nil, "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("%w: invalid branch or tag name", errs.ErrInvalid)
	}
	env, cleanup, err := u.buildGitAuthEnv(ctx, repoURL)
	if err != nil {
		return fmt.Errorf("%w: repository credentials: %w", errs.ErrInvalid, err)
	}
	defer cleanup()
	out, err := runGit(ctx, "", env, "ls-remote", "--exit-code", "--refs", "--", repoURL, "refs/heads/"+branch, "refs/tags/"+branch)
	if ctx.Err() != nil {
		return fmt.Errorf("%w: repository verification timed out or was cancelled", errs.ErrInvalid)
	}
	if err != nil {
		// ls-remote exits 2 when no ref matches, even with benign SSH stderr.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			return fmt.Errorf("%w: branch or tag %q not found", errs.ErrInvalid, branch)
		}
		return fmt.Errorf("%w: repository verification failed: %s", errs.ErrInvalid, strings.TrimSpace(out))
	}
	matches := 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && (fields[1] == "refs/heads/"+branch || fields[1] == "refs/tags/"+branch) {
			matches++
		}
	}
	if matches == 0 {
		return fmt.Errorf("%w: branch or tag %q not found", errs.ErrInvalid, branch)
	}
	if matches > 1 {
		return fmt.Errorf("%w: branch and tag share the name %q; use a unique ref name", errs.ErrInvalid, branch)
	}
	return nil
}

func validateRepositoryURL(raw string) error {
	invalid := fmt.Errorf("%w: use an HTTP(S) or SSH repository URL without embedded credentials", errs.ErrInvalid)
	if raw == "" || len(raw) > 512 || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return invalid
	}
	parsedURL := raw
	if !strings.Contains(raw, "://") {
		// Accept the existing user@host:path SCP notation; no local paths or helpers.
		userHost, path, ok := strings.Cut(raw, ":")
		if !ok || !strings.Contains(userHost, "@") || path == "" {
			return invalid
		}
		parsedURL = "ssh://" + userHost + "/" + path
	}
	parsed, err := url.Parse(parsedURL)
	if err != nil || parsed.Hostname() == "" || strings.HasPrefix(parsed.Hostname(), "-") || parsed.Path == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return invalid
	}
	switch parsed.Scheme {
	case "http", "https":
		if parsed.User != nil {
			return invalid
		}
	case "ssh":
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword || strings.HasPrefix(parsed.User.Username(), "-") {
				return invalid
			}
		}
	default:
		return invalid
	}
	return nil
}
