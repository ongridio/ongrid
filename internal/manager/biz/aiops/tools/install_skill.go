package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// install_skill — conversational skill install. The user pastes a skill
// source (a git repo URL, a .tar.gz tarball, or a skills.sh-style link) into
// the chat and asks the agent to install it; the agent extracts the URL and
// calls this tool. Same safety model as cloud_bash: it does NOT install
// directly — it queues a proposal into the human approval inbox and the user
// approves it via the inline confirmation card. Only then does the registered
// executor fetch + install the pack. Installing a skill = granting arbitrary
// code execution (a skill can ship a binary that cloud_bash then runs), so a
// human is always in the loop. There is deliberately NO marketplace catalog /
// search here — the source is whatever the user provided.

// ToolNameInstallSkill is the wire name.
const ToolNameInstallSkill = "install_skill"

// SkillInstallProposer is the narrow seam to the approval inbox. Implemented
// in cmd/main.go over biz/approval.Usecase so this package doesn't import it.
type SkillInstallProposer interface {
	// ProposeInstall queues a skill install for human approval and returns
	// the approval id. sourceType is "git" | "tarball" (already resolved);
	// ref is an optional git branch/tag.
	ProposeInstall(ctx context.Context, url, sourceType, ref, sessionID string, userID uint64) (id string, err error)
}

// InstallSkillTool is the install_skill BaseTool.
type InstallSkillTool struct {
	proposer SkillInstallProposer
	log      *slog.Logger
}

// NewInstallSkillTool builds the tool.
func NewInstallSkillTool(p SkillInstallProposer, log *slog.Logger) *InstallSkillTool {
	if log == nil {
		log = slog.Default()
	}
	return &InstallSkillTool{proposer: p, log: log}
}

// InstallSkillSchema is the args JSON Schema.
var InstallSkillSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "The skill source the user gave — a git repo URL (e.g. https://github.com/owner/repo), a .tar.gz / .tgz tarball URL, or a skills.sh-style link. Extract it verbatim from the user's message; do not invent or guess one."
    },
    "ref": {
      "type": "string",
      "description": "Optional git branch / tag / commit for a git source. Omit for the default branch."
    },
    "type": {
      "type": "string",
      "enum": ["git", "tarball"],
      "description": "Optional. Leave empty to auto-detect from the URL (ends in .tar.gz/.tgz/.tar → tarball, otherwise git)."
    }
  },
  "required": ["url"]
}`)

const installSkillWhenToUse = "当用户在对话里给出一个技能的源地址(git 仓库 URL / .tar.gz 链接 / skills.sh 链接)并希望安装它时用。" +
	"从用户消息里把那个 URL 原样提取出来传进来即可,工具会自动判断是 git 还是 tarball(也可手动传 type)。" +
	"注意:安装会下载并运行外部代码(技能可能自带二进制,之后能被 cloud_bash 调用执行),属于高危操作——" +
	"每次调用都不会立即安装,而是在当前对话弹出一张确认卡片,用户当场批准后才真正下载安装。" +
	"所以可以放心发起,但**不要**引导用户去任何页面或菜单(确认就在对话里)。没有技能市场/货架可搜——源完全由用户提供。"

// Info — Class=destructive: installing a skill grants arbitrary code execution
// (a pack can ship a binary cloud_bash later runs), so it always routes through
// human approval.
func (t *InstallSkillTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:         ToolNameInstallSkill,
		Description:  "Install a skill from a source URL the user provided (git repo / tarball / skills.sh link); queued for human approval before it installs.",
		WhenToUse:    installSkillWhenToUse,
		Parameters:   InstallSkillSchema,
		Class:        "destructive",
		Confirmation: basetool.ConfirmationSelfManaged,
	}, nil
}

type installSkillArgs struct {
	URL  string `json:"url"`
	Ref  string `json:"ref"`
	Type string `json:"type"`
}

// inferSourceType picks git vs tarball from the URL when the caller didn't say.
// Tarball only for explicit archive suffixes; everything else (github.com/…,
// *.git, ssh) is a git clone.
func inferSourceType(url string) string {
	u := strings.ToLower(strings.TrimSpace(url))
	for _, ext := range []string{".tar.gz", ".tgz", ".tar"} {
		if strings.HasSuffix(u, ext) {
			return "tarball"
		}
	}
	return "git"
}

// InvokableRun queues an install approval and returns a human-readable status.
// It never installs anything itself (the approval executor does, post-approval).
func (t *InstallSkillTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	if t.proposer == nil {
		return "", fmt.Errorf("install_skill: approval inbox not wired")
	}
	var in installSkillArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("install_skill: bad args: %w", err)
	}
	url := strings.TrimSpace(in.URL)
	if url == "" {
		return "", fmt.Errorf("install_skill: url is required")
	}
	srcType := strings.TrimSpace(in.Type)
	if srcType != "git" && srcType != "tarball" {
		srcType = inferSourceType(url)
	}
	cfg := basetool.ResolveOptions(opts)
	id, err := t.proposer.ProposeInstall(ctx, url, srcType, strings.TrimSpace(in.Ref), basetool.SessionIDFromContext(ctx), cfg.UserID)
	if err != nil {
		return "", fmt.Errorf("install_skill: propose: %w", err)
	}
	out := map[string]any{
		"status":      "pending_approval",
		"approval_id": id,
		"source_type": srcType,
		"url":         url,
		// LLM-facing instruction (not user-visible copy): an interactive
		// confirmation card is already rendered inline. Keep the reply to one
		// short sentence in the conversation's language; don't restate the URL
		// or invent a page to visit.
		"message": "An interactive confirmation card is now shown inline in this conversation. Do NOT tell the user to open any page or menu, do NOT restate the URL or an id/status table, and do NOT name a specific button label. Reply with a single short sentence saying installing this skill needs the user's confirmation in this conversation before it runs.",
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}
