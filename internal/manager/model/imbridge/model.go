// Package imbridge holds the storage shape for the multi-turn IM
// bridge. An ImApp is the manager-side registration of a
// Feishu / DingTalk bot application; an ImThread maps one IM
// conversation (group chat / DM / reply thread) to an ongrid
// chat_session so messages from the same IM thread flow into the
// same agent conversation.
package imbridge

import (
	"time"

	"gorm.io/plugin/soft_delete"
)

const (
	ProviderFeishu   = "feishu"
	ProviderDingTalk = "dingtalk"
	// ProviderTelegram is stream-only: the manager long-polls getUpdates
	// (outbound, proxy-friendly), there is no webhook path. app_id = bot
	// username/id, app_secret = the BotFather token. See ADR-031.
	ProviderTelegram = "telegram"
	// ProviderSlack is stream-only via Socket Mode: the manager opens an
	// outbound WebSocket to wss-primary.slack.com after fetching a
	// connection URL via apps.connections.open. No public ingress is
	// required (mirrors Telegram getUpdates philosophy → proxy-friendly).
	// Slack needs TWO tokens — an app-level token (xapp-...) for the
	// WebSocket and a bot user token (xoxb-...) for chat.postMessage /
	// chat.update. They're stored together in app_secret as JSON:
	//   {"app_token":"xapp-...","bot_token":"xoxb-..."}
	// app_id is the Slack workspace team id (e.g. "T0123ABCD") or any
	// stable label the operator picks; only used as the uniqueness key
	// alongside provider.
	ProviderSlack = "slack"
)

// Mode selects how inbound events reach the manager. Stream mode is
// the preferred path for private-cloud deploys where the manager
// can't expose a public webhook URL — the manager dials out to the
// platform's WebSocket-over-TLS event channel and receives events
// inline. Webhook mode is the classic HTTP callback shape with
// signature verification; kept as a fallback for deployments behind
// outbound-restrictive firewalls.
const (
	ModeStream  = "stream"
	ModeWebhook = "webhook"
)

// ImApp is one configured IM bot application — credentials + flags. A
// single ImApp serves N IM threads (a group + many DMs). app_id is the
// platform-side identifier (Feishu app_id, DingTalk AppKey); the secret
// is encrypted at rest by SystemSetting reveal/store flow.
type ImApp struct {
	ID       uint64 `gorm:"primaryKey;autoIncrement"`
	Provider string `gorm:"size:16;not null;uniqueIndex:uk_provider_app_id,priority:1"`
	// Mode = ModeStream (default) or ModeWebhook. Stream apps are
	// supervised by imbridge.StreamSupervisor; webhook apps wait for
	// inbound HTTP at /api/v1/im/{provider}/events.
	Mode        string `gorm:"size:16;not null;default:stream"`
	Name        string `gorm:"size:128;not null"`
	AppID       string `gorm:"column:app_id;size:128;not null;uniqueIndex:uk_provider_app_id,priority:2"`
	AppSecret   string `gorm:"column:app_secret;type:text;not null"`
	VerifyToken string `gorm:"column:verify_token;size:128"`
	EncryptKey  string `gorm:"column:encrypt_key;size:128"`
	// AllowFrom is the sender allowlist for PUBLICLY-discoverable
	// providers (Telegram): comma/space/newline-separated numeric
	// Telegram user IDs. Only these users may converse with the bot;
	// everyone else is silently ignored. EMPTY = deny-all for Telegram
	// (a public bot with no allowlist is the exact "anyone can reach the
	// platform" risk — see ADR-031, OpenClaw issue #73756). Unused by
	// Feishu/DingTalk, which are gated by enterprise-tenant membership.
	AllowFrom string `gorm:"column:allow_from;type:text"`
	// IdleTimeoutSeconds is kept for legacy installs but is currently
	// unused — sessions don't auto-rotate any more. Future "long
	// conversation context window" work might re-introduce it as a
	// soft window cap rather than a hard rotate. 0 = no behaviour.
	IdleTimeoutSeconds int `gorm:"column:idle_timeout_seconds;not null;default:0"`
	// DefaultLocale picks the language directive the bridge appends to
	// every user message before handing off to the agent — so a Slack
	// workspace whose admin set this to "en" gets English replies even
	// though the agent persona is written in Chinese. Empty = no
	// directive (LLM mirrors the user). Accepted: "en", "zh".
	// Mirrors [[feedback_ai_output_locale]] for the IM path; RCA has its
	// own Config.DefaultLocale on the investigator usecase.
	DefaultLocale string `gorm:"column:default_locale;size:8;not null;default:''"`
	Enabled       bool   `gorm:"not null;default:true"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     *time.Time            `gorm:"index;column:deleted_at"`
	DeleteMarker  soft_delete.DeletedAt `gorm:"column:delete_marker;not null;default:0;softDelete:milli,DeletedAtField:DeletedAt;uniqueIndex:uk_provider_app_id,priority:3"`
}

func (ImApp) TableName() string { return "im_apps" }

// ImThread maps one IM conversation (Feishu chat_id + optional root_id
// for reply threads; DingTalk conversationId) to one ongrid
// chat_session. The mapping is owned by the ImApp it was created
// under, so two apps in the same group don't collide.
// ImThread is the session-mapping row. Key shape is
// (im_app_id, im_chat_id, im_thread_id) — one session per chat
// (group / DM), shared by everyone in it. The bot is one shared
// assistant for the room, not a per-user agent; this keeps session
// row growth at O(active chats) rather than O(users × chats × time).
//
// Rotation only happens on explicit /new from a user; idle timeouts
// don't auto-rotate any more (would cause unbounded row growth on
// chatty channels).
//
// ImSenderID is recorded (for audit + future S3 binding) but is
// NOT part of the uniqueness key.
type ImThread struct {
	ID         uint64 `gorm:"primaryKey;autoIncrement"`
	ImAppID    uint64 `gorm:"column:im_app_id;not null;uniqueIndex:uk_app_chat,priority:1;index:idx_app_chat"`
	Provider   string `gorm:"size:16;not null"`
	ImChatID   string `gorm:"column:im_chat_id;size:128;not null;uniqueIndex:uk_app_chat,priority:2"`
	ImThreadID string `gorm:"column:im_thread_id;size:128;uniqueIndex:uk_app_chat,priority:3"`
	// ImSenderID is the most recent sender — recorded for audit /
	// future per-user binding but does NOT split the
	// session mapping. All senders in a chat share one session.
	ImSenderID      string `gorm:"column:im_sender_id;size:128"`
	OngridSessionID string `gorm:"column:ongrid_session_id;size:128;not null;index:idx_session"`
	LastSeenAt      time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (ImThread) TableName() string { return "im_threads" }
