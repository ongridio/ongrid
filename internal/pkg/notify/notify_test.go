package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRouterSendUsesDefaultChannels(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		var payload Message
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		if payload.Subject != "CPU high" {
			t.Errorf("subject = %q", payload.Subject)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	router := NewRouter(
		true,
		time.Second,
		[]string{"webhook"},
		NewGenericWebhookSender("webhook", srv.URL+"/notify", "secret", srv.Client()),
	)
	err := router.Send(context.Background(), Message{
		Subject:  "CPU high",
		Severity: SeverityWarning,
		Source:   "alert",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotPath != "/notify" {
		t.Errorf("path = %q, want /notify", gotPath)
	}
}

func TestRouterSendUnknownChannel(t *testing.T) {
	router := NewRouter(true, time.Second, []string{"missing"})
	err := router.Send(context.Background(), Message{Subject: "x"})
	if err == nil || !strings.Contains(err.Error(), `channel "missing" not configured`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRouterDisabledDropsMessage(t *testing.T) {
	router := NewRouter(false, time.Second, []string{"missing"})
	err := router.Send(context.Background(), Message{Subject: "x"})
	if err != nil {
		t.Fatalf("Send disabled: %v", err)
	}
}

func TestFeishuSenderSignsPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		if payload["timestamp"] == "" {
			t.Errorf("timestamp missing")
		}
		if payload["sign"] == "" {
			t.Errorf("sign missing")
		}
		if payload["msg_type"] != "interactive" {
			t.Errorf("msg_type = %v, want interactive", payload["msg_type"])
		}
		if payload["card"] == nil {
			t.Errorf("card missing in payload")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewFeishuSender("feishu", srv.URL, "secret", srv.Client())
	err := sender.Send(context.Background(), Message{Subject: "edge offline"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestFeishuSenderEmitsInteractiveCard(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewFeishuSender("feishu-ops", srv.URL, "", srv.Client())
	occurred := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	err := sender.Send(context.Background(), Message{
		Subject:    "设备离线: 阿里云ECS01 心跳超时",
		Severity:   SeverityCritical,
		Source:     "global",
		DedupeKey:  "pipeline:device_offline:1",
		OccurredAt: occurred,
		Labels: map[string]string{
			"rule":        "device_offline",
			"rule_name":   "设备离线",
			"device_id":   "1",
			"device_name": "阿里云ECS01",
			"incident_id": "88",
			"runbook_url": "https://wiki.ops/runbook/device_offline",
			"console_url": "https://ongrid.example.com",
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got["msg_type"] != "interactive" {
		t.Fatalf("msg_type = %v, want interactive", got["msg_type"])
	}

	card, ok := got["card"].(map[string]any)
	if !ok {
		t.Fatalf("card not a map: %v", got["card"])
	}

	header, ok := card["header"].(map[string]any)
	if !ok || header["template"] != "red" {
		t.Errorf("header template = %v, want red", header["template"])
	}

	elements, ok := card["elements"].([]any)
	if !ok || len(elements) == 0 {
		t.Fatalf("elements missing or empty")
	}

	// Verify action buttons are rendered when console_url is provided
	hasActions := false
	for _, el := range elements {
		if elm, ok := el.(map[string]any); ok && elm["tag"] == "action" {
			hasActions = true
			break
		}
	}
	if !hasActions {
		t.Errorf("expected action element in card elements, got %v", elements)
	}

	// Verify timezone is converted to Beijing time (+8 hours from UTC)
	// occurred is 2026-09-29 10:00:00 UTC => 2026-09-29 18:00:00 CST
	fieldsElem := elements[0].(map[string]any)["fields"].([]any)
	foundTime := false
	for _, f := range fieldsElem {
		fMap := f.(map[string]any)
		textMap := fMap["text"].(map[string]any)
		content := textMap["content"].(string)
		if strings.Contains(content, "发生时间") {
			foundTime = true
			if !strings.Contains(content, "2026-09-29 18:00:00") {
				t.Errorf("expected occurredAt in CST '2026-09-29 18:00:00', got %s", content)
			}
		}
	}
	if !foundTime {
		t.Errorf("time field not found in card")
	}

	// Verify all severities & status mappings
	severityCases := []struct {
		severity Severity
		status   string
		wantTpl  string
	}{
		{SeverityWarning, "firing", "orange"},
		{SeverityWarning, "open", "orange"},
		{SeverityInfo, "firing", "blue"},
		{SeverityCritical, "acknowledged", "yellow"},
		{SeverityCritical, "resolved", "green"},
	}

	for _, sc := range severityCases {
		err = sender.Send(context.Background(), Message{
			Subject:  "状态测试",
			Severity: sc.severity,
			Labels: map[string]string{
				"status": sc.status,
			},
		})
		if err != nil {
			t.Fatalf("Send severity %v status %v: %v", sc.severity, sc.status, err)
		}
		c := got["card"].(map[string]any)
		h := c["header"].(map[string]any)
		if h["template"] != sc.wantTpl {
			t.Errorf("severity=%s status=%s got template %v, want %v", sc.severity, sc.status, h["template"], sc.wantTpl)
		}
	}
}

func TestFeishuCardIgnoresResolvedSubstringInSubject(t *testing.T) {
	card := formatFeishuCard(Message{
		Subject:  "unresolved errors above threshold on systemd-resolved",
		Severity: SeverityCritical,
		Labels: map[string]string{
			"status":      "open",
			"incident_id": "42",
			"console_url": "https://console.example",
			"device_id":   "7",
		},
	})

	header, ok := card["header"].(map[string]any)
	if !ok {
		t.Fatalf("header missing: %#v", card["header"])
	}
	if header["template"] != "red" {
		t.Fatalf("template = %v, want red (open critical must not be treated as recovery)", header["template"])
	}

	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	payload := string(raw)
	if !strings.Contains(payload, "OPEN (发生中)") {
		t.Errorf("expected OPEN status localization in card, got %s", payload)
	}
	if strings.Contains(payload, "RESOLVED (已恢复)") {
		t.Errorf("status must not be marked resolved when Labels[status]=open: %s", payload)
	}
	if !strings.Contains(payload, "#42") || !strings.Contains(payload, "事件 ID") {
		t.Errorf("expected visible incident ID #42 in card: %s", payload)
	}
	if !strings.Contains(payload, "https://console.example/alerts/incidents/42") {
		t.Errorf("expected incident deep link /alerts/incidents/42 in card: %s", payload)
	}
}

func TestFeishuCardResolvedOnlyFromStatusLabel(t *testing.T) {
	card := formatFeishuCard(Message{
		Subject:  "CPU high",
		Severity: SeverityCritical,
		Labels:   map[string]string{"status": "resolved"},
	})
	header := card["header"].(map[string]any)
	if header["template"] != "green" {
		t.Fatalf("resolved status template = %v, want green", header["template"])
	}
}

func TestDingTalkSenderSignsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("timestamp") == "" {
			t.Errorf("timestamp query missing")
		}
		if r.URL.Query().Get("sign") == "" {
			t.Errorf("sign query missing")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewDingTalkSender("dingtalk", srv.URL, "secret", srv.Client())
	err := sender.Send(context.Background(), Message{Subject: "edge offline"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// TestSlackSenderEmitsAttachments locks the new Slack payload shape:
// the operator sees a colored side-bar + structured fields (Severity /
// Source / Rule / Incident / Device / Dedupe) instead of an unstyled
// paragraph. The top-level "text" stays populated as Slack's fallback
// preview (push, email digest, sidebar) for clients that strip attachments.
func TestSlackSenderEmitsAttachments(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewSlackSender("slack-ops", srv.URL, srv.Client())
	occurred := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	err := sender.Send(context.Background(), Message{
		Subject:    "Swap usage 84% on VM-4-8",
		Severity:   SeverityCritical,
		Source:     "alert-evaluator",
		DedupeKey:  "pipeline:swap_high:device_id=1",
		OccurredAt: occurred,
		Labels: map[string]string{
			"rule":        "swap_high",
			"incident_id": "42",
			"device_id":   "1",
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Fallback text: Slack uses this for push / sidebar / email digest.
	if want := "[CRITICAL] Swap usage 84% on VM-4-8"; got["text"] != want {
		t.Errorf("text = %q, want %q", got["text"], want)
	}

	atts, ok := got["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments = %v, want exactly 1", got["attachments"])
	}
	att := atts[0].(map[string]any)

	if att["color"] != "#d92f2f" {
		t.Errorf("color = %v, want #d92f2f", att["color"])
	}
	if att["title"] != "Swap usage 84% on VM-4-8" {
		t.Errorf("title = %v", att["title"])
	}
	if att["footer"] != "ongrid" {
		t.Errorf("footer = %v, want ongrid", att["footer"])
	}
	if got, want := att["ts"], float64(occurred.Unix()); got != want {
		t.Errorf("ts = %v, want %v", got, want)
	}

	fields, ok := att["fields"].([]any)
	if !ok {
		t.Fatalf("fields missing or wrong type: %v", att["fields"])
	}
	want := map[string]string{
		"Severity":   "CRITICAL",
		"Source":     "alert-evaluator",
		"Rule":       "swap_high",
		"Incident":   "#42",
		"Device":     "#1",
		"Dedupe key": "pipeline:swap_high:device_id=1",
	}
	saw := map[string]string{}
	for _, f := range fields {
		m := f.(map[string]any)
		saw[m["title"].(string)] = m["value"].(string)
	}
	for k, v := range want {
		if saw[k] != v {
			t.Errorf("field %q = %q, want %q", k, saw[k], v)
		}
	}
}

// TestSlackSenderColorByUnknownSeverity guards the slate fallback so a
// rogue severity string doesn't break the rail's render contract (Slack
// silently drops a malformed color and the operator gets a bare card).
func TestSlackSenderColorByUnknownSeverity(t *testing.T) {
	if got := slackColor(Severity("bogus")); got == "" {
		t.Errorf("color empty for unknown severity")
	}
	cases := map[Severity]string{
		SeverityCritical: "#d92f2f",
		SeverityWarning:  "#f2c037",
		SeverityInfo:     "#36a64f",
	}
	for sev, want := range cases {
		if got := slackColor(sev); got != want {
			t.Errorf("slackColor(%q) = %q, want %q", sev, got, want)
		}
	}
}
