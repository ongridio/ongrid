package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	openai "github.com/sashabaranov/go-openai"

	aiops "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// ContextWindow belongs to one graph invocation. Only summaries of past user
// turns are durable. A worker and its coordinator always have separate state.
type ContextWindow struct {
	retryBudget         int
	Provider, Model     string
	Capability          llm.WindowCapability
	Selected            int
	Summary             string
	Save                func(context.Context, string, string) error
	Observe             func(context.Context, aiops.ContextUsage)
	Checker             llm.BudgetChecker
	BudgetUser          uint64
	historyCut, stepCut int
	stepSummary         string
	Reported            int
	lastAttempt         string
	LastBefore          int
	LastAfter           int
}

type windowModel struct {
	inner, raw model.ToolCallingChatModel
	window     *ContextWindow
	tools      []*schema.ToolInfo
}

func (m *windowModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &windowModel{inner: inner, raw: m.raw, tools: tools, window: m.window}, nil
}

func (m *windowModel) observe(ctx context.Context, u aiops.ContextUsage) {
	if m.window.Observe != nil {
		m.window.Observe(ctx, u)
	}
}

func (m *windowModel) measured(ctx context.Context, u aiops.ContextUsage, msg *schema.Message) {
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return
	}
	v := msg.ResponseMeta.Usage
	if v.PromptTokens > 0 {
		u.Reported = v.PromptTokens
		m.window.Reported = v.PromptTokens
		m.observe(ctx, u)
	}
	if m.window.Checker != nil {
		_ = m.window.Checker.Record(ctx, m.window.BudgetUser, llm.Usage{PromptTokens: v.PromptTokens, CompletionTokens: v.CompletionTokens, TotalTokens: v.TotalTokens})
	}
}

func (m *windowModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	msg, u, err := windowCall(ctx, m, in, opts, m.inner.Generate)
	if err == nil {
		m.measured(ctx, u, msg)
	}
	return msg, err
}

func (m *windowModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	sr, u, err := windowCall(ctx, m, in, opts, m.inner.Stream)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderWithConvert(sr, func(msg *schema.Message) (*schema.Message, error) {
		m.measured(ctx, u, msg)
		return msg, nil
	}), nil
}

var maximumTokens = regexp.MustCompile(`(?i)maximum context length (?:is|of) ([0-9,]+) tokens`)

// Retry only an explicit pre-generation context rejection, before a stream or
// tool call has been returned. Never replay a turn or an executed action.
func windowCall[T any](ctx context.Context, m *windowModel, in []*schema.Message, opts []model.Option, call func(context.Context, []*schema.Message, ...model.Option) (T, error)) (T, aiops.ContextUsage, error) {
	var zero T
	msgs, options, u, err := m.prepare(ctx, in, opts)
	if err != nil {
		return zero, u, err
	}
	result, err := call(ctx, msgs, options...)
	var upstream *openai.APIError
	if err == nil || !errors.As(err, &upstream) || upstream.HTTPStatusCode != http.StatusBadRequest {
		return result, u, err
	}
	match := maximumTokens.FindStringSubmatch(upstream.Message)
	code, _ := upstream.Code.(string)
	if code != "context_length_exceeded" && len(match) == 0 {
		return result, u, err
	}
	// Keep an inferred retry budget local to this run. Only an explicit model
	// limit can change the displayed capacity.
	if len(match) > 0 {
		limit, e := strconv.Atoi(strings.ReplaceAll(match[1], ",", ""))
		if e == nil && limit >= 1024 && limit < u.Window {
			m.window.Capability.Total = limit
			m.window.Capability.Source = "provider_error"
		}
	}
	m.window.retryBudget = max(1, u.Estimated*3/4)
	next, options, nextUsage, prepareErr := m.prepare(ctx, in, opts)
	if prepareErr != nil {
		return zero, nextUsage, prepareErr
	}
	if nextUsage.Estimated >= u.Estimated {
		return result, u, err
	}
	result, err = call(ctx, next, options...)
	return result, nextUsage, err
}

func savedView(msg *schema.Message) *schema.Message {
	if msg.Role != schema.Tool || len(msg.Content) < 512 {
		return msg
	}
	var v map[string]json.RawMessage
	if json.Unmarshal([]byte(msg.Content), &v) != nil || string(v["kind"]) != "\"tool_result\"" || string(v["storage_status"]) != "\"ready\"" {
		return msg
	}
	var id string
	if json.Unmarshal(v["result_id"], &id) != nil {
		return msg
	}
	if _, err := uuid.Parse(id); err != nil {
		return msg
	}
	// Preserve execution, capture and paging semantics. Only the stored preview is redundant.
	if len(v["preview"]) < 256 {
		return msg
	}
	delete(v, "preview")
	v["view_status"] = json.RawMessage(`"reference"`)
	raw, err := json.Marshal(v)
	if err != nil {
		return msg
	}
	cp := *msg
	cp.Content = string(raw)
	return &cp
}

