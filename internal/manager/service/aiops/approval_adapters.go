package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	aiopschatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	managerbizapproval "github.com/ongridio/ongrid/internal/manager/biz/approval"
	managerbizmcp "github.com/ongridio/ongrid/internal/manager/biz/mcp"
	managerbizsecret "github.com/ongridio/ongrid/internal/manager/biz/secret"
)

// cloudBashPayload is the approval payload for a queued cloud_bash command.
// Credentials are vault credential NAMES; the executor resolves each one's
// TYPE inject rule into env vars at approve time. Credential (singular) is a
// legacy field kept so an in-flight pre-upgrade approval still resolves.
type cloudBashPayload struct {
	SkillAssets      []basetool.SkillAssetRef `json:"skill_assets,omitempty"`
	SkillCredentials []string                 `json:"skill_credentials,omitempty"`
	Command          string                   `json:"command"`
	Credentials      []string                 `json:"credentials,omitempty"`
	Credential       string                   `json:"credential,omitempty"` // legacy single
	// SessionID is the chat session that proposed the command (HLD-019). The
	// executor maps it to a persistent per-session working directory so files
	// a tool writes in one command survive to the next, instead of running in
	// a throwaway temp dir. Empty on legacy/pre-upgrade approvals.
	SessionID string `json:"session_id,omitempty"`
}

