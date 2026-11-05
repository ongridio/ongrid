package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

type deepSeekReasoningKey struct{}

func needsDeepSeekReasoningCompatibility(req openai.ChatCompletionRequest) bool {
	name := strings.ToLower(req.Model)
	if len(req.Tools) == 0 || (!strings.Contains(name, "deepseek") && !strings.HasPrefix(name, "mimo-")) {
		return false
	}
	for _, m := range req.Messages {
		if m.Role == openai.ChatMessageRoleAssistant && m.ReasoningContent == "" {
			return true
		}
	}
	return false
}

// deepSeekReasoningTransport 弥补 DeepSeek/MiMo 下 SDK string+omitempty 无法发送空字段的问题。
// 原模型输出原样回传；历史缺失值只发送空字符串，不伪造思考内容。
type deepSeekReasoningTransport struct {
	base http.RoundTripper
}

func (t *deepSeekReasoningTransport) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	if enabled, _ := req.Context().Value(deepSeekReasoningKey{}).(bool); !enabled {
		return t.base.RoundTrip(req)
	}
	// 兼容分支发送的是请求副本；原请求体仍由本 RoundTripper 负责关闭。
	// 包括构造副本失败的路径，并保留原始错误链。
	defer func() {
		if req.Body != nil {
			if closeErr := req.Body.Close(); closeErr != nil && resp == nil {
				err = errors.Join(err, fmt.Errorf("llm: close original reasoning request: %w", closeErr))
			}
		}
	}()
	if req.GetBody == nil {
		return nil, fmt.Errorf("llm: reasoning compatibility requires a replayable request body")
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("llm: copy reasoning request: %w", err)
	}
	raw, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("llm: read reasoning request: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("llm: close reasoning request: %w", closeErr)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("llm: decode reasoning request: %w", err)
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(payload["messages"], &messages); err != nil {
		return nil, fmt.Errorf("llm: decode reasoning messages: %w", err)
	}
	for _, m := range messages {
		if string(m["role"]) == `"assistant"` {
			if _, present := m["reasoning_content"]; !present {
				m["reasoning_content"] = json.RawMessage(`""`)
			}
		}
	}
	payload["messages"], err = json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("llm: encode reasoning messages: %w", err)
	}
	raw, err = json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("llm: encode reasoning request: %w", err)
	}
	cloned := req.Clone(req.Context())
	cloned.Body = io.NopCloser(bytes.NewReader(raw))
	cloned.ContentLength = int64(len(raw))
	cloned.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	}
	return t.base.RoundTrip(cloned)
}
