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

// The SDK drops Gemini thought signatures in extra_content. Preserve them in
// private history and replay only against the unchanged assistant message.
const chatExtrasPrefix = "ongrid:chat-extras:v1:"

type chatExtrasHistory struct {
	Model   string          `json:"model"`
	Message json.RawMessage `json:"message"`
}

func restoreChatExtras(payload any, chat openai.ChatCompletionRequest) (any, error) {
	var messages []map[string]json.RawMessage
	var fields map[string]json.RawMessage
	for i, message := range chat.Messages {
		if !strings.HasPrefix(message.ReasoningContent, chatExtrasPrefix) {
			continue
		}
		var history chatExtrasHistory
		if err := json.Unmarshal([]byte(strings.TrimPrefix(message.ReasoningContent, chatExtrasPrefix)), &history); err != nil {
			return nil, fmt.Errorf("chat signature history: %w", err)
		}
		var recorded openai.ChatCompletionMessage
		if err := json.Unmarshal(history.Message, &recorded); err != nil {
			return nil, fmt.Errorf("chat signature message: %w", err)
		}
		if history.Model != chat.Model || recorded.Content != message.Content || !reflect.DeepEqual(recorded.ToolCalls, message.ToolCalls) {
			continue
		}
		if fields == nil {
			raw, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal(raw, &fields); err != nil {
				return nil, err
			}
			if err = json.Unmarshal(fields["messages"], &messages); err != nil {
				return nil, err
			}
		}
		// Only the SDK's known fields and the signed extension are replayed.
		var original map[string]json.RawMessage
		if err := json.Unmarshal(history.Message, &original); err != nil {
			return nil, err
		}
		if extra := original["extra_content"]; extra != nil {
			messages[i]["extra_content"] = extra
		}
		if recorded.ReasoningContent != "" {
			messages[i]["reasoning_content"] = original["reasoning_content"]
		}
		if len(recorded.ToolCalls) > 0 {
			messages[i]["tool_calls"] = original["tool_calls"]
		}
	}
	if fields != nil {
		raw, err := json.Marshal(messages)
		if err != nil {
			return nil, err
		}
		fields["messages"] = raw
		return fields, nil
	}
	return payload, nil
}

func preserveChatExtras(resp *http.Response, model string) (*http.Response, error) {
	if resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	closeErr := resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read chat signatures: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close chat signatures: %w", closeErr)
	}
	if len(raw) > 16<<20 {
		return nil, fmt.Errorf("chat response exceeds 16 MiB")
	}
	var payload map[string]json.RawMessage
	var choices []map[string]json.RawMessage
	if err = json.NewDecoder(bytes.NewReader(raw)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("chat response: %w", err)
	}
	if err = json.Unmarshal(payload["choices"], &choices); err != nil {
		return nil, fmt.Errorf("chat choices: %w", err)
	}
	changed := false
	for _, choice := range choices {
		var message map[string]json.RawMessage
		if err = json.Unmarshal(choice["message"], &message); err != nil {
			return nil, err
		}
		var calls []map[string]json.RawMessage
		if value := message["tool_calls"]; value != nil {
			if err = json.Unmarshal(value, &calls); err != nil {
				return nil, err
			}
		}
		hasExtra := len(message["extra_content"]) > 0
		for _, call := range calls {
			hasExtra = hasExtra || len(call["extra_content"]) > 0
		}
		if !hasExtra {
			continue
		}
		history, err := json.Marshal(chatExtrasHistory{Model: model, Message: choice["message"]})
		if err != nil {
			return nil, err
		}
		message["reasoning_content"], err = json.Marshal(chatExtrasPrefix + string(history))
		if err != nil {
			return nil, err
		}
		choice["message"], err = json.Marshal(message)
		if err != nil {
			return nil, err
		}
		changed = true
	}
	if changed {
		payload["choices"], err = json.Marshal(choices)
		if err != nil {
			return nil, err
		}
		raw, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	resp.Header.Del("Content-Length")
	return resp, nil
}
