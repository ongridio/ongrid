package dingtalk

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/client"

	bizbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	"github.com/ongridio/ongrid/internal/manager/biz/imbridge/imformat"
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

// Probe validates the app credentials and obtains a stream endpoint without dialing it.
func Probe(ctx context.Context, appID, appSecret string, opts ...client.ClientOption) error {
	opts = append([]client.ClientOption{client.WithAppCredential(
		client.NewAppCredentialConfig(appID, appSecret),
	)}, opts...)
	if _, err := client.NewStreamClient(opts...).GetConnectionEndpoint(ctx); err != nil {
		return fmt.Errorf("dingtalk credentials: %w", err)
	}
	return nil
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
	stream.RegisterCardCallbackRouter(c.onCard)
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
	space, callbackChat := "", data.ConversationId
	if data.ConversationType == "2" {
		space = "dtv1.card//IM_GROUP." + data.ConversationId
	} else if data.SenderStaffId != "" {
		space = "dtv1.card//IM_ROBOT." + data.SenderStaffId
		callbackChat = data.SenderStaffId
	}
	in.ConnectionIdentity = bizbridge.ConnectionIdentity(c.app)
	sender := senderAdapter{
		deliverySpace: space, callbackChat: callbackChat,
		webhook:   data.SessionWebhook,
		expiresAt: time.UnixMilli(data.SessionWebhookExpiredTime),
		replier:   chatbot.NewChatbotReplier(),
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
	scope := data.SenderCorpId
	if scope == "" {
		scope = data.ChatbotCorpId
	}
	return bizbridge.InboundMessage{
		Subject:       bizbridge.Subject{Scope: scope, User: data.SenderStaffId},
		Private:       data.ConversationType == "1",
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
	deliverySpace, callbackChat string
	webhook                     string
	expiresAt                   time.Time
	replier                     markdownReplier
}

func (s senderAdapter) SendText(ctx context.Context, _, _ string, text string) (string, error) {
	if s.webhook == "" {
		return "", fmt.Errorf("dingtalk: session webhook required")
	}
	if err := s.replier.SimpleReplyMarkdown(ctx, s.webhook, []byte("Ongrid"), []byte(imformat.DingTalk(text))); err != nil {
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

func (s senderAdapter) SplitText(text string) []string {
	return imformat.MarkdownPages(text, func(page string) bool { return len(imformat.DingTalk(page)) <= 16000 })
}

func (s senderAdapter) ApprovalReplyContext() (string, time.Time) { return s.webhook, s.expiresAt }

func NewApprovalSender(app *model.ImApp, target *model.ApprovalTarget, webhook string) (bizbridge.Sender, error) {
	if app.ApprovalTemplateID != "" && target.DeliverySpace != "" {
		return &approvalSender{app: app, target: target, http: &http.Client{}, base: "https://api.dingtalk.com"}, nil
	}
	if webhook == "" || target.ExpiresAt == nil || !target.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("dingtalk reply context expired")
	}
	return senderAdapter{webhook: webhook, expiresAt: *target.ExpiresAt, replier: chatbot.NewChatbotReplier()}, nil
}
