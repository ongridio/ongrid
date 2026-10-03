package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type webhookSender struct {
	name       string
	endpoint   string
	secret     string
	client     *http.Client
	buildBody  func(Message) (any, error)
	signTarget func(endpoint, secret string, body []byte) (string, map[string]string, error)
}

// NewGenericWebhookSender posts the normalized Message JSON. When secret is
// configured it adds an HMAC signature header over the request body.
func NewGenericWebhookSender(name, endpoint, secret string, client *http.Client) Sender {
	return newWebhookSender(name, endpoint, secret, client, func(msg Message) (any, error) {
		return msg, nil
	}, signGenericWebhook)
}

// NewSlackSender posts to a Slack incoming webhook in the attachments
// format so the alert renders with severity-tinted color bar + structured
// fields (Severity / Source / Rule / Incident / Device / Dedupe) instead
// of an unstyled paragraph. Slack incoming webhooks ignore any secret —
// the credential is the URL itself — so the secret field is silently
// dropped at the channel-builder layer.
func NewSlackSender(name, endpoint string, client *http.Client) Sender {
	return newWebhookSender(name, endpoint, "", client, func(msg Message) (any, error) {
		return formatSlack(msg), nil
	}, nil)
}

// formatSlack renders one Message as a Slack incoming-webhook payload using
// the attachments format. We pick attachments over Block Kit because:
//   - it carries the colored side-bar that operators read as "how bad",
//   - it's the universally-supported format (Block Kit needs newer apps),
//   - the schema is JSON-flat and easy to test.
//
// "text" at the top stays populated with a one-line summary so Slack's own
// notification preview (push, sidebar, email digest) shows something useful
// even when the recipient client strips attachments.
func formatSlack(msg Message) map[string]any {
	sevUpper := strings.ToUpper(string(msg.Severity))
	if sevUpper == "" {
		sevUpper = "ALERT"
	}
	summary := fmt.Sprintf("[%s] %s", sevUpper, msg.Subject)

	att := map[string]any{
		"color":    slackColor(msg.Severity),
		"fallback": summary,
		"title":    nonEmpty(msg.Subject, sevUpper),
	}
	if msg.Body != "" {
		att["text"] = msg.Body
		att["mrkdwn_in"] = []string{"text"}
	}

	fields := make([]map[string]any, 0, 6)
	addField := func(title, value string, short bool) {
		if value == "" {
			return
		}
		fields = append(fields, map[string]any{
			"title": title,
			"value": value,
			"short": short,
		})
	}
	addField("Severity", sevUpper, true)
	addField("Source", msg.Source, true)
	if msg.Labels != nil {
		// Surface the alert-pipeline labels operators care about as
		// short fields; the remaining labels stay out of the message
		// to keep the card readable. Rule/incident/device are the same
		// breakdown the incident detail page leads with.
		addField("Rule", msg.Labels["rule"], true)
		if id := msg.Labels["incident_id"]; id != "" {
			addField("Incident", "#"+id, true)
		}
		if did := msg.Labels["device_id"]; did != "" {
			addField("Device", "#"+did, true)
		}
	}
	// Dedupe key is the join key for ops chatter — keep full width so
	// long pipeline:rule:label-set strings stay readable.
	addField("Dedupe key", msg.DedupeKey, false)
	if len(fields) > 0 {
		att["fields"] = fields
	}

	att["footer"] = "ongrid"
	if !msg.OccurredAt.IsZero() {
		att["ts"] = msg.OccurredAt.Unix()
	}

	return map[string]any{
		"text":        summary,
		"attachments": []any{att},
	}
}

// slackColor maps a Severity onto the Slack attachment color rail. Critical
// uses the red the Slack sentinel "danger" resolves to but as a hex so we
// pin the shade across Slack client versions; same idea for warning.
// Unknown severities get a neutral slate so the rail still renders.
func slackColor(sev Severity) string {
	switch sev {
	case SeverityCritical:
		return "#d92f2f"
	case SeverityWarning:
		return "#f2c037"
	case SeverityInfo:
		return "#36a64f"
	default:
		return "#6f7a87"
	}
}

func nonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// NewFeishuSender posts an interactive card payload compatible with Feishu/Lark custom bots.
func NewFeishuSender(name, endpoint, secret string, client *http.Client) Sender {
	return newWebhookSender(name, endpoint, secret, client, func(msg Message) (any, error) {
		payload := map[string]any{
			"msg_type": "interactive",
			"card":     formatFeishuCard(msg),
		}
		if secret != "" {
			ts := fmt.Sprintf("%d", time.Now().Unix())
			payload["timestamp"] = ts
			payload["sign"] = signFeishu(ts, secret)
		}
		return payload, nil
	}, nil)
}

// formatFeishuCard renders a Message as a Feishu interactive message card (Card 2.0).
// It maps severity onto card header colors (red for critical, orange for warning,
// green for resolved/info) and formats structured Chinese fields and action buttons.
func formatFeishuCard(msg Message) map[string]any {
	colorTemplate := "grey"
	prefix := "🔔【系统通知】"

	// Recovery / lifecycle presentation must follow structured status labels
	// only. Never infer "resolved" from subject text — subjects can contain
	// substrings like "unresolved" or service names like "systemd-resolved".
	statusLower := strings.ToLower(strings.TrimSpace(msg.Labels["status"]))
	isResolved := statusLower == "resolved"
	isActive := statusLower == "" || statusLower == "open" || statusLower == "firing"

	if isResolved {
		colorTemplate = "green"
		prefix = "✅【告警恢复】"
	} else if statusLower == "acknowledged" || statusLower == "ack" {
		colorTemplate = "yellow"
		prefix = "🟡【告警认领】"
	} else if statusLower == "silenced" {
		colorTemplate = "grey"
		prefix = "⚪【告警静音】"
	} else {
		switch strings.ToLower(strings.TrimSpace(string(msg.Severity))) {
		case "critical", "fatal", "emergency", "high", "p0", "p1":
			colorTemplate = "red"
			prefix = "🚨【严重告警】"
		case "warning", "warn", "medium", "p2":
			colorTemplate = "orange"
			prefix = "⚠️【预警提示】"
		case "info", "notice", "low", "p3":
			colorTemplate = "blue"
			prefix = "ℹ️【通知提醒】"
		default:
			colorTemplate = "grey"
			prefix = "🔔【系统通知】"
		}
	}

	ruleTitle := msg.Subject
	if rn, ok := msg.Labels["rule_name"]; ok && rn != "" {
		if dn, ok2 := msg.Labels["device_name"]; ok2 && dn != "" {
			ruleTitle = fmt.Sprintf("%s - %s", rn, dn)
		} else {
			ruleTitle = rn
		}
	}
	if ruleTitle == "" {
		ruleTitle = "告警事件通知"
	}

	header := map[string]any{
		"template": colorTemplate,
		"title": map[string]any{
			"tag":     "plain_text",
			"content": prefix + ruleTitle,
		},
	}

	elements := make([]any, 0, 6)

	// Fields grid (2 columns)
	fields := make([]map[string]any, 0, 6)
	addField := func(title, val string) {
		if val == "" {
			return
		}
		fields = append(fields, map[string]any{
			"is_short": true,
			"text": map[string]any{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**%s**：\n%s", title, val),
			},
		})
	}

	// 1. 告警级别 (适配所有告警级别)
	sevStr := strings.ToLower(strings.TrimSpace(string(msg.Severity)))
	switch sevStr {
	case "critical", "fatal", "emergency", "high", "p0", "p1":
		addField("告警级别", "<font color='red'>CRITICAL (严重)</font>")
	case "warning", "warn", "medium", "p2":
		addField("告警级别", "<font color='orange'>WARNING (警告)</font>")
	case "info", "notice", "low", "p3":
		addField("告警级别", "<font color='blue'>INFO (信息)</font>")
	default:
		if sevStr != "" {
			addField("告警级别", strings.ToUpper(sevStr))
		}
	}

	// 2. 告警状态 (生命周期状态跟踪)
	// Domain active status is "open" (see alert model); "firing" is the event
	// type / legacy label. Localize both as an active incident.
	if isResolved {
		addField("告警状态", "<font color='green'>RESOLVED (已恢复)</font>")
	} else if isActive {
		addField("告警状态", "<font color='red'>OPEN (发生中)</font>")
	} else if statusLower != "" {
		switch statusLower {
		case "acknowledged", "ack":
			addField("告警状态", "<font color='orange'>ACKNOWLEDGED (已认领)</font>")
		case "silenced":
			addField("告警状态", "<font color='grey'>SILENCED (已静音)</font>")
		default:
			addField("告警状态", strings.ToUpper(statusLower))
		}
	}

	// Incident identity — keep a concise visible reference (not the long dedupe key).
	if iid, ok := msg.Labels["incident_id"]; ok && iid != "" {
		addField("事件 ID", "#"+iid)
	}

	// 3. 告警对象
	if dn, ok := msg.Labels["device_name"]; ok && dn != "" {
		if did, ok2 := msg.Labels["device_id"]; ok2 && did != "" {
			addField("告警对象", fmt.Sprintf("%s (ID: %s)", dn, did))
		} else {
			addField("告警对象", dn)
		}
	} else if did, ok := msg.Labels["device_id"]; ok && did != "" {
		addField("告警对象", "设备 #"+did)
	} else if svc, ok := msg.Labels["service"]; ok && svc != "" {
		addField("告警服务", svc)
	}

	// 4. 规则名称
	if rn, ok := msg.Labels["rule_name"]; ok && rn != "" {
		if rk, ok2 := msg.Labels["rule"]; ok2 && rk != "" && rk != rn {
			addField("规则名称", fmt.Sprintf("%s (`%s`)", rn, rk))
		} else {
			addField("规则名称", rn)
		}
	} else if rk, ok := msg.Labels["rule"]; ok && rk != "" {
		addField("规则名称", fmt.Sprintf("`%s`", rk))
	}

	// 5. 触发时间 (自动转为北京时间 CST / UTC+8，消除 Linux / Docker 容器默认 UTC 导致的 8 小时偏差)
	occurredAt := msg.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	loc := time.FixedZone("CST", 8*3600)
	if tzName, ok := msg.Labels["timezone"]; ok && tzName != "" {
		if l, err := time.LoadLocation(tzName); err == nil {
			loc = l
		}
	} else if l, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		loc = l
	}
	addField("发生时间", occurredAt.In(loc).Format("2006-01-02 15:04:05"))

	if len(fields) > 0 {
		elements = append(elements, map[string]any{
			"tag":    "div",
			"fields": fields,
		})
	}

	// 5. 详细描述 / 触发条件
	descContent := ""
	if msg.Body != "" {
		descContent = msg.Body
	} else if expr, ok := msg.Labels["rule_expr"]; ok && expr != "" {
		descContent = fmt.Sprintf("**触发条件**：`%s`\n%s", expr, msg.Subject)
	} else if msg.Subject != "" && msg.Subject != ruleTitle {
		descContent = fmt.Sprintf("**详情**：%s", msg.Subject)
	}

	if descContent != "" {
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": descContent,
			},
		})
	}

	// 6. 排查建议 (Runbook)
	if rb, ok := msg.Labels["runbook_url"]; ok && rb != "" {
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": fmt.Sprintf("📖 **运维手册**：[点击查看处理指引](%s)", rb),
			},
		})
	}

	// 7. 操作按钮 (Actions) - 仅在提供控制台根路径时生成跳转按钮，避免硬编码地址
	baseURL := strings.TrimRight(strings.TrimSpace(msg.Labels["console_url"]), "/")
	if baseURL != "" {
		actions := make([]map[string]any, 0, 2)
		if did, ok := msg.Labels["device_id"]; ok && did != "" {
			actions = append(actions, map[string]any{
				"tag":  "button",
				"text": map[string]any{"tag": "plain_text", "content": "🔍 查看设备详情"},
				"type": "primary",
				"url":  fmt.Sprintf("%s/devices/%s", baseURL, did),
			})
		}
		if iid, ok := msg.Labels["incident_id"]; ok && iid != "" {
			actions = append(actions, map[string]any{
				"tag":  "button",
				"text": map[string]any{"tag": "plain_text", "content": "📋 告警事件"},
				"type": "default",
				"url":  fmt.Sprintf("%s/alerts/incidents/%s", baseURL, iid),
			})
		} else {
			actions = append(actions, map[string]any{
				"tag":  "button",
				"text": map[string]any{"tag": "plain_text", "content": "📋 告警事件"},
				"type": "default",
				"url":  fmt.Sprintf("%s/alerts", baseURL),
			})
		}

		if len(actions) > 0 {
			elements = append(elements, map[string]any{"tag": "hr"})
			elements = append(elements, map[string]any{
				"tag":     "action",
				"actions": actions,
			})
		}
	}

	// 8. 备注 (Note)
	source := msg.Source
	if source == "" {
		source = "global"
	}
	elements = append(elements, map[string]any{
		"tag": "note",
		"elements": []any{
			map[string]any{
				"tag":     "plain_text",
				"content": fmt.Sprintf("推送来源：Ongrid AIOps 智能运维平台 | 来源: %s", source),
			},
		},
	})

	return map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
		},
		"header":   header,
		"elements": elements,
	}
}

