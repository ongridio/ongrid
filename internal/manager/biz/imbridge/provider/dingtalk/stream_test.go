package dingtalk

import (
	"context"
	"testing"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"

	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

type fakeReplier struct {
	webhook string
	title   string
	text    string
}

func (f *fakeReplier) SimpleReplyMarkdown(_ context.Context, webhook string, title, content []byte) error {
	f.webhook = webhook
	f.title = string(title)
	f.text = string(content)
	return nil
}

func TestInboundFromCallback_WhenTextMessage_NormalizesFields(t *testing.T) {
	data := &chatbot.BotCallbackDataModel{
		ConversationId: "cid-1",
		MsgId:          "msg-1",
		SenderNick:     "Alice",
		SenderStaffId:  "user-1",
		SessionWebhook: "https://example.invalid/session",
		Msgtype:        "text",
		Text:           chatbot.BotCallbackDataTextModel{Content: "  check api  "},
	}

	got, ok := inboundFromCallback("app-1", data)
	if !ok {
		t.Fatal("text callback was rejected")
	}
	if got.Provider != model.ProviderDingTalk || got.AppID != "app-1" || got.ChatID != "cid-1" {
		t.Fatalf("unexpected routing fields: %+v", got)
	}
	if got.Text != "check api" || got.EventID != "msg-1" || got.OpenID != "user-1" {
		t.Fatalf("unexpected message fields: %+v", got)
	}
}

func TestInboundFromCallback_WhenUnsupportedOrIncomplete_Rejects(t *testing.T) {
	cases := []struct {
		name string
		data *chatbot.BotCallbackDataModel
	}{
		{name: "nil", data: nil},
		{name: "non text", data: &chatbot.BotCallbackDataModel{Msgtype: "picture"}},
		{name: "missing webhook", data: &chatbot.BotCallbackDataModel{Msgtype: "text", ConversationId: "cid", Text: chatbot.BotCallbackDataTextModel{Content: "hello"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := inboundFromCallback("app", tc.data); ok {
				t.Fatal("callback should have been rejected")
			}
		})
	}
}

func TestSenderAdapter_SendText_UsesSessionWebhook(t *testing.T) {
	replier := &fakeReplier{}
	sender := senderAdapter{webhook: "https://example.invalid/session", replier: replier}

	messageID, err := sender.SendText(context.Background(), "ignored", "ignored", "final answer")
	if err != nil {
		t.Fatalf("SendText() error = %v", err)
	}
	if messageID == "" || replier.webhook != "https://example.invalid/session" || replier.title != "Ongrid" || replier.text != "final answer" {
		t.Fatalf("unexpected reply: message_id=%q webhook=%q text=%q", messageID, replier.webhook, replier.text)
	}
}

func TestNewStreamFactory_WhenCredentialsMissing_ReturnsError(t *testing.T) {
	if _, err := NewStreamFactory(nil)(&model.ImApp{}, nil); err == nil {
		t.Fatal("expected missing credentials error")
	}
}
