package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// DefaultBaseURL is the open.feishu.cn root. Override via NewClient
// options for international tenants (lark suite).
const DefaultBaseURL = "https://open.feishu.cn"

// Client is the thin Feishu OpenAPI client used by the IM bridge. One
// instance per (app_id, app_secret) — token cache is per-instance so
// rotating credentials means rebuilding the client.
type Client struct {
	baseURL    string
	appID      string
	appSecret  string
	http       *http.Client
	tokMu      sync.Mutex
	tokValue   string
	tokExpires time.Time
}

type Option func(*Client)

func WithBaseURL(u string) Option          { return func(c *Client) { c.baseURL = u } }
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

func NewClient(appID, appSecret string, opts ...Option) *Client {
	c := &Client{
		baseURL:   DefaultBaseURL,
		appID:     appID,
		appSecret: appSecret,
		http:      &http.Client{Timeout: 15 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// tenantAccessToken returns a cached tenant_access_token, refreshing
// when it's within 200s of expiry. Per Feishu docs the token is good
// for up to 7200s; we refresh proactively to avoid the 401 round-trip
// on the user's send path.
func (c *Client) tenantAccessToken(ctx context.Context) (string, error) {
	c.tokMu.Lock()
	defer c.tokMu.Unlock()
	if c.tokValue != "" && time.Until(c.tokExpires) > 200*time.Second {
		return c.tokValue, nil
	}
	body, err := json.Marshal(map[string]string{
		"app_id":     c.appID,
		"app_secret": c.appSecret,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("feishu: tenant_access_token decode: %w; body=%s", err, string(raw))
	}
	if out.Code != 0 || out.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu: tenant_access_token: code=%d msg=%s", out.Code, out.Msg)
	}
	c.tokValue = out.TenantAccessToken
	c.tokExpires = time.Now().Add(time.Duration(out.Expire) * time.Second)
	return c.tokValue, nil
}

// SendText posts a native rich-text message to a chat. receiveID is the
// platform target — for Feishu it's the open_chat_id when targeting a
// group, or open_id for a DM. receiveIDType selects how Feishu
// resolves it (`chat_id` / `open_id`). Returns the new message_id so
// the bridge can edit it later for streaming updates.
func (c *Client) SendText(ctx context.Context, receiveID, receiveIDType, text string) (string, error) {
	tok, err := c.tenantAccessToken(ctx)
	if err != nil {
		return "", err
	}
	msgType, contentJSON, err := messageContent(text)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]string{
		"receive_id": receiveID,
		"msg_type":   msgType,
		"content":    string(contentJSON),
	})
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/open-apis/im/v1/messages?receive_id_type=%s", c.baseURL, receiveIDType)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("feishu: send_text decode: %w; body=%s", err, string(raw))
	}
	if out.Code != 0 {
		return "", fmt.Errorf("feishu: send_text: code=%d msg=%s", out.Code, out.Msg)
	}
	return out.Data.MessageID, nil
}

// EditText patches an existing rich-text message — used for progressive
// streaming updates. messageID is the value returned by
// SendText.
func (c *Client) EditText(ctx context.Context, messageID, text string) error {
	tok, err := c.tenantAccessToken(ctx)
	if err != nil {
		return err
	}
	if messageID == "" {
		return errors.New("feishu: edit_text: messageID required")
	}
	msgType, contentJSON, err := messageContent(text)
	if err != nil {
		return err
	}
	// Feishu requires msg_type on PUT just like POST — omitting it
	// trips 99992402 (field validation failed) even though the docs
	// page for "edit message" doesn't make that obvious.
	body, err := json.Marshal(map[string]string{
		"msg_type": msgType,
		"content":  string(contentJSON),
	})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/open-apis/im/v1/messages/%s", c.baseURL, messageID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("feishu: edit_text decode: %w; body=%s", err, string(raw))
	}
	if out.Code != 0 {
		return fmt.Errorf("feishu: edit_text: code=%d msg=%s", out.Code, out.Msg)
	}
	return nil
}

// messageContent uses Feishu's native post/md node. The platform supports
// CommonMark 0.31 and GFM here, including tables, task lists and code blocks.
// Both locale slots carry the same agent answer so Feishu and Lark clients do
// not hide the message when their UI locale differs from the channel setting.
func messageContent(markdown string) (string, []byte, error) {
	paragraph := []map[string]string{{"tag": "md", "text": markdown}}
	content := [][]map[string]string{paragraph}
	rich, err := json.Marshal(map[string]any{
		"zh_cn": map[string]any{"content": content},
		"en_us": map[string]any{"content": content},
	})
	if err != nil {
		return "", nil, err
	}
	// Feishu caps post messages at 30 KiB while text messages allow 150 KiB.
	// JSON escaping and the duplicated locale entries add overhead, so retain
	// the old text path when the native payload approaches the lower limit.
	if len(rich) <= 28*1024 {
		return "post", rich, nil
	}
	plain, err := json.Marshal(map[string]string{"text": markdown})
	if err != nil {
		return "", nil, err
	}
	return "text", plain, nil
}