// NewDingTalkSender posts a text payload compatible with DingTalk custom bots.
func NewDingTalkSender(name, endpoint, secret string, client *http.Client) Sender {
	return newWebhookSender(name, endpoint, secret, client, func(msg Message) (any, error) {
		return map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": formatText(msg)},
		}, nil
	}, signDingTalkURL)
}

// NewWeComSender posts a text payload compatible with 企业微信 (WeCom) group
// bots. Endpoint URL carries the bot key as a query param; the v1 wiring
// has no extra signing — the secret query string IS the credential. Same
// JSON shape as DingTalk: {"msgtype":"text","text":{"content":"..."}}.
func NewWeComSender(name, endpoint string, client *http.Client) Sender {
	return newWebhookSender(name, endpoint, "", client, func(msg Message) (any, error) {
		return map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": formatText(msg)},
		}, nil
	}, nil)
}

// NewTelegramSender posts to the Telegram Bot API sendMessage endpoint.
// endpoint is the full https://api.telegram.org/bot<TOKEN>/sendMessage URL
// (bot token in the path); chatID is the target chat, sent in the JSON
// body. Telegram's auth model differs from the webhook channels — token in
// the URL, chat_id in the body — so it doesn't use the secret/signing path.
func NewTelegramSender(name, endpoint, chatID string, client *http.Client) Sender {
	return newWebhookSender(name, endpoint, "", client, func(msg Message) (any, error) {
		return map[string]any{
			"chat_id": chatID,
			"text":    formatText(msg),
		}, nil
	}, nil)
}