func control(msg *schema.Message) bool {
	if msg.Role != schema.Tool {
		return false
	}
	var v struct {
		Kind       string
		DraftHash  string                  `json:"draft_hash"`
		Operation  *struct{ State string } `json:"operation"`
		ApprovalID string                  `json:"approval_id"`
		Status     string
	}
	if json.Unmarshal([]byte(msg.Content), &v) != nil {
		return false
	}
	if v.Operation != nil {
		switch v.Operation.State {
		case "pending", "running", "awaiting_approval", "pending_approval", "queued":
			return true
		}
	}
	return v.Kind == "config_draft" || v.Kind == "pending_approval" || v.DraftHash != "" || (v.ApprovalID != "" && (v.Status == "pending" || v.Status == ""))
}

func toolTokens(tools []*schema.ToolInfo) (int, error) {
	n := 0
	for _, t := range tools {
		if t == nil {
			continue
		}
		n += llm.EstimateText(t.Name+t.Desc) + 12
		if t.ParamsOneOf != nil {
			s, err := t.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return 0, err
			}
			b, err := json.Marshal(s)
			if err != nil {
				return 0, err
			}
			n += llm.EstimateText(string(b))
		}
	}
	return n, nil
}

func (m *windowModel) prepare(ctx context.Context, in []*schema.Message, opts []model.Option) ([]*schema.Message, []model.Option, aiops.ContextUsage, error) {
	w := m.window
	opts = slices.Clone(opts)
	if w.Provider != "" {
		opts = append(opts, llm.WithProvider(w.Provider))
	}
	if w.Model != "" {
		opts = append(opts, model.WithModel(w.Model))
	}
	window, budget, out := w.Capability.Budget(w.Selected)
	if w.retryBudget > 0 {
		budget = min(budget, w.retryBudget)
	}
	u := aiops.ContextUsage{Provider: w.Provider, Model: w.Model, Known: w.Capability.Maximum() > 0, Window: window, InputBudget: budget, Reported: w.Reported, Status: "ready", Before: w.LastBefore, After: w.LastAfter}
	common := model.GetCommonOptions(&model.Options{}, opts...)
	if common.MaxTokens != nil && *common.MaxTokens > 0 {
		out = min(out, *common.MaxTokens)
	}
	u.OutputReserve = out
	tools := m.tools
	if common.Tools != nil {
		tools = common.Tools
	}
	if common.ToolChoice != nil && *common.ToolChoice == schema.ToolChoiceForbidden {
		tools = nil
	}
	var err error
	u.Tools, err = toolTokens(tools)
	if err != nil {
		return nil, nil, u, err
	}
	sizes := map[*schema.Message]int{}
	count := func(msg *schema.Message) int {
		if n, ok := sizes[msg]; ok {
			return n
		}
		n, e := llm.EstimateMessageTokens(msg, w.Provider, w.Model)
		if e != nil {
			n = budget + 1
		}
		sizes[msg] = n
		return n
	}
	recount := func(msgs []*schema.Message) {
		u.Estimated = u.Tools
		u.Instructions = 0
		u.ToolResults = 0
		for _, v := range msgs {
			n := count(v)
			u.Estimated += n
			switch v.Role {
			case schema.System:
				u.Instructions += n
			case schema.Tool:
				u.ToolResults += n
			}
		}
	}
	start := 0
	for start < len(in) && in[start].Role == schema.System {
		start++
	}
	current := start
	for i := start; i < len(in); i++ {
		if in[i].Extra["current_context_turn"] == true {
			current = i
			break
		}
	}
	front := current
	if current > start && isSystemReminderMessage(in[current-1].Content) {
		front--
	}
	hist := in[start:front]
	body := in[min(current+1, len(in)):]
	view := func() []*schema.Message {
		v := slices.Clone(in[:start])
		if w.Summary != "" {
			v = append(v, summaryMessage(w.Summary))
		}
		v = append(v, hist[min(w.historyCut, len(hist)):]...)
		v = append(v, in[front:min(current+1, len(in))]...)
		if w.stepSummary != "" {
			v = append(v, summaryMessage(w.stepSummary))
		}
		return append(v, body[min(w.stepCut, len(body)):]...)
	}
	msgs := view()
	recount(msgs)
	shrink := func() {
		for i, msg := range msgs {
			if i < len(msgs)-1 || count(msg) > budget/2 {
				msgs[i] = savedView(msg)
			}
		}
		recount(msgs)
	}
	initial := u.Estimated
	threshold := budget * 8 / 10
	if u.Estimated >= threshold {
		shrink()
	}
	if u.Estimated >= threshold {
		before := u.Estimated
		attempt := fmt.Sprintf("%d/%d/%d/%d", len(hist), len(body), budget, before)
		if attempt != w.lastAttempt {
			w.lastAttempt = attempt

			err = m.compact(ctx, hist, &w.historyCut, &w.Summary, true, &u, out, opts, count)
			msgs = view()
			shrink()
			if err == nil && u.Estimated >= threshold {
				err = m.compact(ctx, body, &w.stepCut, &w.stepSummary, false, &u, out, opts, count)
				msgs = view()
				shrink()
			}
			if err != nil {
				u.Status = "deferred"
			} else if w.LastBefore == before {
				u.Status = "compacted"
			} else {
				u.Status = "ready"
			}
		}
	}
	if u.Estimated < initial && (u.Status == "ready" || u.Status == "compacted") {
		w.LastBefore = initial
		w.LastAfter = u.Estimated
		u.Status = "compacted"
		u.Before = initial
		u.After = u.Estimated
	}
	if ctx.Err() != nil {
		return nil, nil, u, ctx.Err()
	}
	if u.Estimated > budget {
		u.Status = "over_limit"
		m.observe(ctx, u)
		return nil, nil, u, fmt.Errorf("context window exceeded: estimated input %d, available %d (instructions %d, tools %d, tool results %d)", u.Estimated, budget, u.Instructions, u.Tools, u.ToolResults)
	}
	m.observe(ctx, u)
	if w.Checker != nil {
		if e := w.Checker.Check(ctx, w.BudgetUser, u.Estimated); e != nil {
			return nil, nil, u, e
		}
	}
	return msgs, append(opts, model.WithMaxTokens(out)), u, nil
}

