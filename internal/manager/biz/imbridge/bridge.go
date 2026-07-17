// Package imbridge wires the IM platforms to the existing chat agent
// runtime. Inbound webhooks land here after their handler has verified
// + decrypted them; this layer maps the IM thread to an ongrid
// chat_session, drives the agent run, and progressively edits the IM
// message as SSE chunks arrive.
package imbridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/agent"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// Repo is the narrow data-layer surface this bridge needs. Implemented
// by data/imbridge/store.Repo.
type Repo interface {
	GetAppByAppID(ctx context.Context, provider, appID string) (*model.ImApp, error)
	FindThread(ctx context.Context, imAppID uint64, imChatID, imThreadID string) (*model.ImThread, error)
	CreateThread(ctx context.Context, t *model.ImThread) error
	TouchThread(ctx context.Context, id uint64) error
	RotateThreadSession(ctx context.Context, threadID uint64, newSessionID string) error
}

// AgentSession is the interface this bridge needs from the aiops
// runtime. Wires to internal/manager/service/aiops.Service (which
// already exposes PostMessageStreamWithOpts). We keep the dependency
// abstract so the bridge can be tested with a fake.
type AgentSession interface {
	// EnsureSession lazily allocates a chat_session for the given
	// (ownerUserID, label) and returns its ID. The label is a
	// human-readable hint operators see in the chat history (e.g.
	// "Feishu · group X"). Implementations may key on label to avoid
	// duplicate creation on retries.
	EnsureSession(ctx context.Context, ownerUserID uint64, label string) (sessionID string, err error)
	// StreamMessage posts userContent to the given session and calls
	// emit for each agent event. The bridge ignores everything except
	// EventAssistant (which carries the assistant text chunks) and
	// EventDone (terminal). This is a thin wrapper over
	// service.Service.PostMessageStreamWithOpts.
	StreamMessage(ctx context.Context, sessionID string, userContent string, emit agent.Emit) error
}

// Bridge is the singleton wired into manager main.go. Exported methods
// are called from the HTTP webhook handler (provider-agnostic
// dispatch happens inside).
type Bridge struct {
	repo  Repo
	agent AgentSession
	// ServiceUserID is the ongrid users.id used as owner_user_id for
	// IM-originated chat sessions until per-IM-user binding (S3)
	// lands. Configured at boot via NewBridge.
	serviceUserID uint64
	// per-provider client cache so we don't rebuild HTTP / token
	// state on every inbound event. Keyed by app_id.
	feishuCache sync.Map // app_id -> *feishu.Client
	// seen dedups re-delivered events (Telegram getUpdates replays the
	// unacked batch on a poller reconnect) so the agent doesn't answer the
	// same message twice. Keyed by (provider, app_id, event_id).
	seen *dedupSet
	log  *slog.Logger
}

func NewBridge(repo Repo, agent AgentSession, serviceUserID uint64, log *slog.Logger) *Bridge {
	if log == nil {
		log = slog.Default()
	}
	return &Bridge{
		repo:          repo,
		agent:         agent,
		serviceUserID: serviceUserID,
		seen:          newDedupSet(2048),
		log:           log.With(slog.String("comp", "imbridge")),
	}
}

// InboundMessage describes a normalized inbound event the bridge can
// process. The provider-specific webhook handler decrypts + decodes
// to this shape so the bridge stays platform-agnostic.
type InboundMessage struct {
	Provider      string // "feishu" | "dingtalk"
	AppID         string // platform-side app_id (== ImApp.AppID)
	ChatID        string // platform chat / conversation id
	ThreadID      string // optional reply thread id (Feishu root_id)
	OpenID        string // platform user id of the sender (we currently use this only for logging; S3 will bind)
	UserName      string // sender display name (logging only)
	Text          string // normalized message body — text/plain
	EventID       string // platform event id, used for dedup
	ReceiveIDType string // platform-specific hint: "chat_id" / "open_id" / "union_id"
}

