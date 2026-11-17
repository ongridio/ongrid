package store

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type recoveryManifest struct {
	Manifest        manifest `json:"manifest"`
	ExecutionKey    string   `json:"execution_key"`
	UserID          uint64   `json:"user_id"`
	StorageKey      string   `json:"storage_key"`
	PayloadMetadata string   `json:"payload_metadata"`
}

// Recover only publishes already committed results. It never invokes tools.
// shortcut: recovery walks the local tree; use a pending queue before scaling
// beyond a single Manager. The startup scheduler bounds each pass by time.
func (r *Repo) Recover(ctx context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() { r.usageKnown = false }()
	if err := os.MkdirAll(r.root, 0o700); err != nil {
		return 0, err
	}
	count := 0
	err := filepath.WalkDir(r.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if count >= 100 {
			return fs.SkipAll
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".staging-") {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if time.Since(info.ModTime()) > time.Hour {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
			}
			return fs.SkipDir
		}
		if entry.Name() != "manifest.json" {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(f, 64<<10))
		if err = errors.Join(readErr, f.Close()); err != nil {
			return err
		}
		if len(body) >= 64<<10 {
			return errs.ErrInvalid
		}
		var saved recoveryManifest
		if err = json.Unmarshal(body, &saved); err != nil || saved.Manifest.Version != 1 {
			return errs.ErrInvalid
		}
		row := saved.Manifest.Result
		row.ExecutionKey = saved.ExecutionKey
		row.UserID = saved.UserID
		row.StorageKey = saved.StorageKey
		row.ManifestJSON = saved.PayloadMetadata
		payload, err := r.payloadPath(&row)
		if err != nil || filepath.Dir(payload) != filepath.Dir(path) {
			return errs.ErrInvalid
		}
		if existing, err := r.findExecution(ctx, row.ExecutionKey); err == nil {
			if existing.ID != row.ID || existing.SHA256 != row.SHA256 {
				return errs.ErrConflict
			}
			if existing.StorageStatus == "ready" && len(saved.Manifest.References) > 0 {
				return r.publish(ctx, existing, saved.Manifest.References)
			}
			return nil
		} else if !errors.Is(err, errs.ErrNotFound) {
			return err
		}
		reader, err := r.Open(ctx, &row)
		if err != nil {
			return err
		}
		_, readErr = io.Copy(io.Discard, reader)
		if err = errors.Join(readErr, reader.Close()); err != nil {
			return err
		}
		if err = r.publish(ctx, &row, saved.Manifest.References); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}
