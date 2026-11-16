package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testRepo(t *testing.T) *Repo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "results.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	return New(db, t.TempDir())
}

func testRow(body []byte) *model.Result {
	key := sha256.Sum256([]byte(uuid.NewString()))
	return &model.Result{ID: uuid.NewString(), ExecutionKey: hex.EncodeToString(key[:]), SessionID: uuid.NewString(), UserID: 7, ToolName: "test_tool", ExecutionStatus: "success", CaptureStatus: "complete", ContentType: "application/json", ExpectedBytes: int64(len(body)), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour)}
}

func TestSaveAndReadSmallPlainCompressed(t *testing.T) {
	for _, size := range []int{0, 200, InlineLimit, InlineLimit + 1, resultfile.BlockSize + 25} {
		r := testRepo(t)
		body := bytes.Repeat([]byte("a"), size)
		row := testRow(body)
		if err := r.Save(context.Background(), row, body, nil); err != nil {
			t.Fatal(err)
		}
		stored, err := r.Get(context.Background(), row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if (stored.StorageKey == "") != (size <= InlineLimit) {
			t.Fatal("wrong inline policy")
		}
		if (stored.Encoding == resultfile.Seekable) != (size >= resultfile.BlockSize) {
			t.Fatal("wrong compression policy")
		}
		reader, err := r.Open(context.Background(), stored)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if err = reader.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, body) {
			t.Fatal("payload changed")
		}
		// Repeated publication is idempotent; different bytes never replace it.
		retry := *row
		retry.ID = uuid.NewString()
		if err = r.Save(context.Background(), &retry, body, nil); err != nil || retry.ID != row.ID {
			t.Fatalf("retry=%v %s", err, retry.ID)
		}
		if err = r.Save(context.Background(), &retry, append(body, 'x'), nil); !errors.Is(err, errs.ErrConflict) {
			t.Fatalf("conflict=%v", err)
		}
	}
}

func TestRecoveryPublishesDurableOutputWithoutToolExecution(t *testing.T) {
	for _, size := range []int{50, InlineLimit + 5} {
		r := testRepo(t)
		body := bytes.Repeat([]byte("x"), size)
		row := testRow(body)
		until := time.Now().UTC().Add(60 * 24 * time.Hour)
		refs := []model.Reference{{Kind: "approval", ID: uuid.NewString(), ResultID: row.ID, RetainUntil: &until}}
		if err := r.db.Callback().Create().Before("gorm:create").Register("test_failure", func(tx *gorm.DB) {
			if tx.Statement.Table == "tool_results" {
				tx.AddError(errors.New("database unavailable"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		if err := r.Save(context.Background(), row, body, refs); err == nil {
			t.Fatal("expected publication failure")
		}
		if err := r.db.Callback().Create().Remove("test_failure"); err != nil {
			t.Fatal(err)
		}
		if n, err := r.Recover(context.Background()); err != nil || n != 1 {
			t.Fatalf("recover=%d,%v", n, err)
		}
		got, err := r.Get(context.Background(), row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.UserID != 7 || got.ExecutionKey != row.ExecutionKey || got.StorageKey == "" {
			t.Fatal("recovery lost private metadata")
		}
		payload, err := r.Read(context.Background(), got, 0, size)
		if err != nil || !bytes.Equal(payload, body) {
			t.Fatalf("read=%v", err)
		}
		if n, err := r.Recover(context.Background()); err != nil || n != 0 {
			t.Fatalf("idempotent recovery=%d,%v", n, err)
		}
		var ref model.Reference
		if err = r.db.First(&ref).Error; err != nil || ref.RetainUntil == nil {
			t.Fatalf("lost retained reference: %v", err)
		}
	}
}

func TestCleanupProtectsPinnedResultAndLeavesTombstone(t *testing.T) {
	r := testRepo(t)
	now := time.Now().UTC()
	body := bytes.Repeat([]byte("x"), InlineLimit+1)
	row := testRow(body)
	row.ExpiresAt = now.Add(-time.Minute)
	until := now.Add(time.Hour)
	ref := model.Reference{Kind: "report", ID: uuid.NewString(), ResultID: row.ID, RetainUntil: &until}
	if err := r.Save(context.Background(), row, body, []model.Reference{ref}); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Cleanup(context.Background(), now); n != 0 || err != nil {
		t.Fatalf("protected cleanup=%d,%v", n, err)
	}
	if n, err := r.Cleanup(context.Background(), until.Add(time.Second)); n != 1 || err != nil {
		t.Fatalf("expired cleanup=%d,%v", n, err)
	}
	got, err := r.Get(context.Background(), row.ID)
	if err != nil || got.StorageStatus != "expired" {
		t.Fatalf("tombstone=%+v,%v", got, err)
	}
	if _, err = r.Open(context.Background(), got); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expired open=%v", err)
	}
}

func TestQuotaStopsPreflightAndFilePublication(t *testing.T) {
	r := testRepo(t)
	r.quota = 1
	if err := r.CheckWritable(context.Background()); !errors.Is(err, ErrQuota) {
		t.Fatalf("preflight=%v", err)
	}
	body := bytes.Repeat([]byte("x"), InlineLimit+1)
	row := testRow(body)
	if err := r.Save(context.Background(), row, body, nil); !errors.Is(err, ErrQuota) {
		t.Fatalf("save=%v", err)
	}
	if _, err := r.Get(context.Background(), row.ID); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("quota failure published a row: %v", err)
	}
	r.quota = DefaultQuota
	if err := r.CheckWritable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(context.Background(), row, body, nil); err != nil {
		t.Fatal(err)
	}
	r.quota = r.usedBytes
	if err := r.checkQuota(context.Background(), 1); !errors.Is(err, ErrQuota) {
		t.Fatalf("published bytes were not accounted: %v", err)
	}
}
