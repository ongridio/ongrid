package imbridge

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatprotocol"
)

type oneShotSender struct {
	messages []string
}

func (s *oneShotSender) SendText(_ context.Context, _, _, text string) (string, error) {
	s.messages = append(s.messages, text)
	return "sent", nil
}

func TestStreamEditor_WhenSenderCannotEdit_SendsFinalMessageOnce(t *testing.T) {
	sender := &oneShotSender{}
	editor := newStreamEditor(context.Background(), sender, "chat", "chat_id", "", "en",
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	editor.OnEvent(chatprotocol.Event{Type: chatprotocol.EventAssistant, Assistant: &chatprotocol.AssistantEvent{Content: "partial"}})
	editor.OnEvent(chatprotocol.Event{Type: chatprotocol.EventAssistant, Assistant: &chatprotocol.AssistantEvent{Content: "final"}})
	editor.OnEvent(chatprotocol.Event{Type: chatprotocol.EventDone})
	if err := editor.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	if len(sender.messages) != 1 || sender.messages[0] != "final" {
		t.Fatalf("messages = %#v, want one final response", sender.messages)
	}
}

type pagedSender struct {
	messages []string
	edits    map[string]string
	failAt   int
}

func (s *pagedSender) SplitText(text string) []string { return strings.Split(text, "|") }
func (s *pagedSender) SendText(_ context.Context, _, _, text string) (string, error) {
	if s.failAt > 0 && len(s.messages)+1 == s.failAt {
		s.failAt = 0
		return "", fmt.Errorf("temporary failure")
	}
	s.messages = append(s.messages, text)
	return strconv.Itoa(len(s.messages)), nil
}
func (s *pagedSender) EditText(_ context.Context, id, text string) error {
	s.edits[id] = text
	return nil
}
func TestPagedSenderRetriesWithoutDuplicatingSuccessfulPages(t *testing.T) {
	s := &pagedSender{edits: map[string]string{}, failAt: 2}
	e := newStreamEditor(context.Background(), s, "chat", "", "", "en", slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.buf = "first|second|third"
	if err := e.Flush(); err == nil {
		t.Fatal("expected partial failure")
	}
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(s.messages) != 3 {
		t.Fatalf("duplicate pages: %#v", s.messages)
	}
	e.buf = "updated"
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	if s.edits["1"] != "updated" || s.edits["2"] == "" || s.edits["3"] == "" {
		t.Fatalf("stale pages: %#v", s.edits)
	}
}

type pagedOneShot struct{ oneShotSender }

func (s *pagedOneShot) SplitText(text string) []string { return strings.Split(text, "|") }
func TestPagedOneShotOnlySendsOnce(t *testing.T) {
	s := &pagedOneShot{}
	e := newStreamEditor(context.Background(), s, "chat", "", "", "en", slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.buf = "one|two|three"
	for i := 0; i < 2; i++ {
		if err := e.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.messages) != 3 {
		t.Fatalf("messages: %#v", s.messages)
	}
}
