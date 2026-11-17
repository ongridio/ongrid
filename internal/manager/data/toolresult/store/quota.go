package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const DefaultQuota int64 = 10 << 30

var ErrQuota = errors.New("tool result storage quota exceeded")

// Called under mu. This backend owns its directory; manual file changes are
// picked up by the next recovery/cleanup pass or process restart.
func (r *Repo) checkQuota(ctx context.Context, additional int64) error {
	if !r.usageKnown {
		var used int64
		err := filepath.WalkDir(r.root, func(path string, entry fs.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) && path == r.root {
				return nil
			}
			if err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if entry.Type().IsRegular() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				used += info.Size()
			}
			return nil
		})
		if err != nil {
			return err
		}
		r.usedBytes = used
		r.usageKnown = true
	}
	if additional < 0 || r.usedBytes > r.quota-additional {
		return ErrQuota
	}
	return nil
}
