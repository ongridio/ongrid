// Package aiops builds the HTTP routes for the manager/aiops sub-domain.
//
// Routes (all require an authed caller; caller identity is read from
// tenantctx which the auth middleware populates upstream):
//
//	POST   /v1/chat/sessions                         create session
//	GET    /v1/chat/sessions                         list caller's sessions
//	POST   /v1/chat/sessions/{id}/messages           blocking; runs the agent
//	GET    /v1/chat/sessions/{id}/messages           full history
//	DELETE /v1/chat/sessions/{id}                    soft-close
//
// Ownership: non-owner, non-admin callers get 404 (not 403) to avoid
// leaking session existence.
package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	biz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/agent"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/mentions"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	svc "github.com/ongridio/ongrid/internal/manager/service/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// AIOpsService is the narrow service contract the handler depends on.
// *svc.Service satisfies it by structural typing; tests swap in a fake.
type AIOpsService interface {
	CreateSession(ctx context.Context, caller svc.Caller, in svc.CreateSessionInput) (*model.Session, error)
	ListSessions(ctx context.Context, caller svc.Caller, limit, offset int, relatedIncidentID *uint64) ([]*model.Session, error)
	ListMessages(ctx context.Context, caller svc.Caller, sessionID string) ([]*model.Message, error)
	CloseSession(ctx context.Context, caller svc.Caller, sessionID string) error
	DeleteSession(ctx context.Context, caller svc.Caller, sessionID string) error
	RenameSession(ctx context.Context, caller svc.Caller, sessionID string, title string) error
	PostMessage(ctx context.Context, caller svc.Caller, sessionID string, content string) (*agent.Reply, error)
	PostMessageWithOpts(ctx context.Context, caller svc.Caller, sessionID string, content string, opts agent.RunOptions) (*agent.Reply, error)
	PostMessageStream(ctx context.Context, caller svc.Caller, sessionID string, content string, emit agent.Emit) (*agent.Reply, error)
	PostMessageStreamWithOpts(ctx context.Context, caller svc.Caller, sessionID string, content string, emit agent.Emit, opts agent.RunOptions) (*agent.Reply, error)
	StopSession(ctx context.Context, caller svc.Caller, sessionID string) (bool, error)
	UsageToday(ctx context.Context) (*biz.DailyUsage, error)
	ListMutatingProposals(ctx context.Context, caller svc.Caller, f biz.MutatingProposalFilter) ([]*model.MutatingProposal, int64, error)
}

// MentionSearcher is the narrow biz contract for @-mention search. Optional —
// when nil the /v1/aiops/mentions/search route is registered but always
// returns an empty list (the SPA still gets a 200, the popover just sits
// idle).
type MentionSearcher interface {
	Search(ctx context.Context, q mentions.Query) ([]mentions.Item, error)
}

// ModelCatalog is the narrow read surface used by /v1/aiops/models. Returns
// the configured-provider catalog plus the default (provider, model) pair.
// Optional — when nil the endpoint still installs but returns an empty
// catalog so the SPA hides the model selector.
type ModelCatalog interface {
	Providers() []llm.ProviderInfo
	Default() (string, string)
}

// AgentLister is the narrow read surface used by /v1/agents — exposes
// the agent personas the chatruntime AgentRegistry has loaded so the
// SPA can render the Agents inventory page + the Side Panel agent
// switcher. Optional: when nil the endpoint installs but returns an
// empty list (frontend falls back to the default coordinator persona).
type AgentLister interface {
	All() []*chatruntime.Agent
	ByName(name string) (*chatruntime.Agent, bool)
	Remove(name string) bool
}

// UserAgentManager is the Phase-3 CRUD surface for user-defined
// personas. Optional: when nil the /v1/agents/custom routes 404 (legacy
// kernel doesn't build the registry).
type UserAgentManager interface {
	Create(ctx context.Context, in svc.CreateUserAgentInput) (*model.UserAgent, error)
	Update(ctx context.Context, caller svc.Caller, name string, in svc.UpdateUserAgentInput) (*model.UserAgent, error)
	Delete(ctx context.Context, caller svc.Caller, name string) error
}

// Handler bundles the aiops service with HTTP-layer state.
type Handler struct {
	svc        AIOpsService
	mentions   MentionSearcher
	catalog    ModelCatalog
	agents     AgentLister
	userAgents UserAgentManager
	llmClient  llm.Client // for /v1/aiops/query-translate; nil = endpoint 503
}

// NewHandler builds the handler. mentions / catalog may be nil; see
// the matching contracts above for graceful-degradation behaviour.
func NewHandler(s AIOpsService) *Handler { return &Handler{svc: s} }

