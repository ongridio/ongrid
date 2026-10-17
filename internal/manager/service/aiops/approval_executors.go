package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	managerbizapproval "github.com/ongridio/ongrid/internal/manager/biz/approval"
	managerbizdevice "github.com/ongridio/ongrid/internal/manager/biz/device"
	managerbizedge "github.com/ongridio/ongrid/internal/manager/biz/edge"
	managerbizmarketplace "github.com/ongridio/ongrid/internal/manager/biz/marketplace"
	managerbizmcp "github.com/ongridio/ongrid/internal/manager/biz/mcp"
	managerbizsecret "github.com/ongridio/ongrid/internal/manager/biz/secret"
	managersvcfb "github.com/ongridio/ongrid/internal/manager/service/frontierbound"
	"github.com/ongridio/ongrid/internal/pkg/runner"
	"github.com/ongridio/ongrid/internal/pkg/workspace"
)

// SkillAssetLeaser validates runtime-owned package snapshots at execution time.
type SkillAssetLeaser interface {
	AcquireSkillAssets(context.Context, []basetool.SkillAssetRef) (func(), error)
}

// ApprovalExecutors supplies the existing execution paths for approved actions.
type ApprovalExecutors struct {
	Secrets     *managerbizsecret.Usecase
	Runner      runner.Runner
	Workspace   *workspace.Manager
	SkillsRoot  string
	SkillAssets SkillAssetLeaser
	Frontier    *managersvcfb.Client
	Edges       *managerbizedge.Usecase
	Devices     *managerbizdevice.Usecase
	Marketplace *managerbizmarketplace.Usecase
	MCP         *managerbizmcp.Usecase
	Log         *slog.Logger
}

// RegisterRuntimeApprovalExecutor restores the recorded origin at execution.
// It never reads the administrator's current default runtime.
func RegisterRuntimeApprovalExecutor(uc *managerbizapproval.Usecase, kind string, version contract.Version, execute managerbizapproval.Executor) {
	if execute == nil {
		uc.RegisterRuntimeExecutor(kind, string(version), nil)
		return
	}
	uc.RegisterRuntimeExecutor(kind, string(version), func(ctx context.Context, payload string) (string, error) {
		return execute(contract.WithVersion(ctx, version), payload)
	})
}