// HandleInbound resolves the IM thread → ongrid session and kicks off
// the agent stream. Session rules:
//
//   - Map key is (im_app_id, im_chat_id, thread_id). One session per
//     chat — every user in a group shares it. The bot remembers what
//     anyone in the room previously said. Feishu reply threads stay
//     independent (their own thread_id).
//   - Sessions are stable: there is NO auto-rotation on idle. The
//     same mapping row points at the same ongrid session forever
//     unless a user explicitly resets.
//   - Reset: any user sends "/new" / "新会话" / "新建会话" / "重新开始"
//     — the bridge allocates a fresh ongrid session, rotates the
//     mapping pointer, and acknowledges with a short confirmation.
//     The current message is NOT fed to the agent (would otherwise
//     be split across the old + new session boundary).
//
// Row growth = O(active chats × active reply threads), no time
// component. Webhook callers MUST invoke this on a background
// goroutine: agent runs are 30s+ and the platform webhook ack
// deadline is 3s. The stream-mode caller does the same.
func (b *Bridge) HandleInbound(ctx context.Context, sender Sender, msg InboundMessage) error {
	if msg.Text == "" {
		b.log.Debug("inbound has no text — ignoring", slog.String("event_id", msg.EventID))
		return nil
	}

	// Dedup re-delivered events. Long-poll providers replay the unacked
	// batch on a reconnect; without this the agent would answer the same
	// message twice. Marked on entry (not on success) — a duplicate reply
	// is worse than not re-running a message that already started. Events
	// with no id (shouldn't happen for telegram/feishu) skip the check.
	if msg.EventID != "" {
		key := msg.Provider + ":" + msg.AppID + ":" + msg.EventID
		if b.seen.seenOrAdd(key) {
			b.log.Debug("inbound duplicate event — skipping",
				slog.String("provider", msg.Provider),
				slog.String("event_id", msg.EventID))
			return nil
		}
	}

	// 1. Resolve app + thread mapping (lazy create).
	app, err := b.repo.GetAppByAppID(ctx, msg.Provider, msg.AppID)
	if err != nil {
		return fmt.Errorf("resolve app: %w", err)
	}
	if !app.Enabled {
		return errors.New("imbridge: app disabled")
	}

	thread, err := b.repo.FindThread(ctx, app.ID, msg.ChatID, msg.ThreadID)
	if err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "not found") {
			return fmt.Errorf("find thread: %w", err)
		}
		thread = nil
	}

	wantNew := parseSlashCommand(msg.Text) == cmdNew
	now := time.Now().UTC()

	switch {
	case thread == nil:
		// First-ever message in this chat (+ thread). Allocate a
		// fresh ongrid session + write the mapping row.
		sid, err := b.agent.EnsureSession(ctx, b.serviceUserID, b.sessionLabel(msg))
		if err != nil {
			return fmt.Errorf("ensure session: %w", err)
		}
		thread = &model.ImThread{
			ImAppID:         app.ID,
			Provider:        msg.Provider,
			ImChatID:        msg.ChatID,
			ImThreadID:      msg.ThreadID,
			ImSenderID:      msg.OpenID,
			OngridSessionID: sid,
			LastSeenAt:      now,
		}
		if err := b.repo.CreateThread(ctx, thread); err != nil {
			return fmt.Errorf("create thread: %w", err)
		}
	case wantNew:
		// Explicit reset. Allocate a new session and rotate the
		// pointer in place; the old session row stays for audit.
		sid, err := b.agent.EnsureSession(ctx, b.serviceUserID, b.sessionLabel(msg))
		if err != nil {
			return fmt.Errorf("ensure rotated session: %w", err)
		}
		if err := b.repo.RotateThreadSession(ctx, thread.ID, sid); err != nil {
			return fmt.Errorf("rotate thread: %w", err)
		}
		b.log.Info("session rotated",
			slog.Uint64("thread_id", thread.ID),
			slog.String("reason", "/new command"),
			slog.String("new_session_id", sid))
		thread.OngridSessionID = sid
	default:
		_ = b.repo.TouchThread(ctx, thread.ID)
	}

	// 2. Slash-command short-circuit: confirm and stop. The current
	//    message wasn't a real question; running the agent on it
	//    would dirty the brand-new session.
	if wantNew {
		_, _ = sender.SendText(ctx, msg.ChatID, msg.ReceiveIDType,
			localizeReply(app.DefaultLocale, replyNewSession))
		return nil
	}

	// 3. Send a placeholder only when the provider can edit it. Providers
	//    backed by one-shot webhooks (DingTalk) buffer the stream and send
	//    the final answer once, avoiding one message per chunk.
	placeholder := ""
	if _, ok := sender.(MessageEditor); ok {
		placeholder, err = sender.SendText(ctx, msg.ChatID, msg.ReceiveIDType,
			localizeReply(app.DefaultLocale, replyThinking))
		if err != nil {
			b.log.Warn("placeholder send failed; falling back to one-shot reply", slog.Any("err", err))
		}
	}

	// 4. Run the agent with a throttled editor. Append a language directive
	//    when the channel pinned a locale so the agent replies in that
	//    language regardless of persona / model defaults. Empty locale =
	//    auto = LLM mirrors the user. See [[feedback_ai_output_locale]].
	editor := newStreamEditor(ctx, sender, msg.ChatID, msg.ReceiveIDType, placeholder, app.DefaultLocale, b.log)
	emit := func(e agent.Event) {
		editor.OnEvent(e)
	}
	userContent := msg.Text
	if d := localeDirective(app.DefaultLocale); d != "" {
		userContent = msg.Text + "\n\n" + d
	}
	if err := b.agent.StreamMessage(ctx, thread.OngridSessionID, userContent, emit); err != nil {
		_ = editor.OnFatal(err)
		return fmt.Errorf("stream message: %w", err)
	}
	return editor.Flush()
}

