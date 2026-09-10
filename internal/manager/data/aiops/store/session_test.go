package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	biz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// newTestRepo opens an in-memory SQLite DB and applies this package's Migrate.
// No users table is created — the old hand-written SQL migration declared a FK
// chat_sessions.user_id -> users.id, but the gorm-tagged model does not carry
// that FK reference (arch-lint forbids manager -> iam, so we can't share the
// model anyway). UserID is therefore treated as a loose audit column at the
// schema level; uniqueness / existence is enforced by the biz layer.
func newTestRepo(t *testing.T) *SessionRepo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open sqlite :memory:: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewSessionRepoWithAttachmentRoot(db, t.TempDir())
}

func TestAttachmentUploadBindHydrateAndDelete(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s := &model.Session{UserID: 7, Title: "images"}
	if err := repo.CreateSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	data := []byte("private-image-bytes")
	a, err := repo.CreateAttachment(ctx, s.ID, 7, "chart.png", "image/png", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetAttachment(ctx, s.ID, a.ID, 8, false); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("cross-user read error = %v, want not found", err)
	}
	content := "inspect"
	msg := &model.Message{SessionID: s.ID, Role: model.RoleUser, Content: &content, AttachmentIDs: []string{a.ID}}
	if err := repo.AppendMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListMessages(ctx, s.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Attachments) != 1 || !bytes.Equal(rows[0].Attachments[0].Data, data) {
		t.Fatalf("hydrated attachments = %+v", rows)
	}
	path := rows[0].Attachments[0].StoragePath
	if err := repo.DeleteSession(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attachment file still exists: %v", err)
	}
}

func TestSessionRepoRoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	s := &model.Session{
		UserID:    1,
		Title:     "hello",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := repo.CreateSession(ctx, s); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if s.ID == "" {
		t.Fatal("Session id not populated")
	}

	got, err := repo.GetSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Title != "hello" || got.UserID != 1 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.RootSessionID == nil || *got.RootSessionID != s.ID {
		t.Errorf("RootSessionID = %v, want %q", got.RootSessionID, s.ID)
	}
	if got.OwnerAgentID == nil || *got.OwnerAgentID != model.DefaultSessionOwnerAgent {
		t.Errorf("OwnerAgentID = %v, want %q", got.OwnerAgentID, model.DefaultSessionOwnerAgent)
	}
	if got.Initiator != model.SessionInitiatorUser || got.Audience != model.SessionAudienceUser {
		t.Errorf("session defaults = initiator=%q audience=%q", got.Initiator, got.Audience)
	}

	list, err := repo.ListSessions(ctx, 1, 10, 0, nil)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 || list[0].ID != s.ID {
		t.Errorf("ListSessions = %+v", list)
	}

	// Close session
	if err := repo.CloseSession(ctx, s.ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	got2, _ := repo.GetSession(ctx, s.ID)
	if got2.ClosedAt == nil {
		t.Errorf("ClosedAt not set after CloseSession")
	}

	// CloseSession missing id
	missing := "00000000-0000-0000-0000-000000000000"
	if err := repo.CloseSession(ctx, missing); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("CloseSession unknown: want ErrNotFound, got %v", err)
	}

	// GetSession missing
	if _, err := repo.GetSession(ctx, missing); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("GetSession unknown: want ErrNotFound, got %v", err)
	}
}

