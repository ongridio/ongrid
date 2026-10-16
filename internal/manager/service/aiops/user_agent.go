// Package aiops's user_chatprotocol.go is the Phase-3 user-defined persona
// service. It persists each user-created agent to user_agents table
// AND mirrors it into the live chatruntime.AgentRegistry so creates /
// updates / deletes take effect immediately (no manager restart).
//
// Flow on create:
//
//	HTTP POST /v1/agents/custom →
//	  service.CreateUserAgent →
//	    1. validate (name shape, no collision with disk persona)
//	    2. repo.Create (persists row)
//	    3. registry.Add (live runtime now sees it)
//
// Update / Delete are symmetric: db row first, then registry mutation.
// On a partial failure (db succeeds, registry add fails) we log and
// continue — the next manager restart hydrates the registry from db
// so eventual consistency holds.
package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// UserAgentRepo is the narrow persistence contract.
type UserAgentRepo interface {
	List(ctx context.Context) ([]*model.UserAgent, error)
	GetByName(ctx context.Context, name string) (*model.UserAgent, error)
	Create(ctx context.Context, ua *model.UserAgent) error
	Update(ctx context.Context, name string, ua *model.UserAgent) error
	Delete(ctx context.Context, name string) error
}

// UserAgentService bundles persistence + the live chatruntime registry.
// Caller is the authed user (passed through to set UserAgent.UserID;
// not used for visibility filtering — see model comment).
type UserAgentService struct {
	repo     UserAgentRepo
	registry *chatruntime.AgentRegistry
	log      *slog.Logger
}

// NewUserAgentService wires the dependencies. registry may be nil
// when the graph kernel didn't build (legacy mode); CRUD then only
// touches the DB and the next graph-kernel restart will hydrate.
func NewUserAgentService(repo UserAgentRepo, registry *chatruntime.AgentRegistry, log *slog.Logger) *UserAgentService {
	if log == nil {
		log = slog.Default()
	}
	return &UserAgentService{repo: repo, registry: registry, log: log}
}

// nameRE constrains user-chosen agent names to the same shape skill
// keys use (lower_snake or kebab-case allowed; max 64 chars). Prevents
// names that conflict with the path / UI grammar.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// reservedNames are names the user can't use because they collide with
// the in-binary or disk-loaded personas. Built dynamically from the
// registry at validation time so disk additions are honored.
var reservedSourceTags = map[string]bool{
	"builtin": true,
	"disk":    true,
}

// CreateUserAgentInput is the form-shaped input.
type CreateUserAgentInput struct {
	UserID           uint64
	Name             string
	Description      string
	WhenToUse        string
	SystemPrompt     string
	CriticalReminder string
	AllowedTools     []string
	DisallowedTools  []string
	PermissionMode   string
	Model            string
	MaxTurns         int
}

// HydrateRegistry loads every persisted user agent into the live
// registry. Called once at boot after the disk loader has run so
// user-created personas are immediately available to spawned chats.
func (s *UserAgentService) HydrateRegistry(ctx context.Context) error {
	if s.registry == nil {
		return nil
	}
	rows, err := s.repo.List(ctx)
	if err != nil {
		return fmt.Errorf("user_agent: list at boot: %w", err)
	}
	for _, row := range rows {
		ag := userAgentRowToChatruntimeAgent(row)
		s.registry.Replace(ag)
	}
	s.log.Info("user_agent: hydrated registry", slog.Int("count", len(rows)))
	return nil
}

// List returns every user-defined persona.
func (s *UserAgentService) List(ctx context.Context) ([]*model.UserAgent, error) {
	return s.repo.List(ctx)
}