func newWebhookSender(
	name string,
	endpoint string,
	secret string,
	client *http.Client,
	buildBody func(Message) (any, error),
	signTarget func(endpoint, secret string, body []byte) (string, map[string]string, error),
) Sender {
	if name == "" {
		name = "webhook"
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &webhookSender{
		name:       name,
		endpoint:   endpoint,
		secret:     secret,
		client:     client,
		buildBody:  buildBody,
		signTarget: signTarget,
	}
}

func (s *webhookSender) Name() string { return s.name }

func (s *webhookSender) Send(ctx context.Context, msg Message) error {
	if s.endpoint == "" {
		return fmt.Errorf("endpoint required")
	}
	payload, err := s.buildBody(msg)
	if err != nil {
		return fmt.Errorf("build payload: %w", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	endpoint := s.endpoint
	headers := map[string]string{}
	if s.signTarget != nil {
		endpoint, headers, err = s.signTarget(s.endpoint, s.secret, body)
		if err != nil {
			return fmt.Errorf("sign request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ongrid-notify/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}
	return nil
}

func formatText(msg Message) string {
	parts := []string{fmt.Sprintf("[%s] %s", strings.ToUpper(string(msg.Severity)), msg.Subject)}
	if msg.Body != "" {
		parts = append(parts, msg.Body)
	}
	if msg.Source != "" {
		parts = append(parts, "source: "+msg.Source)
	}
	if msg.DedupeKey != "" {
		parts = append(parts, "dedupe: "+msg.DedupeKey)
	}
	return strings.Join(parts, "\n")
}

func signGenericWebhook(endpoint string, secret string, body []byte) (string, map[string]string, error) {
	headers := map[string]string{}
	if secret == "" {
		return endpoint, headers, nil
	}
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write(body); err != nil {
		return "", nil, err
	}
	headers["X-Ongrid-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return endpoint, headers, nil
}

func signFeishu(timestamp, secret string) string {
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func signDingTalkURL(endpoint, secret string, _ []byte) (string, map[string]string, error) {
	if secret == "" {
		return endpoint, nil, nil
	}
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	stringToSign := ts + "\n" + secret
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte(stringToSign)); err != nil {
		return "", nil, err
	}
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", nil, err
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil, nil
}
