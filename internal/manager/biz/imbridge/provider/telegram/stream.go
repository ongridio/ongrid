package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	bizbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	"github.com/ongridio/ongrid/internal/manager/biz/imbridge/imformat"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// pollTimeoutSec is the server-side long-poll wait; the per-poll context
// adds a buffer on top so a stalled connection still unblocks.
const pollTimeoutSec = 25

// StreamClient is the Telegram inbound loop. Telegram has no websocket
// stream like Feishu, so we long-poll getUpdates (outbound → proxy-friendly).
// Satisfies bizbridge.StreamClient; the StreamSupervisor adds
// reconnect-with-backoff, so a poll error just returns here.
type StreamClient struct {
	app     *model.ImApp
	bridge  *bizbridge.Bridge
	client  *Client
	allowed map[string]struct{} // sender user-id allowlist; see ADR-031
	log     *slog.Logger
}

// NewStreamClient builds a Telegram stream client for one ImApp
// (app_secret = bot token). The sender allowlist (app.AllowFrom) is
// parsed once here — the bot is publicly discoverable, so any update
// from a user NOT on the list is silently dropped in handle() before
// it ever reaches the agent (ADR-031; consistent with OpenClaw's
// allowFrom). validate() guarantees AllowFrom is non-empty for
// Telegram, so an empty set here can only mean a malformed legacy row,
// which correctly denies everyone.
func NewStreamClient(app *model.ImApp, bridge *bizbridge.Bridge, log *slog.Logger) *StreamClient {
	if log == nil {
		log = slog.Default()
	}
	allowed := make(map[string]struct{})
	for _, id := range bizbridge.ParseAllowFrom(app.AllowFrom) {
		allowed[id] = struct{}{}
	}
	return &StreamClient{
		app:     app,
		bridge:  bridge,
		client:  NewClient(app.AppSecret),
		allowed: allowed,
		log:     log.With(slog.String("provider", "telegram"), slog.Uint64("im_app_id", app.ID)),
	}
}

// ProviderName satisfies bizbridge.StreamClient.
func (c *StreamClient) ProviderName() string { return "telegram" }

// Run long-polls getUpdates until ctx is cancelled. Each text message is
// bridged to the agent on a detached goroutine (agent runs outlive a poll).
// A poll error returns to the supervisor, which retries with backoff. Only
// one poller may run per bot (Telegram rejects concurrent getUpdates) — the
// supervisor guarantees one client per ImApp.
func (c *StreamClient) Run(ctx context.Context) error {
	c.log.Info("starting telegram getUpdates poll")
	offset := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pollCtx, cancel := context.WithTimeout(ctx, (pollTimeoutSec+10)*time.Second)
		ups, err := c.client.GetUpdates(pollCtx, offset, pollTimeoutSec)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("getUpdates: %w", err)
		}
		for _, u := range ups {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1 // ack so this update isn't redelivered
			}
			if u.CallbackQuery != nil {
				if err := c.handleCallback(ctx, u.CallbackQuery); err != nil {
					c.log.Warn("telegram approval callback acknowledgement failed")
				}
			} else {
				c.handle(u)
			}
		}
	}
}

func (c *StreamClient) handle(u Update) {
	m := u.Message
	if m == nil || m.Chat == nil || m.Text == "" {
		return // S1: text-only DMs/groups; ignore non-text + edits
	}
	chatID := strconv.FormatInt(m.Chat.ID, 10)
	openID, userName := "", ""
	if m.From != nil {
		openID = strconv.FormatInt(m.From.ID, 10)
		userName = m.From.FirstName
	}
	// Access control: the bot is publicly reachable by username, so only
	// allowlisted sender user IDs may converse. Anyone else is dropped
	// SILENTLY — no reply, no placeholder, no agent run, no ack (mirrors
	// OpenClaw allowFrom; a reply would confirm the bot exists + leak that
	// it's an agent). We log at WARN with the sender's id so an admin can
	// add a legitimate user to allow_from. See ADR-031.
	if _, ok := c.allowed[openID]; !ok || m.From == nil {
		c.log.Warn("telegram inbound from non-allowlisted sender — ignored",
			slog.String("user_id", openID),
			slog.String("user_name", userName),
			slog.String("chat_id", chatID))
		return
	}
	in := bizbridge.InboundMessage{ConnectionIdentity: bizbridge.ConnectionIdentity(c.app),
		Subject:       bizbridge.Subject{User: openID},
		Private:       m.Chat.Type == "private",
		Provider:      model.ProviderTelegram,
		AppID:         c.app.AppID,
		ChatID:        chatID,
		OpenID:        openID,
		UserName:      userName,
		Text:          m.Text,
		EventID:       strconv.Itoa(u.UpdateID),
		ReceiveIDType: "chat_id",
	}
	sender := senderAdapter{client: c.client, chatID: chatID}
	// Detach: agent runs take 30s+; the poll loop must keep moving.
	go func() {
		if err := c.bridge.HandleInbound(context.Background(), sender, in); err != nil {
			c.log.Warn("telegram bridge handle_inbound failed", slog.Any("err", err))
		}
	}()
}

// senderAdapter satisfies bizbridge.Sender. Telegram's editMessageText
// needs chat_id + message_id, so chatID is bound per inbound chat and the
// platform message id is round-tripped as a decimal string.
type senderAdapter struct {
	client *Client
	chatID string
}

func (s senderAdapter) SendText(ctx context.Context, receiveID, _, text string) (string, error) {
	chat := receiveID
	if chat == "" {
		chat = s.chatID
	}
	id, err := s.client.SendMessage(ctx, chat, text)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(id), nil
}

func (s senderAdapter) EditText(ctx context.Context, messageID, text string) error {
	mid, err := strconv.Atoi(messageID)
	if err != nil {
		return fmt.Errorf("telegram message id %q: %w", messageID, err)
	}
	return s.client.EditMessageText(ctx, s.chatID, mid, text)
}

// NewStreamFactory returns the bizbridge.StreamClientFactory main.go
// registers for the "telegram" provider.
func NewStreamFactory(log *slog.Logger) bizbridge.StreamClientFactory {
	return func(app *model.ImApp, bridge *bizbridge.Bridge) (bizbridge.StreamClient, error) {
		if app.AppSecret == "" {
			return nil, fmt.Errorf("telegram: bot token (app_secret) required")
		}
		return NewStreamClient(app, bridge, log), nil
	}
}

func (s senderAdapter) SplitText(text string) []string {
	return imformat.MarkdownPages(text, func(page string) bool { return imformat.TelegramFits(page) })
}

func NewApprovalSender(app *model.ImApp, target *model.ApprovalTarget, _ string) (bizbridge.Sender, error) {
	client := NewClient(app.AppSecret)
	client.durable = true
	return senderAdapter{client: client, chatID: target.ChatID}, nil
}