// SetMentionSearcher wires the @-mention search backend post-construction.
// Nil is allowed (clears the wiring).
func (h *Handler) SetMentionSearcher(m MentionSearcher) { h.mentions = m }

// SetModelCatalog wires the multi-provider catalog post-construction.
func (h *Handler) SetModelCatalog(c ModelCatalog) { h.catalog = c }

// SetLLMClient wires the LLM client used by /v1/aiops/query-translate
// (the natural-language → LogQL/TraceQL/PromQL helper). Optional —
// when nil the endpoint returns 503 and the SPA hides the ✨ button.
func (h *Handler) SetLLMClient(c llm.Client) { h.llmClient = c }

// SetAgentLister wires the chatruntime AgentRegistry post-construction
// so /v1/agents can list loaded personas. Nil is allowed; the endpoint
// then returns an empty list.
func (h *Handler) SetAgentLister(a AgentLister) { h.agents = a }

// SetUserAgentManager wires the user-agent CRUD service post-
// construction so /v1/agents/custom routes work. Nil = those routes
// 503.
func (h *Handler) SetUserAgentManager(m UserAgentManager) { h.userAgents = m }

// Register attaches aiops routes on r.
func (h *Handler) Register(r chi.Router) {
	r.Post("/v1/chat/sessions", h.createSession)
	r.Get("/v1/chat/sessions", h.listSessions)
	r.Post("/v1/chat/sessions/{id}/messages", h.postMessage)
	r.Post("/v1/chat/sessions/{id}/messages/stream", h.postMessageStream)
	r.Post("/v1/chat/sessions/{id}/stop", h.stopSession)
	r.Get("/v1/chat/sessions/{id}/messages", h.listMessages)
	r.Delete("/v1/chat/sessions/{id}", h.closeSession)
	r.Patch("/v1/chat/sessions/{id}", h.renameSession)
	r.Get("/v1/usage/today", h.usageToday)
	r.Get("/v1/aiops/mutating-proposals", h.listMutatingProposals)
	r.Get("/v1/aiops/mentions/search", h.searchMentions)
	r.Get("/v1/aiops/models", h.listModels)
	r.Post("/v1/aiops/query-translate", h.queryTranslate)
	r.Get("/v1/agents", h.listAgents)
	r.Get("/v1/agents/{name}", h.getAgent)
	r.Post("/v1/agents/custom", h.createUserAgent)
	r.Patch("/v1/agents/custom/{name}", h.updateUserAgent)
	r.Delete("/v1/agents/custom/{name}", h.deleteUserAgent)
	// Generic delete: works on any non-builtin / non-default agent.
	// Disk-source agents (loaded from agents/*.md) get session-scoped
	// removal — the in-memory registry drops them, but the .md file
	// stays and they re-appear on restart. User-source agents go
	// through the userAgents service (DB row removal).
	r.Delete("/v1/agents/{name}", h.deleteAgent)
}

// --------- DTOs ---------

type createSessionReq struct {
	Title string   `json:"title"`
	Scope []string `json:"scope,omitempty"`
	// RelatedIncidentID links the session back to an alert incident.
	// Set by the IncidentDetail "深入诊断" button so the per-incident
	// agent-timeline panel can list this session under the incident.
	RelatedIncidentID *uint64 `json:"related_incident_id,omitempty"`
	// AgentID pins the session to a chatruntime persona. The Side Panel
	// agent picker + Agents page "使用此助理" button send this. Empty =
	// global coordinator default.
	AgentID string `json:"agent_id,omitempty"`
}

