package imbridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// AdminRepo is the surface UC needs from the data layer. The webhook
// + stream paths use the more specific Repo interface; this is a
// superset that includes ImApp CRUD.
type AdminRepo interface {
	Repo
	ListApps(ctx context.Context, provider string) ([]*model.ImApp, error)
	GetApp(ctx context.Context, id uint64) (*model.ImApp, error)
	CreateApp(ctx context.Context, app *model.ImApp) error
	UpdateApp(ctx context.Context, app *model.ImApp) error
	DeleteApp(ctx context.Context, id uint64) error
}

// UC bundles the admin operations consumed by the HTTP handler.
type UC struct {
	repo   AdminRepo
	health *StreamHealthStore
}

// NewUC builds the admin usecase. health carries the supervisor's
// last-known stream state for the app list; nil is replaced by an empty
// store so callers that do not track health still work.
func NewUC(repo AdminRepo, health *StreamHealthStore) *UC {
	if health == nil {
		health = NewStreamHealthStore()
	}
	return &UC{repo: repo, health: health}
}

// AppInput is the mutation payload.
type AppInput struct {
	Provider      string
	Mode          string
	Name          string
	AppID         string
	AppSecret     string
	VerifyToken   string
	EncryptKey    string
	AllowFrom     string // Telegram / Slack sender allowlist; see ParseAllowFrom
	Enabled       bool
	DefaultLocale string // "" | "en" | "zh" — see model.ImApp.DefaultLocale
}

