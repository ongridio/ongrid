package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	devicestore "github.com/ongridio/ongrid/internal/manager/data/device/store"
	edgestore "github.com/ongridio/ongrid/internal/manager/data/edge/store"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestSQLiteRegistrationDuringHeartbeat(t *testing.T) {
	db, err := dbx.Open(config.DBConfig{Dialect: "sqlite", Path: filepath.Join(t.TempDir(), "registration.db")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(2)
	for _, migrate := range []func(*gorm.DB) error{devicestore.Migrate, edgestore.Migrate} {
		if err := migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	edge := edgemodel.Edge{AccessKeyID: "registering"}
	other := edgemodel.Edge{AccessKeyID: "heartbeating"}
	for _, row := range []*edgemodel.Edge{&edge, &other} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	heartbeatConn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = heartbeatConn.Close() })
	// Fail immediately on this connection so the interleaving needs no sleeps.
	if _, err := heartbeatConn.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		t.Fatal(err)
	}
	heartbeat := func() error {
		_, err := heartbeatConn.ExecContext(ctx, "UPDATE edges SET last_seen_at = ? WHERE id = ?", time.Now().UTC(), other.ID)
		return err
	}
	type registrationKey struct{}
	attempted, blocked := false, false
	if err := db.Callback().Query().After("gorm:query").Register("heartbeat_after_registration_read", func(tx *gorm.DB) {
		if attempted || tx.Statement.Table != "edges" || tx.Statement.Context.Value(registrationKey{}) != true {
			return
		}
		attempted = true
		if err := heartbeat(); err != nil {
			var coded interface{ Code() int }
			if errors.As(err, &coded) && coded.Code() == 5 { // SQLITE_BUSY
				blocked = true
			} else {
				tx.AddError(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	u := edgebiz.NewUsecase(edgestore.NewRepo(db), devicestore.NewRepo(db), devicestore.NewEdgeDeviceRepo(db), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := u.HandleRegister(context.WithValue(ctx, registrationKey{}, true), edge.ID, tunnel.HostInfo{Hostname: "node-a", Fingerprint: "legacy-a", HardwareFingerprint: "hardware-a"}, "test"); err != nil {
		t.Fatalf("registration during unrelated heartbeat: %v", err)
	}
	if !attempted || !blocked {
		t.Fatal("heartbeat was not held until registration committed")
	}
	if err := heartbeat(); err != nil {
		t.Fatalf("heartbeat after registration: %v", err)
	}
}
