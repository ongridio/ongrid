// Package mcp is the biz tier for external MCP server registrations
// (HLD-018). It owns CRUD validation, credential resolution (it pulls the
// referenced vault credential's fields and expands them into the HTTP header
// template), and the connection probe (initialize → tools/list) whose result
// is cached back onto the row.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/mcp"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
)

// Transport values.
const (
	TransportHTTP  = "http"
	TransportStdio = "stdio"
)

// Repo is the persistence contract (data/mcp/store).
type Repo interface {
	Create(ctx context.Context, s *model.Server) error
	Get(ctx context.Context, id uint64) (*model.Server, error)
	GetByName(ctx context.Context, name string) (*model.Server, error)
	List(ctx context.Context) ([]*model.Server, error)
	Update(ctx context.Context, id uint64, patch *model.Server) error
	Delete(ctx context.Context, id uint64) error
	SetStatus(ctx context.Context, id uint64, status, lastErr string) error
	SetToolsCache(ctx context.Context, id uint64, toolsJSON string) error
}

// SecretResolver resolves a named vault credential into its plaintext field
// map (in-process only). *bizsecret.Usecase satisfies this structurally.
type SecretResolver interface {
	ResolveFields(ctx context.Context, name string) (map[string]string, error)
}

// ToolChangeHook is notified after MCP registrations or discovered tool
// snapshots change. cmd/ongrid uses it to refresh the chat runtime toolbag
// without restarting the manager.
type ToolChangeHook func(ctx context.Context)

// Usecase is the MCP-server-registration facade.
type Usecase struct {
	repo           Repo
	secrets        SecretResolver
	log            *slog.Logger
	onToolsChanged ToolChangeHook
}

// NewUsecase wires the repo, credential resolver, and logger.
func NewUsecase(repo Repo, secrets SecretResolver, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{repo: repo, secrets: secrets, log: log}
}

// SetToolChangeHook wires the optional hot-reload callback. nil clears it.
func (u *Usecase) SetToolChangeHook(h ToolChangeHook) {
	if u == nil {
		return
	}
	u.onToolsChanged = h
}

func (u *Usecase) notifyToolsChanged(ctx context.Context) {
	if u == nil || u.onToolsChanged == nil {
		return
	}
	u.onToolsChanged(ctx)
}

// Create validates and stores a new server registration.
func (u *Usecase) Create(ctx context.Context, s *model.Server) (*model.Server, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil server", errs.ErrInvalid)
	}
	if err := normalizeAndValidate(s); err != nil {
		return nil, err
	}
	if err := u.repo.Create(ctx, s); err != nil {
		return nil, err
	}
	u.notifyToolsChanged(ctx)
	return s, nil
}

// Update validates and writes the editable fields of an existing server.
func (u *Usecase) Update(ctx context.Context, id uint64, patch *model.Server) error {
	if patch == nil {
		return fmt.Errorf("%w: nil patch", errs.ErrInvalid)
	}
	if err := normalizeAndValidate(patch); err != nil {
		return err
	}
	if err := u.repo.Update(ctx, id, patch); err != nil {
		return err
	}
	u.notifyToolsChanged(ctx)
	return nil
}

// Delete removes a server registration.
func (u *Usecase) Delete(ctx context.Context, id uint64) error {
	if err := u.repo.Delete(ctx, id); err != nil {
		return err
	}
	u.notifyToolsChanged(ctx)
	return nil
}

// Get returns one server by id.
func (u *Usecase) Get(ctx context.Context, id uint64) (*model.Server, error) {
	return u.repo.Get(ctx, id)
}

// List returns all registered servers.
func (u *Usecase) List(ctx context.Context) ([]*model.Server, error) { return u.repo.List(ctx) }

// ListEnabled returns the enabled servers — boot connects to these to pull
// tools and register them into the agent toolbag.
func (u *Usecase) ListEnabled(ctx context.Context) ([]*model.Server, error) {
	all, err := u.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Server, 0, len(all))
	for _, s := range all {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out, nil
}

// CallTool connects to the named server and invokes one tool, returning the
// flattened text content. Used by the MCP tool adapter (trusted path) and by
// the mcp_call approval executor (post-approval). Errors when the server is
// unknown, unreachable, or the tool reports isError.
func (u *Usecase) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (string, error) {
	s, err := u.repo.GetByName(ctx, serverName)
	if err != nil {
		return "", err
	}
	cli, err := u.BuildClient(ctx, s)
	if err != nil {
		return "", err
	}
	if err := cli.Initialize(ctx); err != nil {
		return "", err
	}
	res, err := cli.CallTool(ctx, toolName, args)
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", fmt.Errorf("mcp tool %s/%s reported error: %s", serverName, toolName, res.TextContent())
	}
	return res.TextContent(), nil
}

