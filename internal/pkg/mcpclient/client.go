// Package mcpclient is a minimal Model Context Protocol (MCP) client over
// Streamable HTTP — the narrow slice ongrid needs as an MCP *client*
// (HLD-018): initialize → tools/list → tools/call. JSON-RPC 2.0 framing; the
// server may answer with application/json or a text/event-stream SSE body, we
// handle both. No external deps — the protocol surface is small and stable.
//
// Out of scope (MVP): resources, prompts, OAuth, stdio transport (that lands
// via pkg/runner subprocess later). Session id (Mcp-Session-Id header) is
// captured from initialize and echoed on subsequent calls when present.
package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProtocolVersion is the MCP revision ongrid advertises at initialize.
const ProtocolVersion = "2025-03-26"

// Client is an MCP client bound to one server endpoint.
type Client struct {
	endpoint    string
	headers     map[string]string // static auth headers (credential-injected)
	http        *http.Client
	sessionID   string // captured from initialize, echoed afterwards
	nextID      int
	listChanged bool
}

// NewHTTP builds a Streamable-HTTP MCP client. headers carries any
// credential-derived auth (e.g. Authorization). timeout 0 → 30s default.
func NewHTTP(endpoint string, headers map[string]string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		endpoint: endpoint,
		headers:  headers,
		http:     &http.Client{Timeout: timeout},
	}
}

