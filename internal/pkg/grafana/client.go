// Package grafana is a thin HTTP client for the Grafana 9+/10+/11 admin
// API. We use it to:
//
//   - Verify connectivity from the manager (Health)
//   - Idempotently upsert the Prometheus datasource so dashboards have
//     somewhere to query (UpsertDatasource)
//   - Push ongrid's pre-built dashboards into the user's Grafana
//     (UpsertDashboard) under a known folder
//
// Auth is a Service Account token (Bearer). API-key tokens (legacy) work
// too — same Bearer header.
package grafana

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the API wrapper. Construct via New (Bearer / SA token) or
// NewWithBasicAuth (one-shot bootstrap with admin creds).
type Client struct {
	baseURL  string
	token    string // Bearer; empty if basicAuth in use
	basicUsr string
	basicPwd string
	hc       *http.Client
}

// New builds a Client. baseURL is the Grafana root (e.g.
// "https://grafana.example.com"); trailing slash is trimmed. token is a
// Bearer credential — service account or API key. hc may be nil (15s default).
func New(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   strings.TrimSpace(token),
		hc:      hc,
	}
}

// NewWithBasicAuth is the bootstrap form. Used only at first boot to
// create a real SA + token via the Grafana admin user; after that
// rotate to a New(...) Client with the SA token.
func NewWithBasicAuth(baseURL, user, password string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		basicUsr: strings.TrimSpace(user),
		basicPwd: password,
		hc:       hc,
	}
}

// Health calls /api/health (no auth required by Grafana, but we still send
// the token so the same call exercises the auth path). Returns nil on
// 2xx + database=ok in body.
func (c *Client) Health(ctx context.Context) error {
	body, err := c.do(ctx, http.MethodGet, "/api/health", nil)
	if err != nil {
		return err
	}
	var resp struct {
		Database string `json:"database"`
		Version  string `json:"version"`
	}
	if jerr := json.Unmarshal(body, &resp); jerr != nil {
		return fmt.Errorf("grafana: decode health: %w", jerr)
	}
	if resp.Database != "ok" {
		return fmt.Errorf("grafana: unhealthy: database=%s", resp.Database)
	}
	return nil
}

// Datasource is the minimal shape we send for upsert. UID is required so
// dashboards can refer to the datasource by stable id. JSONData carries
// plugin-specific settings and SecureJSONData carries credentials Grafana
// stores encrypted.
type Datasource struct {
	UID            string            `json:"uid"`
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	URL            string            `json:"url"`
	Access         string            `json:"access"` // "proxy"
	IsDefault      bool              `json:"isDefault,omitempty"`
	BasicAuth      bool              `json:"basicAuth,omitempty"`
	BasicAuthUser  string            `json:"basicAuthUser,omitempty"`
	JSONData       map[string]any    `json:"jsonData,omitempty"`
	SecureJSONData map[string]string `json:"secureJsonData,omitempty"`
}

// ElasticsearchDatasourceConfig is the secret-bearing, in-process contract
// between the logs control plane and Grafana integration. APIKey must always
// be the read-only query credential; it is never persisted in a Grafana
// datasource's ordinary jsonData.
type ElasticsearchDatasourceConfig struct {
	URL          string
	IndexPattern string
	APIKey       string
	CAPEM        string
	TLSInsecure  bool
}