// Create persists a new user agent and mirrors it into the registry.
// Validates name shape, rejects collisions with built-in / disk-loaded
// personas (keeps the system → user precedence clean).
func (s *UserAgentService) Create(ctx context.Context, in CreateUserAgentInput) (*model.UserAgent, error) {
	name := strings.TrimSpace(in.Name)
	if !nameRE.MatchString(name) {
		return nil, fmt.Errorf("%w: name must match [a-z][a-z0-9_-]{0,63}", errs.ErrInvalid)
	}
	if reservedSourceTags[name] {
		return nil, fmt.Errorf("%w: name %q is reserved", errs.ErrInvalid, name)
	}
	if strings.TrimSpace(in.Description) == "" {
		return nil, fmt.Errorf("%w: description required", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.SystemPrompt) == "" {
		return nil, fmt.Errorf("%w: system_prompt required", errs.ErrInvalid)
	}
	// Collision with disk-loaded / builtin personas. We don't allow
	// users to shadow them — the file-loaded ones win on disk reload
	// and that race would surprise the user.
	if s.registry != nil {
		if existing, ok := s.registry.ByName(name); ok && existing.Source != "user" {
			return nil, fmt.Errorf("%w: name %q already used by a built-in persona", errs.ErrInvalid, name)
		}
	}
	// Collision with existing user chatprotocol.
	if existing, _ := s.repo.GetByName(ctx, name); existing != nil {
		return nil, fmt.Errorf("%w: name %q already taken", errs.ErrInvalid, name)
	}

	allowedJSON, _ := marshalStringSlice(in.AllowedTools)
	disallowedJSON, _ := marshalStringSlice(in.DisallowedTools)
	now := time.Now().UTC()
	row := &model.UserAgent{
		UserID:              in.UserID,
		Name:                name,
		Description:         strings.TrimSpace(in.Description),
		WhenToUse:           in.WhenToUse,
		SystemPrompt:        in.SystemPrompt,
		CriticalReminder:    in.CriticalReminder,
		AllowedToolsJSON:    allowedJSON,
		DisallowedToolsJSON: disallowedJSON,
		PermissionMode:      in.PermissionMode,
		Model:               in.Model,
		MaxTurns:            in.MaxTurns,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := s.repo.Create(ctx, row); err != nil {
		return nil, fmt.Errorf("user_agent: create: %w", err)
	}
	if s.registry != nil {
		s.registry.Add(userAgentRowToChatruntimeAgent(row))
	}
	return row, nil
}

// UpdateUserAgentInput mirrors CreateUserAgentInput minus the immutable
// Name field.
type UpdateUserAgentInput struct {
	Description      string
	WhenToUse        string
	SystemPrompt     string
	CriticalReminder string
	AllowedTools     []string
	DisallowedTools  []string
	PermissionMode   string
	Model            string
	MaxTurns         int
}

// Update edits a user agent's fields (name immutable). Replaces the
// registry copy in place so chat sessions pick up the new prompt /
// tool list on the next user message.
//
// Authorization:
//   - viewer is refused outright
//   - non-owners get ErrForbidden (IDOR fix — previously any logged-in
//     user could overwrite anyone's custom agent)
//   - admin bypasses ownership for ops repair scenarios
func (s *UserAgentService) Update(ctx context.Context, caller Caller, name string, in UpdateUserAgentInput) (*model.UserAgent, error) {
	if caller.IsViewer() {
		return nil, fmt.Errorf("%w: viewer cannot modify agents", errs.ErrForbidden)
	}
	if !nameRE.MatchString(name) {
		return nil, errs.ErrNotFound
	}
	if strings.TrimSpace(in.Description) == "" {
		return nil, fmt.Errorf("%w: description required", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.SystemPrompt) == "" {
		return nil, fmt.Errorf("%w: system_prompt required", errs.ErrInvalid)
	}
	existing, err := s.repo.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if !caller.IsAdmin() && existing.UserID != caller.UserID {
		return nil, fmt.Errorf("%w: not the owner of this agent", errs.ErrForbidden)
	}
	allowedJSON, _ := marshalStringSlice(in.AllowedTools)
	disallowedJSON, _ := marshalStringSlice(in.DisallowedTools)
	now := time.Now().UTC()
	updated := &model.UserAgent{
		ID:                  existing.ID,
		UserID:              existing.UserID,
		Name:                name,
		Description:         strings.TrimSpace(in.Description),
		WhenToUse:           in.WhenToUse,
		SystemPrompt:        in.SystemPrompt,
		CriticalReminder:    in.CriticalReminder,
		AllowedToolsJSON:    allowedJSON,
		DisallowedToolsJSON: disallowedJSON,
		PermissionMode:      in.PermissionMode,
		Model:               in.Model,
		MaxTurns:            in.MaxTurns,
		CreatedAt:           existing.CreatedAt,
		UpdatedAt:           now,
	}
	if err := s.repo.Update(ctx, name, updated); err != nil {
		return nil, err
	}
	if s.registry != nil {
		s.registry.Replace(userAgentRowToChatruntimeAgent(updated))
	}
	return updated, nil
}

// Delete removes a user chatprotocol. Returns ErrNotFound when no user-defined
// row matches. Disk-loaded personas can't be deleted via this path —
// they live as files and survive a restart.
//
// Authorization: same as Update — viewer denied, non-owner
// denied unless admin.
func (s *UserAgentService) Delete(ctx context.Context, caller Caller, name string) error {
	if caller.IsViewer() {
		return fmt.Errorf("%w: viewer cannot modify agents", errs.ErrForbidden)
	}
	existing, err := s.repo.GetByName(ctx, name)
	if err != nil {
		return err
	}
	if !caller.IsAdmin() && existing.UserID != caller.UserID {
		return fmt.Errorf("%w: not the owner of this agent", errs.ErrForbidden)
	}
	if err := s.repo.Delete(ctx, name); err != nil {
		return err
	}
	if s.registry != nil {
		s.registry.Remove(name)
	}
	return nil
}

// userAgentRowToChatruntimeAgent rebuilds the in-memory Agent struct
// from a UserAgent row. Used both at boot (HydrateRegistry) and after
// CRUD mutations.
func userAgentRowToChatruntimeAgent(row *model.UserAgent) *chatruntime.Agent {
	if row == nil {
		return nil
	}
	allowed := unmarshalStringSlice(row.AllowedToolsJSON)
	disallowed := unmarshalStringSlice(row.DisallowedToolsJSON)
	return &chatruntime.Agent{
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

func marshalStringSlice(s []string) (string, error) {
	if len(s) == 0 {
		return "", nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalStringSlice(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// IsInvalid returns true when err is errs.ErrInvalid (used by tests).
func IsInvalid(err error) bool { return errors.Is(err, errs.ErrInvalid) }
