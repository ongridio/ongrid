package basetool

import (
	"encoding/json"
	"strings"
)

// ResultOutcome 只检查工具结果的顶层执行信封，不递归解释查询返回的业务状态。
// 工具错误可作为模型可恢复的结果返回，但不能因此记成执行成功。
func ResultOutcome(result string) (status, detail string) {
	var body struct {
		Status     string          `json:"status"`
		Error      json.RawMessage `json:"error"`
		OK         *bool           `json:"ok"`
		IsError    bool            `json:"isError"`
		ExitCode   *int            `json:"exit_code"`
		ExitStatus *int            `json:"exit_status"`
	}
	if json.Unmarshal([]byte(result), &body) != nil {
		return "success", ""
	}
	if len(body.Error) > 0 && string(body.Error) != "null" {
		var message string
		if json.Unmarshal(body.Error, &message) == nil {
			detail = strings.TrimSpace(message)
		} else if len(body.Error) > 0 && body.Error[0] == '{' {
			detail = string(body.Error)
		}
	}
	if len([]rune(detail)) > 512 {
		detail = string([]rune(detail)[:512])
	}
	if body.Status == "timeout" {
		return "timeout", detail
	}
	if detail != "" || body.Status == "error" || body.Status == "failed" || body.Status == "blocked" || body.Status == "call_budget_exceeded" || body.IsError || (body.OK != nil && !*body.OK) || (body.ExitCode != nil && *body.ExitCode != 0) || (body.ExitStatus != nil && *body.ExitStatus != 0) {
		if detail == "" {
			detail = "tool reported execution failure"
		}
		return "error", detail
	}
	return "success", ""
}