// BuildClient constructs an MCP client for s, resolving the referenced
// credential (if any) and expanding {{field}} placeholders in the header
// template into concrete HTTP headers. stdio transport is not supported yet.
func (u *Usecase) BuildClient(ctx context.Context, s *model.Server) (*mcpclient.Client, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil server", errs.ErrInvalid)
	}
	switch strings.ToLower(strings.TrimSpace(s.Transport)) {
	case TransportStdio:
		return nil, fmt.Errorf("stdio transport not supported yet")
	case TransportHTTP, "":
		// fallthrough below
	default:
		return nil, fmt.Errorf("%w: unknown transport %q", errs.ErrInvalid, s.Transport)
	}
	if strings.TrimSpace(s.Endpoint) == "" {
		return nil, fmt.Errorf("%w: endpoint required for http transport", errs.ErrInvalid)
	}

	var fields map[string]string
	if strings.TrimSpace(s.Credential) != "" {
		var err error
		fields, err = u.secrets.ResolveFields(ctx, s.Credential)
		if err != nil {
			return nil, fmt.Errorf("resolve credential %q: %w", s.Credential, err)
		}
	}

	headers, err := expandHeaders(s.HeaderTemplateJSON, fields)
	if err != nil {
		return nil, err
	}
	return mcpclient.NewHTTP(s.Endpoint, headers, 0), nil
}

// TestConnection probes the server (initialize → tools/list) and caches the
// outcome. On success it stores the tools snapshot + status "ok"; on failure
// it records status "error" with the error message and returns the error.
func (u *Usecase) TestConnection(ctx context.Context, id uint64) ([]mcpclient.Tool, error) {
	s, err := u.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	cli, err := u.BuildClient(ctx, s)
	if err != nil {
		_ = u.repo.SetStatus(ctx, id, "error", err.Error())
		return nil, err
	}
	if err := cli.Initialize(ctx); err != nil {
		_ = u.repo.SetStatus(ctx, id, "error", err.Error())
		return nil, err
	}
	tools, err := cli.ListTools(ctx)
	if err != nil {
		_ = u.repo.SetStatus(ctx, id, "error", err.Error())
		return nil, err
	}
	if b, mErr := json.Marshal(tools); mErr == nil {
		_ = u.repo.SetToolsCache(ctx, id, string(b))
	}
	_ = u.repo.SetStatus(ctx, id, "ok", "")
	u.notifyToolsChanged(ctx)
	return tools, nil
}

// --- helpers ---

// normalizeAndValidate trims + defaults the editable fields and enforces the
// invariants (name required, known transport, http needs an endpoint).
func normalizeAndValidate(s *model.Server) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return fmt.Errorf("%w: name required", errs.ErrInvalid)
	}
	s.Transport = strings.ToLower(strings.TrimSpace(s.Transport))
	if s.Transport == "" {
		s.Transport = TransportHTTP
	}
	switch s.Transport {
	case TransportHTTP:
		if strings.TrimSpace(s.Endpoint) == "" {
			return fmt.Errorf("%w: endpoint required for http transport", errs.ErrInvalid)
		}
	case TransportStdio:
		// command may be empty this phase; no further checks.
	default:
		return fmt.Errorf("%w: transport must be http or stdio", errs.ErrInvalid)
	}
	return nil
}

// expandHeaders parses the header template (a JSON map[string]string) and
// replaces every {{field}} placeholder with the matching credential value.
func expandHeaders(templateJSON string, fields map[string]string) (map[string]string, error) {
	if strings.TrimSpace(templateJSON) == "" {
		return nil, nil
	}
	var tmpl map[string]string
	if err := json.Unmarshal([]byte(templateJSON), &tmpl); err != nil {
		return nil, fmt.Errorf("%w: header template must be a JSON object of strings: %v", errs.ErrInvalid, err)
	}
	out := make(map[string]string, len(tmpl))
	for k, v := range tmpl {
		for fk, fv := range fields {
			v = strings.ReplaceAll(v, "{{"+fk+"}}", fv)
		}
		out[k] = v
	}
	return out, nil
}
