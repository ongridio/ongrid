package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// WatchToolsChanged listens to the optional server-initiated SSE stream after
// Initialize. The caller owns reconnection and polling fallback. It must not
// concurrently call Initialize on this client.
func (c *Client) WatchToolsChanged(ctx context.Context, changed func()) error {
	if !c.listChanged {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return err
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	client := *c.http
	client.Timeout = 0 // watch lifetime belongs to ctx, not the RPC timeout.
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("MCP notification stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusMethodNotAllowed {
		return nil
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return fmt.Errorf("MCP notification stream HTTP %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data strings.Builder
	flush := func() error {
		raw := strings.TrimSpace(data.String())
		data.Reset()
		if raw == "" {
			return nil
		}
		var messages []json.RawMessage
		if strings.HasPrefix(raw, "[") {
			if err := json.Unmarshal([]byte(raw), &messages); err != nil {
				return err
			}
		} else {
			messages = []json.RawMessage{json.RawMessage(raw)}
		}
		for _, msg := range messages {
			var notification struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal(msg, &notification); err != nil {
				return err
			}
			if notification.Method == "notifications/tools/list_changed" && changed != nil {
				changed()
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return fmt.Errorf("decode MCP notification: %w", err)
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 1<<20 {
				return fmt.Errorf("MCP notification exceeds 1 MiB")
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}