type sessionDTO struct {
	ID                string     `json:"id"`
	UserID            uint64     `json:"user_id"`
	Title             string     `json:"title"`
	Scope             []string   `json:"scope,omitempty"`
	RelatedIncidentID *uint64    `json:"related_incident_id,omitempty"`
	AgentID           *string    `json:"agent_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	ClosedAt          *time.Time `json:"closed_at,omitempty"`
}

type listSessionsResp struct {
	Items []sessionDTO `json:"items"`
	Total int          `json:"total"`
}

type postMessageReq struct {
	Content          string         `json:"content"`
	Provider         string         `json:"provider,omitempty"`
	Model            string         `json:"model,omitempty"`
	Mentions         []mentionInput `json:"mentions,omitempty"`
	WebSearchEnabled bool           `json:"web_search_enabled,omitempty"`
	// Locale is the SPA's UI language ("en-US"/"zh-CN") so the agent
	// answers in that language. Optional (IM/other callers omit it).
	Locale string `json:"locale,omitempty"`
}

// mentionInput is the wire shape the SPA sends for each @-mention chip.
// Mirrors mentions.Mention; lives here so the HTTP DTO is self-
// contained and JSON-tagged.
type mentionInput struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

func (m mentionInput) toAgent() agent.Mention {
	return agent.Mention{Type: m.Type, ID: m.ID, Label: m.Label}
}

func toAgentMentions(in []mentionInput) []agent.Mention {
	if len(in) == 0 {
		return nil
	}
	out := make([]agent.Mention, 0, len(in))
	for _, m := range in {
		out = append(out, m.toAgent())
	}
	return out
}

type assistantMessageDTO struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type toolCallDTO struct {
	Name       string  `json:"name"`
	DeviceID   *uint64 `json:"edge_id,omitempty"`
	Status     string  `json:"status"`
	DurationMs int64   `json:"duration_ms"`
	Error      string  `json:"error,omitempty"`
}

type usageDTO struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type postMessageResp struct {
	SessionID        string              `json:"session_id"`
	AssistantMessage assistantMessageDTO `json:"assistant_message"`
	ToolCalls        []toolCallDTO       `json:"tool_calls"`
	Usage            usageDTO            `json:"usage"`
	Iterations       int                 `json:"iterations"`
}

type messageDTO struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	Content    string `json:"content,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	// Model carries the LLM model id that produced the message — only
	// populated for role=assistant rows where the routing layer recorded
	// it. Older rows return "" so the SPA can fall back to "default".
	Model     string    `json:"model,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type listMessagesResp struct {
	Items []messageDTO `json:"items"`
	Total int          `json:"total"`
}

type usageTodayDTO struct {
	Date             string `json:"date"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	Requests         int64  `json:"requests"`
}