// UpsertDatasource creates or replaces by UID. Grafana doesn't have a
// single "PUT by uid" idempotent endpoint that creates if missing, so we
// do a GET-then-POST/PUT pattern.
//
// Read-only edge case: when the existing datasource was created via
// file-based provisioning (deploy/install/grafana/provisioning/datasources)
// with `editable: false`, Grafana refuses both PUT and DELETE through the
// API. We detect this via the readOnly field on the GET response and
// short-circuit to a no-op — the dashboards we push reference the
// datasource by stable UID, so an unmodifiable but correct row is fine.
func (c *Client) UpsertDatasource(ctx context.Context, ds Datasource) error {
	if ds.UID == "" {
		return errors.New("grafana: datasource uid is required")
	}
	if ds.Access == "" {
		ds.Access = "proxy"
	}
	body, err := c.do(ctx, http.MethodGet, "/api/datasources/uid/"+ds.UID, nil)
	if err == nil && len(body) > 0 {
		var existing struct {
			ID       int64 `json:"id"`
			ReadOnly bool  `json:"readOnly"`
		}
		if jerr := json.Unmarshal(body, &existing); jerr != nil {
			return fmt.Errorf("grafana: decode existing datasource: %w", jerr)
		}
		if existing.ReadOnly {
			// Provisioned + editable:false. Trust the existing row;
			// dashboards reference by UID and that hasn't changed.
			return nil
		}
		_, perr := c.do(ctx, http.MethodPut, fmt.Sprintf("/api/datasources/%d", existing.ID), ds)
		// Forward-compat: even if a future Grafana drops readOnly from the
		// GET response, a 403 with the read-only message is unambiguous.
		if perr != nil && isReadOnlyError(perr) {
			return nil
		}
		return perr
	}
	if !isNotFound(err) {
		return err
	}
	_, cerr := c.do(ctx, http.MethodPost, "/api/datasources", ds)
	return cerr
}

// isReadOnlyError matches the error body Grafana returns when refusing to
// mutate a provisioned (editable:false) datasource. Used as a defensive
// fallback alongside the readOnly field check.
func isReadOnlyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "read-only data source") ||
		strings.Contains(msg, "Cannot update read-only")
}

// EnsureFolder upserts a folder by uid. Idempotent: 412 (precondition
// failed) on POST means it already exists, treat as success.
func (c *Client) EnsureFolder(ctx context.Context, uid, title string) error {
	if uid == "" || title == "" {
		return errors.New("grafana: folder uid and title required")
	}
	body, err := c.do(ctx, http.MethodGet, "/api/folders/"+uid, nil)
	if err == nil && len(body) > 0 {
		return nil // already there
	}
	if !isNotFound(err) {
		return err
	}
	payload := map[string]string{"uid": uid, "title": title}
	_, cerr := c.do(ctx, http.MethodPost, "/api/folders", payload)
	return cerr
}

// ServiceAccount represents a Grafana service account (subset of fields
// we touch). Used for bootstrap: find-or-create then mint a token.
type ServiceAccount struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Login string `json:"login"`
	Role  string `json:"role"`
}

// FindServiceAccountByName returns the SA whose name matches exactly,
// or (nil, nil) if absent. Wraps GET /api/serviceaccounts/search?query=
// which does a substring match — we filter here for exact equality.
func (c *Client) FindServiceAccountByName(ctx context.Context, name string) (*ServiceAccount, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/serviceaccounts/search?query="+name, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ServiceAccounts []ServiceAccount `json:"serviceAccounts"`
	}
	if jerr := json.Unmarshal(body, &resp); jerr != nil {
		return nil, fmt.Errorf("grafana: decode SA search: %w", jerr)
	}
	for _, sa := range resp.ServiceAccounts {
		if sa.Name == name {
			out := sa
			return &out, nil
		}
	}
	return nil, nil
}

// CreateServiceAccount calls POST /api/serviceaccounts. role is one of
// Viewer / Editor / Admin.
func (c *Client) CreateServiceAccount(ctx context.Context, name, role string) (*ServiceAccount, error) {
	body, err := c.do(ctx, http.MethodPost, "/api/serviceaccounts", map[string]string{
		"name": name, "role": role,
	})
	if err != nil {
		return nil, err
	}
	var sa ServiceAccount
	if jerr := json.Unmarshal(body, &sa); jerr != nil {
		return nil, fmt.Errorf("grafana: decode SA create: %w", jerr)
	}
	return &sa, nil
}