// Tool is one entry from tools/list. InputSchema is raw JSON Schema, passed
// straight to the LLM as the tool's parameters.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// CallResult is the tools/call payload. Content blocks are flattened to text
// by TextContent() for the agent-facing result.
type CallResult struct {
	Raw               json.RawMessage `json:"-"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	Content           []ContentBlock  `json:"content"`
	IsError           bool            `json:"isError"`
}

// ContentBlock is one MCP content item (we surface text; other kinds are
// stringified best-effort).
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// TextContent joins all text blocks with newlines — the agent-facing string.
func (r *CallResult) TextContent() string {
	var b strings.Builder
	for i, c := range r.Content {
		if i > 0 {
			b.WriteByte('\n')
		}
		if c.Text != "" {
			b.WriteString(c.Text)
		} else {
			b.WriteString("[" + c.Type + "]")
		}
	}
	if b.Len() == 0 && len(r.StructuredContent) > 0 {
		return string(r.StructuredContent)
	}
	return b.String()
}

// Initialize performs the MCP handshake and the required initialized
// notification. Must be called before ListTools / CallTool.
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ongrid", "version": "1"},
	}
	raw, err := c.call(ctx, "initialize", params)
	if err != nil {
		return err
	}
	var result struct {
		Capabilities struct {
			Tools struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode MCP initialize: %w", err)
	}
	c.listChanged = result.Capabilities.Tools.ListChanged
	// initialized is a notification (no id, no response expected).
	return c.notify(ctx, "notifications/initialized", map[string]any{})
}

// ListTools returns the server's advertised tools.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var tools []Tool
	cursor := ""
	seen := map[string]bool{}
	totalBytes := 0
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		totalBytes += len(raw)
		if totalBytes > MaxResponseBytes {
			return nil, fmt.Errorf("mcp: tool catalog exceeds byte limit")
		}
		var page struct {
			Tools []Tool `json:"tools"`
			Next  string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("mcp: decode tools/list: %w", err)
		}
		tools = append(tools, page.Tools...)
		if len(tools) > 10000 {
			return nil, fmt.Errorf("mcp: tool catalog exceeds 10000 entries")
		}
		if page.Next == "" {
			return tools, nil
		}
		if seen[page.Next] || len(seen) >= 100 {
			return nil, fmt.Errorf("mcp: invalid or excessive tools/list pagination")
		}
		seen[page.Next] = true
		cursor = page.Next
	}
}

// CallTool invokes a tool by name with JSON arguments.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	toolcapture.Raw(ctx, raw, false)
	var res CallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcp: decode tools/call: %w", err)
	}
	res.Raw = append(json.RawMessage(nil), raw...)
	return &res, nil
}

// --- JSON-RPC plumbing ---

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.nextID++
	body := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params}
	respBytes, sessionID, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	if method == "tools/call" {
		toolcapture.Raw(ctx, respBytes, true)
	}
	if sessionID != "" {
		c.sessionID = sessionID
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(respBytes, &rpc); err != nil {
		return nil, fmt.Errorf("mcp: decode %s response: %w", method, err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("mcp: %s rpc error %d: %s", method, rpc.Error.Code, rpc.Error.Message)
	}
	return rpc.Result, nil
}

// notify sends a notification (no id) and ignores any body.
func (c *Client) notify(ctx context.Context, method string, params any) error {
	body := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	_, _, err := c.post(ctx, body)
	return err
}

// post sends one JSON-RPC message and returns the decoded single response
// (unwrapping SSE when the server streams). Returns the Mcp-Session-Id header
// if the server set one.
func (c *Client) post(ctx context.Context, body map[string]any) ([]byte, string, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("mcp: POST %s: %w", c.endpoint, err)
	}
	defer resp.Body.Close()
	sessionID := resp.Header.Get("Mcp-Session-Id")

	// Notifications / accepted-with-no-body answer 202.
	if resp.StatusCode == http.StatusAccepted {
		return []byte(`{"jsonrpc":"2.0"}`), sessionID, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, sessionID, fmt.Errorf("mcp: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		var captured bytes.Buffer
		msg, err := firstSSEData(io.TeeReader(io.LimitReader(resp.Body, MaxResponseBytes+1), &captured), body["id"])
		if err != nil && body["method"] == "tools/call" {
			toolcapture.Raw(ctx, captured.Bytes()[:min(captured.Len(), MaxResponseBytes)], true)
		}
		return msg, sessionID, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		if body["method"] == "tools/call" {
			toolcapture.Raw(ctx, b, true)
		}
		return nil, sessionID, err
	}
	if len(b) > MaxResponseBytes {
		if body["method"] == "tools/call" {
			toolcapture.Raw(ctx, b[:MaxResponseBytes], true)
		}
		return nil, sessionID, fmt.Errorf("mcp: response exceeds %d bytes; execution may already have completed", MaxResponseBytes)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return []byte(`{"jsonrpc":"2.0"}`), sessionID, nil
	}
	return b, sessionID, nil
}

// firstSSEData reads an SSE stream and returns the first `data:` JSON payload
// that carries a JSON-RPC response (has "result" or "error"). MCP servers
// stream the response as one or more SSE events; the response message is the
// one we want.
const MaxResponseBytes = 16 << 20

func firstSSEData(r io.Reader, expected ...any) ([]byte, error) {
	sc := bufio.NewScanner(io.LimitReader(r, MaxResponseBytes+1))
	sc.Buffer(make([]byte, 0, 64<<10), MaxResponseBytes+1)
	var data strings.Builder
	total := 0
	flush := func() ([]byte, bool) {
		b := []byte(strings.TrimSpace(data.String()))
		data.Reset()
		var message struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(b, &message) != nil || (len(message.Result) == 0 && len(message.Error) == 0) {
			return nil, false
		}
		if len(expected) > 0 && expected[0] != nil {
			id, err := json.Marshal(expected[0])
			if err != nil || !bytes.Equal(id, message.ID) {
				return nil, false
			}
		}
		return b, true
	}
	for sc.Scan() {
		line := sc.Text()
		total += len(line) + 1
		if total > MaxResponseBytes {
			return nil, fmt.Errorf("mcp: SSE response exceeds %d bytes; execution may already have completed", MaxResponseBytes)
		}
		if line == "" {
			if b, ok := flush(); ok {
				return b, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("mcp: read SSE response: %w", err)
	}
	if b, ok := flush(); ok {
		return b, nil
	}
	return nil, fmt.Errorf("mcp: no matching JSON-RPC response in SSE stream")
}
