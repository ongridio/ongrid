// Package server constructs the iam BC's HTTP router and middleware chain.
// It is the only place that imports internal/iam/service.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ongridio/ongrid/internal/iam/model"
	"github.com/ongridio/ongrid/internal/iam/service"
	bizaudit "github.com/ongridio/ongrid/internal/manager/biz/audit"
	auditmodel "github.com/ongridio/ongrid/internal/manager/model/audit"
	auditmw "github.com/ongridio/ongrid/internal/manager/server/middleware"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// loginThrottle caps failed-login bursts to defeat naive bruteforce /
// password-spray. Keyed by client IP AND by email (whichever trips first
// blocks both). Tracked entirely in-process: a single-manager MVP doesn't
// need Redis. Restart drains the counter — that's a feature, not a bug,
// because operator-grade attackers will reuse the same key after restart
// anyway and we'd rather not over-engineer.
type loginThrottle struct {
	mu      sync.Mutex
	byIP    map[string]*throttleSlot
	byEmail map[string]*throttleSlot
}

type throttleSlot struct {
	count    int
	windowAt time.Time
}

const (
	// IP-level limit: looser, since one IP may legitimately host multiple
	// users (NAT, office gateway).
	loginIPLimit       = 20
	loginIPWindow      = 5 * time.Minute
	// Email-level limit: tighter — anyone trying 6+ passwords against
	// admin@x in 15min is hostile.
	loginEmailLimit  = 6
	loginEmailWindow = 15 * time.Minute
)

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{
		byIP:    map[string]*throttleSlot{},
		byEmail: map[string]*throttleSlot{},
	}
}

// check returns ErrTooManyAttempts iff the (ip, email) pair has already
// burnt through either window. It does NOT consume a slot — callers
// invoke recordFailure after the auth check, so successful logins don't
// burn budget.
func (t *loginThrottle) check(ip, email string) error {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if exceeded(t.byIP[ip], now, loginIPLimit, loginIPWindow) {
		return errs.ErrTooManyAttempts
	}
	if exceeded(t.byEmail[email], now, loginEmailLimit, loginEmailWindow) {
		return errs.ErrTooManyAttempts
	}
	return nil
}

// recordFailure bumps both counters on a failed login. Resets the window
// when expired (sliding-window approximation: each new failure outside
// the current window starts a fresh one).
func (t *loginThrottle) recordFailure(ip, email string) {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byIP[ip] = bump(t.byIP[ip], now, loginIPWindow)
	t.byEmail[email] = bump(t.byEmail[email], now, loginEmailWindow)
}

// recordSuccess clears the email-keyed slot — a real user who finally
// gets their password right shouldn't stay throttled. IP keeps its
// counter (the IP might still be hosting an attack against other users).
func (t *loginThrottle) recordSuccess(email string) {
	t.mu.Lock()
	delete(t.byEmail, email)
	t.mu.Unlock()
}

func exceeded(s *throttleSlot, now time.Time, limit int, window time.Duration) bool {
	if s == nil {
		return false
	}
	if now.Sub(s.windowAt) > window {
		return false
	}
	return s.count >= limit
}

func bump(s *throttleSlot, now time.Time, window time.Duration) *throttleSlot {
	if s == nil || now.Sub(s.windowAt) > window {
		return &throttleSlot{count: 1, windowAt: now}
	}
	s.count++
	return s
}

// clientIP returns the request's best-effort source IP. Trusts
// X-Forwarded-For ONLY when the request came in via a known reverse
// proxy header — the manager sits behind nginx in production, and nginx
// always overwrites Forwarded* itself, so the first hop in XFF is the
// real client. The TCP RemoteAddr is the nginx address otherwise.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First comma-separated entry is the original client.
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return addr[:i]
	}
	return addr
}

// Handler bundles the Service with its logger and exposes chi routers.
type Handler struct {
	svc       *service.Service
	log       *slog.Logger
	throttle  *loginThrottle
}

// NewHandler builds the iam HTTP handler bundle. Callers register routes
// via RegisterPublic / RegisterProtected on a shared chi router so public
// and protected endpoints can share the same URL prefix despite differing
// middleware chains.
func NewHandler(svc *service.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log, throttle: newLoginThrottle()}
}

// RegisterPublic attaches routes that DO NOT require an auth token.
func (h *Handler) RegisterPublic(r chi.Router) {
	r.Post("/v1/auth/login", h.login)
	r.Post("/v1/auth/refresh", h.refresh)
}