func (e ApprovalExecutors) Register(approvalUC *managerbizapproval.Usecase) {
	register := func(kind string, execute managerbizapproval.Executor) {
		// Preserve the existing unversioned route for historical/non-runtime proposals.
		approvalUC.RegisterExecutor(kind, execute)
		RegisterRuntimeApprovalExecutor(approvalUC, kind, contract.V3, execute)
		RegisterRuntimeApprovalExecutor(approvalUC, kind, contract.V2, execute)
	}
	register("cloud_bash", func(ctx context.Context, payloadJSON string) (string, error) {
		var p cloudBashPayload
		if err := json.Unmarshal([]byte(payloadJSON), &p); err != nil {
			return "", err
		}
		if len(p.SkillAssets) > 0 {
			if e.SkillAssets == nil {
				return "", fmt.Errorf("skill snapshot runtime unavailable; submit a new request")
			}
			release, err := e.SkillAssets.AcquireSkillAssets(ctx, p.SkillAssets)
			if err != nil {
				return "", err
			}
			defer release()
		}
		if len(p.SkillCredentials) > 0 && len(p.SkillAssets) > 0 {
			if e.Marketplace == nil {
				return "", fmt.Errorf("skill credential bindings unavailable")
			}
			skillNames := make([]string, 0, len(p.SkillAssets))
			for _, ref := range p.SkillAssets {
				skillNames = append(skillNames, ref.Name)
			}
			current := e.Marketplace.BoundCredentialNamesForSkills(ctx, skillNames)
			allowed := make(map[string]bool, len(current))
			for _, name := range current {
				allowed[name] = true
			}
			for _, name := range p.SkillCredentials {
				if !allowed[name] {
					return "", fmt.Errorf("skill credential binding was revoked; submit a new request")
				}
			}
		}
		names := append([]string(nil), p.Credentials...)
		if p.Credential != "" { // legacy single-credential approvals
			names = append(names, p.Credential)
		}
		// Resolve each bound credential's TYPE inject rule and merge into one
		// env. Later credentials win on key collisions (rare across types).
		env := map[string]string{}
		for _, name := range names {
			injected, _, err := e.Secrets.ResolveInjection(ctx, name)
			if err != nil {
				// Surface the available credential names so the agent can retry
				// with the right one instead of guessing (it tends to invent
				// e.g. "tencent" when the vault has "tencent-prod").
				return "", fmt.Errorf("resolve credential %q: %w%s", name, err, availableCredentialsHint(ctx, e.Secrets))
			}
			for k, v := range injected {
				env[k] = v
			}
		}
		// Tools live on a host-mounted persistent volume, NOT in the image:
		// they survive container recreation, don't bloat the image, and the
		// agent can install more at runtime (each install command is itself
		// gated by the human approval card). cloudBashToolsDir is the
		// PYTHONUSERBASE, so `pip install` (PIP_USER) drops packages +
		// entrypoint scripts under it; its bin dir + every installed skill's
		// bin dir go on PATH. PIP_BREAK_SYSTEM_PACKAGES sidesteps PEP 668 so a
		// non-root --user install isn't refused.
		env["PATH"] = cloudBashToolsDir + "/bin:" + skillBinPATH(e.SkillsRoot)
		if len(p.SkillAssets) > 0 {
			var bins []string
			for _, ref := range p.SkillAssets {
				if ref.Directory != "" {
					bins = append(bins, filepath.Join(ref.Directory, "bin"))
					if ref.PackageDirectory != "" && ref.PackageDirectory != ref.Directory {
						bins = append(bins, filepath.Join(ref.PackageDirectory, "bin"))
					}
				}
			}
			// Pinned packages precede legacy installed bins. Explicit absolute paths
			// from load_skill also point into the same immutable snapshot.
			if len(bins) > 0 {
				env["PATH"] = strings.Join(bins, ":") + ":" + env["PATH"]
			}
		}
		env["PYTHONUSERBASE"] = cloudBashToolsDir
		env["PIP_USER"] = "1"
		env["PIP_BREAK_SYSTEM_PACKAGES"] = "1"
		env["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
		// Optional pip index mirror — tccli + deps from pypi.org can take many
		// minutes from a China-based host; a mirror cuts it to seconds. Generic
		// (empty default = pypi); the test env sets it to a Tsinghua mirror.
		if idx := strings.TrimSpace(os.Getenv("ONGRID_PIP_INDEX_URL")); idx != "" {
			env["PIP_INDEX_URL"] = idx
		}
		// Resolve the session's persistent workspace as cwd (HLD-019). Empty
		// workdir → runner uses a transient temp dir (legacy behavior).
		workdir, err := e.Workspace.Session(p.SessionID)
		if err != nil {
			return "", err
		}
		// Give the command a writable HOME. The runner passes ONLY this env map
		// to the child (it does NOT inherit the manager's environment), so
		// without this HOME is unset and any tool that writes a dotdir on
		// startup — tccli → ~/.tccli, awscli → ~/.aws, terraform plugin cache —
		// fails with a permission/HOME error (the sandbox is non-root and the
		// passwd home doesn't exist). Point HOME at the session's persistent
		// workspace so that per-tool state also survives across commands.
		if workdir != "" {
			env["HOME"] = workdir
		}
		res, err := e.Runner.Run(ctx, runner.Spec{Script: p.Command, Env: env, Workdir: workdir, Timeout: 10 * time.Minute})
		if err != nil {
			return "", err
		}
		out, _ := json.Marshal(map[string]any{
			"stdout": res.Stdout, "stderr": res.Stderr,
			"exit_code": res.ExitCode, "truncated": res.Truncated,
		})
		return string(out), nil
	})
	register("host_bash", func(ctx context.Context, payloadJSON string) (string, error) {
		var p hostBashPayload
		if err := json.Unmarshal([]byte(payloadJSON), &p); err != nil {
			return "", err
		}
		tool := aiopstools.NewBashToolWithProposer(e.Frontier, e.Edges, e.Devices, nil, e.Log)
		return tool.RunApproved(ctx, p.DeviceIDs, p.Command, p.TimeoutSeconds)
	})
	// HLD-018 P2: mcp_call executor — on approve, connect the server and run
	// the tool. Trusted servers skip this and run synchronously in the tool.
	register("mcp_call", func(ctx context.Context, payloadJSON string) (string, error) {
		var p mcpCallPayload
		if err := json.Unmarshal([]byte(payloadJSON), &p); err != nil {
			return "", err
		}
		if (contract.VersionFromContext(ctx) == contract.V3 || contract.VersionFromContext(ctx) == contract.V2) && p.Revision == "" {
			return "", fmt.Errorf("MCP approval has no registration revision; submit a new request")
		}
		out, err := e.MCP.CallToolRevision(ctx, p.Server, p.Tool, p.Arguments, p.Revision)
		if err != nil {
			return "", err
		}
		res, _ := json.Marshal(map[string]any{"stdout": out, "exit_code": 0})
		return string(res), nil
	})
	// Conversational skill install (extensions): on approve, fetch + install
	// the pack from the user-provided source, then summarize what landed (incl.
	// credential slots, so the agent can next prompt to bind a credential).
	// Class=destructive — a skill can ship a binary cloud_bash later runs, so
	// this only runs after a human approves. The approval IS the authorization,
	// so the install runs with admin authority; installed_by = proposing user.
	register("install_skill", func(ctx context.Context, payloadJSON string) (string, error) {
		var p installSkillPayload
		if err := json.Unmarshal([]byte(payloadJSON), &p); err != nil {
			return "", err
		}
		src := managerbizmarketplace.Source{
			Type: managerbizmarketplace.SourceType(p.Type),
			URL:  p.URL,
			Ref:  p.Ref,
		}
		res, err := e.Marketplace.Install(ctx, managerbizmarketplace.Caller{UserID: p.UserID, Role: "admin"}, src)
		if err != nil {
			return "", err
		}
		out, _ := json.Marshal(map[string]any{
			"installed":        res.Pack.PackID,
			"version":          res.Pack.Version,
			"credential_slots": res.Capabilities.Summary.CredentialSlots,
			"warnings":         res.Warnings,
		})
		return string(out), nil
	})
}
