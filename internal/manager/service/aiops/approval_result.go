package aiops

import (
	"encoding/json"
	"fmt"

	approval "github.com/ongridio/ongrid/internal/manager/model/approval"
)

// approvalToolResult 为模型补充持久化审批事实，不改变执行器结果或审批状态机。
// 使用 RawMessage 保留大整数及原有顶层错误字段，避免包装后误报工具成功。
func approvalToolResult(a *approval.Approval) (string, error) {
	out := make(map[string]json.RawMessage)
	if a.ResultJSON != nil {
		raw := json.RawMessage(*a.ResultJSON)
		if err := json.Unmarshal(raw, &out); err != nil || out == nil {
			out = make(map[string]json.RawMessage)
			if json.Valid(raw) {
				out["result"] = raw
			} else {
				text, err := json.Marshal(*a.ResultJSON)
				if err != nil {
					return "", fmt.Errorf("encode approval result text: %w", err)
				}
				out["result"] = text
			}
		}
	}
	if _, ok := out["status"]; !ok && (a.Status == approval.StatusFailed || a.ResultJSON == nil) {
		status, err := json.Marshal(a.Status)
		if err != nil {
			return "", fmt.Errorf("encode approval status: %w", err)
		}
		out["status"] = status
	}
	decision := "unknown"
	message := "Approval decision evidence is unavailable; do not infer approval or a bypass from the tool output."
	if a.ApprovedBy != nil && a.DecidedAt != nil && (a.Status == approval.StatusExecuted || a.Status == approval.StatusFailed) {
		decision = "approved"
		message = "This tool call waited for human approval. A human approved it before execution was attempted. The returned output is the post-approval result, not evidence of bypassing approval. Approval does not imply execution succeeded; inspect the tool result."
	}
	metadata, err := json.Marshal(struct {
		ID       string `json:"id"`
		Decision string `json:"decision"`
		Status   string `json:"status"`
		Message  string `json:"message"`
	}{a.ID, decision, a.Status, message})
	if err != nil {
		return "", fmt.Errorf("encode approval evidence: %w", err)
	}
	// 此保留字段只能由审批记录生成，不能信任工具返回的同名声明。
	out["_ongrid_approval"] = metadata
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode approved tool result: %w", err)
	}
	return string(encoded), nil
}