func TestMessageAndToolCallLifecycle(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	s := &model.Session{UserID: 1, Title: "t", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := repo.CreateSession(ctx, s); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	content := "what's happening"
	messageTime := time.Now().UTC()
	userMsg := &model.Message{
		SessionID: s.ID, Role: model.RoleUser, Content: &content, CreatedAt: messageTime,
	}
	if err := repo.AppendMessage(ctx, userMsg); err != nil {
		t.Fatalf("AppendMessage user: %v", err)
	}

	asstMsg := &model.Message{SessionID: s.ID, Role: model.RoleAssistant, Content: nil, CreatedAt: messageTime.Add(time.Second)}
	if err := repo.AppendMessage(ctx, asstMsg); err != nil {
		t.Fatalf("AppendMessage assistant: %v", err)
	}

	tc := &model.ToolCall{
		MessageID:     asstMsg.ID,
		ToolName:      "get_host_load",
		ArgumentsJSON: `{"edge_name":"n"}`,
		Status:        model.StatusPending,
		StartedAt:     time.Now().UTC(),
		CreatedAt:     time.Now().UTC(),
	}
	if err := repo.CreateToolCall(ctx, tc); err != nil {
		t.Fatalf("CreateToolCall: %v", err)
	}

	result := `{"cpu_pct":12.3}`
	endedAt := time.Now().UTC()
	if err := repo.UpdateToolCallResult(ctx, tc.ID, model.StatusSuccess, &result, nil, endedAt); err != nil {
		t.Fatalf("UpdateToolCallResult: %v", err)
	}

	msgs, err := repo.ListMessages(ctx, s.ID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != model.RoleUser || msgs[1].Role != model.RoleAssistant {
		t.Errorf("ordering wrong: %v %v", msgs[0].Role, msgs[1].Role)
	}

	// Limit
	limited, err := repo.ListMessages(ctx, s.ID, 1)
	if err != nil {
		t.Fatalf("ListMessages limit: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("ListMessages limit=1 returned %d", len(limited))
	} else if limited[0].ID != asstMsg.ID {
		t.Errorf("ListMessages limit=1 returned %s, want latest assistant message %s", limited[0].ID, asstMsg.ID)
	}

	// Unknown tool call update.
	missing := "00000000-0000-0000-0000-000000000000"
	if err := repo.UpdateToolCallResult(ctx, missing, model.StatusError, nil, strp("x"), endedAt); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("UpdateToolCallResult unknown: want ErrNotFound, got %v", err)
	}

	// Interface compliance: the concrete *SessionRepo must satisfy biz.SessionRepo.
	var _ biz.SessionRepo = repo
}

func strp(s string) *string { return &s }

// TestListByParent covers the worker session lookup path used by
// audit + the SPA worker-tree view. Asserts:
//   - parentless rows are excluded
//   - rows with matching parent_session_id come back ordered chronologically
//   - parent_session_id with no children returns an empty (non-nil) slice
//   - empty parentID short-circuits to empty slice
func TestListByParent(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	// Seed: 1 coordinator + 2 workers under it + 1 standalone session.
	parent := &model.Session{
		ID: "parent-1", UserID: 42, Title: "coordinator",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.CreateSession(ctx, parent); err != nil {
		t.Fatalf("CreateSession parent: %v", err)
	}
	agentName := "incident-investigator"
	parentID := parent.ID
	worker1 := &model.Session{
		ID: "worker-aaaa1111", UserID: 42, Title: "Worker: incident-investigator",
		AgentID: &agentName, ParentSessionID: &parentID, Background: false,
		CreatedAt: time.Now().UTC().Add(time.Second), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.CreateSession(ctx, worker1); err != nil {
		t.Fatalf("CreateSession worker1: %v", err)
	}
	worker2 := &model.Session{
		ID: "worker-bbbb2222", UserID: 42, Title: "Worker: general-purpose",
		AgentID: strp("general-purpose"), ParentSessionID: &parentID, Background: true,
		CreatedAt: time.Now().UTC().Add(2 * time.Second), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.CreateSession(ctx, worker2); err != nil {
		t.Fatalf("CreateSession worker2: %v", err)
	}
	standalone := &model.Session{
		ID: "standalone-1", UserID: 42, Title: "no parent",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.CreateSession(ctx, standalone); err != nil {
		t.Fatalf("CreateSession standalone: %v", err)
	}

	// Workers under the coordinator come back in chronological order.
	kids, err := repo.ListByParent(ctx, parentID)
	if err != nil {
		t.Fatalf("ListByParent: %v", err)
	}
	if len(kids) != 2 {
		t.Fatalf("ListByParent returned %d rows, want 2", len(kids))
	}
	if kids[0].ID != worker1.ID || kids[1].ID != worker2.ID {
		t.Errorf("order = [%s, %s], want [%s, %s]",
			kids[0].ID, kids[1].ID, worker1.ID, worker2.ID)
	}
	if kids[0].AgentID == nil || *kids[0].AgentID != "incident-investigator" {
		t.Errorf("kids[0].AgentID = %v", kids[0].AgentID)
	}
	if !kids[1].Background {
		t.Errorf("kids[1].Background = false, want true")
	}

	// Unknown parent returns an empty (non-nil) slice.
	none, err := repo.ListByParent(ctx, "no-such-parent")
	if err != nil {
		t.Fatalf("ListByParent unknown: %v", err)
	}
	if none == nil || len(none) != 0 {
		t.Errorf("unknown parent: want empty non-nil slice, got %v", none)
	}

	// Empty parentID short-circuits — never matches the standalone row
	// (whose ParentSessionID is nil, not empty string).
	empty, err := repo.ListByParent(ctx, "")
	if err != nil {
		t.Fatalf("ListByParent empty: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("empty parentID: want empty non-nil slice, got %v", empty)
	}
}

func TestListSessionsHidesInternalWork(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	userSession := &model.Session{UserID: 42, Title: "user", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	internalSession := &model.Session{
		UserID: 42, Title: "worker", Kind: model.SessionKindWork,
		Initiator: model.SessionInitiatorAgent, Audience: model.SessionAudienceInternal,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.CreateSession(ctx, userSession); err != nil {
		t.Fatalf("CreateSession user: %v", err)
	}
	if err := repo.CreateSession(ctx, internalSession); err != nil {
		t.Fatalf("CreateSession internal: %v", err)
	}
	list, err := repo.ListSessions(ctx, 42, 10, 0, nil)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 || list[0].ID != userSession.ID {
		t.Errorf("ListSessions = %+v, want only user session %q", list, userSession.ID)
	}
}
