package slack

import (
	"context"
	"encoding/json"
	"fmt"
	bridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	"strings"
)

func slackApprovalBody(channel string, card bridge.ApprovalCard) map[string]any {
	text := card.PlainText()
	blocks := []map[string]any{}
	runes := []rune(text)
	for len(runes) > 0 {
		n := min(len(runes), 2500)
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]string{"type": "plain_text", "text": string(runes[:n])}})
		runes = runes[n:]
	}
	body := map[string]any{"channel": channel, "text": text, "blocks": blocks, "unfurl_links": false, "unfurl_media": false}
	actions := []map[string]any{}
	for _, a := range []struct{ label, token, style string }{{card.Label("批准", "Approve"), card.Buttons.Approve, "primary"}, {card.Label("拒绝", "Reject"), card.Buttons.Reject, "danger"}} {
		if a.token != "" {
			actions = append(actions, map[string]any{"type": "button", "text": map[string]string{"type": "plain_text", "text": a.label}, "value": a.token, "action_id": "ongrid_" + a.style, "style": a.style, "confirm": map[string]any{"title": map[string]string{"type": "plain_text", "text": card.Label("确认审批", "Confirm decision")}, "text": map[string]string{"type": "plain_text", "text": card.Label("请核对本卡片展示的操作详情和范围。", "Review the operation details shown in this card before confirming.")}, "confirm": map[string]string{"type": "plain_text", "text": card.Label("确认", "Confirm")}, "deny": map[string]string{"type": "plain_text", "text": card.Label("取消", "Cancel")}}})
		}
	}
	if card.ReviewURL != "" {
		actions = append(actions, map[string]any{"type": "button", "text": map[string]string{"type": "plain_text", "text": card.Label("查看详情", "Details")}, "url": card.ReviewURL, "action_id": "ongrid_details"})
	}
	if len(actions) > 0 {
		body["blocks"] = append(body["blocks"].([]map[string]any), map[string]any{"type": "actions", "elements": actions})
	}
	return body
}
func (s senderAdapter) SendApproval(ctx context.Context, card bridge.ApprovalCard) (string, error) {
	var out struct {
		TS string `json:"ts"`
	}
	body := slackApprovalBody(s.channel, card)
	if s.threadTS != "" {
		body["thread_ts"] = s.threadTS
	}
	err := s.client.call(ctx, "chat.postMessage", s.client.botToken, body, &out)
	if err == nil && out.TS == "" {
		err = fmt.Errorf("slack approval: missing message id")
	}
	return out.TS, err
}
func (s senderAdapter) EditApproval(ctx context.Context, id string, card bridge.ApprovalCard) error {
	body := slackApprovalBody(s.channel, card)
	body["ts"] = id
	return s.client.call(ctx, "chat.update", s.client.botToken, body, nil)
}
func (c *StreamClient) handleInteraction(ctx context.Context, raw json.RawMessage) error {
	var p struct {
		Type string `json:"type"`
		Team struct {
			ID string `json:"id"`
		} `json:"team"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
		Container struct {
			MessageTS string `json:"message_ts"`
		} `json:"container"`
		Actions []struct {
			Value    string `json:"value"`
			ActionID string `json:"action_id"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.Type != "block_actions" || len(p.Actions) != 1 || p.Actions[0].ActionID == "ongrid_details" {
		return nil
	}
	if _, ok := c.allowed[p.User.ID]; !ok {
		return nil
	}
	err := c.bridge.HandleApprovalCallback(ctx, bridge.ApprovalCallback{ConnectionIdentity: bridge.ConnectionIdentity(c.app), Provider: "slack", AppIdentifier: c.app.AppID, ChatID: p.Channel.ID, MessageID: p.Container.MessageTS, Subject: bridge.Subject{Scope: p.Team.ID, User: p.User.ID}, Token: p.Actions[0].Value})
	message := bridge.ApprovalReceipt(err, c.app.DefaultLocale, "slack")
	// 私有回执不泄露审批参数；失败不改变已持久化的业务决策。
	if receiptErr := c.client.call(ctx, "chat.postEphemeral", c.client.botToken, map[string]string{"channel": p.Channel.ID, "user": p.User.ID, "text": message}, nil); receiptErr != nil {
		c.log.Warn("slack approval receipt unavailable")
	}
	return err
}

// Slack 会将 /ongrid-bind 发为 slash_commands（不会产生普通 message 事件）。
func (c *StreamClient) handleBindingCommand(ctx context.Context, raw json.RawMessage) error {
	var p struct {
		Command   string `json:"command"`
		Text      string `json:"text"`
		TeamID    string `json:"team_id"`
		UserID    string `json:"user_id"`
		ChannelID string `json:"channel_id"`
		TriggerID string `json:"trigger_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.Command != "/ongrid-bind" {
		return nil
	}
	if _, ok := c.allowed[p.UserID]; !ok {
		return nil
	}
	return c.bridge.HandleInbound(ctx, senderAdapter{client: c.client, channel: p.ChannelID}, bridge.InboundMessage{Provider: "slack", AppID: c.app.AppID, ConnectionIdentity: bridge.ConnectionIdentity(c.app), ChatID: p.ChannelID, OpenID: p.UserID, Subject: bridge.Subject{Scope: p.TeamID, User: p.UserID}, Private: strings.HasPrefix(p.ChannelID, "D"), Text: "/bind " + p.Text, EventID: p.TriggerID, ReceiveIDType: "channel"})
}
