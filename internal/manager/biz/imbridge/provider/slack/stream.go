package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	bizbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// pingInterval is the keep-alive we send Slack — Slack closes idle Socket
// Mode connections after ~30s. A 20s ping keeps the TLS WebSocket warm
// through NATs / proxies as well.
const pingInterval = 20 * time.Second

// envelope is the canonical Socket Mode wrapper around every inbound
// event. envelope_id is what we ack; type tells us how to decode payload.
// disconnect envelopes have no payload — Slack uses them to ask the
// client to close the WebSocket and re-open with a fresh URL.
type envelope struct {
	EnvelopeID             string          `json:"envelope_id"`
	Type                   string          `json:"type"`
	Payload                json.RawMessage `json:"payload"`
	AcceptsResponsePayload bool            `json:"accepts_response_payload"`
	RetryAttempt           int             `json:"retry_attempt"`
	RetryReason            string          `json:"retry_reason"`
}

// eventsAPIPayload is the relevant subset of the payload we get when
// envelope.type == "events_api". Same shape Events-API webhook delivers,
// just wrapped in Socket Mode.
type eventsAPIPayload struct {
	TeamID string `json:"team_id"`
	Event  struct {
		Type     string `json:"type"`      // "message" | "app_mention" | ...
		Subtype  string `json:"subtype"`   // empty for normal user text; "bot_message", "message_changed" etc. to ignore
		Channel  string `json:"channel"`   // C…/G…/D… channel id
		User     string `json:"user"`      // sender user id
		Text     string `json:"text"`      // raw text (may contain <@U…> mention markup)
		TS       string `json:"ts"`        // message ts (used as event id for dedup)
		BotID    string `json:"bot_id"`    // present on bot-sent messages — used to break echo loops
		ThreadTS string `json:"thread_ts"` // present on reply threads — used as ImThread.ImThreadID
	} `json:"event"`
}

// StreamClient is the Slack inbound loop. Mirrors telegram.StreamClient:
// satisfies bizbridge.StreamClient so the supervisor adds the same
// reconnect-with-backoff. A WebSocket error returns to the supervisor.
type StreamClient struct {
	app     *model.ImApp
	bridge  *bizbridge.Bridge
	client  *Client
	allowed map[string]struct{} // Slack user_id allowlist (parsed from app.AllowFrom)
	log     *slog.Logger
}

// NewStreamClient builds a stream client for one Slack ImApp. The sender
// allowlist (app.AllowFrom) is parsed once here. Slack workspaces are
// less publicly-discoverable than Telegram bots, but the same
// belt-and-braces safety applies: only allowlisted users may converse;
// everyone else (including other bots) is silently dropped.
func NewStreamClient(app *model.ImApp, bridge *bizbridge.Bridge, log *slog.Logger) (*StreamClient, error) {
	if log == nil {
		log = slog.Default()
	}
	client, err := NewClientFromSecret(app.AppSecret)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{})
	for _, id := range bizbridge.ParseAllowFrom(app.AllowFrom) {
		allowed[id] = struct{}{}
	}
	return &StreamClient{
		app:     app,
		bridge:  bridge,
		client:  client,
		allowed: allowed,
		log:     log.With(slog.String("provider", "slack"), slog.Uint64("im_app_id", app.ID)),
	}, nil
}

// ProviderName satisfies bizbridge.StreamClient.
func (c *StreamClient) ProviderName() string { return "slack" }

// Run opens a Socket Mode WebSocket and processes events until ctx cancels
// or the connection drops. Any error returns to the supervisor, which
// retries with backoff. On a Slack-initiated disconnect we close cleanly
// + return nil so the supervisor reconnects immediately (no backoff sleep
// for a planned reconnect).
func (c *StreamClient) Run(ctx context.Context) error {
	wsURL, err := c.client.OpenConnection(ctx)
	if err != nil {
		return fmt.Errorf("apps.connections.open: %w", err)
	}
	parsed, err := url.Parse(wsURL)
	if err != nil {
		return fmt.Errorf("parse ws url: %w", err)
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = DialTimeout
	c.log.Info("dialing slack socket mode", slog.String("host", parsed.Host))
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()

	// Keep the connection warm. Slack's idle close hits ~30s. The ping
	// runs in a goroutine so the read loop never blocks behind a write.
	pingDone := make(chan struct{})
	go c.pingLoop(ctx, conn, pingDone)
	defer close(pingDone)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, raw, rerr := conn.ReadMessage()
		if rerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("ws read: %w", rerr)
		}
		// Probe for "type" — Slack mixes hello/disconnect envelopes
		// with events_api ones, but they all carry a top-level "type".
		var probe struct {
			Type string `json:"type"`
		}
		if uerr := json.Unmarshal(raw, &probe); uerr != nil {
			c.log.Warn("slack ws: decode probe", slog.Any("err", uerr))
			continue
		}
		switch probe.Type {
		case "hello":
			c.log.Info("slack socket mode: hello received")
			continue
		case "disconnect":
			c.log.Info("slack socket mode: server requested disconnect — will reconnect")
			return nil
		default:
			// envelope_id-carrying events (events_api / interactive /
			// slash_commands). We only handle events_api today; others
			// still get ack'd so Slack doesn't retry them at us forever.
		}
		var env envelope
		if uerr := json.Unmarshal(raw, &env); uerr != nil {
			c.log.Warn("slack ws: decode envelope", slog.Any("err", uerr))
			continue
		}
		// Debug log every envelope so an operator can tell whether Slack
		// pushed nothing vs. pushed a kind we didn't handle. Slack-side
		// retries are bounded by our ack, so even a busy workspace logs
		// no more than ~1 line per inbound action.
		c.log.Info("slack envelope received",
			slog.String("envelope_type", env.Type),
			slog.String("envelope_id", env.EnvelopeID),
			slog.Int("retry_attempt", env.RetryAttempt))
		// ack FIRST, handle second. Slack times the ack window from
		// envelope delivery (~3s); handing inbound off to the bridge
		// before ack would race that and trigger Slack-side retries.
		if env.EnvelopeID != "" {
			ackBody, _ := json.Marshal(map[string]string{"envelope_id": env.EnvelopeID})
			if werr := conn.WriteMessage(websocket.TextMessage, ackBody); werr != nil {
				return fmt.Errorf("ws ack: %w", werr)
			}
		}
		if env.Type == "events_api" {
			c.handleEvent(env.Payload)
		}
	}
}

