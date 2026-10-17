package store

import (
	"strings"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

func TestInterruptedWorkersRecoveryScopeAndIdempotence(t *testing.T) {
	repo := newTestRepo(t)
	ctx := t.Context()
	cutoff := time.Now().UTC()
	old := cutoff.Add(-time.Hour)
	for _, tc := range []struct {
		id, version, audience, initiator string
		future, closed                   bool
	}{
		{"worker-v2", "v2", "internal", "agent", false, false},
		{"worker-v3", "v3", "internal", "agent", false, false},
		{"worker-user", "v3", "user", "user", false, false},
		{"ordinary-internal", "v3", "internal", "agent", false, false},
		{"worker-new", "v3", "internal", "agent", true, false},
		{"worker-done", "v2", "internal", "agent", false, true},
	} {
		created := old
		if tc.future {
			created = cutoff.Add(time.Hour)
		}
		row := &model.Session{ID: tc.id, UserID: 42, RuntimeVersion: tc.version, Audience: tc.audience, Initiator: tc.initiator, AgentID: strp("test"), Kind: "flow", CreatedAt: created}
		if tc.closed {
			row.ClosedAt = &old
		}
		if err := repo.CreateSession(ctx, row); err != nil {
			t.Fatal(err)
		}
		m := &model.Message{SessionID: row.ID, Role: model.RoleAssistant, CreatedAt: created}
		if err := repo.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		for _, status := range []string{model.StatusPending, model.StatusSuccess} {
			call := &model.ToolCall{MessageID: m.ID, ToolName: status, ArgumentsJSON: "{}", Status: status, StartedAt: created, CreatedAt: created}
			if err := repo.CreateToolCall(ctx, call); err != nil {
				t.Fatal(err)
			}
		}
	}
	n, err := repo.ReconcileInterruptedWorkers(ctx, cutoff)
	if err != nil || n != 2 {
		t.Fatalf("recovered=%d err=%v", n, err)
	}
	for _, id := range []string{"worker-v2", "worker-v3", "worker-user", "ordinary-internal", "worker-new", "worker-done"} {
		row, err := repo.GetSession(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		recovered := id == "worker-v2" || id == "worker-v3"
		if (row.ClosedAt != nil) != (recovered || id == "worker-done") {
			t.Fatalf("unexpected closed state: %+v", row)
		}
		messages, err := repo.ListMessages(ctx, id, 0)
		if err != nil {
			t.Fatal(err)
		}
		audits := 0
		for _, m := range messages {
			if m.Role == model.RoleSystem && m.Content != nil && strings.Contains(*m.Content, "interrupted by manager restart") {
				audits++
			}
			for _, call := range m.ToolCalls {
				want := call.ToolName
				if recovered && want == model.StatusPending {
					want = model.StatusError
				}
				if call.Status != want {
					t.Fatalf("%s tool status=%s want=%s", id, call.Status, want)
				}
			}
		}
		if (audits == 1) != recovered {
			t.Fatalf("%s interruption audit count=%d", id, audits)
		}
		if id == "worker-v2" && row.RuntimeVersion != "v2" {
			t.Fatal("recovery changed runtime ownership")
		}
	}
	n, err = repo.ReconcileInterruptedWorkers(ctx, cutoff)
	if err != nil || n != 0 {
		t.Fatalf("second recovery=%d err=%v", n, err)
	}
}

func TestInterruptedWorkerRecoveryRollsBackOnAuditFailure(t *testing.T) {
	repo := newTestRepo(t)
	ctx := t.Context()
	old := time.Now().UTC().Add(-time.Hour)
	if err := repo.CreateSession(ctx, &model.Session{ID: "worker-rollback", UserID: 42, Audience: "internal", Initiator: "agent", AgentID: strp("test"), CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	m := &model.Message{SessionID: "worker-rollback", Role: model.RoleAssistant, CreatedAt: old}
	if err := repo.AppendMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	call := &model.ToolCall{MessageID: m.ID, ToolName: "read", ArgumentsJSON: "{}", Status: model.StatusPending, StartedAt: old, CreatedAt: old}
	if err := repo.CreateToolCall(ctx, call); err != nil {
		t.Fatal(err)
	}
	// 审计落库失败时，会话和工具更新必须一起回滚。
	if err := repo.db.Exec("CREATE TRIGGER reject_recovery_audit BEFORE INSERT ON chat_messages BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END").Error; err != nil {
		t.Fatal(err)
	}
	n, err := repo.ReconcileInterruptedWorkers(ctx, time.Now().UTC())
	if err == nil || n != 0 {
		t.Fatalf("recovery=%d err=%v", n, err)
	}
	row, err := repo.GetSession(ctx, "worker-rollback")
	if err != nil {
		t.Fatal(err)
	}
	if row.ClosedAt != nil {
		t.Fatal("recovery committed without audit")
	}
	messages, err := repo.ListMessages(ctx, "worker-rollback", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || len(messages[0].ToolCalls) != 1 || messages[0].ToolCalls[0].Status != model.StatusPending {
		t.Fatalf("tool update escaped rollback: %+v", messages)
	}
}
