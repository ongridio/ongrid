// Package imbridge wires the IM webhook endpoints to the bridge biz
// layer. these endpoints sit OUTSIDE the bearer-auth group
// — the platforms can't carry our manager JWT. Authentication comes
// from the platform's signature scheme; the handler verifies before
// dispatching anything downstream.
package imbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	iammodel "github.com/ongridio/ongrid/internal/iam/model"
	bizbridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	"github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/dingtalk"
	"github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/feishu"
	"github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/slack"
	"github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/telegram"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// requireAdmin gates all /v1/im/apps routes — IM app config is
// platform-wide and exposes app_secret / encryption keys, so even read
// access is admin-only. Returns false after writing the error.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		http.Error(w, errs.ErrUnauthorized.Error(), errs.HTTPStatus(errs.ErrUnauthorized))
		return false
	}
	if t.Role != iammodel.RoleAdmin {
		http.Error(w, errs.ErrForbidden.Error(), errs.HTTPStatus(errs.ErrForbidden))
		return false
	}
	return true
}

// AppRepo is the narrow data-layer surface the HTTP handler needs to
// look up an ImApp from app_id. Bridge biz has its own lookup; we
// repeat the call here because the handler needs the app row BEFORE
// signature verification (to know which secret to verify with).
type AppRepo interface {
	GetAppByAppID(ctx context.Context, provider, appID string) (*model.ImApp, error)
}

type Handler struct {
	bridge *bizbridge.Bridge
	apps   AppRepo
	uc     *bizbridge.UC
	log    *slog.Logger
}

func NewHandler(bridge *bizbridge.Bridge, apps AppRepo, uc *bizbridge.UC, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{bridge: bridge, apps: apps, uc: uc, log: log.With(slog.String("comp", "imbridge.http"))}
}

// RegisterPublic adds /v1/im/{provider}/events to the unauthenticated
// public router. The handler enforces auth via the platform's
// signature scheme; bearer middleware would just reject the platform.
func (h *Handler) RegisterPublic(r chi.Router) {
	r.Post("/v1/im/feishu/events", h.handleFeishuEvent)
	// dingtalk handler will land in S2 — same shape, different signer.
}

// RegisterProtected adds the admin CRUD endpoints. Authenticated
// (bearer) and gated to superusers / admins at the call site.
func (h *Handler) RegisterProtected(r chi.Router) {
	r.Get("/v1/im/apps", h.listApps)
	r.Post("/v1/im/apps", h.createApp)
	r.Get("/v1/im/apps/{id}", h.getApp)
	r.Put("/v1/im/apps/{id}", h.updateApp)
	r.Delete("/v1/im/apps/{id}", h.deleteApp)
	r.Post("/v1/im/apps/{id}/test", h.testApp)
	r.Post("/v1/im/apps/{id}/reveal", h.revealAppSecret)
}

// ---- Feishu --------------------------------------------------------

// Feishu event request envelope (v2). When encrypt_key is set, the
// payload arrives as {"encrypt": "<base64-ciphertext>"} and Feishu
// will perform a URL verification challenge by POSTing a normal
// {"type": "url_verification", "challenge": "..."}.
type feishuEnvelope struct {
	Encrypt   string                 `json:"encrypt"`
	Schema    string                 `json:"schema"`
	Type      string                 `json:"type"`      // url_verification only
	Challenge string                 `json:"challenge"` // url_verification only
	Token     string                 `json:"token"`     // url_verification only
	Header    map[string]interface{} `json:"header"`    // present on encrypted payloads after decrypt
	Event     map[string]interface{} `json:"event"`
}

// feishuChallenge is the response shape Feishu expects when verifying
// the webhook URL. We echo back challenge verbatim.
type feishuChallenge struct {
	Challenge string `json:"challenge"`
}

