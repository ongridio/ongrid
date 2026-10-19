package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// ToolNameSendNotification is the assistant-facing wire name. It sends a
// one-way notification through a configured notification channel.
const ToolNameSendNotification = "send_notification"

// ToolNameSendIMMessage sends a message through a configured two-way IM app
// to an explicit platform group ID. It is distinct from notification delivery.
const ToolNameSendIMMessage = "send_im_message"

// NotificationChannel is one configured outbound notification channel,
// narrowed to what the tool needs to resolve + report it.
type NotificationChannel struct {
	ID   uint64
	Name string
	Kind string
}

// NotificationSender is the seam to the notification-channel store + notify router. Implemented in
// cmd/main.go over the alert channel repo + notify.Router (same
// BuildSenderFromChannel path the alert notifier / flow notify node use), so
// this package stays decoupled from the data layer.
type NotificationSender interface {
	ListNotificationChannels(ctx context.Context) ([]NotificationChannel, error)
	SendNotification(ctx context.Context, channelID uint64, title, text string) error
}

// IMMessageSender sends through an IM Bridge application. GroupID is the
// platform conversation identifier, such as a Feishu open_chat_id, Telegram
// chat_id, or Slack channel ID.
type IMMessageSender interface {
	SendIMGroupMessage(ctx context.Context, imAppID uint64, groupID, text string) error
}

// SendNotificationTool lets the assistant proactively push a message through
// a configured notification channel. It does not target a two-way IM session.
type SendNotificationTool struct {
	sender NotificationSender
	log    *slog.Logger
}

// NewSendNotificationTool builds the assistant-facing notification tool.
func NewSendNotificationTool(s NotificationSender, log *slog.Logger) *SendNotificationTool {
	if log == nil {
		log = slog.Default()
	}
	return &SendNotificationTool{sender: s, log: log}
}

var sendNotificationSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "channel": { "type": "string", "description": "目标通知渠道名——设置→通知中配置的飞书 / 钉钉 / Slack / Telegram 等渠道名称；不是双向 IM 机器人。" },
    "text": { "type": "string", "description": "要发送的正文（纯文本，可带换行）。" },
    "title": { "type": "string", "description": "可选标题 / 主题。" }
  },
  "required": ["channel", "text"]
}`)

const sendNotificationWhenToUse = "用户要把某个结论 / 通知主动推送到飞书、钉钉等群里时用（比如\"把这段诊断发到运维群\"）。" +
	"channel 传“设置→通知”中配置的通知渠道名；它不是双向 IM 机器人。"

// Info — Class=write: it sends a real message (side-effecting, viewers can't
// use it) but it is not destructive.
func (t *SendNotificationTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameSendNotification,
		Description: "Send a message through a configured notification channel (Feishu / DingTalk / Slack / Telegram / WeCom). Pass the channel name from Settings → Notifications; this does not target a two-way IM bot.",
		WhenToUse:   sendNotificationWhenToUse,
		Parameters:  sendNotificationSchema,
		Class:       "write",
	}, nil
}

type sendNotificationArgs struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
	Title   string `json:"title"`
}

// SendIMMessageTool sends a message to an explicit group through a configured
// two-way IM app. It deliberately requires both the local IM app ID and the
// platform group ID: a group ID is only meaningful within one provider app.
type SendIMMessageTool struct {
	sender IMMessageSender
	log    *slog.Logger
}

// NewSendIMMessageTool builds the two-way IM group-message tool.
func NewSendIMMessageTool(s IMMessageSender, log *slog.Logger) *SendIMMessageTool {
	if log == nil {
		log = slog.Default()
	}
	return &SendIMMessageTool{sender: s, log: log}
}

var sendIMMessageSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "im_app_id": { "type": "integer", "description": "Configured IM app ID from Settings → IM." },
    "group_id": { "type": "string", "description": "Target platform group/conversation ID. Examples: Feishu open_chat_id, Telegram chat_id, or Slack channel ID." },
    "text": { "type": "string", "description": "Message body. Markdown support follows the selected IM provider." }
  },
  "required": ["im_app_id", "group_id", "text"]
}`)

const sendIMMessageWhenToUse = "Use when the user explicitly asks to send a message to a known group through a two-way IM app. " +
	"This is not an alert notification: provide the configured IM app ID and the platform group ID."

func (t *SendIMMessageTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameSendIMMessage,
		Description: "Send a message to an explicit group through a configured IM app. Requires im_app_id and group_id; use send_notification for configured notification targets.",
		WhenToUse:   sendIMMessageWhenToUse,
		Parameters:  sendIMMessageSchema,
		Class:       "write",
	}, nil
}

type sendIMMessageArgs struct {
	IMAppID uint64 `json:"im_app_id"`
	GroupID string `json:"group_id"`
	Text    string `json:"text"`
}

func (t *SendIMMessageTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.sender == nil {
		return "", fmt.Errorf("send_im_message: IM sender not wired")
	}
	var in sendIMMessageArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("send_im_message: bad args: %w", err)
	}
	in.GroupID = strings.TrimSpace(in.GroupID)
	in.Text = strings.TrimSpace(in.Text)
	if in.IMAppID == 0 || in.GroupID == "" || in.Text == "" {
		return "", fmt.Errorf("send_im_message: im_app_id, group_id, and text are required")
	}
	if err := t.sender.SendIMGroupMessage(ctx, in.IMAppID, in.GroupID, in.Text); err != nil {
		return "", fmt.Errorf("send_im_message: send to group %q: %w", in.GroupID, err)
	}
	t.log.Info("send_im_message: sent", slog.Uint64("im_app_id", in.IMAppID), slog.String("group_id", in.GroupID))
	out, err := json.Marshal(map[string]any{"sent": true, "im_app_id": in.IMAppID, "group_id": in.GroupID})
	if err != nil {
		return "", fmt.Errorf("send_im_message: marshal result: %w", err)
	}
	return string(out), nil
}

// InvokableRun resolves the channel by name (case-insensitive) and sends.
// A miss returns the available channel names so the LLM can self-correct.
func (t *SendNotificationTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.sender == nil {
		return "", fmt.Errorf("send_notification: channels not wired")
	}
	var in sendNotificationArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("send_notification: bad args: %w", err)
	}
	in.Channel = strings.TrimSpace(in.Channel)
	if in.Channel == "" || strings.TrimSpace(in.Text) == "" {
		return "", fmt.Errorf("send_notification: channel and text are required")
	}
	chans, err := t.sender.ListNotificationChannels(ctx)
	if err != nil {
		return "", fmt.Errorf("send_notification: list channels: %w", err)
	}
	var target *NotificationChannel
	for i := range chans {
		if strings.EqualFold(chans[i].Name, in.Channel) {
			target = &chans[i]
			break
		}
	}
	if target == nil {
		names := make([]string, 0, len(chans))
		for _, c := range chans {
			names = append(names, c.Name)
		}
		if len(names) == 0 {
			return "", fmt.Errorf("send_notification: no notification channels configured. Add one under Settings → Notifications first")
		}
		return "", fmt.Errorf("send_notification: channel %q not found. Available channels: %s", in.Channel, strings.Join(names, ", "))
	}
	if err := t.sender.SendNotification(ctx, target.ID, in.Title, in.Text); err != nil {
		return "", fmt.Errorf("send_notification: send to %q: %w", target.Name, err)
	}
	t.log.Info("send_notification: sent", slog.String("channel", target.Name), slog.String("kind", target.Kind))
	out, _ := json.Marshal(map[string]any{"sent": true, "channel": target.Name, "kind": target.Kind})
	return string(out), nil
}
