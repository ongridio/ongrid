package store

import (
	"context"
	"strings"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

func TestContextCursorAndTurnIsolation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql"} {
		t.Run(dialect, func(t *testing.T) { testContextCursor(t, newReasoningTestRepo(t, dialect)) })
	}
}

func TestReadArchivedChatPassages(t *testing.T) {
	ctx := context.Background()
	for _, dialect := range []string{"sqlite", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			r := newReasoningTestRepo(t, dialect)
			s := &model.Session{UserID: 7, Title: "archived"}
			if err := r.CreateSession(ctx, s); err != nil {
				t.Fatal(err)
			}
			body := strings.Repeat("旧", 1800) + "机架例外 QT-82" + strings.Repeat("旧", 1000)
			now := time.Now().UTC().Truncate(time.Second)
			msg := &model.Message{SessionID: s.ID, Role: model.RoleUser, Content: &body, CreatedAt: now}
			bound := &model.Message{SessionID: s.ID, Role: model.RoleUser, Content: &body, CreatedAt: now.Add(time.Second)}
			for _, m := range []*model.Message{msg, bound} {
				if e := r.AppendMessage(ctx, m); e != nil {
					t.Fatal(e)
				}
			}
			if e := r.BeginContextTurn(ctx, s.ID, bound.ID); e != nil {
				t.Fatal(e)
			}
			ok, e := r.SaveContextCheckpoint(ctx, s.ID, bound.ID, 0, model.ContextCheckpoint{Summary: "earlier exchange", FromID: bound.ID, FromTime: bound.CreatedAt})
			if !ok || e != nil {
				t.Fatal(ok, e)
			}
			hits, e := r.ReadArchivedChat(ctx, s.ID, 7, "", "机架例外", 0)
			if e != nil || len(hits) != 1 || hits[0].ID != msg.ID || hits[0].Start != 1700 || hits[0].Next != 2600 || !strings.Contains(hits[0].Text, "QT-82") {
				t.Fatal(hits, e)
			}
			hits, e = r.ReadArchivedChat(ctx, s.ID, 7, msg.ID, "", 2600)
			if e != nil || len(hits) != 1 || hits[0].Next != 0 || hits[0].Start != 2600 {
				t.Fatal(hits, e)
			}
			if _, e = r.ReadArchivedChat(ctx, s.ID, 8, msg.ID, "", 0); e == nil {
				t.Fatal("wrong owner")
			}
			if hits, e = r.ReadArchivedChat(ctx, s.ID, 7, bound.ID, "", 0); e != nil || len(hits) != 0 {
				t.Fatal("retained message returned", e)
			}
		})
	}
}

func testContextCursor(t *testing.T, r *SessionRepo) {
	ctx := context.Background()
	s := &model.Session{UserID: 1, Title: "long chat", RuntimeVersion: "v3"}
	if err := r.CreateSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	// Cross both the former 50-message limit and a database page boundary.
	var first, boundary, last *model.Message
	start := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 270; i++ {
		m := &model.Message{SessionID: s.ID, Role: model.RoleUser, CreatedAt: start.Add(time.Duration(i) * time.Second)}
		if err := r.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = m
		}
		if i == 210 {
			boundary = m
		}
		last = m
	}
	full, e := r.ContextHistory(ctx, s, last)
	if e != nil || len(full) != 270 || full[0].ID != first.ID {
		t.Fatalf("full history %d: %v", len(full), e)
	}
	if e = r.BeginContextTurn(ctx, s.ID, last.ID); e != nil {
		t.Fatal(e)
	}
	if e = r.BeginContextTurn(ctx, s.ID, first.ID); e != nil {
		t.Fatal(e)
	}
	cp := model.ContextCheckpoint{FromTime: boundary.CreatedAt, FromID: boundary.ID, Summary: "confirmed evidence"}
	if ok, e := r.SaveContextCheckpoint(ctx, s.ID, first.ID, 0, cp); ok || e != nil {
		t.Fatalf("stale turn: %t %v", ok, e)
	}
	if ok, e := r.SaveContextCheckpoint(ctx, s.ID, last.ID, 0, cp); !ok || e != nil {
		t.Fatalf("checkpoint: %t %v", ok, e)
	}
	if ok, e := r.SaveContextCheckpoint(ctx, s.ID, last.ID, 0, cp); ok || e != nil {
		t.Fatalf("stale revision: %t %v", ok, e)
	}
	if e = r.SaveContextUsage(ctx, s.ID, last.ID, model.ContextUsage{Window: 256000, Reported: 918}); e != nil {
		t.Fatal(e)
	}
	restored, e := r.GetSession(ctx, s.ID)
	if e != nil || restored.ContextCheckpoint == nil || restored.ContextUsage == nil || restored.ContextUsage.Reported != 918 {
		t.Fatalf("restore: %+v %v", restored, e)
	}
	suffix, e := r.ContextHistory(ctx, restored, last)
	if e != nil || len(suffix) != 60 || suffix[0].ID != boundary.ID {
		t.Fatalf("suffix %d %v", len(suffix), e)
	}
	raw, e := r.ListMessages(ctx, s.ID, 0)
	if e != nil || len(raw) != 270 {
		t.Fatalf("transcript %d %v", len(raw), e)
	}
	late := &model.Message{Role: model.RoleAssistant, SessionID: s.ID, CreatedAt: first.CreatedAt.Add(time.Millisecond)}
	if e = r.AppendMessage(ctx, late); e != nil {
		t.Fatal(e)
	}
	restored, e = r.GetSession(ctx, s.ID)
	if e != nil || restored.ContextCheckpoint != nil || restored.ContextRevision != 2 {
		t.Fatalf("late evidence failed to invalidate checkpoint: %+v %v", restored, e)
	}
	if ok, e := r.SaveContextCheckpoint(ctx, s.ID, last.ID, 1, cp); ok || e != nil {
		t.Fatal("stale compactor overwrote a repair", e)
	}

}
