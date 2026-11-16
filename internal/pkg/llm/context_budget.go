package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type ContextBudget struct {
	Window int
	Output int
}
type contextBudgetModel struct {
	inner  model.ToolCallingChatModel
	budget ContextBudget
	tools  []*schema.ToolInfo
}

// WithContextBudget limits every provider request, including each ReAct step.
// UTF-8 byte counts conservatively estimate text tokens without assuming a
// particular provider tokenizer. Configuration must fit the selected model.
func WithContextBudget(inner model.ToolCallingChatModel, budget ContextBudget) model.ToolCallingChatModel {
	if budget.Window == 0 {
		budget.Window = 131072
	}
	if budget.Output == 0 {
		budget.Output = 8192
	}
	return &contextBudgetModel{inner: inner, budget: budget}
}
func (m *contextBudgetModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	cp := *m
	cp.inner = inner
	cp.tools = append([]*schema.ToolInfo(nil), tools...)
	return &cp, nil
}
func (m *contextBudgetModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	msgs, options, err := m.prepare(input, opts)
	if err != nil {
		return nil, err
	}
	return m.inner.Generate(ctx, msgs, options...)
}
func (m *contextBudgetModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msgs, options, err := m.prepare(input, opts)
	if err != nil {
		return nil, err
	}
	return m.inner.Stream(ctx, msgs, options...)
}
func (m *contextBudgetModel) prepare(input []*schema.Message, opts []model.Option) ([]*schema.Message, []model.Option, error) {
	common := model.GetCommonOptions(&model.Options{}, opts...)
	output := m.budget.Output
	if common.MaxTokens != nil && *common.MaxTokens > 0 {
		output = min(output, *common.MaxTokens)
	}
	tools := m.tools
	if common.Tools != nil {
		tools = common.Tools
	}
	if common.ToolChoice != nil && *common.ToolChoice == schema.ToolChoiceForbidden {
		tools = nil
	}
	definitions := 0
	for _, t := range tools {
		if t == nil {
			continue
		}
		definitions += len(t.Name) + len(t.Desc) + 64
		if t.ParamsOneOf != nil {
			js, err := t.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return nil, nil, err
			}
			b, err := json.Marshal(js)
			if err != nil {
				return nil, nil, err
			}
			definitions += len(b)
		}
	}
	// Protocol framing and provider-specific reasoning/tool wrappers need room.
	limit := m.budget.Window - output - 4096 - definitions
	if limit <= 0 || output <= 0 {
		return nil, nil, fmt.Errorf("LLM context budget: tool definitions and output reserve exceed context window")
	}
	available := limit
	limit -= 512 // reserve the explicit omission notice before trimming
	msgs := append([]*schema.Message(nil), input...)
	size := func() int {
		n := 0
		for _, v := range msgs {
			if v != nil {
				b, err := json.Marshal(v)
				if err != nil {
					return limit + 1
				}
				n += len(b) + 32
			}
		}
		return n
	}
	changed := false
	for i, v := range msgs {
		if size() <= limit {
			break
		}
		if v == nil || v.Role != schema.Tool || len(v.Content) < 512 || controlResult(v.Content) || i >= len(msgs)-2 {
			continue
		}
		cp := *v
		cp.Content = compactToolContent(v.Content)
		msgs[i] = &cp
		changed = true
	}
	// Drop complete old user turns. Keep all system instructions and the
	// latest user turn; never leave tool results without their call message.
	for size() > limit {
		first, next := -1, -1
		for i, v := range msgs {
			if v != nil && v.Role == schema.User {
				if first < 0 {
					first = i
				} else {
					pinned := false
					for _, previous := range msgs[first:i] {
						pinned = pinned || (previous != nil && previous.Role == schema.Tool && controlResult(previous.Content))
					}
					if !pinned {
						next = i
						break
					}
					first = i
				}
			}
		}
		if first < 0 || next < 0 {
			break
		}
		kept := append([]*schema.Message(nil), msgs[:first]...)
		for _, v := range msgs[first:next] {
			if v != nil && v.Role == schema.System {
				kept = append(kept, v)
			}
		}
		msgs = append(kept, msgs[next:]...)
		changed = true
	}
	// Long current turns may contain many reads. Remove only complete old
	// assistant/tool batches, retaining the latest batch and control results.
	for size() > limit {
		removed := false
		for i, v := range msgs {
			if v == nil || v.Role != schema.Assistant || len(v.ToolCalls) == 0 {
				continue
			}
			end := i + 1
			for end < len(msgs) && msgs[end] != nil && msgs[end].Role == schema.Tool {
				end++
			}
			if end >= len(msgs)-2 || end-i-1 != len(v.ToolCalls) {
				continue
			}
			pinned := false
			seen := map[string]bool{}
			for _, r := range msgs[i+1 : end] {
				pinned = pinned || controlResult(r.Content)
				seen[r.ToolCallID] = true
			}
			for _, c := range v.ToolCalls {
				if !seen[c.ID] {
					pinned = true
				}
			}
			if pinned {
				continue
			}
			msgs = append(append([]*schema.Message(nil), msgs[:i]...), msgs[end:]...)
			changed = true
			removed = true
			break
		}
		if !removed {
			break
		}
	}
	if changed {
		msgs = append([]*schema.Message{schema.SystemMessage("Earlier evidence was omitted to fit the context budget. Tool previews and query pages are partial; use saved result IDs to retrieve specific evidence. Do not infer completeness or repeat completed actions.")}, msgs...)
	}
	if size() > available {
		return nil, nil, fmt.Errorf("LLM context budget exceeded: required messages need %d estimated tokens, available %d; reduce input or tool definitions", size(), available)
	}
	options := append([]model.Option(nil), opts...)
	options = append(options, model.WithMaxTokens(output))
	return msgs, options, nil
}

func controlResult(content string) bool {
	var v struct {
		Kind      string `json:"kind"`
		DraftHash string `json:"draft_hash"`
	}
	if json.Unmarshal([]byte(content), &v) != nil {
		return false
	}
	return v.Kind == "config_draft" || v.DraftHash != "" || v.Kind == "pending_approval"
}
func compactToolContent(content string) string {
	// These fields are quoted evidence, not authorization. Result readers
	// independently authorize IDs against the authenticated session tree.
	var raw map[string]json.RawMessage
	view := map[string]any{"context_view": "omitted", "notice": "Earlier tool output omitted; original session record is unchanged."}
	if json.Unmarshal([]byte(content), &raw) == nil {
		for _, k := range []string{"result_id", "execution_status", "capture_status", "storage_status", "status", "mode", "scan_start", "scan_end", "scan_complete", "count_exact"} {
			if v := raw[k]; len(v) > 0 && len(v) <= 128 {
				view[k] = v
			}
		}
	} else {
		n := min(len(content), 192)
		for n > 0 && !utf8.ValidString(content[:n]) {
			n--
		}
		view["excerpt"] = content[:n]
	}
	b, _ := json.Marshal(view) // validated RawMessages and scalars only
	return string(b)
}
