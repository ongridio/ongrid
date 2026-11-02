package imbridge

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatprotocol"
)

// ApprovalStatusReader returns only the status of an approval belonging to
// this session. Approval payloads and credentials never enter IM notices.
type ApprovalStatusReader interface {
	ApprovalStatus(ctx context.Context, sessionID, approvalID string) (string, error)
}

type approvalNotice struct{ id, messageID, status, tool, kind string }

func (e *streamEditor) notifyApproval(p *chatprotocol.ApprovalPendingEvent) error {
	if e.durableApprovals || p == nil || p.ApprovalID == "" {
		return nil
	}
	e.flushMu.Lock()
	defer e.flushMu.Unlock()
	for _, notice := range e.approvals {
		if notice.id == p.ApprovalID {
			return nil
		}
	}
	n := approvalNotice{id: p.ApprovalID, status: "pending", tool: p.ToolName, kind: p.Kind}
	id, err := e.sender.SendText(e.ctx, e.chatID, e.receiveIDType, e.approvalText(n))
	if err != nil {
		return fmt.Errorf("send approval notice: %w", err)
	}
	n.messageID = id
	e.approvals = append(e.approvals, n)
	return nil
}

func (e *streamEditor) approvalText(n approvalNotice) string {
	english := strings.HasPrefix(strings.ToLower(e.locale), "en")
	label := map[string]string{"pending": "等待管理员确认", "approved": "已批准，等待执行结果", "executed": "已执行", "rejected": "已拒绝", "failed": "执行失败或请求已失效"}[n.status]
	if english {
		label = map[string]string{"pending": "Awaiting administrator approval", "approved": "Approved; awaiting execution result", "executed": "Executed", "rejected": "Rejected", "failed": "Execution failed or request invalidated"}[n.status]
	}
	if label == "" {
		label = n.status
	}
	// Only identifiers are included. Command arguments can contain secrets;
	// the authenticated approval page remains the authoritative review surface.
	text := "**" + label + "**\n\nID: " + safeNoticeIdentifier(n.id)
	category := "受保护操作"
	if english {
		category = "Protected operation"
	}
	if n.kind == "agent_tool" {
		category = "Agent 工具操作"
		if english {
			category = "Agent tool operation"
		}
	}
	if n.kind == "mcp_call" {
		category = "MCP 工具操作"
		if english {
			category = "MCP tool operation"
		}
	}
	text += "\n\n" + category
	if n.tool != "" {
		text += "\n\nTool: " + safeNoticeIdentifier(n.tool)
	}
	if english {
		text += "\n\nReview the operation in Ongrid’s Approvals page. Approval requires an administrator account."
	} else {
		text += "\n\n请在 Ongrid「待确认」页面查看操作详情，由管理员批准或拒绝。"
	}
	if link := approvalLink(e.publicURL, n.id); link != "" {
		title := "查看详情并确认"
		if english {
			title = "Review approval"
		}
		text += "\n\n[" + title + "](" + link + ")"
	}
	return text
}

// Only a canonical HTTPS origin is accepted. Never derive links from inbound
// headers, platform messages, or executable tokens.
func ApprovalPublicURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(u.Host, "\\()<> ") {
		return "", fmt.Errorf("IM approval public URL must be an HTTPS origin without credentials, path, query or fragment")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func approvalLink(base, id string) string {
	// UUIDs and test identifiers are safe path segments, not Markdown or URLs.
	if base == "" || id == "" || safeNoticeIdentifier(id) != id {
		return ""
	}
	return base + "/approvals/" + url.PathEscape(id)
}

func safeNoticeIdentifier(value string) string {
	if len(value) > 128 {
		return "[redacted]"
	}
	if value == "" {
		return "[redacted]"
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return "[redacted]"
		}
	}
	return value
}

func (e *streamEditor) refreshApprovals() error {
	if e.approvalReader == nil {
		return nil
	}
	e.flushMu.Lock()
	defer e.flushMu.Unlock()
	for i := range e.approvals {
		n := &e.approvals[i]
		status, err := e.approvalReader.ApprovalStatus(e.ctx, e.sessionID, n.id)
		if err != nil {
			return fmt.Errorf("read approval status: %w", err)
		}
		if status == n.status {
			continue
		}
		next := *n
		next.status = status
		if e.editor != nil && n.messageID != "" {
			err = e.editor.EditText(e.ctx, n.messageID, e.approvalText(next))
		} else {
			_, err = e.sender.SendText(e.ctx, e.chatID, e.receiveIDType, e.approvalText(next))
		}
		if err != nil {
			return fmt.Errorf("update approval notice: %w", err)
		}
		n.status = status
	}
	return nil
}
