// Package telegram is the Telegram Bot API provider for the IM bridge
// (ADR-021 + ADR-031). Unlike Feishu (websocket stream) it long-polls
// getUpdates — an OUTBOUND call, so it traverses the manager's
// HTTP(S)_PROXY on GFW-restricted hosts (setWebhook would require Telegram
// to reach the manager inbound, which is unreliable from mainland China).
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/imbridge/imformat"
)

// maxCallRetries bounds retries on rate-limit (429) and transient server (5xx)
// responses. Network-level errors (resp err) are NOT retried here — they bubble
// to the supervisor's reconnect-with-backoff, and for getUpdates the per-poll
// context deadline is intentional stall detection.
const maxCallRetries = 3

// maxRetryWait caps how long a single retry will sleep, even if Telegram's
// retry_after asks for longer (a multi-minute 429 should surface, not block a
// poll loop indefinitely).
const maxRetryWait = 60 * time.Second

// backoffDelay is the wait for transient (5xx) retries when Telegram gives no
// retry_after: 1s, 2s, 4s.
func backoffDelay(attempt int) time.Duration {
	return time.Duration(1<<uint(attempt)) * time.Second
}

// Client is a minimal Telegram Bot API client. It uses a zero-value
// http.Client (DefaultTransport), so it honors HTTP(S)_PROXY/NO_PROXY env —
// the manager's proxy config carries it out to api.telegram.org. Per-call
// timeouts come from the caller's context (long-poll vs. send differ).
type Client struct {
	token string
	hc    *http.Client
	base  string // API root; overridable in tests, defaults to the public API
}

// NewClient builds a client for one bot token.
func NewClient(token string) *Client {
	return &Client{token: token, hc: &http.Client{}, base: "https://api.telegram.org"}
}

func (c *Client) endpoint(method string) string {
	return c.base + "/bot" + c.token + "/" + method
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) call(ctx context.Context, method string, body any) (json.RawMessage, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", method, err)
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, err
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// Retry rate-limit (429, honoring retry_after) + transient server
		// errors (5xx) — decided on HTTP status before decoding, since a 5xx
		// body may not even be valid JSON. Other 4xx (400/401/403) are hard
		// errors; retrying won't help.
		if attempt < maxCallRetries && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) {
			wait := backoffDelay(attempt)
			if resp.StatusCode == http.StatusTooManyRequests {
				var e apiResp
				if json.Unmarshal(raw, &e) == nil && e.Parameters != nil && e.Parameters.RetryAfter > 0 {
					wait = time.Duration(e.Parameters.RetryAfter) * time.Second
				}
			}
			if wait > maxRetryWait {
				wait = maxRetryWait
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		var env apiResp
		if uerr := json.Unmarshal(raw, &env); uerr != nil {
			return nil, fmt.Errorf("%s decode: %w (body=%s)", method, uerr, truncate(raw, 200))
		}
		if !env.OK {
			return nil, fmt.Errorf("telegram %s: %d %s", method, env.ErrorCode, env.Description)
		}
		return env.Result, nil
	}
}

// Update is one getUpdates entry (only the fields the bridge consumes).
type Update struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		MessageID int `json:"message_id"`
		From      *struct {
			ID        int64  `json:"id"`
			FirstName string `json:"first_name"`
			Username  string `json:"username"`
		} `json:"from"`
		Chat *struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

// GetUpdates long-polls for messages from offset, waiting up to timeoutSec
// server-side. ctx cancellation aborts the wait (supervisor shutdown).
func (c *Client) GetUpdates(ctx context.Context, offset, timeoutSec int) ([]Update, error) {
	res, err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message"},
	})
	if err != nil {
		return nil, err
	}
	var ups []Update
	if err := json.Unmarshal(res, &ups); err != nil {
		return nil, fmt.Errorf("getUpdates result: %w", err)
	}
	return ups, nil
}

// SendMessage posts text to chatID and returns the new message_id.
func (c *Client) SendMessage(ctx context.Context, chatID, text string) (int, error) {
	res, err := c.call(ctx, "sendMessage", telegramMessageBody(chatID, text))
	if err != nil {
		return 0, err
	}
	var m struct {
		MessageID int `json:"message_id"`
	}
	if err := json.Unmarshal(res, &m); err != nil {
		return 0, fmt.Errorf("sendMessage result: %w", err)
	}
	return m.MessageID, nil
}

// EditMessageText replaces the text of (chatID, messageID). Telegram 400s a
// no-op edit ("message is not modified") when the new text already equals the
// message's current content — harmless for progressive streaming, where a
// throttled tick or the final flush can repeat the last chunk. Unlike Feishu,
// Telegram rejects it, so we swallow that specific error as success; any other
// 400/error still propagates.
func (c *Client) EditMessageText(ctx context.Context, chatID string, messageID int, text string) error {
	body := telegramMessageBody(chatID, text)
	body["message_id"] = messageID
	_, err := c.call(ctx, "editMessageText", body)
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}

func telegramMessageBody(chatID, markdown string) map[string]any {
	return map[string]any{
		"chat_id":              chatID,
		"text":                 imformat.TelegramHTML(markdown),
		"parse_mode":           "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