func (h *Handler) handleFeishuEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	var env feishuEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	// URL verification handshake. Feishu sends a plain (or encrypted)
	// {type: url_verification, challenge: xxx} when the URL is first
	// configured in the open platform console. Echo it back so the
	// admin UI shows green.
	if env.Type == "url_verification" {
		writeJSON(w, http.StatusOK, feishuChallenge{Challenge: env.Challenge})
		return
	}

	// At this point we MUST resolve the app first because we don't
	// know which secret to verify with. Feishu puts the app_id in the
	// decrypted event payload header — but we need to verify BEFORE
	// decrypting. Workaround: read app_id from the X-Lark-Source-App
	// header that Feishu sets on every webhook delivery.
	appID := r.Header.Get("X-Lark-Source-App")
	if appID == "" {
		// Fallback: try to read header.app_id from a plaintext payload
		// (when encrypt_key isn't configured the body is plaintext).
		if hdr, _ := env.Header["app_id"].(string); hdr != "" {
			appID = hdr
		}
	}
	if appID == "" {
		http.Error(w, "missing app_id", http.StatusBadRequest)
		return
	}

	app, err := h.apps.GetAppByAppID(r.Context(), model.ProviderFeishu, appID)
	if err != nil {
		http.Error(w, "unknown app", http.StatusUnauthorized)
		return
	}

	// Signature verification — required when encrypt_key is set
	// (otherwise events are plaintext and we accept them based on
	// the platform's network-level reachability rules; weaker
	// security, but matches what Feishu docs call "non-encrypted mode").
	if app.EncryptKey != "" {
		ts := r.Header.Get("X-Lark-Request-Timestamp")
		nonce := r.Header.Get("X-Lark-Request-Nonce")
		sig := r.Header.Get("X-Lark-Signature")
		if err := feishu.VerifyEventSignature(ts, nonce, app.EncryptKey, body, sig); err != nil {
			h.log.Warn("feishu signature mismatch", slog.String("app_id", appID))
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
	}

	// Decrypt the event payload when encrypted. The decrypted bytes
	// are another JSON envelope of the same shape, sans .encrypt.
	if env.Encrypt != "" {
		if app.EncryptKey == "" {
			http.Error(w, "encrypt set but no encrypt_key configured", http.StatusUnauthorized)
			return
		}
		pt, derr := feishu.DecryptEvent(app.EncryptKey, env.Encrypt)
		if derr != nil {
			h.log.Warn("feishu decrypt failed", slog.Any("err", derr))
			http.Error(w, "decrypt failed", http.StatusUnauthorized)
			return
		}
		if uerr := json.Unmarshal(pt, &env); uerr != nil {
			h.log.Warn("feishu decrypted payload not json", slog.Any("err", uerr))
			http.Error(w, "bad decrypted payload", http.StatusBadRequest)
			return
		}
		// After decrypt env.Type may now read "event_callback" with
		// header / event populated; re-handle a possible second-pass
		// challenge (Feishu has done this in the past after rotating
		// formats — be defensive).
		if env.Type == "url_verification" {
			writeJSON(w, http.StatusOK, feishuChallenge{Challenge: env.Challenge})
			return
		}
	}

	// Extract the bits we care about. Feishu's message event shape:
	//   header.event_type == "im.message.receive_v1"
	//   event.message.chat_id
	//   event.message.message_id
	//   event.message.message_type (= "text")
	//   event.message.content (JSON string: {"text": "..."})
	//   event.sender.sender_id.{open_id|union_id|user_id}
	//
	// We do shallow type-assertions because the platform reserves
	// the right to add fields.
	in, ok := extractFeishuMessage(env)
	if !ok {
		// Non-message event (e.g. a permission grant) — ack and move on.
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	in.Provider = model.ProviderFeishu
	in.AppID = appID

	// Spawn the agent run on a background goroutine. Feishu's webhook
	// has a 3-second ack deadline; if we block, the platform will
	// retry and we'll process the same event multiple times.
	go func() {
		ctx := context.Background()
		sender := feishuSender{client: feishu.NewClient(app.AppID, app.AppSecret)}
		if err := h.bridge.HandleInbound(ctx, sender, in); err != nil {
			h.log.Warn("inbound bridge failed", slog.Any("err", err))
		}
	}()

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// extractFeishuMessage pulls the (chat_id, message_id, text) triple
// out of the Feishu im.message.receive_v1 envelope. Returns
// (msg, true) on success.
func extractFeishuMessage(env feishuEnvelope) (bizbridge.InboundMessage, bool) {
	out := bizbridge.InboundMessage{}
	if env.Header == nil || env.Event == nil {
		return out, false
	}
	eventType, _ := env.Header["event_type"].(string)
	if eventType != "im.message.receive_v1" {
		return out, false
	}
	eventID, _ := env.Header["event_id"].(string)
	out.EventID = eventID

	msg, _ := env.Event["message"].(map[string]interface{})
	sender, _ := env.Event["sender"].(map[string]interface{})
	if msg == nil {
		return out, false
	}
	out.ChatID, _ = msg["chat_id"].(string)
	// root_id is set on replies; empty for top-level messages
	out.ThreadID, _ = msg["root_id"].(string)
	if out.ChatID == "" {
		return out, false
	}
	msgType, _ := msg["message_type"].(string)
	if msgType != "text" {
		// S1 supports text only. Cards / files / audio dropped silently.
		return out, false
	}
	contentRaw, _ := msg["content"].(string)
	// content is a JSON string e.g. `{"text":"hello bot"}`
	var c struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(contentRaw), &c)
	out.Text = c.Text

	if sender != nil {
		if sid, ok := sender["sender_id"].(map[string]interface{}); ok {
			out.OpenID, _ = sid["open_id"].(string)
		}
	}

	// Feishu expects chat_id as the receive_id_type when targeting
	// the group; for DMs the inbound chat_id is still a chat_id
	// (private chat is a 1:1 chat in Feishu's model).
	out.ReceiveIDType = "chat_id"
	return out, true
}

// feishuSender adapts the Feishu client to the bridge's Sender
// interface.
type feishuSender struct {
	client *feishu.Client
}

func (s feishuSender) SendText(ctx context.Context, receiveID, receiveIDType, text string) (string, error) {
	return s.client.SendText(ctx, receiveID, receiveIDType, text)
}

func (s feishuSender) EditText(ctx context.Context, messageID, text string) error {
	return s.client.EditText(ctx, messageID, text)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// guard against unused imports if a future refactor drops Feishu.
var _ = errors.New

// ---- admin CRUD ----------------------------------------------------

type appDTO struct {
	ID                 uint64 `json:"id"`
	Provider           string `json:"provider"`
	Mode               string `json:"mode"`
	Name               string `json:"name"`
	AppID              string `json:"app_id"`
	HasSecret          bool   `json:"has_secret"`
	VerifyToken        string `json:"verify_token,omitempty"`
	EncryptKey         string `json:"encrypt_key,omitempty"`
	AllowFrom          string `json:"allow_from,omitempty"`
	DefaultLocale      string `json:"default_locale,omitempty"`
	Enabled            bool   `json:"enabled"`
	IdleTimeoutSeconds int    `json:"idle_timeout_seconds"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
}

func toAppDTO(a *model.ImApp) appDTO {
	return appDTO{
		ID:                 a.ID,
		Provider:           a.Provider,
		Mode:               a.Mode,
		Name:               a.Name,
		AppID:              a.AppID,
		HasSecret:          a.AppSecret != "",
		VerifyToken:        a.VerifyToken,
		EncryptKey:         a.EncryptKey,
		AllowFrom:          a.AllowFrom,
		DefaultLocale:      a.DefaultLocale,
		Enabled:            a.Enabled,
		IdleTimeoutSeconds: a.IdleTimeoutSeconds,
		CreatedAt:          a.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:          a.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

type appPayload struct {
	Provider      string `json:"provider"`
	Mode          string `json:"mode"`
	Name          string `json:"name"`
	AppID         string `json:"app_id"`
	AppSecret     string `json:"app_secret,omitempty"`
	VerifyToken   string `json:"verify_token,omitempty"`
	EncryptKey    string `json:"encrypt_key,omitempty"`
	AllowFrom     string `json:"allow_from,omitempty"`
	DefaultLocale string `json:"default_locale,omitempty"`
	Enabled       bool   `json:"enabled"`
}

func (p appPayload) toInput() bizbridge.AppInput {
	return bizbridge.AppInput{
		Provider:      p.Provider,
		Mode:          p.Mode,
		Name:          p.Name,
		AppID:         p.AppID,
		AppSecret:     p.AppSecret,
		VerifyToken:   p.VerifyToken,
		EncryptKey:    p.EncryptKey,
		AllowFrom:     p.AllowFrom,
		DefaultLocale: p.DefaultLocale,
		Enabled:       p.Enabled,
	}
}

func (h *Handler) listApps(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	provider := r.URL.Query().Get("provider")
	rows, err := h.uc.ListApps(r.Context(), provider)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items := make([]appDTO, 0, len(rows))
	for _, a := range rows {
		items = append(items, toAppDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (h *Handler) getApp(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id, ok := parseIDFromURL(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	app, err := h.uc.GetApp(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toAppDTO(app))
}

func (h *Handler) createApp(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var p appPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	app, err := h.uc.CreateApp(r.Context(), p.toInput())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, toAppDTO(app))
}

func (h *Handler) updateApp(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id, ok := parseIDFromURL(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	var p appPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	app, err := h.uc.UpdateApp(r.Context(), id, p.toInput())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, toAppDTO(app))
}

func (h *Handler) deleteApp(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id, ok := parseIDFromURL(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := h.uc.DeleteApp(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type appTestResult struct {
	Accepted  bool   `json:"accepted"`
	Message   string `json:"message,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

// testApp verifies stored credentials against the provider without sending a message.
func (h *Handler) testApp(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id, ok := parseIDFromURL(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	app, err := h.uc.GetApp(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	started := time.Now()
	err = probeApp(ctx, app)
	out := appTestResult{Accepted: err == nil, LatencyMS: time.Since(started).Milliseconds()}
	if err != nil {
		out.Message = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

func probeApp(ctx context.Context, app *model.ImApp) error {
	if app == nil {
		return fmt.Errorf("%w: IM app required", errs.ErrInvalid)
	}
	switch app.Provider {
	case model.ProviderFeishu:
		return feishu.NewClient(app.AppID, app.AppSecret).Probe(ctx)
	case model.ProviderDingTalk:
		return dingtalk.Probe(ctx, app.AppID, app.AppSecret)
	case model.ProviderTelegram:
		return telegram.NewClient(app.AppSecret).Probe(ctx)
	case model.ProviderSlack:
		client, err := slack.NewClientFromSecret(app.AppSecret)
		if err != nil {
			return err
		}
		return client.Probe(ctx)
	default:
		return fmt.Errorf("%w: unsupported IM provider %q", errs.ErrInvalid, app.Provider)
	}
}

// revealAppSecret returns the cleartext app_secret. Mirrors the
// SystemSetting reveal flow — the list endpoint only returns
// has_secret=true to avoid logging secrets every page render.
func (h *Handler) revealAppSecret(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id, ok := parseIDFromURL(r)
	if !ok {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	app, err := h.uc.GetApp(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"app_secret": app.AppSecret})
}

func parseIDFromURL(r *http.Request) (uint64, bool) {
	idStr := chi.URLParam(r, "id")
	var id uint64
	for _, c := range idStr {
		if c < '0' || c > '9' {
			return 0, false
		}
		id = id*10 + uint64(c-'0')
	}
	return id, id > 0
}
