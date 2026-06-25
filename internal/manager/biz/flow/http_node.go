// http_node.go — the HTTP Request node (NodeHTTP). Lets a flow call an
// external HTTP API: method / url / headers / body all accept {{…}} templates
// (resolved by the engine before Execute runs). Output: status / body (parsed
// JSON when possible, else raw text) / headers — referenceable downstream.
package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpNodeMaxBody caps the response we read so a giant payload can't blow up
// the run (1 MiB is plenty for an automation step).
const httpNodeMaxBody = 1 << 20

func execHTTP(ctx context.Context, _ Executors, cfg map[string]any, _ *RunContext) (NodeResult, error) {
	method := strings.ToUpper(strings.TrimSpace(toStr(cfg["method"])))
	if method == "" {
		method = http.MethodGet
	}
	url := strings.TrimSpace(toStr(cfg["url"]))
	if url == "" {
		return NodeResult{}, fmt.Errorf("http node: url is empty")
	}

	var body io.Reader
	if b, ok := cfg["body"]; ok && b != nil {
		switch bv := b.(type) {
		case string:
			if strings.TrimSpace(bv) != "" {
				body = strings.NewReader(bv)
			}
		default:
			jb, err := json.Marshal(bv)
			if err == nil {
				body = bytes.NewReader(jb)
			}
		}
	}

	timeout := 30 * time.Second
	switch tv := cfg["timeout_seconds"].(type) {
	case float64:
		if tv > 0 {
			timeout = time.Duration(tv) * time.Second
		}
	case string:
		if n, err := time.ParseDuration(strings.TrimSpace(tv) + "s"); err == nil && n > 0 {
			timeout = n
		}
	}
	if timeout > 120*time.Second {
		timeout = 120 * time.Second
	}

	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, method, url, body)
	if err != nil {
		return NodeResult{}, fmt.Errorf("http node: %w", err)
	}
	if hdrs, ok := cfg["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			req.Header.Set(k, toStr(v))
		}
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Network / timeout failure → the node's error port (engine routes on err).
		return NodeResult{}, fmt.Errorf("http node: %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, httpNodeMaxBody))

	var parsed any
	if json.Unmarshal(raw, &parsed) != nil {
		parsed = string(raw) // non-JSON response stays text
	}
	headers := make(map[string]any, len(resp.Header))
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}
	// A 4xx/5xx is a real response, not a transport error: surface it on the
	// normal port with the status, so the flow can branch on it via a
	// condition ({{nodes.http.output.status}} >= 400) rather than dead-ending.
	out := map[string]any{"status": resp.StatusCode, "body": parsed, "headers": headers}
	return NodeResult{Output: out, Port: PortNext}, nil
}

func toStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}
