package llm

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

var (
	// ErrOutputTruncated prevents partial answers/tool calls from being dispatched.
	ErrOutputTruncated = errors.New("llm: provider output limit reached before completion")
	// ErrEmptyCompletion means the provider returned neither an answer nor a tool call.
	ErrEmptyCompletion = errors.New("llm: provider returned an empty assistant response")
	// ErrCompletionRefused is terminal: never retry content filtering or refusals.
	ErrCompletionRefused = errors.New("llm: provider filtered or refused the completion")
)

// truncationRetryLimit bounds automatic recovery without overriding caller limits.
func truncationRetryLimit(explicitLimit, used int) int {
	const ceiling = 16384
	if explicitLimit != 0 || used <= 0 || used >= ceiling {
		return 0
	}
	return min(used*2, ceiling)
}

// recoverCompletion makes at most one additional completion request. No tools
// have executed at this boundary; partial results are never added to history.
// Each attempt still goes through chatOnce's budget check and usage recording.
func recoverCompletion(ctx context.Context, req ChatReq, call func(context.Context, ChatReq) (*ChatResp, error)) (*ChatResp, error) {
	var total Usage
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := call(ctx, req)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, ErrEmptyCompletion
		}
		total.PromptTokens += resp.Usage.PromptTokens
		total.CompletionTokens += resp.Usage.CompletionTokens
		total.TotalTokens += resp.Usage.TotalTokens
		switch resp.FinishReason {
		case "content_filter":
			return nil, ErrCompletionRefused
		case "length":
			limit := truncationRetryLimit(req.MaxTokens, resp.Usage.CompletionTokens)
			if attempt != 0 || limit == 0 {
				return nil, fmt.Errorf("%w (completion_tokens=%d)", ErrOutputTruncated, resp.Usage.CompletionTokens)
			}
			req.MaxTokens = limit
		default:
			if strings.TrimSpace(resp.Assistant.Content) != "" || len(resp.Assistant.ToolCalls) != 0 {
				complete := *resp
				complete.Usage = total
				return &complete, nil
			}
			// Only normal/unspecified stops are recoverable empty responses.
			// Unknown finish reasons must not trigger an unsolicited continuation.
			if attempt != 0 || (resp.FinishReason != "" && resp.FinishReason != "stop") {
				return nil, ErrEmptyCompletion
			}
			req.Messages = append(append([]Message(nil), req.Messages...), Message{
				Role:    "user",
				Content: "The previous completion returned no answer and no tool calls. Continue the pending user request from the existing evidence. Return a valid call to an available tool if needed, or a final answer supported by the evidence. Do not invent results or repeat completed actions.",
			})
		}
		// One short jittered backoff, interruptible by the original deadline.
		timer := time.NewTimer(100*time.Millisecond + time.Duration(rand.IntN(100))*time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
