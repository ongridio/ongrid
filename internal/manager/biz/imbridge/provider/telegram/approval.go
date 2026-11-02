package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	bridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"strconv"
	"strings"
	"time"
)

type callbackQuery struct {
	ID   string `json:"id"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Message *struct {
		MessageID int `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
	Data string `json:"data"`
}

func telegramApprovalBody(chat string, card bridge.ApprovalCard) map[string]any {
	body := map[string]any{"chat_id": chat, "text": card.PlainText(), "link_preview_options": map[string]bool{"is_disabled": true}}
	buttons := []map[string]string{}
	if card.Buttons.Approve != "" {
		buttons = append(buttons, map[string]string{"text": card.Label("批准", "Approve"), "callback_data": card.Buttons.Approve})
	}
	if card.Buttons.Reject != "" {
		buttons = append(buttons, map[string]string{"text": card.Label("拒绝", "Reject"), "callback_data": card.Buttons.Reject})
	}
	if card.ReviewURL != "" {
		buttons = append(buttons, map[string]string{"text": card.Label("查看详情", "Details"), "url": card.ReviewURL})
	}
	rows := [][]map[string]string{}
	if len(buttons) > 0 {
		rows = append(rows, buttons)
	}
	body["reply_markup"] = map[string]any{"inline_keyboard": rows}
	return body
}
func (s senderAdapter) SendApproval(ctx context.Context, card bridge.ApprovalCard) (string, error) {
	raw, err := s.client.call(ctx, "sendMessage", telegramApprovalBody(s.chatID, card))
	if err != nil {
		return "", err
	}
	var out struct {
		ID int `json:"message_id"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.ID == 0 {
		return "", fmt.Errorf("telegram approval: missing message id")
	}
	return strconv.Itoa(out.ID), nil
}
func (s senderAdapter) EditApproval(ctx context.Context, id string, card bridge.ApprovalCard) error {
	mid, err := strconv.Atoi(id)
	if err != nil {
		return err
	}
	body := telegramApprovalBody(s.chatID, card)
	body["message_id"] = mid
	_, err = s.client.call(ctx, "editMessageText", body)
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}
func (c *StreamClient) handleCallback(parent context.Context, q *callbackQuery) error {
	if q == nil || q.Message == nil {
		return nil
	}
	user := strconv.FormatInt(q.From.ID, 10)
	_, allowed := c.allowed[user]
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	err := error(errs.ErrForbidden)
	if allowed {
		err = c.bridge.HandleApprovalCallback(ctx, bridge.ApprovalCallback{ConnectionIdentity: bridge.ConnectionIdentity(c.app), Provider: "telegram", AppIdentifier: c.app.AppID, ChatID: strconv.FormatInt(q.Message.Chat.ID, 10), MessageID: strconv.Itoa(q.Message.MessageID), Subject: bridge.Subject{User: user}, Token: q.Data})
	}
	message := bridge.ApprovalReceipt(err, c.app.DefaultLocale, "telegram")
	ack, done := context.WithTimeout(parent, 2*time.Second)
	defer done()
	_, ackErr := c.client.call(ack, "answerCallbackQuery", map[string]any{"callback_query_id": q.ID, "text": message, "show_alert": err != nil})
	return ackErr
}
