package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ongridio/ongrid/internal/manager/biz/alert/investigator"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func addEvidenceSession(t *testing.T, r *SessionRepo, id, parent string, owner uint64) {
	t.Helper()
	s := &model.Session{ID: id, UserID: owner, Title: id}
	if parent != "" {
		s.ParentSessionID = &parent
	}
	if err := r.CreateSession(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func addEvidenceCall(t *testing.T, r *SessionRepo, session, id, result string, at time.Time) {
	t.Helper()
	m := &model.Message{ID: "message-" + id, SessionID: session, Role: model.RoleAssistant, CreatedAt: at}
	if err := r.AppendMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	llmID := "reused-provider-call-id"
	if err := r.CreateToolCall(context.Background(), &model.ToolCall{ID: id, MessageID: m.ID, ToolName: "query_logql", ArgumentsJSON: `{}`,
		ResultJSON: &result, Status: model.StatusSuccess, LLMCallID: &llmID, StartedAt: at, EndedAt: &at, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
}

func TestInvestigationEvidenceUsesDelegationEdgesAndDatabaseCallIDs(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	addEvidenceSession(t, r, "root", "", 7)
	addEvidenceSession(t, r, "child", "root", 7)
	addEvidenceSession(t, r, "grandchild", "child", 7)
	addEvidenceSession(t, r, "foreign", "", 7)
	if err := r.db.Model(&model.Session{}).Where("id = ?", "foreign").Update("root_session_id", "root").Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"root", "child", "grandchild", "foreign"} {
		addEvidenceCall(t, r, id, "call-"+id, `{"records":[]}`, now)
	}
	addEvidenceCall(t, r, "root", "future", `{}`, now.Add(time.Hour))
	rows, err := r.ListInvestigationCalls(ctx, "root", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Calls) != 3 || rows.Incomplete || rows.OwnerID != 7 {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	for _, c := range rows.Calls {
		if c.SessionID == "foreign" || c.ID == "future" {
			t.Fatal("cross-investigation or future call included")
		}
	}
	got, owner, err := r.GetInvestigationCall(ctx, "root", "call-grandchild", now.Add(time.Second))
	if err != nil || got.SessionID != "grandchild" || owner != 7 {
		t.Fatalf("delegated source: %+v %d %v", got, owner, err)
	}
	for _, id := range []string{"call-foreign", "future", "reused-provider-call-id"} {
		if _, _, err := r.GetInvestigationCall(ctx, "root", id, now.Add(time.Second)); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("accepted foreign/ambiguous ID %s: %v", id, err)
		}
	}
	if err := r.DeleteSession(ctx, "grandchild"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.GetInvestigationCall(ctx, "root", "call-grandchild", now.Add(time.Second)); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("deleted source remained readable: %v", err)
	}
}

func TestInvestigationEvidenceBoundsRowsAndUnicodePayloads(t *testing.T) {
	r := newTestRepo(t)
	now := time.Now().UTC()
	addEvidenceSession(t, r, "root", "", 7)
	for i := 0; i < investigator.MaxEvidenceCalls+1; i++ {
		addEvidenceCall(t, r, "root", fmt.Sprintf("call-%03d", i), `{}`, now)
	}
	large := `{"password":"dummy-sensitive-value","records":["` + strings.Repeat("中", investigator.MaxEvidenceResultBytes) + `"]}`
	if err := r.db.Model(&model.ToolCall{}).Where("id = ?", "call-000").Updates(map[string]any{
		"result_json": large, "arguments_json": large, "error": large,
	}).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := r.ListInvestigationCalls(context.Background(), "root", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Incomplete || len(rows.Calls) != investigator.MaxEvidenceCalls {
		t.Fatalf("row cap: %d %v", len(rows.Calls), rows.Incomplete)
	}
	first := rows.Calls[0]
	if !first.Truncated || first.ResultBytes != len(large) || len(*first.ResultJSON) > investigator.MaxEvidenceResultBytes || !utf8.ValidString(*first.ResultJSON) {
		t.Fatalf("payload cap failed: bytes=%d original=%d truncated=%v", len(*first.ResultJSON), first.ResultBytes, first.Truncated)
	}
	detail, _, err := r.GetInvestigationCall(context.Background(), "root", "call-000", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []investigator.EvidenceCall{first, detail} {
		if call.Error == nil || !call.Truncated || strings.Contains(call.ArgumentsJSON+*call.ResultJSON+*call.Error, "dummy-sensitive-value") {
			t.Fatal("SQL reader returned oversized field content instead of an omission marker")
		}
	}
}

func TestInvestigationEvidenceRejectsOwnerMismatch(t *testing.T) {
	r := newTestRepo(t)
	now := time.Now().UTC()
	addEvidenceSession(t, r, "root", "", 7)
	addEvidenceSession(t, r, "other-owner", "root", 8)
	addEvidenceCall(t, r, "other-owner", "foreign", `{}`, now)
	rows, err := r.ListInvestigationCalls(context.Background(), "root", now.Add(time.Second))
	if err != nil || !rows.Incomplete || len(rows.Calls) != 0 {
		t.Fatalf("owner mismatch: %+v %v", rows, err)
	}
	if _, _, err := r.GetInvestigationCall(context.Background(), "root", "foreign", now.Add(time.Second)); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("cross-owner call exposed: %v", err)
	}
}
