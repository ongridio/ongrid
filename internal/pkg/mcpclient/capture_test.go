package mcpclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
)

type captureTransport func(*http.Request) (*http.Response, error)

func (f captureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCallCapturesUnmodifiedResultAndTransportLimit(t *testing.T) {
	const original = `{"content":[{"type":"image","data":"opaque","mimeType":"image/png"}],"structuredContent":{"id":9007199254740993},"futureField":{"value":true},"isError":true}`
	for _, oversized := range []bool{false, true} {
		c := NewHTTP("https://example.test/mcp", nil, 0)
		c.http.Transport = captureTransport(func(*http.Request) (*http.Response, error) {
			body := `{"jsonrpc":"2.0","id":1,"result":` + original + `}`
			if oversized {
				body = strings.Repeat("x", MaxResponseBytes+1)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		ctx := toolcapture.WithState(context.Background())
		result, err := c.CallTool(ctx, "test", nil)
		raw, _, partial := toolcapture.Get(ctx)
		if oversized {
			if err == nil || !strings.Contains(err.Error(), "exceeds") || !partial || len(raw) != MaxResponseBytes {
				t.Fatalf("limit: %v partial=%v bytes=%d", err, partial, len(raw))
			}
		} else if err != nil || partial || string(raw) != original || !result.IsError {
			t.Fatalf("capture: %v partial=%v raw=%s", err, partial, raw)
		}
	}
}

func TestSSEMultilineMatchesIDAndBoundsWholeStream(t *testing.T) {
	input := "data: {\"id\":9,\"result\":\"wrong\"}\n\ndata: {\"id\":1,\n" +
		"data: \"result\":{\"ok\":true}}\n\n"
	b, err := firstSSEData(strings.NewReader(input), 1)
	if err != nil || !strings.Contains(string(b), `"ok":true`) {
		t.Fatalf("multiline response=%s,%v", b, err)
	}
	input = strings.Repeat(": keepalive\n", MaxResponseBytes/12+2)
	if _, err = firstSSEData(strings.NewReader(input), 1); err == nil {
		t.Fatal("unbounded stream accepted")
	}
}
