package tools

import (
	"context"
	"strings"
	"testing"
)

func TestSendIMMessageTool_WhenNoNotificationChannels_ExplainsConfigurationPath(t *testing.T) {
	tool := NewSendIMMessageTool(fakeIMSender{}, nil)
	_, err := tool.InvokableRun(context.Background(), `{"channel":"ops","text":"alert"}`)
	if err == nil {
		t.Fatal("expected missing-channel error")
	}
	if !strings.Contains(err.Error(), "Settings → Notifications") {
		t.Fatalf("missing-channel error = %q, want Settings → Notifications guidance", err)
	}
}

type fakeIMSender struct{}

func (fakeIMSender) ListIMChannels(context.Context) ([]IMChannel, error)  { return nil, nil }
func (fakeIMSender) SendIM(context.Context, uint64, string, string) error { return nil }
