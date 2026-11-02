package imbridge

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatprotocol"
)

// Sender is the minimum platform-agnostic outbound surface. Providers
// that can replace an existing message may also implement MessageEditor.
type Sender interface {
	// SendText creates a new text message in the target chat.
	SendText(ctx context.Context, receiveID, receiveIDType, text string) (messageID string, err error)
}

// MessageEditor is implemented by providers such as Feishu, Telegram,
// and Slack. DingTalk's session webhook only creates messages, so it
// intentionally does not implement this interface.
// MessageSplitter returns provider-sized Markdown pages.
type MessageSplitter interface {
	SplitText(text string) []string
}

type MessageEditor interface {
	// EditText replaces the body of a previously-sent text message.
	EditText(ctx context.Context, messageID, text string) error
}

// streamEditor turns the SSE-shape chatprotocol.Event stream into throttled
// EditText calls on the platform. It assumes the assistant text grows
// monotonically — we always send the full accumulated buffer, never
// deltas, because Feishu / DingTalk both replace rather than append on
// edit.
type streamEditor struct {
	ctx           context.Context
	sender        Sender
	editor        MessageEditor
	chatID        string
	receiveIDType string
	messageID     string // initially the placeholder id; "" if placeholder send failed
	locale        string // app.DefaultLocale, drives the OnFatal apology language
	log           *slog.Logger

	publicURL        string
	durableApprovals bool
	approvalReader   ApprovalStatusReader
	sessionID        string
	approvals        []approvalNotice
	flushMu          sync.Mutex
	pages            []sentPage
	mu               sync.Mutex
	buf              string
	lastAt           time.Time
	// throttle: every 800ms or 200 char delta
}

const (
	editIntervalMs   = 800
	editCharsTrigger = 200
)

func newStreamEditor(ctx context.Context, sender Sender, chatID, receiveIDType, placeholderMessageID, locale string, log *slog.Logger) *streamEditor {
	editor, _ := sender.(MessageEditor)
	return &streamEditor{
		ctx:           ctx,
		sender:        sender,
		editor:        editor,
		chatID:        chatID,
		receiveIDType: receiveIDType,
		messageID:     placeholderMessageID,
		locale:        locale,
		log:           log,
	}
}

// OnEvent is wired as the emit callback for chatprotocol.RunStreamWithOpts.
// We only care about EventAssistant (text chunks) and EventDone
// (terminal — force-flush). Tool calls / task notifications are
// suppressed in IM for now; they'd be too noisy as inline chat
// messages.
func (e *streamEditor) OnEvent(ev chatprotocol.Event) {
	switch ev.Type {
	case chatprotocol.EventApprovalPending:
		if err := e.notifyApproval(ev.Approval); err != nil {
			e.log.Warn("imbridge: approval notice failed", slog.Any("err", err))
		}
	case chatprotocol.EventToolEnd:
		if err := e.refreshApprovals(); err != nil {
			e.log.Warn("imbridge: approval status refresh failed", slog.Any("err", err))
		}
	case chatprotocol.EventAssistant:
		// Pull the text content out of the event. chatprotocol.Event for
		// assistant carries the persisted assistant turn — we
		// concatenate its Content field. If the runtime later
		// exposes a per-chunk delta we should switch to that to
		// avoid quadratic edit traffic.
		text := assistantText(ev)
		if text == "" {
			return
		}
		e.mu.Lock()
		e.buf = text
		shouldFlush := e.editor != nil && e.shouldFlushLocked()
		e.mu.Unlock()
		if shouldFlush {
			e.flush()
		}
	case chatprotocol.EventDone:
		e.flush() // terminal: ensure last chunk lands
	default:
		// EventToolStart / EventToolEnd / EventTaskNotification —
		// surfaced in the web UI's per-turn timeline; in IM they'd
		// fragment the message and confuse users. Drop for now.
	}
}

// OnFatal is called by the bridge when the agent run aborted — we
// replace the placeholder with the error so the IM user gets a clear
// signal instead of a stale "思考中…".
func (e *streamEditor) OnFatal(err error) error {
	prefix := "⚠ 助手执行失败："
	if strings.ToLower(strings.TrimSpace(e.locale)) == "en" {
		prefix = "⚠ Assistant failed: "
	}
	e.mu.Lock()
	e.buf = prefix + err.Error()
	e.mu.Unlock()
	return e.flush()
}

