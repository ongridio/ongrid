package imbridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	approval "github.com/ongridio/ongrid/internal/manager/model/approval"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ErrIdentityUnbound 表示已校验卡片所属消息，但点击者尚未关联 Ongrid 账号。
// 保留 Forbidden 错误链，供其他渠道沿用原有拒绝处理。
var ErrIdentityUnbound = fmt.Errorf("IM identity not bound: %w", errs.ErrForbidden)

type InteractionRepo interface {
	GetApp(context.Context, uint64) (*model.ImApp, error)
	CreateBindingChallenge(context.Context, *model.BindingChallenge) error
	GetBindingChallenge(context.Context, string, uint64) (*model.BindingChallenge, error)
	ClaimBindingChallenge(context.Context, string, uint64, string, string, string) error
	ConfirmIdentityBinding(context.Context, *model.BindingChallenge, *model.IdentityBinding) error
	FindIdentityBinding(context.Context, uint64, string) (*model.IdentityBinding, error)
	GetIdentityBinding(context.Context, string) (*model.IdentityBinding, error)
	ListIdentityBindings(context.Context, uint64) ([]model.IdentityBinding, error)
	DeleteIdentityBinding(context.Context, string, uint64) error
	SaveActionTokens(context.Context, []model.ActionToken) error
	BindActionMessage(context.Context, []string, string) error
	GetActionToken(context.Context, string) (*model.ActionToken, error)
	QueueDecision(context.Context, *model.DecisionCommand) error
	ClaimDecision(context.Context) (*model.DecisionCommand, error)
	FinishDecision(context.Context, string, string) error
	CompactInteractions(context.Context) error
	GetApprovalTarget(context.Context, string) (*model.ApprovalTarget, error)
}
type ApprovalDecider interface {
	AuthorizeApproval(context.Context, uint64) error
	Get(context.Context, string) (*approval.Approval, error)
	Approve(context.Context, uint64, string) (*approval.Approval, error)
	Reject(context.Context, uint64, string, string) error
}

// Subject 只能由验签或已认证的平台连接填充；昵称、email 和模型输出均不能作为身份。
type Subject struct {
	Scope string `json:"scope"`
	User  string `json:"user"`
}

