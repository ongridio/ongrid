package imbridge

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/agent"
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

	editor.OnEvent(agent.Event{Type: agent.EventAssistant, Assistant: &agent.AssistantEvent{Content: "partial"}})
	editor.OnEvent(agent.Event{Type: agent.EventAssistant, Assistant: &agent.AssistantEvent{Content: "final"}})
	editor.OnEvent(agent.Event{Type: agent.EventDone})
	if err := editor.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	if len(sender.messages) != 1 || sender.messages[0] != "final" {
		t.Fatalf("messages = %#v, want one final response", sender.messages)
	}
}