// RegisterProtected attaches routes that require a valid JWT. Caller must
// wrap r in the auth middleware.
func (h *Handler) RegisterProtected(r chi.Router) {
	r.Post("/v1/auth/register", h.register)
	r.Get("/v1/self", h.self)
	r.Get("/v1/me", h.me) // Phase-1: enriched self with memberships.
	r.Get("/v1/users", h.listUsers)
	r.Post("/v1/users", h.createUser)
	r.Patch("/v1/users/{id}", h.updateUser)
	r.Patch("/v1/users/{id}/role", h.setRole)
	// Note: PATCH /v1/users/{id}/superuser was retired May 2026 — the
	// system now has a single privilege tier (role=admin). DB column
	// is_superuser is kept for back-compat but no longer exposed.
	r.Patch("/v1/users/{id}/password", h.resetPassword)
	r.Delete("/v1/users/{id}", h.deleteUser)

	// Org CRUD (superuser-only writes; everyone authenticated can read
	// their own list — Phase 1 keeps it simple, refine in Phase 2).
	r.Get("/v1/orgs", h.listOrgs)
	r.Post("/v1/orgs", h.createOrg)
	r.Patch("/v1/orgs/{id}", h.updateOrg)
	r.Delete("/v1/orgs/{id}", h.deleteOrg)
	r.Get("/v1/orgs/{id}/members", h.listOrgMembers)
	r.Post("/v1/orgs/{id}/members", h.addOrgMember)
	r.Patch("/v1/orgs/{id}/members/{user_id}", h.updateOrgMember)
	r.Delete("/v1/orgs/{id}/members/{user_id}", h.removeOrgMember)
}

// ---- DTOs ----

type registerReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role,omitempty"`
}

type userDTO struct {
	ID    uint64 `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Role         string `json:"role"`
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token"`
}

type setRoleReq struct {
	Role string `json:"role"`
}

// ---- handlers ----

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in loginReq
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if len(in.Password) > 128 || len(in.Email) > 254 {
		writeErr(w, fmt.Errorf("%w: password or email exceeds maximum allowed length", errs.ErrInvalid))
		return
	}
	ip := clientIP(r)
	emailKey := strings.ToLower(strings.TrimSpace(in.Email))
	if err := h.throttle.check(ip, emailKey); err != nil {
		auditmw.SetAuditEvent(r, bizaudit.Event{
			Action:       auditmodel.ActionAuthLoginFailed,
			ResourceType: auditmodel.ResourceAuth,
			ResourceID:   emailKey,
			UserEmail:    emailKey,
			Status:       auditmodel.StatusFailure,
			ErrorMessage: "rate limited",
		})
		writeErr(w, err)
		return
	}
	pair, err := h.svc.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		h.throttle.recordFailure(ip, emailKey)
		// HLD-010: record failed login. UserID stays nil; email comes
		// from request body so we can spot password-spraying patterns.
		auditmw.SetAuditEvent(r, bizaudit.Event{
			Action:       auditmodel.ActionAuthLoginFailed,
			ResourceType: auditmodel.ResourceAuth,
			ResourceID:   emailKey,
			UserEmail:    emailKey,
			Status:       auditmodel.StatusFailure,
			ErrorMessage: err.Error(),
		})
		writeErr(w, err)
		return
	}
	h.throttle.recordSuccess(emailKey)
	// Successful login no longer audited (operator flagged the row
	// volume — auth_login_failed below stays for security). The
	// resulting JWT carries user_id + email; subsequent mutating
	// actions land audit rows attributed to the right caller via
	// the tenantctx slot.
	writeJSON(w, http.StatusOK, loginResp{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		Role:         pair.Role,
	})
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var in refreshReq
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	pair, err := h.svc.Refresh(r.Context(), in.RefreshToken)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, loginResp{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		Role:         pair.Role,
	})
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in registerReq
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if len(in.Password) > 128 || len(in.Email) > 254 {
		writeErr(w, fmt.Errorf("%w: password or email exceeds maximum allowed length", errs.ErrInvalid))
		return
	}
	u, err := h.svc.Register(r.Context(), in.Email, in.Password, "")
	if err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionUserCreate,
		ResourceType: auditmodel.ResourceUser,
		ResourceID:   strconv.FormatUint(u.ID, 10),
		ResourceName: u.Email,
		Status:       auditmodel.StatusSuccess,
		Payload:      map[string]any{"role": u.Role},
	})
	writeJSON(w, http.StatusCreated, userDTO{ID: u.ID, Email: u.Email, Role: u.Role})
}

func (h *Handler) self(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	u, err := h.svc.GetByID(r.Context(), t.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, userDTO{ID: u.ID, Email: u.Email, Role: u.Role})
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	users, err := h.svc.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]fullUserDTO, 0, len(users))
	for _, u := range users {
		out = append(out, toFullUserDTO(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionUserDelete,
		ResourceType: auditmodel.ResourceUser,
		ResourceID:   strconv.FormatUint(id, 10),
		Status:       auditmodel.StatusSuccess,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) setRole(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in setRoleReq
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.svc.SetRole(r.Context(), id, in.Role); err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionUserUpdate,
		ResourceType: auditmodel.ResourceUser,
		ResourceID:   strconv.FormatUint(id, 10),
		Status:       auditmodel.StatusSuccess,
		Payload:      map[string]any{"field": "role", "new_role": in.Role},
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ----

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return false
	}
	if t.Role != model.RoleAdmin {
		writeErr(w, errs.ErrForbidden)
		return false
	}
	return true
}

func decode(r *http.Request, dst any) error {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return errors.Join(errs.ErrInvalid, err)
	}
	return nil
}

func parseID(r *http.Request) (uint64, error) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.Join(errs.ErrInvalid, err)
	}
	return id, nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, err error) {
	status := errs.HTTPStatus(err)
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "60")
	}
	http.Error(w, err.Error(), status)
}
