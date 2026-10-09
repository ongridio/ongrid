//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	devicestore "github.com/ongridio/ongrid/internal/manager/data/device/store"
	edgestore "github.com/ongridio/ongrid/internal/manager/data/edge/store"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Opt-in: starts and removes its own MySQL, never uses an existing database.
func TestMySQLPoolHeartbeatBurst(t *testing.T) {
	if os.Getenv("ONGRID_TEST_DB_POOL") != "1" {
		t.Skip("set ONGRID_TEST_DB_POOL=1 with Docker available")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	container, err := tcmysql.Run(ctx, "mysql:8.0",
		tcmysql.WithDatabase("ongrid_pool_test"),
		testcontainers.WithCmdArgs("--max-connections=151"))
	if container != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := container.Terminate(ctx); err != nil {
				t.Errorf("terminate MySQL: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := container.ConnectionString(ctx, "parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := dbx.Open(config.DBConfig{Dialect: "mysql", DSN: dsn}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	if got := pool.Stats().MaxOpenConnections; got != config.DefaultDBMaxOpenConns {
		t.Fatalf("pool limit = %d, want %d", got, config.DefaultDBMaxOpenConns)
	}
	var serverLimit int
	if err := pool.QueryRowContext(ctx, "SELECT @@max_connections").Scan(&serverLimit); err != nil || serverLimit != 151 {
		t.Fatalf("MySQL connection limit = %d: %v", serverLimit, err)
	}
	if err := db.AutoMigrate(&edgemodel.Edge{}, &devicemodel.Device{}, &devicemodel.EdgeDevice{}); err != nil {
		t.Fatal(err)
	}
	uc := edgebiz.NewUsecase(edgestore.NewRepo(db), devicestore.NewRepo(db), devicestore.NewEdgeDeviceRepo(db), quiet)
	for id := uint64(1); id <= 220; id++ {
		name := fmt.Sprintf("pool-test-%d", id)
		row := edgemodel.Edge{ID: id, Name: name, AccessKeyID: name, SecretKeyHash: "unused", Status: edgemodel.StatusOnline}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	for wave := range 4 {
		start := make(chan struct{})
		var wg sync.WaitGroup
		for id := uint64(1); id <= 220; id++ {
			wg.Go(func() {
				<-start
				rpcCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				if wave == 0 {
					name := fmt.Sprintf("pool-test-%d", id)
					if err := uc.HandleRegister(rpcCtx, id, tunnel.HostInfo{
						Hostname: name, Fingerprint: name, HardwareFingerprint: name,
						OS: "linux", Arch: "amd64", CPUCount: 2,
					}, "test"); err != nil {
						t.Errorf("register %d: %v", id, err)
					}
				} else if err := uc.HandleHeartbeat(rpcCtx, id, time.Now().UTC()); err != nil {
					t.Errorf("heartbeat %d: %v", id, err)
				}
			})
		}
		close(start)
		wg.Wait()
	}
	var registered int64
	if err := db.Model(&edgemodel.Edge{}).Where("device_id IS NOT NULL AND status = ?", edgemodel.StatusOnline).Count(&registered).Error; err != nil || registered != 220 {
		t.Fatalf("registered online edges = %d, want 220: %v", registered, err)
	}
	stats := pool.Stats()
	if stats.InUse != 0 || stats.Idle > config.DefaultDBMaxIdleConns {
		t.Fatalf("pool did not return connections: %+v", stats)
	}
	var key string
	var rejected int
	if err := pool.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Connection_errors_max_connections'").Scan(&key, &rejected); err != nil || rejected != 0 {
		t.Fatalf("MySQL rejected connections = %d: %v", rejected, err)
	}
	t.Logf("220 concurrent registrations and 660 heartbeats: elapsed=%s wait_count=%d open=%d idle=%d", time.Since(started), stats.WaitCount, stats.OpenConnections, stats.Idle)

	// Occupy every slot, then verify cancellation while queued and recovery
	// after release. No MySQL connection limit is changed to make this pass.
	var held []*sql.Conn
	defer func() {
		for _, conn := range held {
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	for range config.DefaultDBMaxOpenConns {
		conn, err := pool.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	queued, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	err = uc.HandleHeartbeat(queued, 1, time.Now().UTC())
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("heartbeat with full pool = %v, want deadline exceeded", err)
	}
	if stats := pool.Stats(); stats.OpenConnections != config.DefaultDBMaxOpenConns || stats.WaitCount == 0 {
		t.Fatalf("pool grew instead of queueing: %+v", stats)
	}
	for _, conn := range held {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	held = nil
	if err := uc.HandleHeartbeat(ctx, 1, time.Now().UTC()); err != nil {
		t.Fatalf("heartbeat after pool recovery: %v", err)
	}
	if stats := pool.Stats(); stats.InUse != 0 || stats.Idle != config.DefaultDBMaxIdleConns {
		t.Fatalf("pool after recovery: %+v", stats)
	}
	customDB, err := dbx.Open(config.DBConfig{Dialect: "mysql", DSN: dsn, MaxOpenConns: 20, MaxIdleConns: 5}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	customPool, err := customDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer customPool.Close()
	if got := customPool.Stats().MaxOpenConnections; got != 20 {
		t.Fatalf("configured pool limit = %d, want 20", got)
	}
}
