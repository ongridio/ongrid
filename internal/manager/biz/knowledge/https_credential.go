// https_credential.go — phase 01-02.
//
// HTTPSCredential is the HTTPS PAT (Personal Access Token) authentication
// counterpart to SSHIdentity. Each row stores one username + token pair
// for the set of hosts it covers. The Sync() path in usecase.go consults
// pickHTTPSCredentialForHost at clone time to build a temporary GIT_ASKPASS
// script that feeds credentials to git — the standard HTTPS authentication
// mechanism for private repositories.
//
// What this file owns:
//   - Credential DTOs (CreateHTTPSCredential / UpdateHTTPSCredential inputs)
//   - CRUD usecase methods (List, Create, Update, Delete)
//   - host pattern matching (exact priority → glob fallback via filepath.Match)
//   - extractHTTPSHost: parse host from an HTTP/HTTPS git URL
//
// Security invariants enforced here:
//   - T-02-01: List/Create/Update responses always clear Token and set HasToken.
//     Plaintext tokens never leave the biz boundary (except pickHTTPSCredentialForHost
//     which calls u.repo.ListHTTPSCredentials directly to access tokens for injection).
//   - T-02-02: host matching goes through extractHTTPSHost (net/url canonical parse)
//     then exact-first / glob-fallback, preventing spoofing via similar domain names.
//   - T-02-03: UpdateHTTPSCredential uses *string token semantics — nil means "do not
//     change the stored token", preventing accidental token erasure on empty PATCH.
package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// CreateHTTPSCredentialInput is the create-form payload for an HTTPS
// credential. Token must be non-empty at creation time (CRED-01). Username
// defaults to "oauth2" if empty (GitLab PAT convention).
type CreateHTTPSCredentialInput struct {
	Name     string
	Hosts    []string
	Username string
	Token    string
}

// UpdateHTTPSCredentialInput edits the credential-mutable fields (name,
// host patterns, username, token). Token="" means "do not change the stored
// token"; Token!="" means "rotate to this new value" (CRED-03).
type UpdateHTTPSCredentialInput struct {
	Name     string
	Hosts    []string
	Username string
	Token    string
}

// ListHTTPSCredentials returns every credential sorted by name. Tokens are
// scrubbed from the returned rows; HasToken reflects whether a token is set.
func (u *Usecase) ListHTTPSCredentials(ctx context.Context) ([]*model.HTTPSCredential, error) {
	rows, err := u.repo.ListHTTPSCredentials(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		r.HasToken = r.Token != ""
		r.Token = ""
	}
	return rows, nil
}

// CreateHTTPSCredential validates + persists a new credential. Returns the
// row with the token scrubbed and HasToken set.
func (u *Usecase) CreateHTTPSCredential(ctx context.Context, in CreateHTTPSCredentialInput) (*model.HTTPSCredential, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: name required", errs.ErrInvalid)
	}
	if len(name) > 128 {
		return nil, fmt.Errorf("%w: name too long (max 128)", errs.ErrInvalid)
	}

	hosts := normalizeHosts(in.Hosts)
	if len(hosts) == 0 {
		return nil, fmt.Errorf("%w: hosts required (at least one host pattern)", errs.ErrInvalid)
	}

	token := strings.TrimSpace(in.Token)
	if token == "" {
		return nil, fmt.Errorf("%w: token required", errs.ErrInvalid)
	}

	username := strings.TrimSpace(in.Username)
	if username == "" {
		username = "oauth2"
	}

	hostsJSON, err := json.Marshal(hosts)
	if err != nil {
		return nil, fmt.Errorf("encode hosts: %w", err)
	}

	now := time.Now().UTC()
	row := &model.HTTPSCredential{
		Name:      name,
		HostsJSON: string(hostsJSON),
		Username:  username,
		Token:     token,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := u.repo.CreateHTTPSCredential(ctx, row); err != nil {
		return nil, err
	}
	row.HasToken = true
	row.Token = ""
	return row, nil
}

// UpdateHTTPSCredential edits name / hosts / username / token. If Token is
// empty the stored token is left unchanged; if non-empty the stored token is
// replaced (rotation semantics). Returns the updated row with token scrubbed.
func (u *Usecase) UpdateHTTPSCredential(ctx context.Context, id uint64, in UpdateHTTPSCredentialInput) (*model.HTTPSCredential, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: name required", errs.ErrInvalid)
	}

	hosts := normalizeHosts(in.Hosts)
	if len(hosts) == 0 {
		return nil, fmt.Errorf("%w: hosts required (at least one host pattern)", errs.ErrInvalid)
	}

	hostsJSON, err := json.Marshal(hosts)
	if err != nil {
		return nil, fmt.Errorf("encode hosts: %w", err)
	}

	username := strings.TrimSpace(in.Username)
	if username == "" {
		username = "oauth2"
	}

	// token="" → nil (do not touch stored token); token!="" → &token (rotate).
	token := strings.TrimSpace(in.Token)
	var tokPtr *string
	if token != "" {
		tokPtr = &token
	}

	if err := u.repo.UpdateHTTPSCredential(ctx, id, name, string(hostsJSON), username, tokPtr); err != nil {
		return nil, err
	}

	row, err := u.repo.GetHTTPSCredential(ctx, id)
	if err != nil {
		return nil, err
	}
	row.HasToken = row.Token != ""
	row.Token = ""
	return row, nil
}

// DeleteHTTPSCredential removes the credential by id.
func (u *Usecase) DeleteHTTPSCredential(ctx context.Context, id uint64) error {
	return u.repo.DeleteHTTPSCredential(ctx, id)
}

// pickHTTPSCredentialForHost picks the HTTPS credential to use for the given
// host. Matching order:
//  1. Exact host present in a credential's Hosts list
//  2. Glob match (filepath.Match — same semantics as SSH Identity matching)
//  3. nil → no credential; anonymous HTTPS (public repos still work)
//
// The match is order-stable: credentials are sorted by name in
// ListHTTPSCredentials, so two credentials both glob-matching "git.acme.*"
// always resolve to the same one for the same host.
//
// IMPORTANT: This method calls u.repo.ListHTTPSCredentials directly (not the
// public u.ListHTTPSCredentials) so that the returned rows contain the real
// token value — required for GIT_ASKPASS injection. Tokens never leave this
// function to callers except through the askpass script mechanism.
func (u *Usecase) pickHTTPSCredentialForHost(ctx context.Context, host string) (*model.HTTPSCredential, error) {
	rows, err := u.repo.ListHTTPSCredentials(ctx)
	if err != nil {
		return nil, err
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil, nil
	}
	// Pass 1 — exact match.
	for _, r := range rows {
		for _, pat := range parseHosts(r.HostsJSON) {
			if strings.EqualFold(pat, host) {
				return r, nil
			}
		}
	}
	// Pass 2 — glob match.
	for _, r := range rows {
		for _, pat := range parseHosts(r.HostsJSON) {
			if strings.ContainsAny(pat, "*?[") {
				ok, _ := filepath.Match(pat, host)
				if ok {
					return r, nil
				}
			}
		}
	}
	return nil, nil
}

// extractHTTPSHost parses the host out of an HTTP or HTTPS git URL using
// net/url for canonical parsing. Returns the lowercase hostname; returns ""
// if the URL is not HTTP/HTTPS or cannot be parsed.
func extractHTTPSHost(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	u, err := url.Parse(repoURL)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
