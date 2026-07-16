package dingtalk

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/client"

	bizbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// StreamClient receives DingTalk chatbot callbacks over the official
// outbound Stream connection. No public webhook endpoint is required.
type StreamClient struct {
	app    *model.ImApp
	bridge *bizbridge.Bridge
	log    *slog.Logger
}

func NewStreamClient(app *model.ImApp, bridge *bizbridge.Bridge, log *slog.Logger) *StreamClient {
	if log == nil {
		log = slog.Default()
	}
	return &StreamClient{
		app:    app,
		bridge: bridge,
		log:    log.With(slog.String("provider", model.ProviderDingTalk), slog.Uint64("im_app_id", app.ID)),
	}
}

func (c *StreamClient) ProviderName() string { return model.ProviderDingTalk }

// Run starts the SDK connection and blocks until the supervisor cancels it.
// The SDK owns transient reconnects; disabling reconnect before Close keeps a
// credential rotation from leaving an old client alive in the background.
func (c *StreamClient) Run(ctx context.Context) error {
	stream := client.NewStreamClient(client.WithAppCredential(
		client.NewAppCredentialConfig(c.app.AppID, c.app.AppSecret),
	))
	stream.RegisterChatBotCallbackRouter(c.onMessage)
	c.log.Info("starting dingtalk stream connection")
	if err := stream.Start(ctx); err != nil {
		return fmt.Errorf("start dingtalk stream: %w", err)
	}
	<-ctx.Done()
	stream.AutoReconnect = false
	stream.Close()
	return ctx.Err()
}

func (c *StreamClient) onMessage(_ context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
	in, ok := inboundFromCallback(c.app.AppID, data)
	if !ok {
		return nil, nil
	}
	sender := senderAdapter{
		webhook: data.SessionWebhook,
		replier: chatbot.NewChatbotReplier(),
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				c.log.Error("dingtalk bridge panic recovered",
					slog.Any("panic", recovered),
					slog.String("stack", string(debug.Stack())))
			}
		}()
		if err := c.bridge.HandleInbound(context.Background(), sender, in); err != nil {
			c.log.Warn("dingtalk bridge handle_inbound failed", slog.Any("err", err))
		}
	}()
	return nil, nil
}

func inboundFromCallback(appID string, data *chatbot.BotCallbackDataModel) (bizbridge.InboundMessage, bool) {
	if data == nil || data.Msgtype != "text" || strings.TrimSpace(data.Text.Content) == "" ||
		data.ConversationId == "" || data.SessionWebhook == "" {
		return bizbridge.InboundMessage{}, false
	}
	openID := data.SenderStaffId
	if openID == "" {
		openID = data.SenderId
	}
	return bizbridge.InboundMessage{
		Provider:      model.ProviderDingTalk,
		AppID:         appID,
		ChatID:        data.ConversationId,
		OpenID:        openID,
		UserName:      data.SenderNick,
		Text:          strings.TrimSpace(data.Text.Content),
		EventID:       data.MsgId,
		ReceiveIDType: "conversation_id",
	}, true
}

type markdownReplier interface {
	SimpleReplyMarkdown(ctx context.Context, sessionWebhook string, title, content []byte) error
}

// senderAdapter intentionally implements only bizbridge.Sender. DingTalk's
// per-message session webhook cannot edit a prior message, so the bridge
// buffers streamed output and invokes one native Markdown reply with the final answer.
type senderAdapter struct {
	webhook string
	replier markdownReplier
}

func (s senderAdapter) SendText(ctx context.Context, _, _ string, text string) (string, error) {
	if s.webhook == "" {
		return "", fmt.Errorf("dingtalk: session webhook required")
	}
	if err := s.replier.SimpleReplyMarkdown(ctx, s.webhook, []byte("Ongrid"), []byte(text)); err != nil {
		return "", fmt.Errorf("dingtalk reply markdown: %w", err)
	}
	return "sent", nil
}

func NewStreamFactory(log *slog.Logger) bizbridge.StreamClientFactory {
	return func(app *model.ImApp, bridge *bizbridge.Bridge) (bizbridge.StreamClient, error) {
		if app.AppID == "" || app.AppSecret == "" {
			return nil, fmt.Errorf("dingtalk: client id and client secret required")
		}
		return NewStreamClient(app, bridge, log), nil
	}
}
