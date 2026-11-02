package imbridge

import (
	"errors"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// ApprovalReceipt 仅给出可操作的提示，不回显内部错误或身份信息。
// 所有文案保持在 Telegram callback 回执的 200 字符限制内。
func ApprovalReceipt(err error, locale, provider string) string {
	c := ApprovalCard{Locale: locale}
	switch {
	case err == nil:
		return c.Label("已接收，请等待审批状态更新。", "Received. Wait for the approval status update.")
	case errors.Is(err, ErrIdentityUnbound):
		command := "/bind"
		if provider == "slack" {
			command = "/ongrid-bind"
		}
		return strings.ReplaceAll(c.Label("尚未绑定账号。在 Ongrid 设置 → IM → 我的 IM 审批身份生成绑定码，私聊机器人发送 COMMAND 绑定码，再回到页面确认并重试卡片。", "Not bound. In Ongrid Settings > IM > My IM approval identities, generate a code. DM this bot: COMMAND CODE. Return to confirm the binding, then retry."), "COMMAND", command)
	case errors.Is(err, errs.ErrNotFound):
		return c.Label("卡片已失效，请使用最新审批卡片；没有有效卡片时请重新发起操作。", "This card has expired. Use the latest approval card or request the operation again.")
	case errors.Is(err, errs.ErrConflict):
		return c.Label("审批已处理或状态已变化，请查看最新状态，不要重复执行。", "The approval was handled or changed. Check the latest status; do not repeat execution.")
	case errors.Is(err, errs.ErrForbidden):
		return c.Label("当前身份无审批权限或机器人凭证已变更，请检查账号权限和 IM 绑定。", "Approval permission is missing or bot credentials changed. Check your account permissions and IM binding.")
	case errors.Is(err, errs.ErrInvalid):
		return c.Label("操作详情不完整，不能批准。请拒绝并缩小操作范围后重新发起。", "Full operation details are unavailable. Reject and request a smaller operation.")
	default:
		return c.Label("暂时无法接收审批，请稍后重试并查看卡片状态。", "Temporarily unable to accept approval. Retry shortly and check the card status.")
	}
}
