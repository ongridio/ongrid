package agenttrace

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

func adapt(s *Step) {
	a := s.Attributes
	s.SDK, s.Kind = "otel", "span"
	kind := ""
	switch {
	case stringAttr(a, "openinference.span.kind") != "":
		s.SDK, kind = "openinference", stringAttr(a, "openinference.span.kind")
	case stringAttr(a, "langsmith.span.kind") != "" || s.Scope.Name == "langsmith":
		s.SDK, kind = "langsmith", stringAttr(a, "langsmith.span.kind")
	case stringAttr(a, "langfuse.observation.type") != "" || s.Scope.Name == "langfuse-sdk":
		s.SDK, kind = "langfuse", stringAttr(a, "langfuse.observation.type")
	case stringAttr(a, "traceloop.span.kind") != "" || strings.HasPrefix(s.Scope.Name, "traceloop"):
		s.SDK, kind = "openllmetry", stringAttr(a, "traceloop.span.kind")
	case stringAttr(a, "gen_ai.operation.name", "gen_ai.request.model", "gen_ai.system", "gen_ai.provider.name") != "":
		s.SDK = "gen_ai"
	}
	if kind == "" {
		kind = stringAttr(a, "gen_ai.operation.name", "llm.request.type")
	}
	switch strings.ToLower(kind) {
	case "agent", "invoke_agent", "create_agent":
		s.Kind = "agent"
	case "chain", "workflow", "task":
		s.Kind = "workflow"
	case "llm", "generation", "chat", "completion", "text_completion", "generate_content":
		s.Kind = "llm"
	case "tool", "execute_tool":
		s.Kind = "tool"
	case "retriever", "retrieval":
		s.Kind = "retriever"
	case "embedding", "embeddings":
		s.Kind = "embedding"
	case "reranker", "rerank":
		s.Kind = "reranker"
	}
	s.Model = stringAttr(a, "gen_ai.response.model", "llm.response.model_name", "llm.model_name", "langfuse.observation.model.name", "gen_ai.request.model", "embedding.model_name")
	if s.Kind == "span" && s.Model != "" {
		s.Kind = "llm"
	}
	s.Provider = stringAttr(a, "gen_ai.provider.name", "llm.provider", "gen_ai.system", "llm.system")
	// LangSmith trace.session_id identifies a project, not a conversation.
	s.SessionID = stringAttr(a, "gen_ai.conversation.id", "session.id", "langsmith.metadata.thread_id", "langsmith.metadata.session_id", "langsmith.metadata.conversation_id", "traceloop.association.properties.session_id", "traceloop.association.properties.thread_id")
	if s.Scope.Name == "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime" || s.Scope.Name == "github.com/ongridio/ongrid/internal/manager/biz/aiops/agent" {
		s.ChatSessionID = stringAttr(a, "ongrid.agent.session.id")
	}
	if s.Identity.Environment == "" {
		s.Identity.Environment = stringAttr(a, "langfuse.environment")
	}
	s.Input = content(a, "input.value", "langfuse.observation.input", "traceloop.entity.input", "gen_ai.input.messages", "gen_ai.prompt", "langfuse.trace.input")
	s.Output = content(a, "output.value", "langfuse.observation.output", "traceloop.entity.output", "gen_ai.output.messages", "gen_ai.completion", "langfuse.trace.output")
	if s.Kind == "llm" || s.Input.State != "available" {
		if messages := indexedContent(a, "llm.input_messages.", "gen_ai.prompt."); messages.State == "available" {
			s.Input = messages
		}
	}
	if s.Kind == "llm" || s.Output.State != "available" {
		if messages := indexedContent(a, "llm.output_messages.", "gen_ai.completion.", "retrieval.documents."); messages.State == "available" {
			s.Output = messages
		}
	}
	if s.Input.State != "available" {
		s.Input = eventContent(s.Events, false)
	}
	if s.Output.State != "available" {
		s.Output = eventContent(s.Events, true)
	}
	s.Usage = decodeUsage(a)
	if s.Usage.Conflict {
		s.Issues = append(s.Issues, "usage_conflict")
	}
	if s.Identity.ServiceName == "" || strings.HasPrefix(s.Identity.ServiceName, "unknown_service") || s.Identity.Environment == "" || s.Identity.ServiceNamespace == "" {
		s.Issues = append(s.Issues, "incomplete_identity")
	}
}

