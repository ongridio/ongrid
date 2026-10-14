package imbridge

import (
	"context"
	"io"
	"log/slog"
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
