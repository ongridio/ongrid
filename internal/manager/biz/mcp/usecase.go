// Package mcp is the biz tier for external MCP server registrations
// (HLD-018). It owns CRUD validation, credential resolution (it pulls the
// referenced vault credential's fields and expands them into the HTTP header
// template), and the connection probe (initialize → tools/list) whose result
// is cached back onto the row.
package mcp

import (
	"context"
	"crypto/sha256"
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

// ToolChangeHook updates the in-memory chat tool catalog after an MCP server
// changes. It receives one server only: the caller must not reconnect every
// configured MCP server while handling an admin CRUD request. A nil server
// removes the tools whose wire names belong to serverName.
type ToolChangeHook func(ctx context.Context, serverName string, server *model.Server, tools []mcpclient.Tool)

// Usecase is the MCP-server-registration facade.
type Usecase struct {
	repo                Repo
	secrets             SecretResolver
	log                 *slog.Logger
	onToolsChange       ToolChangeHook
	registrationChanges chan struct{}
	// commitGate serializes registration changes and discovery publication.
	// Remote MCP requests never hold it; waiting respects request cancellation.
	commitGate    chan struct{}
	probeSequence uint64
	latestProbe   map[uint64]uint64
	manualProbes  map[uint64]int
}

// SetToolChangeHook wires the optional runtime catalog updater. It is set
// once during manager startup before HTTP requests are served.
func (u *Usecase) SetToolChangeHook(h ToolChangeHook) {
	if u != nil {
		u.onToolsChange = h
	}
}

func (u *Usecase) notifyToolsChanged(ctx context.Context, serverName string, server *model.Server, tools []mcpclient.Tool) {
	if u != nil && u.onToolsChange != nil {
		u.onToolsChange(ctx, serverName, server, tools)
	}
}

// NewUsecase wires the repo, credential resolver, and logger.
func NewUsecase(repo Repo, secrets SecretResolver, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{repo: repo, secrets: secrets, log: log, registrationChanges: make(chan struct{}, 1), commitGate: make(chan struct{}, 1), latestProbe: make(map[uint64]uint64), manualProbes: make(map[uint64]int)}
}

// RegistrationChanges 唤醒唯一的发现 supervisor。合并连续变更，由消费者重新读取
// 最新注册表；不在 HTTP 请求中等待远端连接，也不为每次保存启动 goroutine。
func (u *Usecase) RegistrationChanges() <-chan struct{} { return u.registrationChanges }

func (u *Usecase) notifyRegistrationChanged() {
	select {
	case u.registrationChanges <- struct{}{}:
	default:
	}
}

func (u *Usecase) enterCommit(ctx context.Context) error {
	select {
	case u.commitGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (u *Usecase) leaveCommit() { <-u.commitGate }

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
	u.notifyRegistrationChanged()
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
	if err := u.enterCommit(ctx); err != nil {
		return err
	}
	defer u.leaveCommit()
	previous, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := u.repo.Update(ctx, id, patch); err != nil {
		return err
	}
	defer u.notifyRegistrationChanged()
	// Invalidate discoveries started before this change, including disable/re-enable.
	delete(u.latestProbe, id)
	updated, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	connectionChanged := !sameToolEndpoint(previous, updated)
	// Remove the old wire-name prefix first. A changed endpoint or credential
	// must be verified again before the assistant can call its stale tools.
	u.notifyToolsChanged(ctx, previous.Name, nil, nil)
	if connectionChanged {
		if err := u.repo.SetToolsCache(ctx, id, ""); err != nil {
			return fmt.Errorf("clear mcp tools cache: %w", err)
		}
		if err := u.repo.SetStatus(ctx, id, "", ""); err != nil {
			return fmt.Errorf("clear mcp probe status: %w", err)
		}
		return nil
	}
	if updated.Enabled {
		tools, err := cachedTools(updated.ToolsCacheJSON)
		if err == nil && len(tools) > 0 {
			u.notifyToolsChanged(ctx, updated.Name, updated, tools)
		}
	}
	return nil
}

// Delete removes a server registration.
func (u *Usecase) Delete(ctx context.Context, id uint64) error {
	if err := u.enterCommit(ctx); err != nil {
		return err
	}
	defer u.leaveCommit()
	server, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := u.repo.Delete(ctx, id); err != nil {
		return err
	}
	defer u.notifyRegistrationChanged()
	delete(u.latestProbe, id)
	u.notifyToolsChanged(ctx, server.Name, nil, nil)
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
	return u.CallToolRevision(ctx, serverName, toolName, args, "")
}

// CallToolRevision prevents a pending approval from following a replaced endpoint,
// credential or tool schema. Empty revision is reserved for existing direct callers.
func (u *Usecase) CallToolRevision(ctx context.Context, serverName, toolName string, args map[string]any, revision string) (string, error) {
	s, err := u.repo.GetByName(ctx, serverName)
	if err != nil {
		return "", err
	}
	if !s.Enabled {
		return "", fmt.Errorf("%w: MCP server is disabled", errs.ErrForbidden)
	}
	if revision != "" {
		current, err := u.serverRevision(ctx, s)
		if err != nil {
			return "", err
		}
		if current != revision {
			return "", fmt.Errorf("%w: MCP registration changed; request a new approval", errs.ErrConflict)
		}
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

// Revision returns an opaque identity without exposing resolved credentials.
func (u *Usecase) Revision(ctx context.Context, name string) (string, error) {
	s, err := u.repo.GetByName(ctx, name)
	if err != nil {
		return "", err
	}
	if !s.Enabled {
		return "", fmt.Errorf("%w: MCP server is disabled", errs.ErrForbidden)
	}
	return u.serverRevision(ctx, s)
}

func (u *Usecase) serverRevision(ctx context.Context, s *model.Server) (string, error) {
	var fields map[string]string
	if s.Credential != "" {
		if u.secrets == nil {
			return "", fmt.Errorf("MCP credential resolver unavailable")
		}
		var err error
		fields, err = u.secrets.ResolveFields(ctx, s.Credential)
		if err != nil {
			return "", fmt.Errorf("resolve MCP revision credential: %w", err)
		}
	}
	// Exclude health/status timestamps: a healthy refresh must not invalidate
	// an approval whose configuration and schema remain identical.
	data, err := json.Marshal(struct {
		ID                                                    uint64
		Name, Transport, Endpoint, Credential, Headers, Tools string
		Enabled, Trusted                                      bool
		Fields                                                map[string]string
	}{s.ID, s.Name, s.Transport, s.Endpoint, s.Credential, s.HeaderTemplateJSON, s.ToolsCacheJSON, s.Enabled, s.Trusted, fields})
	if err != nil {
		return "", fmt.Errorf("encode MCP revision: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
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
	return u.refreshConnection(ctx, id, true)
}

// RefreshConnection is the background path; unchanged schemas keep their live
// tool handles so polling does not interrupt an otherwise valid running turn.
func (u *Usecase) RefreshConnection(ctx context.Context, id uint64) ([]mcpclient.Tool, error) {
	return u.refreshConnection(ctx, id, false)
}
func (u *Usecase) refreshConnection(ctx context.Context, id uint64, forcePublish bool) ([]mcpclient.Tool, error) {
	if err := u.enterCommit(ctx); err != nil {
		return nil, err
	}
	// 后台刷新不能废弃用户正在执行的连接验证。手动验证负责发布结果，
	// 后台稍后仍会轮询；重复手动请求继续沿用最新请求优先的语义。
	if !forcePublish && u.manualProbes[id] > 0 {
		u.leaveCommit()
		return nil, nil
	}
	if forcePublish {
		u.manualProbes[id]++
		defer func() {
			// 这是内存锁的资源清理，不执行 IO；请求取消后也必须归还计数。
			u.commitGate <- struct{}{}
			defer u.leaveCommit()
			u.manualProbes[id]--
			if u.manualProbes[id] == 0 {
				delete(u.manualProbes, id)
			}
		}()
	}
	s, err := u.repo.Get(ctx, id)
	if err != nil {
		u.leaveCommit()
		return nil, err
	}
	startedRevision, err := u.serverRevision(ctx, s)
	if err != nil {
		u.leaveCommit()
		return nil, err
	}
	u.probeSequence++
	ticket := u.probeSequence
	u.latestProbe[id] = ticket
	u.leaveCommit()

	// Network discovery is intentionally outside the publication gate. An admin
	// can disable/delete a server immediately even when its endpoint is hung.
	tools, probeErr := u.discover(ctx, s)
	if err := u.enterCommit(ctx); err != nil {
		return nil, err
	}
	defer u.leaveCommit()
	if u.latestProbe[id] != ticket {
		return nil, fmt.Errorf("%w: MCP discovery superseded by a configuration change or newer refresh", errs.ErrConflict)
	}
	delete(u.latestProbe, id)
	currentRevision, err := u.serverRevision(ctx, s)
	if err != nil {
		return nil, err
	}
	if currentRevision != startedRevision {
		return nil, fmt.Errorf("%w: MCP credentials changed during discovery", errs.ErrConflict)
	}
	if probeErr != nil {
		if err := u.repo.SetStatus(ctx, id, "error", probeErr.Error()); err != nil {
			return nil, fmt.Errorf("record MCP discovery failure: %w", err)
		}
		// Temporary unavailability does not revoke the last verified catalog.
		return nil, probeErr
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return nil, fmt.Errorf("marshal MCP tools: %w", err)
	}
	if err := u.repo.SetToolsCache(ctx, id, string(b)); err != nil {
		return nil, fmt.Errorf("cache MCP tools: %w", err)
	}
	if err := u.repo.SetStatus(ctx, id, "ok", ""); err != nil {
		return nil, fmt.Errorf("record MCP discovery: %w", err)
	}
	if s.Enabled && (forcePublish || s.ToolsCacheJSON != string(b)) {
		u.notifyToolsChanged(ctx, s.Name, s, tools)
	}
	return tools, nil
}

func (u *Usecase) discover(ctx context.Context, s *model.Server) ([]mcpclient.Tool, error) {
	cli, err := u.BuildClient(ctx, s)
	if err != nil {
		return nil, err
	}
	if err := cli.Initialize(ctx); err != nil {
		return nil, err
	}
	return cli.ListTools(ctx)
}

func cachedTools(raw string) ([]mcpclient.Tool, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var tools []mcpclient.Tool
	if err := json.Unmarshal([]byte(raw), &tools); err != nil {
		return nil, fmt.Errorf("decode cached MCP tools: %w", err)
	}
	return tools, nil
}

func sameToolEndpoint(a, b *model.Server) bool {
	return a != nil && b != nil &&
		a.Endpoint == b.Endpoint &&
		a.Transport == b.Transport &&
		a.Credential == b.Credential &&
		a.HeaderTemplateJSON == b.HeaderTemplateJSON
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