// localeDirective renders the language hint we append to the user content
// before handing to the agent. Empty locale = "" (no directive, LLM
// mirrors). Mirrors the RCA-side helper in alert/investigator but framed
// for chat replies, not RCA reports — see [[feedback_ai_output_locale]].
func localeDirective(locale string) string {
	switch strings.ToLower(strings.TrimSpace(locale)) {
	case "en":
		return "(LANGUAGE: Respond in English regardless of the language the system prompt or persona examples use.)"
	case "zh":
		return "（LANGUAGE：请用简体中文回复，无论 system prompt 或 persona 中的示例用什么语言。）"
	default:
		return ""
	}
}

// IM-bridge canned strings. Plain strings rather than i18n constants so the
// package stays self-contained; pick the locale at the call site.
const (
	replyNewSession = "newSession"
	replyThinking   = "thinking"
)

func localizeReply(locale string, key string) string {
	switch locale {
	case "en":
		switch key {
		case replyNewSession:
			return "✦ New session started. Go ahead — I'll listen from scratch."
		case replyThinking:
			return "✦ Thinking…"
		}
	}
	// Default (empty locale / zh): Chinese, matches the historical behaviour.
	switch key {
	case replyNewSession:
		return "✦ 已开启新会话。直接说出问题，我从头听。"
	case replyThinking:
		return "✦ 思考中…"
	}
	return ""
}

// sessionLabel is the chat-history-page title for ongrid-side
// sessions born from an IM thread. Group / DM chat_id is enough
// to disambiguate; sender isn't on the title since the session is
// shared across senders.
func (b *Bridge) sessionLabel(msg InboundMessage) string {
	base := fmt.Sprintf("%s · %s", msg.Provider, shortChatLabel(msg.ChatID))
	if msg.ThreadID != "" {
		base += " (thread)"
	}
	return base
}

// slashCommand enumerates the bot's reserved control verbs. Currently
// just /new (and Chinese alias 新会话 / 新建会话). Adding /show or /help
// later is just one branch each.
type slashCommand int

const (
	cmdNone slashCommand = iota
	cmdNew
)

func parseSlashCommand(text string) slashCommand {
	t := strings.TrimSpace(text)
	switch t {
	case "/new", "/newsession", "新会话", "新建会话", "重新开始":
		return cmdNew
	}
	return cmdNone
}

func shortChatLabel(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:4] + "…" + id[len(id)-4:]
}