func indexedContent(a Attributes, prefixes ...string) Content {
	for _, prefix := range prefixes {
		groups := map[int]Attributes{}
		for key, value := range a {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			index, field, ok := strings.Cut(strings.TrimPrefix(key, prefix), ".")
			n, err := strconv.Atoi(index)
			if !ok || err != nil || n < 0 {
				continue
			}
			if groups[n] == nil {
				groups[n] = Attributes{}
			}
			groups[n][strings.TrimPrefix(field, "message.")] = value
		}
		if len(groups) == 0 {
			continue
		}
		indices := make([]int, 0, len(groups))
		for n := range groups {
			indices = append(indices, n)
		}
		sort.Ints(indices)
		messages := make([]Attributes, 0, len(groups))
		for _, n := range indices {
			messages = append(messages, groups[n])
		}
		return Content{State: "available", Source: prefix, Value: messages}
	}
	return Content{State: "not_recorded"}
}

func eventContent(events []Event, output bool) Content {
	values := []Attributes{}
	for _, event := range events {
		isOutput := event.Name == "gen_ai.choice" || event.Name == "gen_ai.assistant.message"
		isInput := event.Name == "gen_ai.system.message" || event.Name == "gen_ai.user.message" || event.Name == "gen_ai.tool.message"
		if (output && isOutput) || (!output && isInput) {
			values = append(values, Attributes{"event": event.Name, "body": event.Attributes})
		}
	}
	if len(values) == 0 {
		return Content{State: "not_recorded"}
	}
	return Content{State: "available", Source: "events", Value: values}
}

func decodeUsage(a Attributes) Usage {
	u := Usage{}
	u.InputTokens = token(a, &u.Conflict, "gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens", "llm.token_count.prompt")
	u.OutputTokens = token(a, &u.Conflict, "gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens", "llm.token_count.completion")
	u.TotalTokens = token(a, &u.Conflict, "gen_ai.usage.total_tokens", "llm.token_count.total", "llm.usage.total_tokens")
	u.CachedTokens = token(a, &u.Conflict, "gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.input_tokens.cache_read", "llm.token_count.prompt_details.cache_read")
	if raw, ok := a["langfuse.observation.usage_details"].(string); ok {
		if details, ok := decodeJSON([]byte(raw)).(map[string]any); ok {
			mergeToken(&u.InputTokens, token(details, &u.Conflict, "input"), &u.Conflict)
			mergeToken(&u.OutputTokens, token(details, &u.Conflict, "output"), &u.Conflict)
			mergeToken(&u.TotalTokens, token(details, &u.Conflict, "total"), &u.Conflict)
			mergeToken(&u.CachedTokens, token(details, &u.Conflict, "cache_read_input_tokens"), &u.Conflict)
		} else {
			u.Conflict = true
		}
	}
	if u.InputTokens != nil && u.OutputTokens != nil {
		total := *u.InputTokens + *u.OutputTokens
		if total < *u.InputTokens {
			u.Conflict = true
		} else {
			mergeToken(&u.TotalTokens, &total, &u.Conflict)
		}
	}
	if cost, ok := a["llm.cost.total"]; ok {
		u.ReportedCost = costValue(cost, &u.Conflict)
		u.Currency = "USD"
	}
	if raw, ok := a["langfuse.observation.cost_details"].(string); ok {
		if details, ok := decodeJSON([]byte(raw)).(map[string]any); ok {
			if v, ok := details["total"]; ok {
				cost := costValue(v, &u.Conflict)
				if u.ReportedCost != nil && cost != nil && *cost != *u.ReportedCost {
					u.Conflict = true
				}
				u.ReportedCost = cost
				u.Currency = "USD"
			}
		} else {
			u.Conflict = true
		}
	}
	u.Complete = !u.Conflict && u.InputTokens != nil && u.OutputTokens != nil
	return u
}

func token(a Attributes, conflict *bool, keys ...string) *int64 {
	var result *int64
	for _, key := range keys {
		v, ok := a[key]
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(fmt.Sprint(v), 10, 64)
		if err != nil || n < 0 {
			*conflict = true
			continue
		}
		mergeToken(&result, &n, conflict)
	}
	return result
}

func mergeToken(dst **int64, value *int64, conflict *bool) {
	if value == nil {
		return
	}
	if *dst != nil && **dst != *value {
		*conflict = true
		return
	}
	*dst = value
}

func costValue(value any, conflict *bool) *float64 {
	v, ok := numeric(value)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		*conflict = true
		return nil
	}
	return &v
}
