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
)

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
}

func (c *Client) call(ctx context.Context, method string, body any) (json.RawMessage, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env apiResp
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%s decode: %w (body=%s)", method, err, truncate(raw, 200))
	}
	if !env.OK {
		return nil, fmt.Errorf("telegram %s: %d %s", method, env.ErrorCode, env.Description)
	}
	return env.Result, nil
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
	res, err := c.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text})
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
	_, err := c.call(ctx, "editMessageText", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	})
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