// CreateServiceAccountToken mints a token on the given SA and returns the
// plaintext key. Grafana never returns it again, so the caller must
// persist it immediately.
func (c *Client) CreateServiceAccountToken(ctx context.Context, saID int64, name string) (string, error) {
	body, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/serviceaccounts/%d/tokens", saID), map[string]string{
		"name": name,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		Key string `json:"key"`
	}
	if jerr := json.Unmarshal(body, &resp); jerr != nil {
		return "", fmt.Errorf("grafana: decode SA token: %w", jerr)
	}
	if resp.Key == "" {
		return "", errors.New("grafana: empty token in response")
	}
	return resp.Key, nil
}

// FetchDashboard pulls the full dashboard envelope for `uid`, the same
// shape Grafana ships from GET /api/dashboards/uid/{uid}:
//
//	{ "dashboard": { "uid": ..., "title": ..., "panels": [...] }, "meta": {...} }
//
// The response is returned as raw JSON so the caller can pass it through
// to the SPA without losing fields the manager doesn't model. Returns
// notFoundErr (errors.Is(err, ErrDashboardNotFound)) when Grafana 404s
// so the HTTP handler can map it to a clean 404.
func (c *Client) FetchDashboard(ctx context.Context, uid string) ([]byte, error) {
	if strings.TrimSpace(uid) == "" {
		return nil, errors.New("grafana: dashboard uid is required")
	}
	body, err := c.do(ctx, http.MethodGet, "/api/dashboards/uid/"+uid, nil)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrDashboardNotFound
		}
		return nil, err
	}
	return body, nil
}

// ErrDashboardNotFound is returned by FetchDashboard when Grafana
// responds with 404. Callers check via errors.Is so the wrapping
// http handler can translate it to a 404 without leaking message text.
var ErrDashboardNotFound = errors.New("grafana: dashboard not found")

// UpsertDashboard pushes a dashboard JSON into folderUID. dashboard must
// be the raw `{"uid": ..., "title": ..., "panels": ...}` shape (no
// outer wrapper). overwrite=true tells Grafana to replace any existing
// dashboard with the same UID.
func (c *Client) UpsertDashboard(ctx context.Context, dashboard []byte, folderUID string, overwrite bool) error {
	if len(dashboard) == 0 {
		return errors.New("grafana: empty dashboard payload")
	}
	// Make sure dashboard.id is null so Grafana treats it as a new
	// dashboard (otherwise it tries to look up by id and fails on a
	// fresh install). Existing UID + overwrite handles re-push.
	var raw map[string]any
	if err := json.Unmarshal(dashboard, &raw); err != nil {
		return fmt.Errorf("grafana: parse dashboard: %w", err)
	}
	delete(raw, "id")
	wrapper := map[string]any{
		"dashboard": raw,
		"overwrite": overwrite,
		"message":   "synced from ongrid",
	}
	if folderUID != "" {
		wrapper["folderUid"] = folderUID
	}
	_, err := c.do(ctx, http.MethodPost, "/api/dashboards/db", wrapper)
	return err
}

// --- internal -----------------------------------------------------------

// notFoundErr is returned by do when the response is 404. UpsertDatasource
// and EnsureFolder use this to branch into "create" instead of failing.
var notFoundErr = errors.New("grafana: not found")

func isNotFound(err error) bool { return errors.Is(err, notFoundErr) }

func (c *Client) do(ctx context.Context, method, path string, payload any) ([]byte, error) {
	if c.baseURL == "" {
		return nil, errors.New("grafana: baseURL is empty")
	}
	var body io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("grafana: marshal %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("grafana: build %s %s: %w", method, path, err)
	}
	switch {
	case c.token != "":
		req.Header.Set("Authorization", "Bearer "+c.token)
	case c.basicUsr != "":
		req.SetBasicAuth(c.basicUsr, c.basicPwd)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("grafana: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if resp.StatusCode == http.StatusNotFound {
		return nil, notFoundErr
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("grafana: %s %s returned %d: %s",
			method, path, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}
