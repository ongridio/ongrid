package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSendNotificationTool_WhenNoNotificationChannels_ExplainsConfigurationPath(t *testing.T) {
	tool := NewSendNotificationTool(fakeNotificationSender{}, nil)
	_, err := tool.InvokableRun(context.Background(), `{"channel":"ops","text":"alert"}`)
	if err == nil {
		t.Fatal("expected missing-channel error")
	}
	if !strings.Contains(err.Error(), "Settings → Notifications") {
		t.Fatalf("missing-channel error = %q, want Settings → Notifications guidance", err)
	}
}

func TestSendNotificationTool_InfoUsesNotificationWireName(t *testing.T) {
	info, err := NewSendNotificationTool(fakeNotificationSender{}, nil).Info(context.Background())
	if err != nil {
		t.Fatalf("tool info: %v", err)
	}
	if info.Name != ToolNameSendNotification {
		t.Fatalf("tool name = %q, want %q", info.Name, ToolNameSendNotification)
	}
}

func TestSendIMMessageTool_WhenGroupIDMissing_ReturnsValidationError(t *testing.T) {
	tool := NewSendIMMessageTool(fakeIMMessageSender{}, nil)
	_, err := tool.InvokableRun(context.Background(), `{"im_app_id":1,"text":"hello"}`)
	if err == nil || !strings.Contains(err.Error(), "group_id") {
		t.Fatalf("error = %v, want group_id validation", err)
	}
}

func TestSendIMMessageTool_WhenSenderFails_WrapsError(t *testing.T) {
	want := errors.New("app disabled")
	tool := NewSendIMMessageTool(fakeIMMessageSender{err: want}, nil)
	_, err := tool.InvokableRun(context.Background(), `{"im_app_id":1,"group_id":"oc_123","text":"hello"}`)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}

type fakeNotificationSender struct{}

func (fakeNotificationSender) ListNotificationChannels(context.Context) ([]NotificationChannel, error) {
	return nil, nil
}
func (fakeNotificationSender) SendNotification(context.Context, uint64, string, string) error {
	return nil
}

type fakeIMMessageSender struct{ err error }

func (f fakeIMMessageSender) SendIMGroupMessage(context.Context, uint64, string, string) error {
	return f.err
}