type mutatingProposalDTO struct {
	ID             string     `json:"id"`
	SessionID      string     `json:"session_id"`
	MessageID      *string    `json:"message_id,omitempty"`
	ToolCallID     *string    `json:"tool_call_id,omitempty"`
	ToolName       string     `json:"tool_name"`
	ArgsJSON       string     `json:"args_json"`
	ToolClass      string     `json:"tool_class"`
	ReviewerAgent  string     `json:"reviewer_agent"`
	ReviewerTaskID string     `json:"reviewer_task_id"`
	Decision       string     `json:"decision"`
	DecisionReason *string    `json:"decision_reason,omitempty"`
	OperatorUserID uint64     `json:"operator_user_id"`
	ApproverUserID *uint64    `json:"approver_user_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	ExecutedAt     *time.Time `json:"executed_at,omitempty"`
}

type listMutatingProposalsResp struct {
	Items  []mutatingProposalDTO `json:"items"`
	Total  int64                 `json:"total"`
	Limit  int                   `json:"limit"`
	Offset int                   `json:"offset"`
}

// --------- handlers ---------

func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	var req createSessionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	s, err := h.svc.CreateSession(r.Context(), caller, svc.CreateSessionInput{
		Title:             req.Title,
		Scope:             req.Scope,
		RelatedIncidentID: req.RelatedIncidentID,
		AgentID:           req.AgentID,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSessionDTO(s))
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	q := r.URL.Query()
	limit, offset := 0, 0
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			offset = n
		}
	}
	var relatedIncidentID *uint64
	if v := q.Get("related_incident_id"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			relatedIncidentID = &n
		}
	}
	list, err := h.svc.ListSessions(r.Context(), caller, limit, offset, relatedIncidentID)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]sessionDTO, 0, len(list))
	for _, s := range list {
		items = append(items, toSessionDTO(s))
	}
	writeJSON(w, http.StatusOK, listSessionsResp{Items: items, Total: len(items)})
}

func (h *Handler) postMessage(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req postMessageReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	opts := agent.RunOptions{
		Provider:         req.Provider,
		Model:            req.Model,
		Mentions:         toAgentMentions(req.Mentions),
		WebSearchEnabled: req.WebSearchEnabled,
		Locale:           req.Locale,
	}
	reply, err := h.svc.PostMessageWithOpts(r.Context(), caller, id, req.Content, opts)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPostMessageResp(id, reply))
}

// postMessageStream is the SSE variant of postMessage. The agent loop runs
// inline on the request goroutine; events are emitted as they occur and
// flushed immediately so the browser can render incrementally. The wire
// format is plain SSE: each frame is `event: <type>\ndata: <json>\n\n`.
//
// Frame types:
//
//	assistant   — one assistant turn was persisted
//	tool_start  — a tool_call row was persisted in pending state
//	tool_end    — the tool finished (status: success|error|timeout)
//	done        — final Reply (terminal success)
//	error       — terminal failure (run aborted)
//
// On error after streaming has started we still emit an `error` frame
// rather than changing the status code, since the response headers have
// already been sent.
func (h *Handler) postMessageStream(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req postMessageReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	opts := agent.RunOptions{
		Provider:         req.Provider,
		Model:            req.Model,
		Mentions:         toAgentMentions(req.Mentions),
		WebSearchEnabled: req.WebSearchEnabled,
		Locale:           req.Locale,
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		// No streaming support — fall back to blocking JSON. This keeps
		// dev environments behind buffering proxies usable.
		reply, err := h.svc.PostMessageWithOpts(r.Context(), caller, id, req.Content, opts)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toPostMessageResp(id, reply))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx response buffering
	w.WriteHeader(http.StatusOK)
	// Hint the client connection is alive immediately.
	_, _ = w.Write([]byte(": ok\n\n"))
	flusher.Flush()

	emit := func(e agent.Event) {
		writeSSE(w, flusher, eventName(e.Type), eventPayload(id, e))
	}

	reply, err := h.svc.PostMessageStreamWithOpts(r.Context(), caller, id, req.Content, emit, opts)
	if err != nil {
		writeSSE(w, flusher, "error", map[string]string{
			"error": err.Error(),
			"code":  errCode(err),
		})
		return
	}
	// Belt-and-suspenders: if the agent returned without emitting a
	// terminal "done" (shouldn't happen on the success path), backfill so
	// the client still resolves cleanly.
	if reply != nil {
		writeSSE(w, flusher, "summary", toPostMessageResp(id, reply))
	}
}

func eventName(t agent.EventType) string {
	switch t {
	case agent.EventAssistant:
		return "assistant"
	case agent.EventToolStart:
		return "tool_start"
	case agent.EventToolEnd:
		return "tool_end"
	case agent.EventDone:
		return "done"
	case agent.EventTaskNotification:
		return "task_notification"
	case agent.EventApprovalPending:
		return "approval_pending"
	default:
		return string(t)
	}
}

func eventPayload(sessionID string, e agent.Event) any {
	switch e.Type {
	case agent.EventAssistant:
		if e.Assistant == nil {
			return map[string]any{"session_id": sessionID}
		}
		return map[string]any{
			"session_id":         sessionID,
			"iteration":          e.Assistant.Iteration,
			"message_id":         e.Assistant.MessageID,
			"content":            e.Assistant.Content,
			"created_at":         e.Assistant.CreatedAt,
			"pending_tool_calls": e.Assistant.PendingToolCalls,
		}
	case agent.EventToolStart, agent.EventToolEnd:
		if e.Tool == nil {
			return map[string]any{"session_id": sessionID}
		}
		out := map[string]any{
			"session_id":   sessionID,
			"tool_call_id": e.Tool.ToolCallID,
			"name":         e.Tool.Name,
			"status":       e.Tool.Status,
			"started_at":   e.Tool.StartedAt,
			"duration_ms":  e.Tool.DurationMs,
		}
		if e.Tool.DeviceID != nil {
			out["edge_id"] = *e.Tool.DeviceID
		}
		if e.Tool.EndedAt != nil {
			out["ended_at"] = e.Tool.EndedAt
		}
		if e.Tool.Error != "" {
			out["error"] = e.Tool.Error
		}
		if e.Tool.ArgsJSON != "" {
			// Send as parsed JSON if valid (cleaner client display); fall
			// back to raw string when it isn't (the LLM occasionally emits
			// non-strict JSON we forwarded verbatim).
			var parsed any
			if err := json.Unmarshal([]byte(e.Tool.ArgsJSON), &parsed); err == nil {
				out["arguments"] = parsed
			} else {
				out["arguments_raw"] = e.Tool.ArgsJSON
			}
		}
		if e.Tool.ResultJSON != "" {
			var parsed any
			if err := json.Unmarshal([]byte(e.Tool.ResultJSON), &parsed); err == nil {
				out["result"] = parsed
			} else {
				out["result_raw"] = e.Tool.ResultJSON
			}
		}
		return out
	case agent.EventDone:
		if e.Done == nil {
			return map[string]any{"session_id": sessionID}
		}
		return toPostMessageResp(sessionID, e.Done)
	case agent.EventTaskNotification:
		if e.Notification == nil {
			return map[string]any{"session_id": sessionID}
		}
		out := map[string]any{
			"session_id": sessionID,
			"task_id":    e.Notification.TaskID,
			"status":     e.Notification.Status,
			"summary":    e.Notification.Summary,
		}
		if e.Notification.Result != "" {
			out["result"] = e.Notification.Result
		}
		if e.Notification.Err != "" {
			out["error"] = e.Notification.Err
		}
		if len(e.Notification.Usage) > 0 {
			out["usage"] = e.Notification.Usage
		}
		return out
	case agent.EventApprovalPending:
		if e.Approval == nil {
			return map[string]any{"session_id": sessionID}
		}
		out := map[string]any{
			"session_id":   sessionID,
			"approval_id":  e.Approval.ApprovalID,
			"tool_call_id": e.Approval.ToolCallID,
			"command":      e.Approval.Command,
		}
		if len(e.Approval.Credentials) > 0 {
			out["credentials"] = e.Approval.Credentials
		}
		return out
	default:
		return map[string]any{"session_id": sessionID}
	}
}

// writeSSE serialises one event frame. Errors are intentionally swallowed:
// if the client closed the connection there is nothing useful to do
// mid-stream, and the next Flush will surface the failure to the agent.
func writeSSE(w http.ResponseWriter, f http.Flusher, name string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{}`)
	}
	_, _ = w.Write([]byte("event: "))
	_, _ = w.Write([]byte(name))
	_, _ = w.Write([]byte("\ndata: "))
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n\n"))
	f.Flush()
}

