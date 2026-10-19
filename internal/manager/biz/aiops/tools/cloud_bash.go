package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/compose"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// cloud_bash — the cloud-side (manager) command tool, sibling of host_bash
// (which runs on an edge device via tunnel). cloud_bash runs a command in
// the manager-side Runner sandbox with a bound credential's env injected —
// the path for terraform / cloud-CLI / kubectl that operate on cloud
// resources (HLD-017).
//
// SAFETY (MVP): cloud_bash does NOT execute directly. Every call queues a
// proposal into the human approval inbox (biz/approval); the user approves
// it via the confirmation card rendered inline in the chat conversation, and
// only then does the registered executor run the
// command in the Runner. So the LLM can never run an arbitrary manager-side
// command with cloud credentials without a human in the loop. A read-class
// auto-run allowlist is a future refinement.

// ToolNameCloudBash is the wire name.
const ToolNameCloudBash = "cloud_bash"

// CloudBashProposer is the narrow seam to the approval inbox. Implemented in
// cmd/main.go over biz/approval.Usecase so this package doesn't import it.
type CloudBashProposer interface {
	// ProposeAndAwait queues the command for human approval, surfaces an
	// inline approve/reject card on the live SSE stream, then BLOCKS until
	// the human decides (or a timeout). It returns the executor's REAL
	// result on approve (so the ReAct loop continues naturally with the
	// command output, HLD-021), or a terminal status blob on reject/timeout.
	//
	// credentials are the vault credential names whose fields get injected
	// as env at execute time — the union of the LLM's optional per-call
	// credential and the session's active-skill bound credentials (HLD-017
	// design-time binding). toolCallID ties the live card to this tool
	// call's existing streaming card so the UI renders a single card.
	ProposeAndAwait(ctx context.Context, command string, credentials []string, sessionID, toolCallID string, userID uint64) (result string, err error)
}

// CloudBashTool is the cloud_bash BaseTool.
type CloudBashTool struct {
	proposer CloudBashProposer
	log      *slog.Logger
}

// NewCloudBashTool builds the tool.
func NewCloudBashTool(p CloudBashProposer, log *slog.Logger) *CloudBashTool {
	if log == nil {
		log = slog.Default()
	}
	return &CloudBashTool{proposer: p, log: log}
}

// CloudBashSchema is the args JSON Schema.
var CloudBashSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The shell command to run in the cloud (manager) sandbox, e.g. 'terraform plan'. Runs with the chosen credential's env injected. The cwd is this conversation's PERSISTENT working directory — files you create (e.g. write main.tf, terraform init) survive to the next command, so use relative paths and build up state across calls rather than re-creating everything each time."
    },
    "credential": {
      "type": "string",
      "description": "Optional name of a stored credential (设置→凭证) whose fields are injected as env vars (e.g. 'tencent-prod' → TENCENTCLOUD_SECRET_ID/KEY). Omit for commands that need no cloud auth."
    }
  },
  "required": ["command"]
}`)

const cloudBashWhenToUse = "在云端(manager)运行命令——terraform / 云厂商 CLI / kubectl 等操作云资源的命令。" +
	"不同于 host_bash(在某台设备上跑)。注意:每次调用都会在当前对话里直接弹出一张确认卡片,用户当场点击批准或拒绝;" +
	"这个工具调用会一直阻塞等待,用户批准后立刻执行并把真实的命令输出返回给你(就像普通工具一样),你再据此继续。" +
	"所以可以放心发起,但**不要**引导用户去任何页面或菜单(确认就在对话里)。需要云凭证时传 credential(凭证库里的名字)。"

// Info — Class=destructive: cloud_bash can run anything with cloud creds, so
// it always carries the highest gate (and routes through human approval).
func (t *CloudBashTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:         ToolNameCloudBash,
		Description:  "Run a command in the cloud (manager) sandbox with an injected credential; queued for human approval before it executes.",
		WhenToUse:    cloudBashWhenToUse,
		Parameters:   CloudBashSchema,
		Class:        "destructive",
		Confirmation: basetool.ConfirmationSelfManaged,
	}, nil
}

type cloudBashArgs struct {
	Command    string `json:"command"`
	Credential string `json:"credential"`
}

// mergeCreds returns the de-duped union of the session's bound credentials
// (from ctx) and an optional per-call credential, order-stable (bound first).
func mergeCreds(bound []string, perCall string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(bound)+1)
	add := func(c string) {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			return
		}
		seen[c] = true
		out = append(out, c)
	}
	for _, c := range bound {
		add(c)
	}
	add(perCall)
	return out
}

// InvokableRun queues an approval, surfaces the inline approve/reject card,
// and BLOCKS until the human decides (HLD-021 synchronous propose-confirm,
// modeled on ztna-agent's ReAct loop). On approve it returns the executor's
// REAL command output so the ReAct loop continues naturally; on reject/
// timeout it returns a terminal status blob. The command is never executed
// here — the approval executor runs it once, post-approval; this tool only
// waits and reads the recorded result.
func (t *CloudBashTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	if t.proposer == nil {
		return "", fmt.Errorf("cloud_bash: approval inbox not wired")
	}
	var in cloudBashArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("cloud_bash: bad args: %w", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("cloud_bash: command is required")
	}
	cfg := basetool.ResolveOptions(opts)
	// Union the LLM's optional per-call credential with the session's
	// active-skill bound credentials (HLD-017 design-time binding, attached
	// to ctx by the runtime). De-duped, order-stable.
	creds := mergeCreds(basetool.BoundCredentialsFromContext(ctx), strings.TrimSpace(in.Credential))
	// eino's authoritative per-call id (compose.GetToolCallID) ties the live
	// approval card to THIS tool call's existing streaming card so the UI
	// renders a single card instead of a duplicate. Empty under the legacy
	// kernel — the proposer falls back to a standalone card.
	toolCallID := compose.GetToolCallID(ctx)
	// HLD-019: carry the session id into the approval so the execute-on-approve
	// hook runs the command in this session's persistent workspace.
	// HLD-021: ProposeAndAwait blocks until the human decides and returns the
	// executor's real result, so the ReAct loop continues with the output.
	result, err := t.proposer.ProposeAndAwait(ctx, in.Command, creds, basetool.SessionIDFromContext(ctx), toolCallID, cfg.UserID)
	if err != nil {
		return "", fmt.Errorf("cloud_bash: propose: %w", err)
	}
	return result, nil
}
