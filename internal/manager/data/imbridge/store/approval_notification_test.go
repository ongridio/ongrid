package store

import (
	"context"
	approval "github.com/ongridio/ongrid/internal/manager/model/approval"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"sync"
	"testing"
	"time"
)

func TestApprovalDeliveryProjectionRecoversAndFreezesTarget(t *testing.T) {
	r := newIMBridgeTestRepo(t)
	ctx := context.Background()
	sqlDB, err := r.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := r.db.AutoMigrate(&approval.Approval{}); err != nil {
		t.Fatal(err)
	}
	target := &model.ApprovalTarget{SessionID: "session", AppID: 1, ThreadID: 2, ChatID: "original"}
	if err := r.SaveApprovalTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	for _, a := range []approval.Approval{
		{ID: "own", SessionID: "session", ProposedBy: 1, Status: "pending"},
		{ID: "foreign-owner", SessionID: "session", ProposedBy: 2, Status: "pending"},
		{ID: "foreign-session", SessionID: "other", ProposedBy: 1, Status: "pending"},
		{ID: "old", SessionID: "session", ProposedBy: 1, Status: "pending", CreatedAt: time.Now().Add(-time.Hour)},
	} {
		if err := r.db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}
	// A new repo instance represents restart with no in-memory approval notices.
	restarted := New(r.db)
	for i := 0; i < 2; i++ {
		if err := restarted.SyncApprovalNotifications(ctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	var total int64
	if err := r.db.Model(&model.ApprovalNotification{}).Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("unexpected projection count %d", total)
	}
	if err := r.SaveApprovalTarget(ctx, &model.ApprovalTarget{SessionID: "session", AppID: 9, ThreadID: 9, ChatID: "attacker"}); err == nil {
		t.Fatal("retarget accepted")
	}
	got, err := r.GetApprovalTarget(ctx, "session")
	if err != nil || got.ChatID != "original" {
		t.Fatalf("target overwritten: %+v %v", got, err)
	}
	// Competing workers must acquire one lease, not one each.
	var wg sync.WaitGroup
	var mu sync.Mutex
	var claimed []model.ApprovalNotification
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jobs, err := r.ClaimApprovalNotifications(ctx, time.Now().UTC())
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			claimed = append(claimed, jobs...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(claimed) != 1 {
		t.Fatalf("claimed %d times", len(claimed))
	}
	n := claimed[0]
	old := n
	n.MessageID = "message-1"
	n.DeliveredStatus = "pending"
	n.NextAt = time.Now().Add(-time.Second)
	if err := r.FinishApprovalNotification(ctx, n); err != nil {
		t.Fatal(err)
	}
	jobs, err := restarted.ClaimApprovalNotifications(ctx, time.Now().UTC())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("reclaim %v %v", jobs, err)
	}
	if jobs[0].MessageID != "message-1" {
		t.Fatal("lost message reference")
	}
	if err := r.FinishApprovalNotification(ctx, old); err == nil {
		t.Fatal("stale lease overwrote new owner")
	}
	jobs[0].Done = true
	jobs[0].DeliveredStatus = "executed"
	if err := r.FinishApprovalNotification(ctx, jobs[0]); err != nil {
		t.Fatal(err)
	}
	next, err := r.ClaimApprovalNotifications(ctx, time.Now().Add(time.Hour))
	if err != nil || len(next) != 0 {
		t.Fatal("completed notification reclaimed")
	}
}

func TestApprovalDeliveryLeaseExpiryAndRetention(t *testing.T) {
	r := newIMBridgeTestRepo(t)
	ctx := context.Background()
	db, err := r.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC()
	n := model.ApprovalNotification{ApprovalID: "lease", SessionID: "s", NextAt: now.Add(-time.Minute)}
	if err := r.db.Create(&n).Error; err != nil {
		t.Fatal(err)
	}
	first, err := r.ClaimApprovalNotifications(ctx, now)
	if err != nil || len(first) != 1 {
		t.Fatalf("first %v %v", first, err)
	}
	second, err := r.ClaimApprovalNotifications(ctx, now.Add(3*time.Minute))
	if err != nil || len(second) != 1 {
		t.Fatalf("reclaimed %v %v", second, err)
	}
	if err := r.FinishApprovalNotification(ctx, first[0]); err == nil {
		t.Fatal("expired owner overwrote reclaimed job")
	}
	old := now.Add(-31 * 24 * time.Hour)
	if err := r.db.Model(&model.ApprovalNotification{}).Where("approval_id = ?", "lease").Updates(map[string]any{"done": true, "message_id": "old-message", "updated_at": old}).Error; err != nil {
		t.Fatal(err)
	}
	expired := now.Add(-time.Minute)
	if err := r.SaveApprovalTarget(ctx, &model.ApprovalTarget{SessionID: "s", SecretContext: "ciphertext-test-fixture", ExpiresAt: &expired}); err != nil {
		t.Fatal(err)
	}
	if err := r.CompactApprovalNotifications(ctx, now); err != nil {
		t.Fatal(err)
	}
	target, err := r.GetApprovalTarget(ctx, "s")
	if err != nil || target.SecretContext != "" {
		t.Fatal("expired secret retained")
	}
	var got model.ApprovalNotification
	if err := r.db.First(&got, "approval_id = ?", "lease").Error; err != nil {
		t.Fatal(err)
	}
	if !got.Done || got.MessageID != "" {
		t.Fatal("terminal tombstone not compacted")
	}
}
