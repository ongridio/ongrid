package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	approvalmodel "github.com/ongridio/ongrid/internal/manager/model/approval"
	"github.com/shirou/gopsutil/v3/disk"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/resultfile"
)

const InlineLimit = 32 << 10

type Repo struct {
	quota      int64
	usedBytes  int64
	usageKnown bool
	db         *gorm.DB
	root       string
	// Publication and cleanup share this lock. This backend is for one
	// Manager; independent local disks cannot support multiple Managers.
	mu sync.Mutex
}

func Migrate(db *gorm.DB) error { return db.AutoMigrate(&model.Result{}, &model.Reference{}) }

func New(db *gorm.DB, root string, quota ...int64) *Repo {
	limit := DefaultQuota
	if len(quota) > 0 && quota[0] > 0 {
		limit = quota[0]
	}
	return &Repo{db: db, root: filepath.Clean(root), quota: limit}
}

func (r *Repo) CheckWritable(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkQuota(ctx, resultfile.MaxSize+(8<<20)+(64<<10)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		return fmt.Errorf("check result metadata store: %w", err)
	}
	if err := os.MkdirAll(r.root, 0o700); err != nil {
		return fmt.Errorf("create tool result root: %w", err)
	}
	usage, err := disk.UsageWithContext(ctx, r.root)
	if err != nil {
		return fmt.Errorf("check result disk capacity: %w", err)
	}
	if usage.Free < resultfile.MaxSize+(8<<20)+(64<<10) {
		return ErrQuota
	}
	f, err := os.CreateTemp(r.root, ".probe-")
	if err != nil {
		return fmt.Errorf("check tool result storage: %w", err)
	}
	_, writeErr := f.Write([]byte{0})
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close(), os.Remove(f.Name()))
}

type manifest struct {
	Version    int                 `json:"version"`
	Result     model.Result        `json:"result"`
	Metadata   resultfile.Metadata `json:"metadata"`
	References []model.Reference   `json:"references"`
}

type payloadMetadata struct {
	resultfile.Metadata
	IndexSHA256 string `json:"index_sha256"`
}