// ParseAllowFrom splits a raw allowlist (comma / space / newline / semicolon
// separated) into normalized numeric Telegram user IDs. `telegram:` / `tg:`
// prefixes are stripped (OpenClaw allowFrom compatibility). Non-numeric and
// negative tokens are dropped — only positive user IDs are valid (group /
// supergroup chat IDs are negative and don't belong in a sender allowlist).
// Order-preserving + de-duplicated. Shared by validate() and the Telegram
// provider's poll loop so the parse rule has exactly one definition.
func ParseAllowFrom(raw string) []string {
	seen := make(map[string]struct{})
	var out []string
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r' || r == ';'
	})
	for _, tok := range fields {
		tok = strings.TrimSpace(tok)
		tok = strings.TrimPrefix(tok, "telegram:")
		tok = strings.TrimPrefix(tok, "tg:")
		if tok == "" {
			continue
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	return out
}

func isNumericID(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func (in *AppInput) validate() error {
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	switch provider {
	case model.ProviderFeishu, model.ProviderDingTalk, model.ProviderTelegram, model.ProviderSlack:
	default:
		return fmt.Errorf("%w: provider must be feishu, dingtalk, telegram, or slack", errs.ErrInvalid)
	}
	// Normalize + whitelist default_locale. Empty = no directive
	// (LLM mirrors the user) — the legacy / "auto" mode. Anything outside
	// {en, zh} is rejected so a typo'd "EN-us" doesn't silently degrade
	// to no-directive behaviour. Strip the region tag down to the primary
	// subtag so en-US / zh-CN are also accepted.
	loc := strings.ToLower(strings.TrimSpace(in.DefaultLocale))
	if loc != "" {
		loc = strings.SplitN(loc, "-", 2)[0]
		switch loc {
		case "en", "zh":
		default:
			return fmt.Errorf("%w: default_locale must be empty (auto), en, or zh", errs.ErrInvalid)
		}
	}
	in.DefaultLocale = loc
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode == "" {
		mode = model.ModeStream
	}
	if mode != model.ModeStream && mode != model.ModeWebhook {
		return fmt.Errorf("%w: mode must be stream or webhook", errs.ErrInvalid)
	}
	in.Mode = mode
	if strings.TrimSpace(in.AppID) == "" {
		return fmt.Errorf("%w: app_id required", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: name required", errs.ErrInvalid)
	}
	// Webhook mode requires encrypt_key for signed/encrypted events;
	// stream mode doesn't. Verify token is optional in both modes.
	if mode == model.ModeWebhook && strings.TrimSpace(in.EncryptKey) == "" {
		return fmt.Errorf("%w: encrypt_key required in webhook mode", errs.ErrInvalid)
	}
	// Telegram: poll/stream-only, and the bot is publicly discoverable by
	// username, so it MUST carry a non-empty sender allowlist. An empty
	// allowlist would let anyone on Telegram command a tool-equipped agent
	// (ADR-031; OpenClaw issue #73756). Feishu/DingTalk skip this — they're
	// gated by enterprise-tenant membership.
	if provider == model.ProviderTelegram {
		if mode != model.ModeStream {
			return fmt.Errorf("%w: telegram only supports stream mode", errs.ErrInvalid)
		}
		raw := ParseAllowFrom(in.AllowFrom)
		// Telegram user IDs are numeric (BotFather + getMe return int64).
		// Drop non-numeric tokens here rather than in ParseAllowFrom so the
		// shared parser can be reused by Slack (whose IDs are letter-prefixed
		// U…). A typo'd "alice" lands as "no IDs left" → operator sees the
		// helpful required-error rather than a silently-empty allowlist.
		ids := make([]string, 0, len(raw))
		for _, id := range raw {
			if isNumericID(id) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return fmt.Errorf("%w: telegram requires allow_from — at least one numeric Telegram user ID (the bot is publicly reachable; an empty allowlist would let anyone command the agent)", errs.ErrInvalid)
		}
		in.AllowFrom = strings.Join(ids, ",") // canonicalize stored form
	}
	// Slack: Socket Mode is the only mode the manager supports (webhook
	// Events-API would require a public ingress; the whole point of
	// Socket Mode is to avoid that — same shape as Telegram getUpdates).
	// Allowlist semantics mirror Telegram: a Slack workspace bot accepts
	// messages from any workspace member by default, which on a public
	// or guest-rich workspace would let surprise users command the
	// agent. Require at least one Slack user_id (U…) in allow_from so
	// the operator must consciously open the door.
	if provider == model.ProviderSlack {
		if mode != model.ModeStream {
			return fmt.Errorf("%w: slack only supports stream mode (Socket Mode)", errs.ErrInvalid)
		}
		raw := ParseAllowFrom(in.AllowFrom)
		// Slack user IDs start with U (rare W for guests on Enterprise).
		// Reject obvious typos here so the operator sees a clear error
		// instead of "bot silently ignores everyone".
		ids := make([]string, 0, len(raw))
		for _, id := range raw {
			if len(id) >= 2 && (id[0] == 'U' || id[0] == 'W') {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return fmt.Errorf("%w: slack requires allow_from — at least one Slack user ID (e.g. UABC123, find via Profile → ⋯ → Copy member ID). Without it any workspace member could command a tool-equipped agent", errs.ErrInvalid)
		}
		in.AllowFrom = strings.Join(ids, ",") // canonicalize stored form
	}
	return nil
}

func (uc *UC) ListApps(ctx context.Context, provider string) ([]*model.ImApp, error) {
	return uc.repo.ListApps(ctx, provider)
}

// StreamHealth returns the last-known runtime health of one app's stream
// client. ok is false when the store has not observed the app yet — a
// manager that just booted, or an app the first reconcile tick has not
// reached. Callers must render that as unknown, not as stopped.
func (uc *UC) StreamHealth(appID uint64) (StreamHealth, bool) {
	return uc.health.Get(appID)
}

func (uc *UC) GetApp(ctx context.Context, id uint64) (*model.ImApp, error) {
	return uc.repo.GetApp(ctx, id)
}

func (uc *UC) CreateApp(ctx context.Context, in AppInput) (*model.ImApp, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.AppSecret) == "" {
		return nil, fmt.Errorf("%w: app_secret required", errs.ErrInvalid)
	}
	now := time.Now().UTC()
	app := &model.ImApp{
		Provider:      in.Provider,
		Mode:          in.Mode,
		Name:          in.Name,
		AppID:         in.AppID,
		AppSecret:     in.AppSecret,
		VerifyToken:   in.VerifyToken,
		EncryptKey:    in.EncryptKey,
		AllowFrom:     in.AllowFrom,
		Enabled:       in.Enabled,
		DefaultLocale: in.DefaultLocale,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := uc.repo.CreateApp(ctx, app); err != nil {
		return nil, fmt.Errorf("create im_app: %w", err)
	}
	return app, nil
}

// UpdateApp updates the row. Empty AppSecret = keep current (so the
// edit form doesn't have to re-display + re-submit the secret).
func (uc *UC) UpdateApp(ctx context.Context, id uint64, in AppInput) (*model.ImApp, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	cur, err := uc.repo.GetApp(ctx, id)
	if err != nil {
		return nil, err
	}
	cur.Provider = in.Provider
	cur.Mode = in.Mode
	cur.Name = in.Name
	cur.AppID = in.AppID
	if strings.TrimSpace(in.AppSecret) != "" {
		cur.AppSecret = in.AppSecret
	}
	cur.VerifyToken = in.VerifyToken
	cur.EncryptKey = in.EncryptKey
	cur.AllowFrom = in.AllowFrom
	cur.Enabled = in.Enabled
	cur.DefaultLocale = in.DefaultLocale
	cur.UpdatedAt = time.Now().UTC()
	if err := uc.repo.UpdateApp(ctx, cur); err != nil {
		return nil, fmt.Errorf("update im_app: %w", err)
	}
	return cur, nil
}

func (uc *UC) DeleteApp(ctx context.Context, id uint64) error {
	return uc.repo.DeleteApp(ctx, id)
}
