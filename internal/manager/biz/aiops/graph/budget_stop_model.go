package graph

import (
	"context"
	"encoding/json"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type budgetStopModel struct {
	inner einomodel.ToolCallingChatModel
}

func wrapBudgetStopModel(inner einomodel.ToolCallingChatModel) einomodel.ToolCallingChatModel {
	if inner == nil {
		return nil
	}
	return &budgetStopModel{inner: inner}
}

func (m *budgetStopModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	if msg, ok := finalAnswerAfterToolBudget(input); ok {
		return msg, nil
	}
	return m.inner.Generate(ctx, input, opts...)
}

func (m *budgetStopModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if msg, ok := finalAnswerAfterToolBudget(input); ok {
		return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
	}
	return m.inner.Stream(ctx, input, opts...)
}

func (m *budgetStopModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	next, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &budgetStopModel{inner: next}, nil
}

func finalAnswerAfterToolBudget(messages []*schema.Message) (*schema.Message, bool) {
	env, ok := latestTerminalToolBudget(messages)
	if !ok {
		return nil, false
	}
	tool := strings.TrimSpace(env.Tool)
	if tool == "" {
		tool = "the tool"
	}
	content := "本轮 `" + tool + "` 查询已经达到安全上限。我会停止继续调用工具，基于已经拿到的结果回答：当前证据不足以继续细分这条查询路径；如果前面的结果为空或报错，请检查查询标签/语法/数据源配置，或在下一条消息给出更具体的时间窗、service、device_id 后再查。"
	if wantsEnglishResponse(messages) {
		content = "This turn has reached the safety limit for `" + tool + "` queries. I will stop calling tools and answer from the evidence already collected: the current evidence is not enough to narrow this path further. If earlier results were empty or errored, check the query labels, syntax, or data-source configuration; otherwise send a narrower time window, service, or device_id in the next message and I can query again."
	}
	return &schema.Message{Role: schema.Assistant, Content: content}, true
}

type toolBudgetEnvelope struct {
	Status      string `json:"status"`
	Tool        string `json:"tool"`
	FinalAnswer bool   `json:"final_answer_required"`
}

func latestTerminalToolBudget(messages []*schema.Message) (toolBudgetEnvelope, bool) {
	var zero toolBudgetEnvelope
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil || msg.Role != schema.Tool {
			continue
		}
		var env toolBudgetEnvelope
		if err := json.Unmarshal([]byte(msg.Content), &env); err != nil {
			continue
		}
		if env.Status == "call_budget_exceeded" && env.FinalAnswer {
			return env, true
		}
	}
	return zero, false
}

func wantsEnglishResponse(messages []*schema.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Role != schema.System && msg.Role != schema.User {
			continue
		}
		if strings.Contains(strings.ToLower(msg.Content), "respond in english") {
			return true
		}
	}
	return false
}