// Save publishes file bytes before the DB row. The durable manifest contains
// enough metadata to retry DB publication without executing the tool again.
func (r *Repo) Save(ctx context.Context, row *model.Result, body []byte, refs []model.Reference) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := uuid.Parse(row.ID); err != nil {
		return errs.ErrInvalid
	}
	if len(body) > resultfile.MaxSize {
		return errs.ErrInvalid
	}
	if existing, err := r.FindExecution(ctx, row.ExecutionKey); err == nil {
		digest := sha256.Sum256(body)
		if existing.SHA256 != hex.EncodeToString(digest[:]) || existing.SessionID != row.SessionID || existing.UserID != row.UserID {
			return errs.ErrConflict
		}
		*row = *existing
		for i := range refs {
			refs[i].ResultID = row.ID
		}
		return r.publish(ctx, row, refs)
	}
	// A failed metadata lookup must not prevent durable capture. Publication
	// checks idempotence again inside its transaction, or recovery retries it.
	if len(body) <= InlineLimit {
		digest := sha256.Sum256(body)
		row.InlineBody = body
		row.Encoding = resultfile.Plain
		row.CapturedBytes = int64(len(body))
		row.StoredBytes = int64(len(body))
		row.SHA256 = hex.EncodeToString(digest[:])
		row.StorageStatus = "ready"
		row.ManifestJSON = "{}"
		if err := r.publish(ctx, row, refs); err == nil {
			return nil
		}
		// A small-result DB failure uses the same durable spool as large
		// results; recovery publishes its file representation.
		row.InlineBody = nil
	}
	if err := r.checkQuota(ctx, int64(len(body))+(8<<20)+(64<<10)); err != nil {
		return err
	}
	key := filepath.Join(row.CreatedAt.UTC().Format("2006-01-02"), row.ID[:2], row.ID)
	finalDir := filepath.Join(r.root, key)
	parent := filepath.Dir(finalDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create result directory: %w", err)
	}
	for _, dir := range []string{r.root, filepath.Dir(parent), parent} {
		if err := syncDirectory(dir); err != nil {
			return err
		}
	}
	staging, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return err
	}
	defer func() {
		// A failed unpublished staging directory contains no durable result.
		// A successful rename makes this removal a no-op.
		if err := os.RemoveAll(staging); err != nil { /* recovered by the staging sweeper */
			r.usageKnown = false
		}
	}()
	f, err := os.OpenFile(filepath.Join(staging, "payload.data"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	compressed := len(body) >= resultfile.BlockSize && (strings.HasPrefix(row.ContentType, "text/") || row.ContentType == "application/json")
	meta, writeErr := resultfile.Write(ctx, f, bytes.NewReader(body), compressed)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err = errors.Join(writeErr, f.Close()); err != nil {
		return fmt.Errorf("write result payload: %w", err)
	}
	row.StorageKey = key
	row.Encoding = meta.Encoding
	row.CapturedBytes = meta.Size
	row.StoredBytes = meta.StoredSize
	row.SHA256 = meta.SHA256
	row.StorageStatus = "ready"
	indexBytes, err := json.Marshal(resultfile.BuildIndex(body))
	if err != nil {
		return err
	}
	indexHash := sha256.Sum256(indexBytes)
	if err = writeSynced(filepath.Join(staging, "index.json"), indexBytes); err != nil {
		return err
	}
	encoded, err := json.Marshal(payloadMetadata{meta, hex.EncodeToString(indexHash[:])})
	if err != nil {
		return err
	}
	row.ManifestJSON = string(encoded)
	// Result's public JSON deliberately excludes internal fields. Recovery
	// data is kept separately from the public metadata below.
	recovery := recoveryManifest{manifest{1, *row, meta, refs}, row.ExecutionKey, row.UserID, row.StorageKey, row.ManifestJSON}
	manifestBytes, err := json.Marshal(recovery)
	if err != nil {
		return err
	}
	if err = writeSynced(filepath.Join(staging, "manifest.json"), manifestBytes); err != nil {
		return err
	}
	if err = syncDirectory(staging); err != nil {
		return err
	}
	if err = os.Rename(staging, finalDir); err != nil {
		return fmt.Errorf("publish result file: %w", err)
	}
	r.usedBytes += meta.StoredSize + int64(len(indexBytes)+len(manifestBytes))
	if err = syncDirectory(parent); err != nil {
		return errors.Join(model.ErrPending, err)
	}
	if err = r.publish(ctx, row, refs); err != nil {
		return errors.Join(model.ErrPending, fmt.Errorf("publish spooled result metadata: %w", err))
	}
	return nil
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func (r *Repo) publish(ctx context.Context, row *model.Result, refs []model.Reference) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
			return err
		}
		var stored model.Result
		if err := tx.Where("execution_key = ?", row.ExecutionKey).First(&stored).Error; err != nil {
			return err
		}
		if stored.ID != row.ID || stored.SHA256 != row.SHA256 || stored.UserID != row.UserID || stored.SessionID != row.SessionID {
			return errs.ErrConflict
		}
		for _, ref := range refs {
			ref.ResultID = row.ID
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ref).Error; err != nil {
				return err
			}
			var actual model.Reference
			if err := tx.Where("kind = ? AND ref_id = ?", ref.Kind, ref.ID).First(&actual).Error; err != nil {
				return err
			}
			if actual.ResultID != row.ID {
				return errs.ErrConflict
			}
			if ref.Kind == "approval" && tx.Migrator().HasTable(&approvalmodel.Approval{}) {
				status := approvalmodel.StatusExecuted
				if row.ExecutionStatus != "success" {
					status = approvalmodel.StatusFailed
				}
				body, err := json.Marshal(map[string]any{"kind": "tool_result", "result_id": row.ID, "status": row.ExecutionStatus, "execution_status": row.ExecutionStatus, "capture_status": row.CaptureStatus, "storage_status": "ready", "view_status": "reference", "captured_bytes": row.CapturedBytes})
				if err != nil {
					return err
				}
				if err = tx.Model(&approvalmodel.Approval{}).Where("id = ? AND status = ?", ref.ID, approvalmodel.StatusApproved).Updates(map[string]any{"status": status, "result_json": string(body), "result_id": row.ID, "executed_at": row.CreatedAt}).Error; err != nil {
					return err
				}
			}

		}
		return nil
	})
}