func (m *windowModel) compact(ctx context.Context, part []*schema.Message, offset *int, previous *string, historical bool, u *aiops.ContextUsage, out int, opts []model.Option, count func(*schema.Message) int) error {
	w := m.window
	before := u.Estimated
	budget := u.InputBudget
	remaining := before
	cut := *offset
	for cut < len(part) && remaining >= budget*55/100 {
		end := cut + 1
		if historical {
			if part[cut].Role != schema.User {
				break
			}
			for end < len(part) && part[end].Role != schema.User {
				end++
			}
			if end == len(part) && cut > *offset && remaining < budget*8/10 {
				break
			} // keep the latest complete exchange when possible
		} else {
			calls := part[cut].ToolCalls
			if part[cut].Role != schema.Assistant || len(calls) == 0 {
				break
			}
			ids := map[string]bool{}
			for _, c := range calls {
				ids[c.ID] = true
			}
			for end < len(part) && part[end].Role == schema.Tool && ids[part[end].ToolCallID] {
				delete(ids, part[end].ToolCallID)
				end++
			}
			if len(ids) > 0 || end == len(part) || part[end].Role != schema.Assistant {
				break
			}
		}
		pinned := false
		for _, v := range part[cut:end] {
			if v.Role == schema.System || v.Extra["unfinished_context_turn"] == true || control(v) {
				pinned = true
				break
			}
		}
		if pinned {
			break
		}
		for _, v := range part[cut:end] {
			remaining -= count(savedView(v))
		}
		cut = end
	}
	if cut == *offset {
		return nil
	}

	u.Status = "compacting"
	m.observe(ctx, *u)
	next, e := m.summarize(ctx, *previous, part[*offset:cut], budget, out, opts)
	if e != nil {
		// A live tool step has no durable replay cursor. Only fall back for
		// persisted turns, and only when the model could not finish valid notes.
		if !historical || w.Save == nil || ctx.Err() != nil ||
			(!errors.Is(e, llm.ErrIncompleteResponse) && !errors.Is(e, errInvalidSummary)) {
			return e
		}
		var ok bool
		next, ok = archivedExcerpts(*previous, part[*offset:cut], min(8192, budget/3))
		if !ok {
			return e
		}
		reason := "provider returned incomplete"
		if errors.Is(e, errInvalidSummary) {
			reason = e.Error() // validation messages contain no transcript content
		}
		slog.WarnContext(ctx, "using archived excerpts for context compaction", "model", w.Model, "reason", reason)
	}
	next = withResultReferences(next, part[*offset:cut])
	freed := 0
	for _, v := range part[*offset:cut] {
		freed += count(savedView(v))
	}
	if count(summaryMessage(next)) >= freed+llm.EstimateText(*previous) || llm.EstimateText(next) > budget/3 {
		return fmt.Errorf("summary did not release space")
	}
	if historical && w.Save != nil {
		boundary := ""
		if cut < len(part) {
			boundary, _ = part[cut].Extra["context_row"].(string)
		}
		if e = w.Save(ctx, boundary, next); e != nil {
			return e
		}
	}
	*previous = next
	*offset = cut
	w.LastBefore = before
	u.Before = before
	return nil
}
