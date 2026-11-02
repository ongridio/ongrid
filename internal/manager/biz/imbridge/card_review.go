package imbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	approval "github.com/ongridio/ongrid/internal/manager/model/approval"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/k8sredact"
)

// 平台消息会被持久化：除了结构化字段，还必须处理命令参数内的凭证。
var cardCredential = regexp.MustCompile(`(?i)((?:--)?(?:password|passwd|secret|token|api[_-]?key|authorization|cookie|credential|private[_-]?key)\s*(?:[:=]\s*|\s+))(?:"[^"\n]*"|'[^'\n]*'|[^\s,;]+)`)
var cardBearer = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[a-z0-9+/_.=-]+`)
var cardPrivateKey = regexp.MustCompile(`(?s)-----BEGIN[^\n]*PRIVATE KEY-----.*?-----END[^\n]*PRIVATE KEY-----`)

func cardSafeText(s string) string {
	s = cardPrivateKey.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = cardBearer.ReplaceAllString(s, "${1} [REDACTED]")
	s = cardCredential.ReplaceAllString(s, "${1}[REDACTED]")
	return k8sredact.Text(s)
}

// JSON 解码边界允许 any；字符串形式的 args_json 也递归脱敏，不发送执行令牌。
func cardReviewJSON(raw string) (string, bool) {
	if len(raw) > 64*1024 {
		return "", false
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return "", false
	}
	if _, ok := value.(map[string]any); !ok {
		return "", false
	}
	complete := true
	var clean func(any, int) any
	clean = func(v any, depth int) any {
		if depth > 12 {
			complete = false
			return "[REDACTED: nesting limit]"
		}
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if k8sredact.IsSensitiveKey(k) || strings.Contains(strings.ToLower(k), "cookie") {
					x[k] = "[REDACTED]"
				} else {
					x[k] = clean(val, depth+1)
				}
			}
			return x
		case []any:
			for n := range x {
				x[n] = clean(x[n], depth+1)
			}
			return x
		case string:
			var nested any
			if json.Unmarshal([]byte(x), &nested) == nil {
				return clean(nested, depth+1)
			}
			return cardSafeText(x)
		default:
			return v
		}
	}
	data, err := json.MarshalIndent(clean(value, 0), "", "  ")
	if err != nil || !complete || len(data) > 10000 {
		return "", false
	}
	return string(data), true
}

func (i *Interaction) ReviewCard(ctx context.Context, app *model.ImApp, target *model.ApprovalTarget, id string) (ApprovalCard, error) {
	card := ApprovalCard{Locale: target.Locale}
	a, err := i.approvals.Get(ctx, id)
	if err != nil {
		return card, err
	}
	if a.SessionID != target.SessionID || a.ProposedBy != i.owner || target.AppID != app.ID || target.AppIdentifier != app.AppID || (target.ConnectionIdentity != "" && target.ConnectionIdentity != ConnectionIdentity(app)) {
		return card, errs.ErrForbidden
	}
	labels := map[string]string{"pending": card.Label("等待管理员确认", "Awaiting administrator approval"), "approved": card.Label("已批准，等待执行结果", "Approved; awaiting execution result"), "executed": card.Label("执行成功", "Execution succeeded"), "rejected": card.Label("已拒绝，未执行", "Rejected; not executed"), "failed": card.Label("执行失败或请求已失效", "Execution failed or request invalidated")}
	card.Text = labels[a.Status] + "\nID: " + safeNoticeIdentifier(a.ID)
	details, complete := platformCardReview(app.Provider, a.PayloadJSON)
	if complete {
		card.Details = details
	} else {
		card.Details = card.Label("操作详情无法在卡片中完整展示，请拒绝并缩小操作范围后重新发起。", "Full details cannot be displayed. Reject and request a smaller operation.")
	}
	if a.Status == approval.StatusPending && i.Enabled(app) {
		card.Buttons, err = i.Buttons(ctx, app, target, id)
		if !complete {
			card.Buttons.Approve = ""
		}
		card.Text += "\n" + card.Label("核对下方实际操作参数后批准；批准可能修改设备或数据。仅已授权管理员可以操作。", "Review the actual arguments below. Approval may modify devices or data. Authorized administrators only.")
	}
	if a.ResultJSON != nil {
		if result, ok := cardReviewJSON(*a.ResultJSON); ok {
			card.Result = result
			if app.Provider == "telegram" && len(card.Result) > 500 {
				card.Result = card.Label("输出较长，请在当前会话询问完整执行结果。", "Ask in this conversation for the full execution result.")
			}
		} else {
			card.Result = card.Label("详细输出较长或不是结构化数据，请在当前会话继续询问执行结果。", "Detailed output is long or unstructured. Ask for the result in this conversation.")
		}
	}
	if a.Status == approval.StatusExecuted {
		card.Text += "\n" + card.Label("Agent 将在当前会话继续汇总结果。", "The agent will continue with results in this conversation.")
	}
	return card, err
}

// PreauthorizeBinding 由已认证管理员明确指定本人的平台身份，无需用户跳转网页。
// 不按昵称或最近发言人推断身份，不覆盖其他绑定，也不会在重启时恢复已撤销授权。
func (i *Interaction) PreauthorizeBinding(ctx context.Context, user, appID uint64, subject Subject) (*BindingView, error) {
	if err := i.approvals.AuthorizeApproval(ctx, user); err != nil {
		return nil, err
	}
	app, err := i.repo.GetApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	if !i.Enabled(app) || !subject.valid(app.Provider) {
		return nil, errs.ErrInvalid
	}
	c, err := i.StartBinding(ctx, user, appID)
	if err != nil {
		return nil, err
	}
	if err = i.ClaimBinding(ctx, app, subject, c.Code); err != nil {
		return nil, err
	}
	if err = i.ConfirmBinding(ctx, user, c.ID); err != nil {
		return nil, err
	}
	binding, err := i.repo.FindIdentityBinding(ctx, appID, subject.key())
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(subject)
	if err != nil {
		return nil, fmt.Errorf("encode platform identity: %w", err)
	}
	i.log.InfoContext(ctx, "IM identity preauthorized by account owner", "user_id", user, "app_id", appID, "binding_id", binding.ID)
	return &BindingView{ID: binding.ID, AppID: appID, Subject: string(raw)}, nil
}

// Telegram 的单条消息限制为 4096 字符，为状态、标题与结果预留空间。
// 不截断待审批参数；无法完整展示时，只允许拒绝并重新提出较小操作。
func platformCardReview(provider, raw string) (string, bool) {
	details, complete := cardReviewJSON(raw)
	if provider == "telegram" && len(details) > 2500 {
		return "", false
	}
	return details, complete
}

// PlainText 保留完整操作参数，不将工具内容解释为 Markdown。
func (c ApprovalCard) PlainText() string {
	text := c.Text
	if c.Details != "" {
		text += "\n\n" + c.Label("操作详情（敏感字段已隐藏）", "Operation details (sensitive fields hidden)") + "\n" + c.Details
	}
	if c.Result != "" {
		text += "\n\n" + c.Label("执行结果", "Execution result") + "\n" + c.Result
	}
	return text
}
