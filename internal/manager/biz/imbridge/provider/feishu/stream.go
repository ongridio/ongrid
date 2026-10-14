package feishu

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	bizbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// StreamClient is the long-connection variant of the Feishu provider
// manager dials out to Feishu's WebSocket-based event
// service via the official larksuite/oapi-sdk-go ws.Client; events
// land via the dispatcher and are bridged into the agent runtime.
// No public webhook URL is required.
type StreamClient struct {
	app    *model.ImApp
	bridge *bizbridge.Bridge
	log    *slog.Logger
}

// NewStreamClient builds a Feishu stream client for one ImApp row.
func NewStreamClient(app *model.ImApp, bridge *bizbridge.Bridge, log *slog.Logger) *StreamClient {
	if log == nil {
		log = slog.Default()
	}
	return &StreamClient{app: app, bridge: bridge, log: log.With(slog.String("provider", "feishu"), slog.Uint64("im_app_id", app.ID))}
}

// ProviderName satisfies bizbridge.StreamClient.
func (c *StreamClient) ProviderName() string { return "feishu" }

// Run dials the long-connection and blocks until ctx is cancelled or
// the SDK gives up after exhausting its own reconnect budget. The
// imbridge.StreamSupervisor adds an outer reconnect-with-backoff loop
// so we don't have to handle terminal connection failures here.
func (c *StreamClient) Run(ctx context.Context) error {
	disp := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(_ context.Context, ev *larkim.P2MessageReceiveV1) error {
			return c.onMessage(ev)
		})
	wsClient := larkws.NewClient(c.app.AppID, c.app.AppSecret,
		larkws.WithEventHandler(disp),
		larkws.WithAutoReconnect(true),
	)
	c.log.Info("starting feishu long-connection")
	return wsClient.Start(ctx)
}

// onMessage translates a Feishu P2MessageReceiveV1 envelope into the
// platform-agnostic InboundMessage and hands it to the bridge.
//
// The handler returns quickly: bridge.HandleInbound runs the agent
// (which can take 30s+) on a detached goroutine. Returning from the
// callback right away keeps the SDK's read loop happy.
func (c *StreamClient) onMessage(ev *larkim.P2MessageReceiveV1) error {
	if ev == nil || ev.Event == nil || ev.Event.Message == nil {
		return nil
	}
	msg := ev.Event.Message
	if msg.MessageType == nil || *msg.MessageType != "text" {
		// S1: text-only. Stickers / files / cards dropped silently.
		return nil
	}
	if msg.ChatId == nil || *msg.ChatId == "" {
		return nil
	}
	var content struct {
		Text string `json:"text"`
	}
	if msg.Content != nil {
		_ = json.Unmarshal([]byte(*msg.Content), &content)
	}
	if content.Text == "" {
		return nil
	}

	rootID := ""
	if msg.RootId != nil {
		rootID = *msg.RootId
	}
	eventID := ""
	if ev.EventV2Base != nil && ev.EventV2Base.Header != nil {
		eventID = ev.EventV2Base.Header.EventID
	}
	openID := ""
	if ev.Event.Sender != nil && ev.Event.Sender.SenderId != nil && ev.Event.Sender.SenderId.OpenId != nil {
		openID = *ev.Event.Sender.SenderId.OpenId
	}
	in := bizbridge.InboundMessage{
		Provider:      model.ProviderFeishu,
		AppID:         c.app.AppID,
		ChatID:        *msg.ChatId,
		ThreadID:      rootID,
		OpenID:        openID,
		Text:          content.Text,
		EventID:       eventID,
		ReceiveIDType: "chat_id",
	}
	sender := senderAdapter{client: NewClient(c.app.AppID, c.app.AppSecret)}

	// Detach context: the SDK passes a ctx that cancels when the
	// callback returns. Agent runs need to outlive that — copy
	// values that matter (none today; logger is bound on c) and use
	// Background. Supervisor cancellation comes through c via
	// wsClient.Start ctx instead.
	go func() {
		ctx := context.Background()
		if err := c.bridge.HandleInbound(ctx, sender, in); err != nil {
			c.log.Warn("feishu bridge handle_inbound failed", slog.Any("err", err))
		}
	}()
	return nil
}

// senderAdapter wraps Client to satisfy bizbridge.Sender.
type senderAdapter struct {
	client *Client
}

func (s senderAdapter) SendText(ctx context.Context, receiveID, receiveIDType, text string) (string, error) {
	return s.client.SendText(ctx, receiveID, receiveIDType, text)
}

func (s senderAdapter) EditText(ctx context.Context, messageID, text string) error {
	return s.client.EditText(ctx, messageID, text)
}

// NewStreamFactory returns a bizbridge.StreamClientFactory that the
// supervisor can register for the "feishu" provider. main.go calls
// this once at boot.
func NewStreamFactory(log *slog.Logger) bizbridge.StreamClientFactory {
	return func(app *model.ImApp, bridge *bizbridge.Bridge) (bizbridge.StreamClient, error) {
		return NewStreamClient(app, bridge, log), nil
	}
}
