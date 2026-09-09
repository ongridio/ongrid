package knowledge

import (
	"context"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Resolve once and read immutable Git objects. No checkout or fetch: concurrent
// investigations cannot change another request's version or the knowledge index.
func sourceRevision(ctx context.Context, dir, revision string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, codeGrepTimeout)
	defer cancel()
	ref := revision
	if ref == "" {
		ref = "HEAD"
	} else {
		if len(ref) > 256 {
			return "", fmt.Errorf("%w: revision too long", errs.ErrInvalid)
		}
		_, hexErr := hex.DecodeString(ref)
		if hexErr != nil || (len(ref) != 40 && len(ref) != 64) {
			ref = "refs/tags/" + strings.TrimPrefix(ref, "refs/tags/")
			if err := exec.CommandContext(ctx, "git", "check-ref-format", ref).Run(); err != nil {
				return "", fmt.Errorf("%w: revision must be an exact tag or full commit SHA", errs.ErrInvalid)
			}
		}
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}").Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("resolve source revision: %w", ctx.Err())
		}
		return "", fmt.Errorf("%w: revision %q is not available locally; sync that tag in Code repos before analysis (no HEAD fallback)", errs.ErrNotFound, revision)
	}
	return strings.TrimSpace(string(out)), nil
}

// ResolveSourceRevision returns a full commit without checking out another tree.
func (u *Usecase) ResolveSourceRevision(ctx context.Context, ref, revision string) (string, error) {
	_, dir, err := u.resolveRepoClone(ctx, ref)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(revision) == "" {
		return "", fmt.Errorf("%w: revision required", errs.ErrInvalid)
	}
	return sourceRevision(ctx, dir, revision)
}