// hostBashPayload is the approval payload for mutating host_bash commands.
// Read-only host_bash calls never create approvals; this payload is only for
// user-requested host mutations such as deleting a known file.
type hostBashPayload struct {
	DeviceIDs      []uint64 `json:"device_ids"`
	Command        string   `json:"command"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

// K8sActionPayload stores the exact preflight-validated action plus the
// identity to which its one-time token was bound. The approval executor uses
// this immutable payload after a human approves the card.
type K8sActionPayload struct {
	Args      aiopstools.ExecuteK8sActionArgs `json:"args"`
	UserID    uint64                          `json:"user_id"`
	SessionID string                          `json:"session_id"`
}

// ApprovalWaitTimeout bounds how long a synchronous-blocking tool (HLD-021,
// cloud_bash) waits for a human decision before giving up and returning a
// terminal timeout blob. The decorator timeout that wraps the tool is set a
// minute longer (cbDeps.Timeout) so THIS budget is the one that fires first
// with a clean message.
const ApprovalWaitTimeout = 30 * time.Minute

// approvalPollInterval is how often the blocking tool re-reads the approval
// row while waiting for the human decision.
const approvalPollInterval = 1500 * time.Millisecond

// cloudBashSystemPATH is the fallback PATH for the cloud_bash sandbox when no
// installed skill ships a bin dir. Mirrors runner.buildEnv's default.
const cloudBashSystemPATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// cloudBashToolsDir is the host-mounted persistent volume where cloud_bash
// tools live (the PYTHONUSERBASE for pip --user installs, and a general
// <dir>/bin on PATH for any binary the agent drops there). Bind-mounted from
// the host in docker-compose, so tools survive container recreation and never
// touch the image. An installed tool's command still routes through the human
// approval card, which is the security boundary (HLD-017/021).
const cloudBashToolsDir = "/var/lib/ongrid/tools"

// skillBinPATH returns a PATH that prepends every installed skill's bin dir
// (skillsRoot/<pack>/bin) to the system default, so a CLI an extension ships
// is callable from cloud_bash. Missing root / no bin dirs → the system PATH
// unchanged. Order across packs is directory-listing order (stable enough; an
// operator with colliding tool names across packs should rename).
func skillBinPATH(skillsRoot string) string {
	if skillsRoot == "" {
		return cloudBashSystemPATH
	}
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		return cloudBashSystemPATH
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		bin := filepath.Join(skillsRoot, e.Name(), "bin")
		if fi, err := os.Stat(bin); err == nil && fi.IsDir() {
			dirs = append(dirs, bin)
		}
	}
	if len(dirs) == 0 {
		return cloudBashSystemPATH
	}
	return strings.Join(dirs, ":") + ":" + cloudBashSystemPATH
}

// availableCredentialsHint returns a " (available: a, b)" suffix listing the
// vault's credential names, for an error message when a cloud_bash credential
// resolve misses. Empty string when listing fails or the vault is empty —
// never blocks the real error. Names are not secret (values are).
func availableCredentialsHint(ctx context.Context, secretUC *managerbizsecret.Usecase) string {
	if secretUC == nil {
		return ""
	}
	views, err := secretUC.List(ctx)
	if err != nil || len(views) == 0 {
		return ""
	}
	names := make([]string, 0, len(views))
	for _, v := range views {
		if v != nil && v.Name != "" {
			names = append(names, v.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return " (available credentials: " + strings.Join(names, ", ") + ")"
}

// aiopstools.CloudBashProposer seam — the cloud_bash tool calls
// ProposeAndAwait to queue a command, surface the inline card, then block on
// the human decision and get back the real result (HLD-021).
type CloudBashProposer struct{ Usecase *managerbizapproval.Usecase }

func (s CloudBashProposer) ProposeAndAwait(ctx context.Context, command string, credentials []string, sessionID, toolCallID string, userID uint64) (string, error) {
	title := command
	if len(title) > 100 {
		title = title[:100] + "…"
	}
	a, err := s.Usecase.Propose(ctx, managerbizapproval.ProposeInput{
		RuntimeVersion: string(aiopschatruntime.VersionFromContext(ctx)),
		Kind:           "cloud_bash",
		Title:          title,
		Summary:        strings.Join(credentials, ", "), // plain names; card shows them
		Payload:        cloudBashPayload{SkillCredentials: basetool.BoundCredentialsFromContext(ctx), SkillAssets: basetool.SkillAssetsFromContext(ctx), Command: command, Credentials: credentials, SessionID: sessionID},
		Source:         "agent",
		SessionID:      sessionID,
		ProposedBy:     userID,
	})
	if err != nil {
		return "", err
	}
	// Surface the inline approve/reject card LIVE on the SSE stream that owns
	// this chat turn. The tool no longer returns a pending_approval result
	// blob (HLD-021: it blocks, then returns the real output), so the card is
	// driven by this frame. ToolCallID lets the SPA render it AS the tool
	// call's existing streaming card (single card). Best-effort: a blocking
	// (non-SSE) caller has no emitter — the card just won't show, but the
	// approval still sits in the inbox.
	if emit := aiopschatruntime.EmitFromContext(ctx); emit != nil {
		emit(aiopschatruntime.Event{
			Type: aiopschatruntime.EventApprovalPending,
			Approval: &aiopschatruntime.ApprovalPending{
				ApprovalID:  a.ID,
				ToolCallID:  toolCallID,
				Kind:        "cloud_bash",
				ToolName:    aiopstools.ToolNameCloudBash,
				Command:     command,
				Credentials: credentials,
			},
		})
	}
	return s.awaitDecision(ctx, a.ID)
}

// HostBashProposer adapts mutating host_bash commands to the same inline
// approval card flow as cloud_bash.
type HostBashProposer struct{ Usecase *managerbizapproval.Usecase }

func (s HostBashProposer) ProposeAndAwait(ctx context.Context, deviceIDs []uint64, command string, timeoutSeconds int, sessionID, toolCallID string, userID uint64) (string, error) {
	title := fmt.Sprintf("host_bash device_ids=%v %s", deviceIDs, command)
	if len(title) > 100 {
		title = title[:100] + "…"
	}
	a, err := s.Usecase.Propose(ctx, managerbizapproval.ProposeInput{
		RuntimeVersion: string(aiopschatruntime.VersionFromContext(ctx)),
		Kind:           "host_bash",
		Title:          title,
		Summary:        fmt.Sprintf("device_ids=%v", deviceIDs),
		Payload:        hostBashPayload{DeviceIDs: deviceIDs, Command: command, TimeoutSeconds: timeoutSeconds},
		Source:         "agent",
		SessionID:      sessionID,
		ProposedBy:     userID,
	})
	if err != nil {
		return "", err
	}
	if emit := aiopschatruntime.EmitFromContext(ctx); emit != nil {
		emit(aiopschatruntime.Event{
			Type: aiopschatruntime.EventApprovalPending,
			Approval: &aiopschatruntime.ApprovalPending{
				ApprovalID: a.ID,
				ToolCallID: toolCallID,
				Kind:       "host_bash",
				ToolName:   aiopstools.ToolNameBash,
				Command:    fmt.Sprintf("device_ids=%v %s", deviceIDs, command),
			},
		})
	}
	return CloudBashProposer{Usecase: s.Usecase}.awaitDecision(ctx, a.ID)
}

func (s CloudBashProposer) awaitDecision(ctx context.Context, id string) (string, error) {
	deadline := time.Now().Add(ApprovalWaitTimeout)
	ticker := time.NewTicker(approvalPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := invalidateApproval(ctx, s.Usecase, id, "The originating request was cancelled; submit a new request."); err != nil {
				return "", err
			}
			return `{"status":"cancelled","message":"The approval wait was interrupted. Check the approval record for the final execution status."}`, nil
		case <-ticker.C:
			a, err := s.Usecase.Get(ctx, id)
			if err != nil {
				continue // transient read error — keep polling
			}
			switch a.Status {
			case "executed":
				if a.ResultJSON != nil {
					return *a.ResultJSON, nil
				}
				return `{"status":"executed"}`, nil
			case "failed":
				if a.ResultJSON != nil {
					return *a.ResultJSON, nil
				}
				return `{"status":"failed","message":"The approved command failed to run."}`, nil
			case "rejected":
				return `{"status":"rejected","message":"The user rejected this command; it was not run. Do not retry it without new instructions."}`, nil
			}
			if time.Now().After(deadline) {
				if err := invalidateApproval(ctx, s.Usecase, id, "The approval wait expired; submit a new request."); err != nil {
					return "", err
				}
				return `{"status":"timeout","message":"The approval wait expired. Check the approval record for the final execution status."}`, nil
			}
		}
	}
}

// installSkillPayload is the approval payload for a queued conversational
// skill install (install_skill tool). The executor fetches + installs the
// pack from the user-provided source after a human approves.
type installSkillPayload struct {
	URL    string `json:"url"`
	Type   string `json:"type"` // "git" | "tarball"
	Ref    string `json:"ref,omitempty"`
	UserID uint64 `json:"user_id,omitempty"`
}

// InstallSkillProposer queues a skill install into the human approval
// inbox — same propose-confirm model as cloud_bash.
type InstallSkillProposer struct{ Usecase *managerbizapproval.Usecase }

func (s InstallSkillProposer) ProposeInstall(ctx context.Context, url, sourceType, ref, sessionID string, userID uint64) (string, error) {
	title := "install skill: " + url
	if len(title) > 120 {
		title = title[:120] + "…"
	}
	a, err := s.Usecase.Propose(ctx, managerbizapproval.ProposeInput{
		RuntimeVersion: string(aiopschatruntime.VersionFromContext(ctx)),
		Kind:           "install_skill",
		Title:          title,
		Summary:        sourceType,
		Payload:        installSkillPayload{URL: url, Type: sourceType, Ref: ref, UserID: userID},
		Source:         "agent",
		SessionID:      sessionID,
		ProposedBy:     userID,
	})
	if err != nil {
		return "", err
	}
	return a.ID, nil
}

// mcpCallPayload is the approval payload for a queued MCP tool call (HLD-018
// P2). Server/Tool/Arguments drive the executor; Command is a human-readable
// one-liner the inline approval card shows (reuses the cloud_bash card).
type mcpCallPayload struct {
	Revision  string         `json:"revision,omitempty"`
	Server    string         `json:"server"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Command   string         `json:"command"`
}

