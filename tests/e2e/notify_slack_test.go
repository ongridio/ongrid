//go:build e2e

// Catalog: G3 — Slack notification 通道：创建 incoming-webhook 渠道 →
//          POST /test → 假 Slack endpoint 收到 attachments 富格式 payload
//          (color rail + fields)。验证 internal/pkg/notify/webhook.go
//          formatSlack 的输出与 G3 描述一致，且 testChannel 路径会真的
//          发到我们传入的 endpoint。
package e2e

import (
	"strings"
	"testing"

	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

func TestNotify_SlackRichFormat_G3(t *testing.T) {
	env := testenv.Start(t)
	pair := env.LoginAdmin()

	// 1. Create a Slack channel pointing at our fake.
	createStatus, body, err := env.DoJSON("POST", "/api/v1/notification-channels", map[string]any{
		"name":     "e2e-slack",
		"type":     "slack",
		"endpoint": env.FakeSlack().WebhookURL(),
		"enabled":  true,
	}, pair.AccessToken)
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if createStatus != 200 && createStatus != 201 {
		t.Fatalf("create channel: status=%d body=%v", createStatus, body)
	}
	idAny, ok := body["id"]
	if !ok {
		t.Fatalf("create channel: response missing id (body=%v)", body)
	}
	channelID := numberToString(idAny)
	if channelID == "" {
		t.Fatalf("create channel: id was not a number-like (got %T %v)", idAny, idAny)
	}

	// 2. Fire the test send.
	testStatus, testBody, err := env.DoJSON("POST", "/api/v1/notification-channels/"+channelID+"/test", nil, pair.AccessToken)
	if err != nil {
		t.Fatalf("test channel: %v", err)
	}
	if testStatus != 200 {
		t.Fatalf("test channel: status=%d body=%v", testStatus, testBody)
	}

	// 3. Assert one POST landed on the fake Slack endpoint, with the
	//    attachments shape produced by formatSlack().
	caps := env.FakeSlack().Captures()
	if len(caps) != 1 {
		t.Fatalf("expected 1 Slack POST, got %d", len(caps))
	}
	c := caps[0]
	if !strings.HasPrefix(c.Path, "/services/") {
		t.Errorf("Slack POST path = %q, want it to start with /services/", c.Path)
	}
	if _, ok := c.Body["text"].(string); !ok {
		t.Errorf("Slack body missing top-level 'text' for fallback preview (body=%v)", c.Body)
	}
	attsAny, ok := c.Body["attachments"].([]any)
	if !ok || len(attsAny) == 0 {
		t.Fatalf("Slack body missing 'attachments' array (body=%v)", c.Body)
	}
	att, _ := attsAny[0].(map[string]any)
	if att == nil {
		t.Fatalf("first attachment was not an object: %T", attsAny[0])
	}
	if _, ok := att["color"].(string); !ok {
		t.Errorf("attachment missing 'color' (the severity rail) — got %v", att)
	}
	if _, ok := att["fallback"].(string); !ok {
		t.Errorf("attachment missing 'fallback' (clients that strip rich format need this)")
	}
	fields, _ := att["fields"].([]any)
	if len(fields) == 0 {
		t.Fatalf("attachment 'fields' empty — the structured render is the whole point of G3")
	}
	if !hasFieldTitled(fields, "Severity") {
		t.Errorf("attachment fields missing 'Severity' — fields=%v", fields)
	}
}

// hasFieldTitled scans the Slack attachment fields array for one with
// the given title.
func hasFieldTitled(fields []any, title string) bool {
	for _, f := range fields {
		m, ok := f.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["title"].(string); t == title {
			return true
		}
	}
	return false
}

// numberToString accepts whatever JSON shape the id came back as
// (float64 from encoding/json, json.Number, or string) and returns the
// path component.
func numberToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// gorm/json default — ids are integral up to 2^53 so no fraction.
		return trimFloat(x)
	}
	return ""
}

func trimFloat(f float64) string {
	// integer-valued float → no decimal point. Avoid strconv import.
	n := int64(f)
	if float64(n) != f {
		return ""
	}
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
