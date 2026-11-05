package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// Signed thinking blocks stay in the private reasoning field and are replayed
// only to the same model with unchanged visible text and tool calls.
const anthropicHistoryPrefix = "ongrid:anthropic:v1:"

type anthropicHistory struct {
	Model   string            `json:"model"`
	Content []json.RawMessage `json:"content"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

func (t *responsesTransport) roundTripAnthropic(original *http.Request, chat openai.ChatCompletionRequest, capability ReasoningCapability) (*http.Response, error) {
	messages := make([]anthropicMessage, 0, len(chat.Messages))
	system := []string{}
	for _, message := range chat.Messages {
		if message.Role == "system" || message.Role == "developer" {
			if message.Content != "" {
				system = append(system, message.Content)
			}
			continue
		}
		role := message.Role
		content := []any{}
		if role == "tool" {
			role = "user"
			content = append(content, map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": message.Content})
		} else {
			if role != "assistant" && role != "user" {
				return nil, fmt.Errorf("anthropic: unsupported message role %q", role)
			}
			if role == "assistant" && strings.HasPrefix(message.ReasoningContent, anthropicHistoryPrefix) {
				var history anthropicHistory
				if err := json.Unmarshal([]byte(strings.TrimPrefix(message.ReasoningContent, anthropicHistoryPrefix)), &history); err != nil {
					return nil, fmt.Errorf("anthropic history: %w", err)
				}
				recorded, err := anthropicAssistant(history.Content)
				if err != nil {
					return nil, err
				}
				if history.Model == chat.Model && recorded.Content == message.Content && reflect.DeepEqual(recorded.ToolCalls, message.ToolCalls) {
					for _, block := range history.Content {
						content = append(content, block)
					}
				}
			}
			if len(content) == 0 {
				if message.Content != "" {
					content = append(content, map[string]string{"type": "text", "text": message.Content})
				}
				for _, part := range message.MultiContent {
					switch part.Type {
					case openai.ChatMessagePartTypeText:
						content = append(content, map[string]string{"type": "text", "text": part.Text})
					case openai.ChatMessagePartTypeImageURL:
						if part.ImageURL == nil {
							return nil, fmt.Errorf("anthropic: missing image URL")
						}
						imageURL := part.ImageURL.URL
						source := map[string]string{"type": "url", "url": imageURL}
						if strings.HasPrefix(imageURL, "data:") {
							media, data, ok := strings.Cut(strings.TrimPrefix(imageURL, "data:"), ";base64,")
							if !ok || media == "" || data == "" {
								return nil, fmt.Errorf("anthropic: invalid inline image")
							}
							source = map[string]string{"type": "base64", "media_type": media, "data": data}
						}
						content = append(content, map[string]any{"type": "image", "source": source})
					default:
						return nil, fmt.Errorf("anthropic: unsupported message part %q", part.Type)
					}
				}
				for _, call := range message.ToolCalls {
					input := json.RawMessage(call.Function.Arguments)
					if !json.Valid(input) {
						return nil, fmt.Errorf("anthropic: invalid tool arguments")
					}
					content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input})
				}
			}
		}
		if len(content) == 0 {
			continue
		}
		if len(messages) > 0 && messages[len(messages)-1].Role == role {
			messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, content...)
		} else {
			messages = append(messages, anthropicMessage{Role: role, Content: content})
		}
	}
	// The shared request has no output-token option. Keep a bounded native limit,
	// including room for the 2,048-token budget on legacy thinking models.
	payload := map[string]any{"model": chat.Model, "messages": messages, "max_tokens": 8192}
	if len(system) > 0 {
		payload["system"] = strings.Join(system, "\n")
	}
	if effort := chat.ReasoningEffort; effort != "" {
		if effort == "none" {
			payload["thinking"] = map[string]string{"type": "disabled"}
		} else {
			if effort != "enabled" {
				payload["output_config"] = map[string]string{"effort": effort}
			}
			switch capability.Thinking {
			case "enabled":
				payload["thinking"] = map[string]any{"type": "enabled", "budget_tokens": 2048}
			case "adaptive":
				payload["thinking"] = map[string]string{"type": "adaptive"}
			}
		}
	}
	if len(chat.Tools) > 0 {
		tools := make([]map[string]any, 0, len(chat.Tools))
		for _, tool := range chat.Tools {
			if tool.Function == nil {
				return nil, fmt.Errorf("anthropic: missing tool definition")
			}
			tools = append(tools, map[string]any{"name": tool.Function.Name, "description": tool.Function.Description, "input_schema": tool.Function.Parameters})
		}
		payload["tools"] = tools
		if choice, ok := chat.ToolChoice.(string); ok {
			if choice == "required" {
				choice = "any"
			}
			payload["tool_choice"] = map[string]string{"type": choice}
		}
	}
	request, err := requestWithJSON(original, payload)
	if err != nil {
		return nil, err
	}
	request.URL.Path = strings.TrimSuffix(request.URL.Path, "/chat/completions") + "/messages"
	request.Header.Set("anthropic-version", anthropicAPIVersion)
	request.Header.Set("x-api-key", strings.TrimPrefix(original.Header.Get("Authorization"), "Bearer "))
	resp, err := t.base.RoundTrip(request)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, err
	}
	defer resp.Body.Close()
	var result struct {
		ID         string            `json:"id"`
		Model      string            `json:"model"`
		StopReason string            `json:"stop_reason"`
		Content    []json.RawMessage `json:"content"`
		Usage      struct {
			Input         int `json:"input_tokens"`
			Output        int `json:"output_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("anthropic response: %w", err)
	}
	if result.StopReason != "end_turn" && result.StopReason != "tool_use" && result.StopReason != "stop_sequence" && result.StopReason != "refusal" {
		return nil, fmt.Errorf("anthropic: incomplete response (%s)", result.StopReason)
	}
	assistant, err := anthropicAssistant(result.Content)
	if err != nil {
		return nil, err
	}
	history, err := json.Marshal(anthropicHistory{Model: chat.Model, Content: result.Content})
	if err != nil {
		return nil, err
	}
	assistant.ReasoningContent = anthropicHistoryPrefix + string(history)
	inputTokens := result.Usage.Input + result.Usage.CacheRead + result.Usage.CacheCreation
	finishReason := openai.FinishReasonStop
	if len(assistant.ToolCalls) > 0 {
		finishReason = openai.FinishReasonToolCalls
	}
	converted := openai.ChatCompletionResponse{ID: result.ID, Model: result.Model, Choices: []openai.ChatCompletionChoice{{Message: assistant, FinishReason: finishReason}}, Usage: openai.Usage{PromptTokens: inputTokens, CompletionTokens: result.Usage.Output, TotalTokens: inputTokens + result.Usage.Output}}
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

func anthropicAssistant(blocks []json.RawMessage) (openai.ChatCompletionMessage, error) {
	message := openai.ChatCompletionMessage{Role: "assistant"}
	for _, raw := range blocks {
		var block struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return message, fmt.Errorf("anthropic content: %w", err)
		}
		switch block.Type {
		case "thinking", "redacted_thinking":
		case "text":
			message.Content += block.Text
		case "tool_use":
			if block.ID == "" || block.Name == "" || !json.Valid(block.Input) || len(block.Input) == 0 || block.Input[0] != '{' {
				return message, fmt.Errorf("anthropic: invalid tool call")
			}
			message.ToolCalls = append(message.ToolCalls, openai.ToolCall{ID: block.ID, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: block.Name, Arguments: string(block.Input)}})
		default:
			return message, fmt.Errorf("anthropic: unsupported content type %q", block.Type)
		}
	}
	if message.Content == "" && len(message.ToolCalls) == 0 {
		return message, fmt.Errorf("anthropic: empty assistant output")
	}
	return message, nil
}
