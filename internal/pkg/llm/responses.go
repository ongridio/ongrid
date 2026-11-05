package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"

	openai "github.com/sashabaranov/go-openai"
)

// The existing private reasoning field survives graph and database round trips.
// A versioned envelope keeps Responses items out of visible text and other APIs.
const responsesHistoryPrefix = "ongrid:responses:v1:"

type responsesHistory struct {
	Model  string            `json:"model"`
	Output []json.RawMessage `json:"output"`
}

// responsesTransport routes known native models and explicit provider rejections
// to Responses. Its cache is scoped to one credential/base-URL SDK client.
type responsesTransport struct {
	base   http.RoundTripper
	models sync.Map
}

func (t *responsesTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(req.URL.Path, "/chat/completions") || req.Body == nil {
		return t.base.RoundTrip(req)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	var chat openai.ChatCompletionRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&chat); err != nil {
		return nil, err
	}
	capability, ok := req.Context().Value(reasoningCapabilityKey{}).(ReasoningCapability)
	if !ok {
		capability = BuiltinReasoning("", chat.Model)
	}
	if capability.Mode == ReasoningClaudeMode {
		return t.roundTripAnthropic(req, chat, capability)
	}
	// Terra's Chat endpoint rejects max before returning its Responses hint.
	if req.URL.Hostname() == "api.openai.com" && modelVersion(chat.Model, "gpt-6.1-terra") {
		return t.roundTripResponses(req, chat)
	}
	if _, use := t.models.Load(chat.Model); use {
		return t.roundTripResponses(req, chat)
	}
	// A chat may switch providers. Never send opaque Responses state as a
	// DeepSeek reasoning string (or as an unknown Chat Completions field).
	clean := chat
	// Chat Completions rejects tool_choice without tools; Responses accepts
	// it and uses it to forbid tools inferred from history.
	if len(clean.Tools) == 0 {
		clean.ToolChoice = nil
	}
	clean.Messages = append([]openai.ChatCompletionMessage(nil), chat.Messages...)
	for i := range clean.Messages {
		if strings.HasPrefix(clean.Messages[i].ReasoningContent, responsesHistoryPrefix) || strings.HasPrefix(clean.Messages[i].ReasoningContent, anthropicHistoryPrefix) || strings.HasPrefix(clean.Messages[i].ReasoningContent, chatExtrasPrefix) {
			clean.Messages[i].ReasoningContent = ""
		}
	}
	// Switch-only and hybrid providers must not receive an invented effort value.
	var payload any = clean
	if (capability.Mode == ReasoningThinkingMode || capability.Mode == ReasoningHybridMode || capability.Mode == ReasoningAdaptiveMode) && chat.ReasoningEffort != "" {
		wire, err := json.Marshal(clean)
		if err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(wire, &fields); err != nil {
			return nil, err
		}
		if chat.ReasoningEffort == "none" || (strings.HasPrefix(strings.ToLower(chat.Model), "glm-5.2") && chat.ReasoningEffort == "minimal") {
			fields["thinking"] = json.RawMessage(`{"type":"disabled"}`)
			delete(fields, "reasoning_effort")
		} else {
			fields["thinking"] = json.RawMessage(`{"type":"enabled"}`)
			if capability.Mode == ReasoningAdaptiveMode {
				fields["thinking"] = json.RawMessage(`{"type":"adaptive"}`)
			}
			if capability.Mode != ReasoningHybridMode || chat.ReasoningEffort == "enabled" {
				delete(fields, "reasoning_effort")
			}
		}
		payload = fields
	}
	payload, err = restoreChatExtras(payload, chat)
	if err != nil {
		return nil, err
	}
	request, err := requestWithJSON(req, payload)
	if err != nil {
		return nil, err
	}
	// Foreign history must be cleared before checking for missing reasoning.
	if needsDeepSeekReasoningCompatibility(clean) {
		request = request.WithContext(context.WithValue(request.Context(), deepSeekReasoningKey{}, true))
	}
	resp, err := t.base.RoundTrip(request)
	if err != nil || resp.StatusCode != http.StatusBadRequest {
		if err != nil {
			return resp, err
		}
		return preserveChatExtras(resp, chat.Model)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	var failure struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &failure)
	message := strings.ToLower(failure.Error.Message)
	if !strings.Contains(message, "/responses") || (!strings.Contains(message, "not supported") && !strings.Contains(message, "only supported")) {
		return resp, nil
	}
	// The rejected request produced no model output or tool execution.
	t.models.Store(chat.Model, true)
	return t.roundTripResponses(req, chat)
}

func requestWithJSON(original *http.Request, value any) (*http.Request, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	req := original.Clone(original.Context())
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return req, nil
}