// Flush is the bridge's final post-run nudge — ensures the last buf
// has been written even if the agent terminated without emitting Done.
func (e *streamEditor) Flush() error {
	// A status lookup failure must not suppress the assistant's final answer.
	return errors.Join(e.flush(), e.refreshApprovals())
}

func (e *streamEditor) shouldFlushLocked() bool {
	if time.Since(e.lastAt) >= editIntervalMs*time.Millisecond {
		return true
	}
	return false
}

type sentPage struct{ id, text string }

func (e *streamEditor) flush() error {
	e.flushMu.Lock()
	defer e.flushMu.Unlock()
	if splitter, ok := e.sender.(MessageSplitter); ok {
		return e.flushPages(splitter)
	}

	e.mu.Lock()
	buf := e.buf
	mid := e.messageID
	e.lastAt = time.Now()
	e.mu.Unlock()
	if buf == "" {
		return nil
	}
	if mid == "" {
		// No placeholder exists. This is the normal path for providers
		// without message editing and the fallback path after a failed
		// placeholder send.
		newID, err := e.sender.SendText(e.ctx, e.chatID, e.receiveIDType, buf)
		if err != nil {
			e.log.Warn("imbridge: fallback send failed", slog.Any("err", err))
			return err
		}
		e.mu.Lock()
		e.messageID = newID
		e.mu.Unlock()
		return nil
	}
	if e.editor == nil {
		// A one-shot provider already sent its final response. EventDone
		// and the bridge's final Flush may both arrive; do not duplicate it.
		return nil
	}
	if err := e.editor.EditText(e.ctx, mid, buf); err != nil {
		e.log.Warn("imbridge: edit failed", slog.String("message_id", mid), slog.Any("err", err))
		return err
	}
	return nil
}

// assistantText pulls the persisted assistant text out of an
// EventAssistant payload. The agent runtime stores the full
// accumulated assistant turn (not per-token deltas) on the event so we
// can just take it.
func assistantText(ev chatprotocol.Event) string {
	// chatprotocol.Event.Assistant is set on assistant events — see
	// internal/manager/biz/aiops/chatprotocol/types.go. We avoid a direct
	// type assertion on the field shape to stay forward-compatible:
	// if the runtime adds richer payloads (citations, attachments)
	// they live on the same Event struct.
	if ev.Assistant == nil {
		return ""
	}
	return ev.Assistant.Content
}

// flushPages records each successful page immediately so a later failure can
// retry the remainder without duplicating pages already delivered.
func (e *streamEditor) flushPages(splitter MessageSplitter) error {
	e.mu.Lock()
	body := e.buf
	e.lastAt = time.Now()
	e.mu.Unlock()
	if body == "" {
		return nil
	}
	parts := splitter.SplitText(body)
	if len(e.pages) == 0 && e.messageID != "" {
		e.pages = append(e.pages, sentPage{id: e.messageID})
	}
	for i, part := range parts {
		if i < len(e.pages) {
			if e.pages[i].text == part || e.editor == nil {
				continue
			}
			if err := e.editor.EditText(e.ctx, e.pages[i].id, part); err != nil {
				return err
			}
			e.pages[i].text = part
		} else {
			id, err := e.sender.SendText(e.ctx, e.chatID, e.receiveIDType, part)
			if err != nil {
				return err
			}
			e.pages = append(e.pages, sentPage{id: id, text: part})
		}
	}
	// A rewritten/fatal response can be shorter than the streamed draft.
	// Clear stale trailing content instead of leaving contradictory output.
	if e.editor != nil {
		note := "（内容已更新，请查看前文）"
		if strings.HasPrefix(strings.ToLower(e.locale), "en") {
			note = "(Content updated; see the preceding message.)"
		}
		for i := len(parts); i < len(e.pages); i++ {
			if e.pages[i].text == note {
				continue
			}
			if err := e.editor.EditText(e.ctx, e.pages[i].id, note); err != nil {
				return err
			}
			e.pages[i].text = note
		}
	}
	return nil
}
