//go:build linux

// Package bash registers the edge-side handler for MethodBashExec
// (internal/pkg/tunnel/bash.go). The handler is a thin shim over
// cmdpolicy.Sandbox: parse the wire request, call sandbox.Exec,
// translate the ShellResult back to a BashExecResponse, return.
//
// Sandbox wiring at boot:
//
//   - Policy: cmdpolicy.DefaultReadOnly() always. If
//     /etc/ongrid-edge/bash-policy.yaml exists we merge it on top
//     (operator extension/override path).
//   - PathValidator: REUSES host_files.SandboxConfig — there is one
//     allowlist and one validator on the edge, shared by every skill
//     that needs to gate paths. Loading the host_files default
//     ensures the bash skill sees the same /var /opt /home /tmp /srv
//     /data prefixes the find_large_files / du_summary tools see.
//
// Failure mode: if the policy fails to validate (no binaries
// resolvable on this host) we still register the handler — bash.exec
// just returns Allowed=false on every call with Reason="not configured".
// That keeps the agent boot path crash-free even on stripped-down
// images while making the gap obvious in audit logs.
package bash

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/cmdpolicy"
	"github.com/ongridio/ongrid/internal/edgeagent/host_files"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// DefaultPolicyOverridePath is the fixed location an operator may drop
// a YAML to extend / restrict the default policy. Absent file is
// expected and silently ignored; parse errors are logged but boot
// continues with the default-only baseline so a typo can't hose the
// agent.
const DefaultPolicyOverridePath = "/etc/ongrid-edge/bash-policy.yaml"

// Register installs the bash.exec handler on client. log may be nil
// (defaults to slog.Default()). Returns an error only when the
// underlying sandbox cannot be constructed at all (we never get here
// in practice — DefaultReadOnly always succeeds; this is here for
// symmetry with host_files.Register).
func Register(client tunnel.Client, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}

	policy := cmdpolicy.DefaultReadOnly()
	if _, err := os.Stat(DefaultPolicyOverridePath); err == nil {
		merged, err := cmdpolicy.LoadFromYAML(DefaultPolicyOverridePath, policy)
		if err != nil {
			log.Warn("bash: policy override load failed; falling back to baseline",
				slog.String("path", DefaultPolicyOverridePath),
				slog.Any("err", err))
		} else {
			policy = merged
			log.Info("bash: policy override loaded",
				slog.String("path", DefaultPolicyOverridePath))
		}
	}

	pathValidator := host_files.DefaultSandboxConfig()
	if err := pathValidator.Validate(); err != nil {
		// PathValidator unhealthy — log + continue with a permissive
		// nil. Without paths the LLM can still run pure read-system
		// commands (ps / df / ss / etc.) which don't carry a /-prefix
		// arg.
		log.Warn("bash: host_files path validator unhealthy; absolute-path arg validation disabled",
			slog.Any("err", err))
		pathValidator = nil
	}

	sandbox := &cmdpolicy.Sandbox{
		Policy:        policy,
		PathValidator: pathValidator,
		Logger:        log,
	}

	log.Info("bash: sandbox ready",
		slog.String("policy", "read-only"),
		slog.Int("allowed_bins", len(policy.Bins())),
		slog.Int("path_allowlist", len(policy.PathAllowlist)),
		slog.Int("network_host_allowlist", len(policy.NetworkHostAllowlist)),
	)

	client.RegisterHandler(tunnel.MethodBashExec, makeHandler(sandbox, log))
	return nil
}

// makeHandler is split out so tests can wire a sandbox directly without
// going through Register (which depends on the real fs at
// /etc/ongrid-edge/bash-policy.yaml).
func makeHandler(sandbox *cmdpolicy.Sandbox, log *slog.Logger) tunnel.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
		var req tunnel.BashExecRequest
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, fmt.Errorf("bash: bad req: %w", err)
			}
		}
		if req.Cmd == "" {
			return nil, fmt.Errorf("bash: cmd required")
		}

		// Optional per-call timeout override. We bound the override at
		// 5 min just so a runaway agent call can't park a tunnel slot
		// indefinitely; the policy default (300s) is the more useful
		// number.
		callCtx := ctx
		if req.Timeout > 0 {
			d := time.Duration(req.Timeout) * time.Second
			if d > 5*time.Minute {
				d = 5 * time.Minute
			}
			cctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			callCtx = cctx
		}

		log.Debug("bash: exec invoked", slog.String("cmd", req.Cmd), slog.Bool("unrestricted", req.Unrestricted))
		var (
			res *cmdpolicy.ShellResult
			err error
		)
		if req.Unrestricted {
			// Admin write gate is ON: bypass cmdpolicy and run the raw
			// command through a shell. Log at WARN so the edge journal has a
			// clear audit trail of every privileged command (the manager
			// only sets this when the operator turned the gate on).
			log.Warn("bash: UNRESTRICTED exec (cmdpolicy bypassed — write gate on)", slog.String("cmd", req.Cmd))
			res, err = sandbox.ExecRaw(callCtx, req.Cmd)
		} else {
			res, err = sandbox.Exec(callCtx, req.Cmd)
		}
		if err != nil {
			return nil, fmt.Errorf("bash: exec: %w", err)
		}
		return json.Marshal(tunnel.BashExecResponse{
			Allowed:    res.Allowed,
			Reason:     res.Reason,
			Stdout:     res.Stdout,
			Stderr:     res.Stderr,
			ExitCode:   res.ExitCode,
			Truncated:  res.Truncated,
			DurationMs: res.DurationMs,
		})
	}
}
