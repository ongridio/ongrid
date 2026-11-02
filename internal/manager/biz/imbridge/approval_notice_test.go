package imbridge

import (
	"context"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatprotocol"
	"io"
	"log/slog"
	"strings"
	"testing"
)

type noticeStatus struct{ value string }

func (s *noticeStatus) ApprovalStatus(context.Context, string, string) (string, error) {
	return s.value, nil
}

func TestApprovalNoticeIsSeparateDeduplicatedAndUpdated(t *testing.T) {
	sender := &pagedSender{edits: map[string]string{}}
	e := newStreamEditor(context.Background(), sender, "chat", "", "", "en", slog.New(slog.NewTextHandler(io.Discard, nil)))
	state := &noticeStatus{value: "pending"}
	e.approvalReader = state
	event := chatprotocol.Event{Type: chatprotocol.EventApprovalPending, Approval: &chatprotocol.ApprovalPendingEvent{ApprovalID: "approval-1", ToolName: "host_bash", Command: "secret-command", Credentials: []string{"secret-ref"}}}
	e.OnEvent(event)
	e.OnEvent(event)
	if len(sender.messages) != 1 {
		t.Fatalf("duplicate notices: %#v", sender.messages)
	}
	if strings.Contains(sender.messages[0], "secret-") || !strings.Contains(sender.messages[0], "administrator") {
		t.Fatal("unsafe or missing review guidance")
	}
	e.buf = "assistant final"
	state.value = "rejected"
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.edits["1"], "Rejected") {
		t.Fatalf("notice not updated: %#v", sender.edits)
	}
	if len(sender.messages) != 2 || sender.messages[1] != "assistant final" {
		t.Fatalf("notice overwrote answer: %#v", sender.messages)
	}
}

func TestApprovalNoticeOneShotSendsStatusOnce(t *testing.T) {
	sender := &oneShotSender{}
	e := newStreamEditor(context.Background(), sender, "chat", "", "", "zh", slog.New(slog.NewTextHandler(io.Discard, nil)))
	state := &noticeStatus{value: "executed"}
	e.approvalReader = state
	if err := e.notifyApproval(&chatprotocol.ApprovalPendingEvent{ApprovalID: "a"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := e.refreshApprovals(); err != nil {
			t.Fatal(err)
		}
	}
	if len(sender.messages) != 2 || !strings.Contains(sender.messages[1], "已执行") {
		t.Fatalf("messages: %#v", sender.messages)
	}
}

func TestApprovalPublicURLRejectsUntrustedOrigins(t *testing.T) {
	for _, input := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/path", "https://example.com?approve=yes", "https://example.com/#x", "//example.com", "https://example.com/evil)"} {
		if _, err := ApprovalPublicURL(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	got, err := ApprovalPublicURL("https://ongrid.example/")
	if err != nil || got != "https://ongrid.example" {
		t.Fatalf("url=%q err=%v", got, err)
	}
	if got, err := ApprovalPublicURL(""); err != nil || got != "" {
		t.Fatal("empty config must preserve old notices")
	}
}

func TestApprovalNoticeLinkAndStatusAreSafe(t *testing.T) {
	e := &streamEditor{publicURL: "https://ongrid.example", locale: "en"}
	body := e.approvalText(approvalNotice{id: "approval-1", tool: "host_bash", status: "approved"})
	if !strings.Contains(body, "https://ongrid.example/approvals/approval-1") || !strings.Contains(body, "awaiting execution result") || strings.Contains(body, "in progress") {
		t.Fatal(body)
	}
	body = e.approvalText(approvalNotice{id: "a](https://evil.invalid)", tool: "x\n[click](https://evil.invalid)", status: "pending"})
	if strings.Contains(body, "evil.invalid") || strings.Contains(body, "/approvals/") {
		t.Fatal(body)
	}
}
