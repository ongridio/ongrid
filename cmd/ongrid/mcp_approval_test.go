package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	approval "github.com/ongridio/ongrid/internal/manager/biz/approval"
	store "github.com/ongridio/ongrid/internal/manager/data/approval/store"
)

func TestMCPApprovalLifecycle(t *testing.T) {
	for _, decision := range []string{"approve", "reject", "failure", "cancel"} {
		t.Run(decision, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "approvals.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() {
				if err := sqlDB.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := store.Migrate(db); err != nil {
				t.Fatal(err)
			}
			uc := approval.NewUsecase(store.NewRepo(db), nil)
			var executions atomic.Int32
			uc.RegisterExecutor("mcp_call", func(_ context.Context, payload string) (string, error) {
				executions.Add(1)
				var p mcpCallPayload
				if err := json.Unmarshal([]byte(payload), &p); err != nil {
					return "", err
				}
				if p.Server != "test" || p.Tool != "cluster.list" || p.Arguments["limit"] != float64(20) {
					return "", errors.New("incorrect frozen payload")
				}
				if decision == "failure" {
					return "", errors.New("remote failed")
				}
				return `{"clusters":["test"]}`, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type outcome struct {
				result string
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := (mcpProposerShim{uc: uc}).ProposeMCPCallAndAwait(ctx, "test", "cluster.list", map[string]any{"limit": 20}, "session-445", "call-445", 7)
				done <- outcome{result, err}
			}()
			var id string
			for id == "" && ctx.Err() == nil {
				rows, err := uc.List(ctx, "pending", 10)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) > 0 {
					if rows[0].SessionID != "session-445" || rows[0].ProposedBy != 7 {
						t.Fatalf("proposal lost session/user: %+v", rows[0])
					}
					id = rows[0].ID
				} else {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if id == "" {
				t.Fatal("proposal not created")
			}
			select {
			case got := <-done:
				t.Fatalf("returned before human decision: %+v", got)
			default:
			}
			if executions.Load() != 0 {
				t.Fatal("executed before confirmation")
			}
			want := `{"clusters":["test"]}`
			switch decision {
			case "reject":
				if err := uc.Reject(ctx, 7, id, "no"); err != nil {
					t.Fatal(err)
				}
				want = `"status":"rejected"`
			case "cancel":
				cancel()
				want = `"status":"cancelled"`
			default:
				if _, err := uc.Approve(ctx, 7, id); err != nil {
					t.Fatal(err)
				}
				if _, err := uc.Approve(ctx, 7, id); err == nil {
					t.Fatal("duplicate approval accepted")
				}
				if decision == "failure" {
					want = "remote failed"
				}
			}
			select {
			case got := <-done:
				if got.err != nil || !strings.Contains(got.result, want) {
					t.Fatalf("unexpected outcome: %+v; want %s", got, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("approval did not complete")
			}
			wantCalls := int32(1)
			if decision == "reject" || decision == "cancel" {
				wantCalls = 0
			}
			if executions.Load() != wantCalls {
				t.Fatalf("executions = %d, want %d", executions.Load(), wantCalls)
			}
		})
	}
}