func (c *StreamClient) pingLoop(ctx context.Context, conn *websocket.Conn, done <-chan struct{}) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-t.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				c.log.Debug("slack ws ping failed (will surface on next read)", slog.Any("err", err))
			}
		}
	}
}

// handleEvent decodes an events_api payload and routes user-typed text
// (message / app_mention) to the bridge. Bot-sent messages and Slack's
// own non-user subtypes (message_changed / channel_join / ...) are
// dropped so we don't loop on our own replies.
func (c *StreamClient) handleEvent(payload json.RawMessage) {
	var p eventsAPIPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.log.Warn("slack events_api: decode payload", slog.Any("err", err))
		return
	}
	ev := p.Event
	// Log every events_api event we get so operators can see what's
	// being sent (and what's being filtered). team_id and channel are
	// useful when one Slack app drives multiple workspaces or rooms.
	c.log.Info("slack events_api event",
		slog.String("event_type", ev.Type),
		slog.String("subtype", ev.Subtype),
		slog.String("channel", ev.Channel),
		slog.String("user", ev.User),
		slog.Bool("has_bot_id", ev.BotID != ""),
		slog.Int("text_len", len(ev.Text)))
	if ev.Type != "message" && ev.Type != "app_mention" {
		return
	}
	if ev.BotID != "" || ev.Subtype != "" {
		return // own messages + edit/delete notifications
	}
	if ev.Text == "" || ev.Channel == "" {
		return
	}
	if _, ok := c.allowed[ev.User]; !ok {
		c.log.Warn("slack inbound from non-allowlisted sender — ignored",
			slog.String("user_id", ev.User),
			slog.String("channel", ev.Channel))
		return
	}
	in := bizbridge.InboundMessage{
		Provider:      model.ProviderSlack,
		AppID:         c.app.AppID,
		ChatID:        ev.Channel,
		ThreadID:      ev.ThreadTS,
		OpenID:        ev.User,
		UserName:      ev.User, // Slack only ships user_id here; the bridge logs it
		Text:          stripMentions(ev.Text),
		EventID:       ev.TS,
		ReceiveIDType: "channel",
	}
	sender := senderAdapter{client: c.client, channel: ev.Channel}
	// Detach: agent runs take 30s+; the read loop must keep moving
	// (and must keep acking — Slack reuses one socket for all events).
	go func() {
		if err := c.bridge.HandleInbound(context.Background(), sender, in); err != nil {
			c.log.Warn("slack bridge handle_inbound failed", slog.Any("err", err))
		}
	}()
}

// stripMentions rewrites Slack's <@U…> mention markup to a bare mention
// the agent prompt can read. We keep the user-id letters so the model
// sees a stable reference; full display-name resolution would need a
// users.info round-trip per message which we skip for the MVP.
func stripMentions(s string) string {
	// <@UABCD> → @UABCD ; <#C1234|general> → #general ; <https://x|x> → x
	// Minimal sweep — we treat anything between < and > with a |.
	out := strings.Builder{}
	for {
		i := strings.Index(s, "<")
		if i < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:i])
		j := strings.Index(s[i:], ">")
		if j < 0 {
			out.WriteString(s[i:])
			return out.String()
		}
		seg := s[i+1 : i+j]
		if strings.HasPrefix(seg, "@") || strings.HasPrefix(seg, "#") {
			// keep the prefix + first segment (id) so the agent sees @UABCD
			pipe := strings.Index(seg, "|")
			if pipe > 0 {
				seg = seg[:pipe]
			}
			out.WriteString(seg)
		} else if pipe := strings.LastIndex(seg, "|"); pipe > 0 {
			out.WriteString(seg[pipe+1:])
		} else {
			out.WriteString(seg)
		}
		s = s[i+j+1:]
	}
}

// senderAdapter satisfies bizbridge.Sender. channel is bound per inbound
// message so the bridge can hand its own pre-computed ts back into
// EditText. Slack updates only need (channel, ts) — same as Telegram
// chat_id+message_id.
type senderAdapter struct {
	client  *Client
	channel string
}

func (s senderAdapter) SendText(ctx context.Context, receiveID, _ string, text string) (string, error) {
	channel := receiveID
	if channel == "" {
		channel = s.channel
	}
	return s.client.PostMessage(ctx, channel, text)
}

func (s senderAdapter) EditText(ctx context.Context, messageID, text string) error {
	return s.client.UpdateMessage(ctx, s.channel, messageID, text)
}

// NewStreamFactory returns the bizbridge.StreamClientFactory main.go
// registers for the "slack" provider.
func NewStreamFactory(log *slog.Logger) bizbridge.StreamClientFactory {
	return func(app *model.ImApp, bridge *bizbridge.Bridge) (bizbridge.StreamClient, error) {
		return NewStreamClient(app, bridge, log)
	}
}