func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	msgs, err := h.svc.ListMessages(r.Context(), caller, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]messageDTO, 0, len(msgs))
	for _, m := range msgs {
		items = append(items, toMessageDTO(m))
	}
	writeJSON(w, http.StatusOK, listMessagesResp{Items: items, Total: len(items)})
}

// closeSession backs DELETE /v1/chat/sessions/{id}. The route is named
// "close" for historical reasons; the actual operation is now a hard
// delete (rows + dependent messages / tool_calls are wiped). The soft-
// close path is still available via the service layer for callers that
// want to preserve audit history, but the UI DELETE is the destructive
// kind users expect.
// stopSession backs POST /v1/chat/sessions/{id}/stop — interrupts the
// session's in-flight turn (the SPA calls this on Esc). Idempotent: returns
// {"stopped": false} when no turn was running.
func (h *Handler) stopSession(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	stopped, err := h.svc.StopSession(r.Context(), caller, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": stopped})
}

func (h *Handler) closeSession(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.svc.DeleteSession(r.Context(), caller, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type renameSessionReq struct {
	Title string `json:"title"`
}

// renameSession handles PATCH /v1/chat/sessions/{id} — body is
// {"title": "..."}. Empty / whitespace-only titles 400; non-owners
// 404 (mirroring the rest of the session ownership story).
func (h *Handler) renameSession(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req renameSessionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	if err := h.svc.RenameSession(r.Context(), caller, id, req.Title); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// usageToday returns the cluster-global daily token rollup. Any
// authenticated caller may invoke it (no admin gating); auth itself is
// enforced by upstream middleware via tenantctx.
func (h *Handler) usageToday(w http.ResponseWriter, r *http.Request) {
	if _, ok := callerFromCtx(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	u, err := h.svc.UsageToday(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, usageTodayDTO{
		Date:             u.Date.Format("2006-01-02"),
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		Requests:         u.Requests,
	})
}

// @Summary List AI mutating proposal audit rows
// @Router /api/v1/aiops/mutating-proposals [get]
// @Success 200 {object} listMutatingProposalsResp
//
// listMutatingProposals backs GET /v1/aiops/mutating-proposals. It is the
// read-only audit surface for ReviewGate proposals; callers can filter by
// tool_name (for example execute_k8s_action) and reviewer decision.
func (h *Handler) listMutatingProposals(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	q := r.URL.Query()
	limit := parseOptionalInt(q.Get("limit"))
	offset := parseOptionalInt(q.Get("offset"))
	filter := biz.MutatingProposalFilter{
		ToolName: strings.TrimSpace(q.Get("tool_name")),
		Decision: strings.TrimSpace(q.Get("decision")),
		Limit:    limit,
		Offset:   offset,
	}
	items, total, err := h.svc.ListMutatingProposals(r.Context(), caller, filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	out := make([]mutatingProposalDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toMutatingProposalDTO(item))
	}
	writeJSON(w, http.StatusOK, listMutatingProposalsResp{
		Items:  out,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// searchMentions backs GET /v1/aiops/mentions/search?q=&type=&limit=
// — used by the SPA chat input's @-popover. Auth-gated by the caller-
// in-context check so anonymous probes get 401, matching the rest of
// the aiops surface.
func (h *Handler) searchMentions(w http.ResponseWriter, r *http.Request) {
	if _, ok := callerFromCtx(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	q := r.URL.Query()
	term := strings.TrimSpace(q.Get("q"))
	filter := strings.TrimSpace(q.Get("type"))
	limit := 0
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if h.mentions == nil {
		// Search backend not wired (deployments without device/alert biz);
		// return an empty list so the popover degrades cleanly.
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	items, err := h.mentions.Search(r.Context(), mentions.Query{
		Term:   term,
		Filter: mentions.Type(filter),
		Limit:  limit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if items == nil {
		items = []mentions.Item{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// listModels backs GET /v1/aiops/models — returns the configured-
// provider catalog and the default (provider, model) pair so the SPA
// can render the per-message model selector. An empty catalog hides
// the selector entirely (no LLM configured / single-provider mode).
func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := callerFromCtx(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	type providerDTO struct {
		ID     string   `json:"id"`
		Label  string   `json:"label"`
		Models []string `json:"models"`
		Model  string   `json:"model,omitempty"`
	}
	type defaultDTO struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	type respDTO struct {
		Providers []providerDTO `json:"providers"`
		Default   defaultDTO    `json:"default"`
	}
	out := respDTO{Providers: []providerDTO{}}
	if h.catalog != nil {
		for _, p := range h.catalog.Providers() {
			out.Providers = append(out.Providers, providerDTO{
				ID:     p.ID,
				Label:  p.Label,
				Models: p.Models,
				Model:  p.Model,
			})
		}
		defID, defModel := h.catalog.Default()
		out.Default = defaultDTO{Provider: defID, Model: defModel}
	}
	writeJSON(w, http.StatusOK, out)
}

// --------- translation ---------

func toSessionDTO(s *model.Session) sessionDTO {
	out := sessionDTO{
		ID:                s.ID,
		UserID:            s.UserID,
		Title:             s.Title,
		RelatedIncidentID: s.RelatedIncidentID,
		AgentID:           s.AgentID,
		CreatedAt:         s.CreatedAt,
		UpdatedAt:         s.UpdatedAt,
		ClosedAt:          s.ClosedAt,
	}
	if s.ScopeJSON != nil && *s.ScopeJSON != "" {
		var scope []string
		if err := json.Unmarshal([]byte(*s.ScopeJSON), &scope); err == nil {
			out.Scope = scope
		}
	}
	return out
}

func toMessageDTO(m *model.Message) messageDTO {
	out := messageDTO{
		ID:        m.ID,
		Role:      m.Role,
		CreatedAt: m.CreatedAt,
	}
	if m.Content != nil {
		out.Content = *m.Content
	}
	if m.ToolCallID != nil {
		out.ToolCallID = *m.ToolCallID
	}
	if m.ToolName != nil {
		out.ToolName = *m.ToolName
	}
	if m.Model != nil {
		out.Model = *m.Model
	}
	return out
}

func toPostMessageResp(sessionID string, reply *agent.Reply) postMessageResp {
	if reply == nil {
		return postMessageResp{SessionID: sessionID}
	}
	var asst assistantMessageDTO
	if reply.Message != nil {
		asst.ID = reply.Message.ID
		if reply.Message.Content != nil {
			asst.Content = *reply.Message.Content
		}
		asst.CreatedAt = reply.Message.CreatedAt
	}
	tcs := make([]toolCallDTO, 0, len(reply.ToolCalls))
	for _, tc := range reply.ToolCalls {
		dto := toolCallDTO{
			Name:     tc.ToolName,
			Status:   tc.Status,
			DeviceID: tc.DeviceID,
		}
		if tc.EndedAt != nil {
			dto.DurationMs = tc.EndedAt.Sub(tc.StartedAt).Milliseconds()
		}
		if tc.Error != nil {
			dto.Error = *tc.Error
		}
		tcs = append(tcs, dto)
	}
	return postMessageResp{
		SessionID:        sessionID,
		AssistantMessage: asst,
		ToolCalls:        tcs,
		Usage: usageDTO{
			PromptTokens:     reply.Usage.PromptTokens,
			CompletionTokens: reply.Usage.CompletionTokens,
			TotalTokens:      reply.Usage.TotalTokens,
		},
		Iterations: reply.Iterations,
	}
}

func toMutatingProposalDTO(p *model.MutatingProposal) mutatingProposalDTO {
	if p == nil {
		return mutatingProposalDTO{}
	}
	return mutatingProposalDTO{
		ID:             p.ID,
		SessionID:      p.SessionID,
		MessageID:      p.MessageID,
		ToolCallID:     p.ToolCallID,
		ToolName:       p.ToolName,
		ArgsJSON:       p.ArgsJSON,
		ToolClass:      p.ToolClass,
		ReviewerAgent:  p.ReviewerAgent,
		ReviewerTaskID: p.ReviewerTaskID,
		Decision:       p.Decision,
		DecisionReason: p.DecisionReason,
		OperatorUserID: p.OperatorUserID,
		ApproverUserID: p.ApproverUserID,
		CreatedAt:      p.CreatedAt,
		DecidedAt:      p.DecidedAt,
		ExecutedAt:     p.ExecutedAt,
	}
}

// --------- helpers ---------

func callerFromCtx(ctx context.Context) (svc.Caller, bool) {
	t, ok := tenantctx.From(ctx)
	if !ok {
		return svc.Caller{}, false
	}
	return svc.Caller{UserID: t.UserID, Role: t.Role}, true
}

func parseID(r *http.Request) (string, error) {
	raw := chi.URLParam(r, "id")
	if raw == "" {
		return "", errors.Join(errs.ErrInvalid, errors.New("invalid session id"))
	}
	return raw, nil
}

func parseOptionalInt(raw string) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return n
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeErr(w http.ResponseWriter, err error) {
	status := errs.HTTPStatus(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: err.Error(), Code: errCode(err)})
}

func errCode(err error) string {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		return "not-found"
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrForbidden), errors.Is(err, errs.ErrTenantMismatch):
		return "forbidden"
	case errors.Is(err, errs.ErrConflict):
		return "conflict"
	case errors.Is(err, errs.ErrInvalid):
		return "invalid"
	case errors.Is(err, errs.ErrBudgetExceeded):
		return "budget-exceeded"
	case errors.Is(err, errs.ErrEdgeOffline):
		return "edge-offline"
	case errors.Is(err, errs.ErrNotWiredYet):
		return "not-wired-yet"
	default:
		return "internal"
	}
}

// --------- agent inventory (Phase 1) ---------

// agentDTO is the Side-Panel-friendly view of a chatruntime persona.
// Trimmed down from chatruntime.Agent (which carries fields the SPA
// doesn't need to render — Background / OmitClaudeMd / Metadata yaml).
type agentDTO struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	WhenToUse        string   `json:"when_to_use,omitempty"`
	Tools            []string `json:"tools,omitempty"`
	DisallowedTools  []string `json:"disallowed_tools,omitempty"`
	PermissionMode   string   `json:"permission_mode,omitempty"`
	Model            string   `json:"model,omitempty"`
	MaxTurns         int      `json:"max_turns,omitempty"`
	SystemPrompt     string   `json:"system_prompt,omitempty"`
	CriticalReminder string   `json:"critical_reminder,omitempty"`
	// Source: "builtin" | "disk" | "user". When empty default to
	// "builtin" client-side. Determines whether the SPA shows
	// edit/delete affordances on the agent card.
	Source string `json:"source,omitempty"`
}

func toAgentDTO(a *chatruntime.Agent) agentDTO {
	if a == nil {
		return agentDTO{}
	}
	src := a.Source
	if src == "" {
		// Disk-loaded personas don't tag Source today; default them so
		// the SPA can decide visibility ("disk" = read-only).
		src = "disk"
	}
	return agentDTO{
		Name:             a.Name,
		Description:      a.Description,
		WhenToUse:        a.WhenToUse,
		Tools:            append([]string(nil), a.Tools...),
		DisallowedTools:  append([]string(nil), a.DisallowedTools...),
		PermissionMode:   a.PermissionMode,
		Model:            a.Model,
		MaxTurns:         a.MaxTurns,
		SystemPrompt:     a.SystemPrompt,
		CriticalReminder: a.CriticalReminder,
		Source:           src,
	}
}

type listAgentsResp struct {
	Items []agentDTO `json:"items"`
	Total int        `json:"total"`
}

func (h *Handler) listAgents(w http.ResponseWriter, r *http.Request) {
	if h.agents == nil {
		writeJSON(w, http.StatusOK, listAgentsResp{Items: []agentDTO{}, Total: 0})
		return
	}
	all := h.agents.All()
	items := make([]agentDTO, 0, len(all))
	for _, a := range all {
		items = append(items, toAgentDTO(a))
	}
	writeJSON(w, http.StatusOK, listAgentsResp{Items: items, Total: len(items)})
}

func (h *Handler) getAgent(w http.ResponseWriter, r *http.Request) {
	if h.agents == nil {
		writeErr(w, errs.ErrNotFound)
		return
	}
	name := chi.URLParam(r, "name")
	a, ok := h.agents.ByName(name)
	if !ok {
		writeErr(w, errs.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toAgentDTO(a))
}

// --------- user-agent CRUD (Phase 3) ---------

type userAgentReq struct {
	Name             string   `json:"name,omitempty"` // ignored on PATCH; required on POST
	Description      string   `json:"description"`
	WhenToUse        string   `json:"when_to_use,omitempty"`
	SystemPrompt     string   `json:"system_prompt"`
	CriticalReminder string   `json:"critical_reminder,omitempty"`
	AllowedTools     []string `json:"allowed_tools,omitempty"`
	DisallowedTools  []string `json:"disallowed_tools,omitempty"`
	PermissionMode   string   `json:"permission_mode,omitempty"`
	Model            string   `json:"model,omitempty"`
	MaxTurns         int      `json:"max_turns,omitempty"`
}

func (h *Handler) createUserAgent(w http.ResponseWriter, r *http.Request) {
	if h.userAgents == nil {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	if caller.IsViewer() {
		writeErr(w, fmt.Errorf("%w: viewer cannot create agents", errs.ErrForbidden))
		return
	}
	var req userAgentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	row, err := h.userAgents.Create(r.Context(), svc.CreateUserAgentInput{
		UserID:           caller.UserID,
		Name:             req.Name,
		Description:      req.Description,
		WhenToUse:        req.WhenToUse,
		SystemPrompt:     req.SystemPrompt,
		CriticalReminder: req.CriticalReminder,
		AllowedTools:     req.AllowedTools,
		DisallowedTools:  req.DisallowedTools,
		PermissionMode:   req.PermissionMode,
		Model:            req.Model,
		MaxTurns:         req.MaxTurns,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUserAgentDTO(row))
}

func (h *Handler) updateUserAgent(w http.ResponseWriter, r *http.Request) {
	if h.userAgents == nil {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	name := chi.URLParam(r, "name")
	var req userAgentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	row, err := h.userAgents.Update(r.Context(), caller, name, svc.UpdateUserAgentInput{
		Description:      req.Description,
		WhenToUse:        req.WhenToUse,
		SystemPrompt:     req.SystemPrompt,
		CriticalReminder: req.CriticalReminder,
		AllowedTools:     req.AllowedTools,
		DisallowedTools:  req.DisallowedTools,
		PermissionMode:   req.PermissionMode,
		Model:            req.Model,
		MaxTurns:         req.MaxTurns,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserAgentDTO(row))
}

func (h *Handler) deleteUserAgent(w http.ResponseWriter, r *http.Request) {
	if h.userAgents == nil {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	name := chi.URLParam(r, "name")
	if err := h.userAgents.Delete(r.Context(), caller, name); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteAgent is the generic delete: works on any non-builtin agent
// regardless of Source. For source=user we also clear the DB row;
// for source=disk we just remove from the live registry (the .md
// file remains and reloads on restart — this is intentional, the
// SPA delete is session-scoped). Source=builtin and the special
// "default" persona reject with ErrInvalid.
func (h *Handler) deleteAgent(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	if caller.IsViewer() {
		writeErr(w, fmt.Errorf("%w: viewer cannot delete agents", errs.ErrForbidden))
		return
	}
	name := chi.URLParam(r, "name")
	if name == "default" {
		writeErr(w, fmt.Errorf("%w: 默认助理不可删除", errs.ErrInvalid))
		return
	}
	if h.agents == nil {
		writeErr(w, errs.ErrNotFound)
		return
	}
	persona, ok := h.agents.ByName(name)
	if !ok {
		writeErr(w, errs.ErrNotFound)
		return
	}
	if persona.Source == "builtin" {
		writeErr(w, fmt.Errorf("%w: 内置助理不可删除", errs.ErrInvalid))
		return
	}
	// User-source: clear DB row first (registry refresh happens via
	// the userAgents service hook, same path as the legacy endpoint).
	if persona.Source == "user" {
		if h.userAgents == nil {
			writeErr(w, errs.ErrNotWiredYet)
			return
		}
		if err := h.userAgents.Delete(r.Context(), caller, name); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Disk-source (or empty): drop from live registry only. Session-
	// scoped — comes back on restart.
	h.agents.Remove(name)
	w.WriteHeader(http.StatusNoContent)
}

// userAgentDTO mirrors a saved row in shape compatible with agentDTO,
// so the SPA can dedupe between /v1/agents and the create response.
func toUserAgentDTO(row *model.UserAgent) agentDTO {
	if row == nil {
		return agentDTO{}
	}
	allowed := unmarshalUASlice(row.AllowedToolsJSON)
	disallowed := unmarshalUASlice(row.DisallowedToolsJSON)
	return agentDTO{
		Name:             row.Name,
		Description:      row.Description,
		WhenToUse:        row.WhenToUse,
		Tools:            allowed,
		DisallowedTools:  disallowed,
		PermissionMode:   row.PermissionMode,
		Model:            row.Model,
		MaxTurns:         row.MaxTurns,
		SystemPrompt:     row.SystemPrompt,
		CriticalReminder: row.CriticalReminder,
		Source:           "user",
	}
}

func unmarshalUASlice(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