// MCPCaller is the trusted-server synchronous path for MCP tools.
type MCPCaller struct{ Usecase *managerbizmcp.Usecase }

func (s MCPCaller) CallMCPTool(ctx context.Context, server, tool string, args map[string]any) (string, error) {
	return s.Usecase.CallTool(ctx, server, tool, args)
}

// MCPProposer queues an MCP call into the human approval inbox (default,
// untrusted path) — same propose-confirm model as cloud_bash.
type MCPProposer struct {
	Usecase *managerbizapproval.Usecase
	MCP     *managerbizmcp.Usecase
}

func (s MCPProposer) ProposeMCPCallAndAwait(ctx context.Context, server, tool string, args map[string]any, sessionID, toolCallID string, userID uint64) (string, error) {
	var revision string
	if s.MCP != nil {
		var err error
		revision, err = s.MCP.Revision(ctx, server)
		if err != nil {
			return "", err
		}
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("marshal MCP approval arguments: %w", err)
	}
	cmd := server + " / " + tool + " " + string(argsJSON)
	if len(cmd) > 200 {
		cmd = cmd[:200] + "…"
	}
	a, err := s.Usecase.Propose(ctx, managerbizapproval.ProposeInput{
		RuntimeVersion: string(aiopschatruntime.VersionFromContext(ctx)),
		Kind:           "mcp_call",
		Title:          cmd,
		Payload:        mcpCallPayload{Revision: revision, Server: server, Tool: tool, Arguments: args, Command: cmd},
		Source:         "agent",
		SessionID:      sessionID,
		ProposedBy:     userID,
	})
	if err != nil {
		return "", err
	}
	if emit := aiopschatruntime.EmitFromContext(ctx); emit != nil {
		emit(aiopschatruntime.Event{
			Type: aiopschatruntime.EventApprovalPending,
			Approval: &aiopschatruntime.ApprovalPending{
				ApprovalID: a.ID, ToolCallID: toolCallID, Kind: "mcp_call",
				ToolName: aiopstools.MCPToolName(server, tool), Command: cmd,
			},
		})
	}
	return CloudBashProposer{Usecase: s.Usecase}.awaitDecision(ctx, a.ID)
}