func (r *Repo) FindExecution(ctx context.Context, key string) (*model.Result, error) {
	var row model.Result
	err := r.db.WithContext(ctx).Where("execution_key = ?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	return &row, err
}

func (r *Repo) Get(ctx context.Context, id string) (*model.Result, error) {
	var row model.Result
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	if err == nil && row.StorageStatus == "ready" && !row.ExpiresAt.After(time.Now().UTC()) {
		var pins int64
		if e := r.db.WithContext(ctx).Model(&model.Reference{}).Where("result_id = ? AND retain_until > ?", row.ID, time.Now().UTC()).Count(&pins).Error; e != nil {
			return nil, e
		}
		if pins == 0 {
			row.StorageStatus = "expired"
		}
	}
	return &row, err
}

func (r *Repo) Read(ctx context.Context, row *model.Result, offset int64, limit int) ([]byte, error) {
	if offset < 0 || limit < 0 || limit > resultfile.BlockSize || offset > row.CapturedBytes {
		return nil, errs.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if row.StorageStatus != "ready" {
		return nil, fmt.Errorf("tool result is %s", row.StorageStatus)
	}
	end := min(row.CapturedBytes, offset+int64(limit))
	if row.StorageKey == "" {
		digest := sha256.Sum256(row.InlineBody)
		if int64(len(row.InlineBody)) != row.CapturedBytes || hex.EncodeToString(digest[:]) != row.SHA256 {
			return nil, resultfile.ErrCorrupt
		}
		return append([]byte(nil), row.InlineBody[offset:end]...), nil
	}
	path, err := r.payloadPath(row)
	if err != nil {
		return nil, err
	}
	var meta resultfile.Metadata
	if err = json.Unmarshal([]byte(row.ManifestJSON), &meta); err != nil {
		return nil, resultfile.ErrCorrupt
	}
	if meta.Size != row.CapturedBytes || meta.SHA256 != row.SHA256 || meta.Encoding != row.Encoding {
		return nil, resultfile.ErrCorrupt
	}
	reader, err := resultfile.Open(ctx, path, meta)
	if err != nil {
		return nil, err
	}
	out := make([]byte, end-offset)
	_, err = reader.ReadAt(out, offset)
	return out, errors.Join(err, reader.Close())
}

func (r *Repo) payloadPath(row *model.Result) (string, error) {
	if _, err := uuid.Parse(row.ID); err != nil {
		return "", errs.ErrInvalid
	}
	expected := filepath.Join(row.CreatedAt.UTC().Format("2006-01-02"), row.ID[:2], row.ID)
	if row.StorageKey != expected {
		return "", resultfile.ErrCorrupt
	}
	return filepath.Join(r.root, expected, "payload.data"), nil
}

func (r *Repo) AddReference(ctx context.Context, ref model.Reference) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, err := r.Get(ctx, ref.ResultID)
	if err != nil {
		return err
	}
	if row.StorageStatus != "ready" {
		return errs.ErrConflict
	}
	return r.publish(ctx, row, []model.Reference{ref})
}

// Cleanup leaves expired metadata as a tombstone so callers can distinguish
// expiry from an empty result. Pinned references protect evidence and approvals.
func (r *Repo) Cleanup(ctx context.Context, now time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() { r.usageKnown = false }()
	var rows []model.Result
	err := r.db.WithContext(ctx).Where("expires_at <= ? AND storage_status IN ?", now, []string{"ready", "deleting"}).
		Where("NOT EXISTS (SELECT 1 FROM tool_result_refs ref WHERE ref.result_id = tool_results.id AND ref.retain_until > ?)", now).Limit(100).Find(&rows).Error
	if err != nil {
		return 0, err
	}
	count := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if err := r.db.WithContext(ctx).Model(&row).Update("storage_status", "deleting").Error; err != nil {
			return count, err
		}
		if row.StorageKey != "" {
			path, err := r.payloadPath(&row)
			if err != nil {
				return count, err
			}
			if err = os.RemoveAll(filepath.Dir(path)); err != nil {
				return count, err
			}
			if err = syncDirectory(filepath.Dir(filepath.Dir(path))); err != nil {
				return count, err
			}
		}
		if err := r.db.WithContext(ctx).Model(&row).Updates(map[string]any{"storage_status": "expired", "inline_body": nil}).Error; err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
