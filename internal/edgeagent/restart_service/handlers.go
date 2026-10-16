//go:build linux

// Package restart_service registers the edge-side handler for
// MethodRestartService — the first mutating skill in ongrid (
// double-sign / mutating class). The manager-side
// BaseTool dispatches here AFTER the reviewer worker (agents/reviewer.md)
// returns "Decision: approve"; this package's job is two-fold:
//
//  1. Defense-in-depth allow-list re-check. The manager-side BaseTool
//     already validates `service` against an allow-list, but the edge
//     does not trust the cloud — if the wire body asks for an
//     out-of-list unit we reject without shelling out.
//  2. Mock systemctl shell-out for PR-7. Same posture as host_files
//     PR-8: real shell-out (`systemctl restart <unit>`) lands in a
//     follow-up; this handler returns Mocked=true so audit rows make
//     the posture explicit.
//
// We deliberately do NOT spawn `systemctl` in PR-7. Reasons:
//
//   - The reviewer-flow end-to-end can be exercised against a unit
//     that doesn't exist on the dev box (mock returns success).
//   - CI / unit tests run on platforms without systemd at all.
//   - Real `systemctl` requires root + dbus + a real failing service
//     to test the failure path — out of scope for the SOP gating PR.
//
// The default unit allow-list matches the manager-side BaseTool. Operators may
// override at edge boot by constructing a SandboxConfig directly; the
// Default is the one we ship.
package restart_service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// restartHandlerTimeout caps a single edge-side restart_service handler
// invocation. Mock implementation finishes in microseconds; the
// timeout is here for parity with host_files (10s) and so the real
// systemctl shell-out has a budget when it lands.
const restartHandlerTimeout = 10 * time.Second

// SandboxConfig is the edge-side allow-list for restart_service.
// AllowedUnits is the set of systemd short names (no `.service` suffix)
// that may be restarted. Mocked controls whether the handler returns
// the PR-7 stub response or attempts a real shell-out. Real shell-out
// is not implemented here — Mocked=false simply errors on every call
// for now, so a misconfigured edge never silently restarts a service.
type SandboxConfig struct {
	// AllowedUnits is the canonical allow-list (lowercase, no suffix).
	AllowedUnits []string

	// Mocked = true → the handler pretends success without shelling
	// out. = false → error "real systemctl not yet implemented" so a
	// misconfigured edge can't accidentally restart in PR-7.
	Mocked bool

	// allowed is the precomputed lookup set. Lazily filled by ensure().
	once    sync.Once
	allowed map[string]struct{}
}

// DefaultSandboxConfig returns the production-default config. The unit
// list matches the manager-side BaseTool:
// nginx / redis / prometheus / loki / tempo / grafana / mysql / ongrid.
// Mocked = true while the real systemctl path is still TODO (PR-7
// stub posture). The allow-list is intentionally narrow — the worst
// blast radius on a single-tenant dev box is restarting one of these
// services, which is recoverable.
func DefaultSandboxConfig() *SandboxConfig {
	return &SandboxConfig{
		AllowedUnits: []string{
			"nginx",
			"redis",
			"prometheus",
			"loki",
			"tempo",
			"grafana",
			"mysql",
			"ongrid",
		},
		Mocked: true,
	}
}

// ensure precomputes the allowlist set on first use.
func (s *SandboxConfig) ensure() {
	s.once.Do(func() {
		s.allowed = make(map[string]struct{}, len(s.AllowedUnits))
		for _, u := range s.AllowedUnits {
			s.allowed[strings.ToLower(strings.TrimSpace(u))] = struct{}{}
		}
	})
}

// Validate returns nil when the sandbox is internally consistent. An
// empty AllowedUnits list errors so a misconfigured operator override
// can't accidentally widen the allow-list to "everything".
func (s *SandboxConfig) Validate() error {
	if s == nil {
		return errors.New("restart_service sandbox: config is nil")
	}
	if len(s.AllowedUnits) == 0 {
		return errors.New("restart_service sandbox: AllowedUnits must contain at least one entry")
	}
	return nil
}

// Allows returns true when unit (case-insensitive, suffix-stripped) is
// in the allow-list. The same canonicalization is applied to incoming
// requests in the handler so "Nginx" / "nginx.service" / " nginx "
// all match.
func (s *SandboxConfig) Allows(unit string) bool {
	if s == nil {
		return false
	}
	s.ensure()
	canonical := canonicalUnit(unit)
	if canonical == "" {
		return false
	}
	_, ok := s.allowed[canonical]
	return ok
}

// canonicalUnit normalizes a unit short-name: trim whitespace, drop a
// trailing ".service" suffix, lowercase. Returns "" when nothing is
// left after stripping.
func canonicalUnit(unit string) string {
	u := strings.ToLower(strings.TrimSpace(unit))
	u = strings.TrimSuffix(u, ".service")
	if u == "" || strings.ContainsAny(u, "/\\ \t") {
		return ""
	}
	return u
}

// Register installs the restart_service handler on client gated by the
// default SandboxConfig. Idempotent at the tunnel layer. Returns an
// error when the sandbox itself is unhealthy (empty allow-list); the
// caller decides whether to treat it as fatal. log may be nil.
func Register(client tunnel.Client, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	sb := DefaultSandboxConfig()
	if err := sb.Validate(); err != nil {
		return err
	}
	log.Info("restart_service: sandbox ready",
		slog.Int("allowed_units", len(sb.AllowedUnits)),
		slog.Bool("mocked", sb.Mocked),
	)
	client.RegisterHandler(tunnel.MethodRestartService, makeRestartHandler(sb, log))
	return nil
}

// makeRestartHandler is the per-call closure. Defense-in-depth: even
// after the manager-side BaseTool + reviewer agreed, the edge re-checks
// the unit name against its OWN allow-list before any side effect.
func makeRestartHandler(sb *SandboxConfig, log *slog.Logger) tunnel.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
		var req tunnel.RestartServiceRequest
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, fmt.Errorf("restart_service: bad req: %w", err)
			}
		}
		canonical := canonicalUnit(req.Service)
		if canonical == "" {
			return nil, fmt.Errorf("restart_service: service name required")
		}
		if !sb.Allows(canonical) {
			return nil, fmt.Errorf("restart_service: service %q not in allow-list (%s)",
				req.Service, strings.Join(sb.AllowedUnits, " "))
		}

		log.Info("restart_service invoked",
			slog.String("service", canonical),
			slog.String("reason", req.Reason),
			slog.Bool("mocked", sb.Mocked),
		)

		cctx, cancel := context.WithTimeout(ctx, restartHandlerTimeout)
		defer cancel()

		startedAt := time.Now().UTC()

		// PR-7 stub: never shell out. When sb.Mocked is true return a
		// successful pretend-restart; when an operator flips Mocked to
		// false (anticipating real systemctl) we error explicitly so
		// a half-implemented config can't fire.
		if !sb.Mocked {
			return nil, fmt.Errorf("restart_service: real systemctl shell-out not implemented; set sandbox Mocked=true")
		}
		if err := cctx.Err(); err != nil {
			return nil, fmt.Errorf("restart_service: %w", err)
		}

		endedAt := time.Now().UTC()
		resp := tunnel.RestartServiceResponse{
			Service:   canonical,
			Restarted: true,
			Mocked:    true,
			StartedAt: startedAt,
			EndedAt:   endedAt,
		}
		return json.Marshal(resp)
	}
}