func (t *responsesTransport) roundTripResponses(original *http.Request, chat openai.ChatCompletionRequest) (*http.Response, error) {
	input := make([]any, 0, len(chat.Messages))
	for _, msg := range chat.Messages {
		if msg.Role == "assistant" && strings.HasPrefix(msg.ReasoningContent, responsesHistoryPrefix) {
			var history responsesHistory
			if err := json.Unmarshal([]byte(strings.TrimPrefix(msg.ReasoningContent, responsesHistoryPrefix)), &history); err != nil {
				return nil, fmt.Errorf("responses history: %w", err)
			}
			recorded, err := responseMessage(history.Output)
			if err != nil {
				return nil, fmt.Errorf("responses history: %w", err)
			}
			// Runtime guards may edit text or remove tool calls after generation.
			if history.Model == chat.Model && recorded.Content == msg.Content && reflect.DeepEqual(recorded.ToolCalls, msg.ToolCalls) {
				for _, item := range history.Output {
					input = append(input, item)
				}
				continue
			}
		}
		if msg.Role == "tool" {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": msg.ToolCallID, "output": msg.Content})
			continue
		}
		if len(msg.MultiContent) > 0 {
			parts := make([]map[string]any, 0, len(msg.MultiContent))
			for _, part := range msg.MultiContent {
				switch part.Type {
				case openai.ChatMessagePartTypeText:
					parts = append(parts, map[string]any{"type": "input_text", "text": part.Text})
				case openai.ChatMessagePartTypeImageURL:
					if part.ImageURL == nil {
						return nil, fmt.Errorf("responses: missing image URL")
					}
					parts = append(parts, map[string]any{"type": "input_image", "image_url": part.ImageURL.URL, "detail": part.ImageURL.Detail})
				default:
					return nil, fmt.Errorf("responses: unsupported message part %q", part.Type)
				}
			}
			input = append(input, map[string]any{"role": msg.Role, "content": parts})
		} else if msg.Content != "" {
			input = append(input, map[string]any{"role": msg.Role, "content": msg.Content})
		}
		for _, call := range msg.ToolCalls {
			input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
		}
	}
	payload := map[string]any{"model": chat.Model, "input": input, "store": false, "include": []string{"reasoning.encrypted_content"}}
	if len(chat.Tools) > 0 {
		tools := make([]map[string]any, 0, len(chat.Tools))
		for _, tool := range chat.Tools {
			if tool.Function == nil {
				return nil, fmt.Errorf("responses: missing function definition")
			}
			tools = append(tools, map[string]any{"type": "function", "name": tool.Function.Name, "description": tool.Function.Description, "parameters": tool.Function.Parameters, "strict": false})
		}
		payload["tools"] = tools
	}
	if chat.ToolChoice != nil {
		payload["tool_choice"] = chat.ToolChoice
	}
	if chat.ReasoningEffort != "" {
		payload["reasoning"] = map[string]string{"effort": chat.ReasoningEffort}
	}
	request, err := requestWithJSON(original, payload)
	if err != nil {
		return nil, err
	}
	request.URL.Path = strings.TrimSuffix(request.URL.Path, "/chat/completions") + "/responses"
	resp, err := t.base.RoundTrip(request)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, err
	}
	defer resp.Body.Close()
	var result struct {
		ID     string            `json:"id"`
		Model  string            `json:"model"`
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
			Total  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("responses decode: %w", err)
	}
	if result.Status != "completed" {
		return nil, fmt.Errorf("responses: response status %q", result.Status)
	}
	assistant, err := responseMessage(result.Output)
	if err != nil {
		return nil, err
	}
	history, err := json.Marshal(responsesHistory{Model: chat.Model, Output: result.Output})
	if err != nil {
		return nil, err
	}
	assistant.ReasoningContent = responsesHistoryPrefix + string(history)
	converted := openai.ChatCompletionResponse{ID: result.ID, Model: result.Model, Choices: []openai.ChatCompletionChoice{{Message: assistant}}, Usage: openai.Usage{PromptTokens: result.Usage.Input, CompletionTokens: result.Usage.Output, TotalTokens: result.Usage.Total}}
	body, err := json.Marshal(converted)
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Del("Content-Length")
	return resp, nil
}

func responseMessage(output []json.RawMessage) (openai.ChatCompletionMessage, error) {
	assistant := openai.ChatCompletionMessage{Role: "assistant"}
	for _, raw := range output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return openai.ChatCompletionMessage{}, fmt.Errorf("responses output: %w", err)
		}
		switch item.Type {
		case "reasoning":
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					assistant.Content += part.Text
				}
				if part.Type == "refusal" {
					assistant.Content += part.Refusal
				}
			}
		case "function_call":
			if item.CallID == "" || item.Name == "" || !json.Valid([]byte(item.Arguments)) {
				return openai.ChatCompletionMessage{}, fmt.Errorf("responses: invalid function call")
			}
			assistant.ToolCalls = append(assistant.ToolCalls, openai.ToolCall{ID: item.CallID, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: item.Name, Arguments: item.Arguments}})
		default:
			return openai.ChatCompletionMessage{}, fmt.Errorf("responses: unsupported output type %q", item.Type)
		}
	}
	if assistant.Content == "" && len(assistant.ToolCalls) == 0 {
		return openai.ChatCompletionMessage{}, fmt.Errorf("responses: empty assistant output")
	}
	return assistant, nil
}
