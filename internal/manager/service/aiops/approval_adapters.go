package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	aiopsagent "github.com/ongridio/ongrid/internal/manager/biz/aiops/agent"
	aiopschatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managerbizapproval "github.com/ongridio/ongrid/internal/manager/biz/approval"
	managerbizmcp "github.com/ongridio/ongrid/internal/manager/biz/mcp"
	managerbizsecret "github.com/ongridio/ongrid/internal/manager/biz/secret"
)

// cloudBashPayload is the approval payload for a queued cloud_bash command.
// Credentials are vault credential NAMES; the executor resolves each one's
// TYPE inject rule into env vars at approve time. Credential (singular) is a
// legacy field kept so an in-flight pre-upgrade approval still resolves.
type cloudBashPayload struct {
	Command     string   `json:"command"`
	Credentials []string `json:"credentials,omitempty"`
	Credential  string   `json:"credential,omitempty"` // legacy single
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
		Kind:       "cloud_bash",
		Title:      title,
		Summary:    strings.Join(credentials, ", "), // plain names; card shows them
		Payload:    cloudBashPayload{Command: command, Credentials: credentials, SessionID: sessionID},
		Source:     "agent",
		SessionID:  sessionID,
		ProposedBy: userID,
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
		Kind:       "host_bash",
		Title:      title,
		Summary:    fmt.Sprintf("device_ids=%v", deviceIDs),
		Payload:    hostBashPayload{DeviceIDs: deviceIDs, Command: command, TimeoutSeconds: timeoutSeconds},
		Source:     "agent",
		SessionID:  sessionID,
		ProposedBy: userID,
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

// K8sActionProposer gives the default legacy kernel the same synchronous
// propose-confirm UX used by host_bash. The tool has already consumed a
// matching dry-run token before this method is called; no Kubernetes write is
// dispatched until the approval executor runs.
type K8sActionProposer struct{ Usecase *managerbizapproval.Usecase }

func (s K8sActionProposer) ProposeAndAwait(ctx context.Context, args aiopstools.ExecuteK8sActionArgs, sessionID, toolCallID string, userID uint64) (string, error) {
	command := formatK8sActionApproval(args)
	title := aiopstools.ToolNameExecuteK8sAction + " " + command
	if len(title) > 120 {
		title = title[:120] + "…"
	}
	summary := command
	if reason := strings.TrimSpace(args.Reason); reason != "" {
		summary += " reason=" + reason
	}
	a, err := s.Usecase.Propose(ctx, managerbizapproval.ProposeInput{
		Kind:       aiopstools.ToolNameExecuteK8sAction,
		Title:      title,
		Summary:    summary,
		Payload:    K8sActionPayload{Args: args, UserID: userID, SessionID: sessionID},
		Source:     "agent",
		SessionID:  sessionID,
		ProposedBy: userID,
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
				Kind:       aiopstools.ToolNameExecuteK8sAction,
				ToolName:   aiopstools.ToolNameExecuteK8sAction,
				Command:    command,
			},
		})
	} else if emit := aiopsagent.EmitFromContext(ctx); emit != nil {
		emit(aiopsagent.Event{
			Type: aiopsagent.EventApprovalPending,
			Approval: &aiopsagent.ApprovalPendingEvent{
				ApprovalID: a.ID,
				ToolCallID: toolCallID,
				Kind:       aiopstools.ToolNameExecuteK8sAction,
				ToolName:   aiopstools.ToolNameExecuteK8sAction,
				Command:    command,
			},
		})
	}
	return CloudBashProposer{Usecase: s.Usecase}.awaitDecision(ctx, a.ID)
}

func formatK8sActionApproval(args aiopstools.ExecuteK8sActionArgs) string {
	target := strings.TrimSpace(args.Name)
	if namespace := strings.TrimSpace(args.Namespace); namespace != "" {
		target = namespace + "/" + target
	}
	parts := []string{fmt.Sprintf("cluster_id=%d", args.ClusterID)}
	for _, value := range []string{args.Action, args.Kind, target} {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, value)
		}
	}
	if args.Replicas != nil {
		parts = append(parts, fmt.Sprintf("replicas=%d", *args.Replicas))
	}
	if rv := strings.TrimSpace(args.ExpectedResourceVersion); rv != "" {
		parts = append(parts, "resource_version="+rv)
	}
	return strings.Join(parts, " ")
}

// awaitDecision blocks until the approval row reaches a terminal state, then
// returns the tool result string the ReAct loop continues with. The approve
// REST handler runs the executor synchronously and records the result, so a
// poll only ever reads it back — there is no double execution. "approved"
// (executor mid-run, result not yet stored) is treated as non-terminal:
// cloud_bash always has an executor, so it transitions to executed/failed
// shortly; the timeout is the backstop.
func (s CloudBashProposer) awaitDecision(ctx context.Context, id string) (string, error) {
	deadline := time.Now().Add(ApprovalWaitTimeout)
	ticker := time.NewTicker(approvalPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Stream closed (user navigated away) — stop waiting. The
			// approval remains pending in the inbox for later decision.
			return `{"status":"cancelled","message":"The approval wait was interrupted; the command was not run."}`, nil
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
				return `{"status":"timeout","message":"No approval within 30 minutes; the command was not run."}`, nil
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
		Kind:       "install_skill",
		Title:      title,
		Summary:    sourceType,
		Payload:    installSkillPayload{URL: url, Type: sourceType, Ref: ref, UserID: userID},
		Source:     "agent",
		SessionID:  sessionID,
		ProposedBy: userID,
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
type MCPProposer struct{ Usecase *managerbizapproval.Usecase }

func (s MCPProposer) ProposeMCPCallAndAwait(ctx context.Context, server, tool string, args map[string]any, sessionID, toolCallID string, userID uint64) (string, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("marshal MCP approval arguments: %w", err)
	}
	cmd := server + " / " + tool + " " + string(argsJSON)
	if len(cmd) > 200 {
		cmd = cmd[:200] + "…"
	}
	a, err := s.Usecase.Propose(ctx, managerbizapproval.ProposeInput{
		Kind:       "mcp_call",
		Title:      cmd,
		Payload:    mcpCallPayload{Server: server, Tool: tool, Arguments: args, Command: cmd},
		Source:     "agent",
		SessionID:  sessionID,
		ProposedBy: userID,
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
	} else if emit := aiopsagent.EmitFromContext(ctx); emit != nil {
		emit(aiopsagent.Event{
			Type: aiopsagent.EventApprovalPending,
			Approval: &aiopsagent.ApprovalPendingEvent{
				ApprovalID: a.ID, ToolCallID: toolCallID, Kind: "mcp_call",
				ToolName: aiopstools.MCPToolName(server, tool), Command: cmd,
			},
		})
	}
	return CloudBashProposer{Usecase: s.Usecase}.awaitDecision(ctx, a.ID)
}