func (s Subject) key() string { raw, _ := json.Marshal(s); return digest(string(raw)) }
func (s Subject) valid(provider string) bool {
	return s.User != "" && len(s.User) <= 128 && len(s.Scope) <= 128 && (provider == "telegram" || s.Scope != "")
}
func digest(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func opaque() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Credential rotation invalidates bindings and callbacks as bot labels may not be real platform IDs.
func ConnectionIdentity(a *model.ImApp) string {
	return digest(a.Provider + "\x00" + a.AppID + "\x00" + a.AppSecret)
}

type Interaction struct {
	repo      InteractionRepo
	approvals ApprovalDecider
	owner     uint64
	enabled   map[string]bool
	log       *slog.Logger
}

func NewInteraction(repo InteractionRepo, approvals ApprovalDecider, owner uint64, providers []string, log *slog.Logger) *Interaction {
	enabled := map[string]bool{}
	for _, p := range providers {
		enabled[strings.TrimSpace(p)] = true
	}
	if log == nil {
		log = slog.Default()
	}
	return &Interaction{repo: repo, approvals: approvals, owner: owner, enabled: enabled, log: log}
}
func WithInteraction(i *Interaction) BridgeOption { return func(b *Bridge) { b.interaction = i } }
func (i *Interaction) Enabled(app *model.ImApp) bool {
	return app != nil && app.Enabled && app.Mode == model.ModeStream && i.enabled[app.Provider]
}

type ChallengeView struct {
	ID        string    `json:"id"`
	Code      string    `json:"code,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	Subject   string    `json:"subject,omitempty"`
	Claimed   bool      `json:"claimed"`
}
type BindingView struct {
	ID      string `json:"id"`
	AppID   uint64 `json:"app_id"`
	Subject string `json:"subject"`
}

func (i *Interaction) StartBinding(ctx context.Context, user, appID uint64) (*ChallengeView, error) {
	if err := i.approvals.AuthorizeApproval(ctx, user); err != nil {
		return nil, err
	}
	app, err := i.repo.GetApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	if !i.Enabled(app) || secretbox.KeyIsWeak() {
		return nil, errs.ErrForbidden
	}
	code, err := opaque()
	if err != nil {
		return nil, err
	}
	id, err := opaque()
	if err != nil {
		return nil, err
	}
	c := &model.BindingChallenge{ID: id, CodeHash: digest(code), UserID: user, AppID: appID, AppIdentity: ConnectionIdentity(app), ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	if err = i.repo.CreateBindingChallenge(ctx, c); err != nil {
		return nil, err
	}
	return &ChallengeView{ID: id, Code: code, ExpiresAt: c.ExpiresAt}, nil
}
func (i *Interaction) BindingChallenge(ctx context.Context, user uint64, id string) (*ChallengeView, error) {
	if err := i.approvals.AuthorizeApproval(ctx, user); err != nil {
		return nil, err
	}
	c, err := i.repo.GetBindingChallenge(ctx, id, user)
	if err != nil {
		return nil, err
	}
	v := &ChallengeView{ID: c.ID, ExpiresAt: c.ExpiresAt, Claimed: c.SubjectHash != ""}
	if c.SubjectCipher != "" {
		v.Subject, err = secretbox.Decrypt(c.SubjectCipher)
	}
	return v, err
}
func (i *Interaction) ClaimBinding(ctx context.Context, app *model.ImApp, subject Subject, code string) error {
	if !i.Enabled(app) || !subject.valid(app.Provider) || len(code) != 43 {
		return errs.ErrForbidden
	}
	raw, err := json.Marshal(subject)
	if err != nil {
		return err
	}
	cipher, err := secretbox.Encrypt(string(raw))
	if err != nil {
		return err
	}
	return i.repo.ClaimBindingChallenge(ctx, digest(code), app.ID, ConnectionIdentity(app), subject.key(), cipher)
}
func (i *Interaction) ConfirmBinding(ctx context.Context, user uint64, id string) error {
	if err := i.approvals.AuthorizeApproval(ctx, user); err != nil {
		return err
	}
	c, err := i.repo.GetBindingChallenge(ctx, id, user)
	if err != nil {
		return err
	}
	app, err := i.repo.GetApp(ctx, c.AppID)
	if err != nil {
		return err
	}
	if !i.Enabled(app) || c.AppIdentity != ConnectionIdentity(app) || c.SubjectHash == "" {
		return errs.ErrForbidden
	}
	bid, err := opaque()
	if err != nil {
		return err
	}
	return i.repo.ConfirmIdentityBinding(ctx, c, &model.IdentityBinding{ID: bid, UserID: user, AppID: app.ID, AppIdentity: c.AppIdentity, SubjectHash: c.SubjectHash, SubjectCipher: c.SubjectCipher})
}
func (i *Interaction) Bindings(ctx context.Context, user uint64) ([]BindingView, error) {
	if err := i.approvals.AuthorizeApproval(ctx, user); err != nil {
		return nil, err
	}
	rows, err := i.repo.ListIdentityBindings(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([]BindingView, 0, len(rows))
	for _, r := range rows {
		s, err := secretbox.Decrypt(r.SubjectCipher)
		if err != nil {
			return nil, err
		}
		out = append(out, BindingView{ID: r.ID, AppID: r.AppID, Subject: s})
	}
	return out, nil
}
func (i *Interaction) Unbind(ctx context.Context, user uint64, id string) error {
	if err := i.approvals.AuthorizeApproval(ctx, user); err != nil {
		return err
	}
	return i.repo.DeleteIdentityBinding(ctx, id, user)
}

type ApprovalButtons struct {
	Approve string
	Reject  string
}
type ApprovalCard struct {
	Details   string
	Result    string
	Locale    string
	Text      string
	Buttons   ApprovalButtons
	ReviewURL string
}
type ApprovalCardSender interface {
	SendApproval(context.Context, ApprovalCard) (string, error)
	EditApproval(context.Context, string, ApprovalCard) error
}

func (i *Interaction) Buttons(ctx context.Context, app *model.ImApp, t *model.ApprovalTarget, id string) (ApprovalButtons, error) {
	if !i.Enabled(app) {
		return ApprovalButtons{}, nil
	}
	if t.AppID != app.ID || t.AppIdentifier != app.AppID || (t.ConnectionIdentity != "" && t.ConnectionIdentity != ConnectionIdentity(app)) {
		return ApprovalButtons{}, errs.ErrForbidden
	}
	a, err := i.approvals.Get(ctx, id)
	if err != nil {
		return ApprovalButtons{}, err
	}
	if a.SessionID != t.SessionID || a.ProposedBy != i.owner || a.Status != "pending" {
		return ApprovalButtons{}, errs.ErrConflict
	}
	yes, err := opaque()
	if err != nil {
		return ApprovalButtons{}, err
	}
	no, err := opaque()
	if err != nil {
		return ApprovalButtons{}, err
	}
	chatID := t.ChatID
	if t.CallbackChatID != "" {
		chatID = t.CallbackChatID
	}
	tokens := []model.ActionToken{}
	for action, token := range map[string]string{"approve": yes, "reject": no} {
		tokens = append(tokens, model.ActionToken{Hash: digest(token), ApprovalID: id, SessionID: t.SessionID, AppID: app.ID, AppIdentity: ConnectionIdentity(app), ChatID: chatID, Action: action, ExpiresAt: time.Now().UTC().Add(30 * time.Minute)})
	}
	if err = i.repo.SaveActionTokens(ctx, tokens); err != nil {
		return ApprovalButtons{}, err
	}
	return ApprovalButtons{Approve: yes, Reject: no}, nil
}
func (i *Interaction) BindMessage(ctx context.Context, b ApprovalButtons, mid string) error {
	if b.Approve == "" && b.Reject == "" {
		return nil
	}
	if mid == "" {
		return errs.ErrInvalid
	}
	hashes := []string{}
	for _, token := range []string{b.Approve, b.Reject} {
		if token != "" {
			hashes = append(hashes, digest(token))
		}
	}
	return i.repo.BindActionMessage(ctx, hashes, mid)
}

type ApprovalCallback struct {
	Provider, AppIdentifier, ChatID, MessageID, Token string
	ConnectionIdentity                                string
	Subject                                           Subject
}

func (b *Bridge) HandleApprovalCallback(ctx context.Context, c ApprovalCallback) error {
	if b.interaction == nil {
		return errs.ErrForbidden
	}
	app, err := b.repo.GetAppByAppID(ctx, c.Provider, c.AppIdentifier)
	if err != nil {
		return err
	}
	if c.ConnectionIdentity == "" || c.ConnectionIdentity != ConnectionIdentity(app) {
		return errs.ErrForbidden
	}
	return b.interaction.Enqueue(ctx, app, c)
}
func (i *Interaction) Enqueue(ctx context.Context, app *model.ImApp, c ApprovalCallback) error {
	if !i.Enabled(app) || c.ConnectionIdentity != ConnectionIdentity(app) || !c.Subject.valid(app.Provider) || len(c.Token) != 43 {
		return errs.ErrForbidden
	}
	token, err := i.repo.GetActionToken(ctx, digest(c.Token))
	if err != nil {
		return err
	}
	if token.AppID != app.ID || token.AppIdentity != ConnectionIdentity(app) || token.ChatID != c.ChatID || token.MessageID == "" || token.MessageID != c.MessageID {
		return errs.ErrForbidden
	}
	binding, err := i.repo.FindIdentityBinding(ctx, app.ID, c.Subject.key())
	if errors.Is(err, errs.ErrNotFound) {
		return ErrIdentityUnbound
	}
	if err != nil {
		return fmt.Errorf("find approval identity: %w", err)
	}
	if binding.AppIdentity != token.AppIdentity {
		return errs.ErrForbidden
	}
	if err = i.approvals.AuthorizeApproval(ctx, binding.UserID); err != nil {
		return err
	}
	a, err := i.approvals.Get(ctx, token.ApprovalID)
	if err != nil {
		return err
	}
	if a.ProposedBy != i.owner || a.SessionID != token.SessionID || a.Status != "pending" {
		return errs.ErrConflict
	}
	if token.Action == "approve" {
		if _, complete := platformCardReview(app.Provider, a.PayloadJSON); !complete {
			return errs.ErrInvalid
		}
	}
	return i.repo.QueueDecision(ctx, &model.DecisionCommand{Provider: app.Provider, ChatID: c.ChatID, MessageID: c.MessageID, SubjectCipher: binding.SubjectCipher, ApprovalID: a.ID, TokenHash: token.Hash, BindingID: binding.ID, UserID: binding.UserID, AppID: app.ID, AppIdentity: token.AppIdentity, Action: token.Action, State: "queued"})
}
func (i *Interaction) Run(ctx context.Context) {
	var workers sync.WaitGroup
	for n := 0; n < 4; n++ {
		workers.Add(1)
		go func() { defer workers.Done(); i.runWorker(ctx) }()
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			workers.Wait()
			return
		case <-ticker.C:
			scan, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := i.repo.CompactInteractions(scan); err != nil && ctx.Err() == nil {
				i.log.Warn("IM interaction retention failed", slog.Any("err", err))
			}
			cancel()
		}
	}
}
func (i *Interaction) runWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		i.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (i *Interaction) step(ctx context.Context) {
	defer func() {
		if recover() != nil {
			i.log.Error("IM approval decision interrupted; running command requires reconciliation")
		}
	}()
	scan, cancel := context.WithTimeout(ctx, 5*time.Second)
	c, err := i.repo.ClaimDecision(scan)
	cancel()
	if errors.Is(err, errs.ErrNotFound) || errors.Is(err, errs.ErrConflict) {
		return
	}
	if err != nil {
		i.log.Warn("IM decision claim failed", slog.Any("err", err))
		return
	}
	outcome := "authorization_revoked"
	// 执行不绑定平台 3 秒 ACK 或通知 10 秒超时；原执行器保持自己的超时策略。
	err = i.execute(ctx, c)
	if err == nil {
		outcome = "completed"
	} else if errors.Is(err, errs.ErrConflict) || errors.Is(err, errs.ErrNotFound) {
		outcome = "already_decided_or_expired"
	} else {
		outcome = "failed_or_uncertain"
	}
	finish, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	if err = i.repo.FinishDecision(finish, c.ApprovalID, outcome); err != nil {
		i.log.Error("IM decision outcome persistence failed", slog.Any("err", err))
	}
}
func (i *Interaction) execute(ctx context.Context, c *model.DecisionCommand) error {
	binding, err := i.repo.GetIdentityBinding(ctx, c.BindingID)
	if err != nil {
		return err
	}
	app, err := i.repo.GetApp(ctx, c.AppID)
	if err != nil {
		return err
	}
	if !i.Enabled(app) || binding.UserID != c.UserID || binding.AppID != app.ID || binding.AppIdentity != ConnectionIdentity(app) || c.AppIdentity != binding.AppIdentity {
		return errs.ErrForbidden
	}
	token, err := i.repo.GetActionToken(ctx, c.TokenHash)
	if err != nil {
		return err
	}
	if token.ApprovalID != c.ApprovalID || token.Action != c.Action {
		return errs.ErrForbidden
	}
	if err = i.approvals.AuthorizeApproval(ctx, c.UserID); err != nil {
		return err
	}
	switch c.Action {
	case "approve":
		_, err = i.approvals.Approve(ctx, c.UserID, c.ApprovalID)
		return err
	case "reject":
		return i.approvals.Reject(ctx, c.UserID, c.ApprovalID, "Rejected from IM")
	default:
		return fmt.Errorf("%w: unknown decision", errs.ErrInvalid)
	}
}

func (c ApprovalCard) Label(zh, en string) string {
	if c.Locale == "en" {
		return en
	}
	return zh
}
