package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestCompletionStatus_WirePreservesFinishReasonAndLimit(t *testing.T) {
	for _, tc := range []struct{ model, field string }{
		{"gpt-4o", "max_tokens"}, {"deepseek-reasoner", "max_tokens"},
		{"kimi-k2.5", "max_tokens"}, {"gpt-5", "max_completion_tokens"}, {"o3", "max_completion_tokens"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "bad request", 400)
					return
				}
				if body[tc.field] != float64(3072) {
					t.Errorf("%s = %v, want 3072", tc.field, body[tc.field])
				}
				other := "max_tokens"
				if tc.field == other {
					other = "max_completion_tokens"
				}
				if _, exists := body[other]; exists {
					t.Errorf("unexpected field %s", other)
				}
				writeCompletion(t, w, "stop", "done", "", nil, 10)
			})
			cm, err := NewClientChatModel(ClientChatModelConfig{Client: newTestClient(t, cfg, nil)})
			require.NoError(t, err)
			got, err := cm.Generate(context.Background(), []*schema.Message{schema.UserMessage("question")}, model.WithModel(tc.model), model.WithMaxTokens(3072))
			require.NoError(t, err)
			require.Equal(t, "stop", got.ResponseMeta.FinishReason)
		})
	}
}

// Exercise the real HTTP client through the same adapter used by every runtime.
func TestCompletionStatus_TruncatedToolCallRecoversBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	requests := make(chan map[string]any, 2)
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body
		if calls.Add(1) == 1 {
			writeCompletion(t, w, "length", "unfinished", "", []any{map[string]any{"id": "partial", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{"}}}, 2048)
		} else {
			writeCompletion(t, w, "tool_calls", "", "", []any{map[string]any{"id": "complete", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}, 3000)
		}
	})
	budget := &completionBudget{}
	cm, err := NewClientChatModel(ClientChatModelConfig{Client: newTestClient(t, cfg, budget), UserID: 7})
	require.NoError(t, err)
	require.NoError(t, cm.BindTools([]*schema.ToolInfo{{Name: "lookup"}}))
	got, err := cm.Generate(context.Background(), []*schema.Message{schema.UserMessage("inspect")}, model.WithModel("configured-model"))
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())
	first, second := <-requests, <-requests
	require.NotContains(t, first, "max_tokens")
	require.Equal(t, float64(4096), second["max_tokens"])
	for _, key := range []string{"messages", "tools", "model", "temperature"} {
		require.Equal(t, first[key], second[key], key)
	}
	require.Equal(t, "tool_calls", got.ResponseMeta.FinishReason)
	require.Len(t, got.ToolCalls, 1)
	require.Equal(t, "complete", got.ToolCalls[0].ID)
	require.Empty(t, got.Content)
	require.Equal(t, 5148, got.ResponseMeta.Usage.TotalTokens)
	require.Equal(t, int32(2), budget.checks.Load())
	require.Equal(t, int32(5148), budget.tokens.Load())
}

func TestCompletionStatus_TerminalResponsesNeverDispatchPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name, reason, content, refusal string
		limit, used                    int
		wantCalls                      int32
		wantErr                        error
	}{
		{"explicit limit", "length", "partial", "", 2048, 2048, 1, ErrOutputTruncated},
		{"second truncation", "length", "partial", "", 0, 2048, 2, ErrOutputTruncated},
		{"missing usage", "length", "partial", "", 0, 0, 1, ErrOutputTruncated},
		{"at ceiling", "length", "partial", "", 0, 16384, 1, ErrOutputTruncated},
		{"empty twice", "stop", " ", "", 0, 1, 2, ErrEmptyCompletion},
		{"content filter", "content_filter", "partial", "", 0, 1, 1, ErrCompletionRefused},
		{"refusal", "stop", "", "declined", 0, 1, 1, ErrCompletionRefused},
		{"unknown empty finish", "other", "", "", 0, 1, 1, ErrEmptyCompletion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				writeCompletion(t, w, tc.reason, tc.content, tc.refusal, nil, tc.used)
			})
			budget := &completionBudget{}
			resp, err := newTestClient(t, cfg, budget).Chat(context.Background(), ChatReq{MaxTokens: tc.limit})
			require.ErrorIs(t, err, tc.wantErr)
			require.Nil(t, resp)
			require.Equal(t, tc.wantCalls, calls.Load())
			require.Equal(t, tc.wantCalls*int32(50+tc.used), budget.tokens.Load(), "failed completions still consume budget")
		})
	}
}

func TestCompletionStatus_EmptyReplyContinuesOriginalInput(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "generate", true: "stream"}[stream], func(t *testing.T) {
			var calls atomic.Int32
			requests := make(chan map[string]any, 2)
			_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				requests <- body
				if calls.Add(1) == 1 {
					writeCompletion(t, w, "stop", " ", "", nil, 1)
				} else {
					writeCompletion(t, w, "stop", "verified answer", "", nil, 4)
				}
			})
			cm, err := NewClientChatModel(ClientChatModelConfig{Client: newTestClient(t, cfg, nil)})
			require.NoError(t, err)
			input := []*schema.Message{schema.SystemMessage("policy"), schema.UserMessage("original question")}
			var got *schema.Message
			if stream {
				sr, streamErr := cm.Stream(context.Background(), input)
				require.NoError(t, streamErr)
				defer sr.Close()
				got, err = sr.Recv()
			} else {
				got, err = cm.Generate(context.Background(), input)
			}
			require.NoError(t, err)
			require.Equal(t, "verified answer", got.Content)
			require.Equal(t, 105, got.ResponseMeta.Usage.TotalTokens)
			first, second := <-requests, <-requests
			original := first["messages"].([]any)
			continued := second["messages"].([]any)
			require.Equal(t, original, continued[:len(original)])
			require.Len(t, continued, len(original)+1)
			require.Contains(t, continued[len(original)].(map[string]any)["content"], "no answer and no tool calls")
			require.NotContains(t, second, "max_tokens")
			require.Len(t, input, 2)
		})
	}
}

func TestCompletionStatus_OriginalDeadlineBoundsRecovery(t *testing.T) {
	for _, outer := range []bool{false, true} {
		t.Run(map[bool]string{false: "client timeout", true: "runtime timeout"}[outer], func(t *testing.T) {
			var calls atomic.Int32
			_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				writeCompletion(t, w, "stop", "", "", nil, 1)
			})
			cfg.Timeout = 30 * time.Millisecond
			client := newTestClient(t, cfg, nil)
			if outer {
				client = WithDefaultTimeout(client, func(context.Context) time.Duration { return 20 * time.Millisecond })
			}
			resp, err := client.Chat(context.Background(), ChatReq{})
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Nil(t, resp)
			require.LessOrEqual(t, calls.Load(), int32(1))
		})
	}
}

func TestCompletionStatus_RechecksBudgetBeforeRecovery(t *testing.T) {
	var calls atomic.Int32
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeCompletion(t, w, "stop", "", "", nil, 1)
	})
	budget := &completionBudget{rejectSecond: true}
	resp, err := newTestClient(t, cfg, budget).Chat(context.Background(), ChatReq{})
	require.ErrorIs(t, err, ErrBudgetExceeded)
	require.Nil(t, resp)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, int32(51), budget.tokens.Load())
}

func TestCompletionRecovery_DoesNotMutateResponsesOrCallerBackingArray(t *testing.T) {
	messages := make([]Message, 1, 3)
	messages[0] = Message{Role: "user", Content: "question"}
	backing := messages[:cap(messages)]
	backing[1] = Message{Content: "caller sentinel"}
	final := &ChatResp{Assistant: Message{Content: "answer"}, Usage: Usage{TotalTokens: 3}}
	calls := 0
	got, err := recoverCompletion(context.Background(), ChatReq{Messages: messages}, func(_ context.Context, req ChatReq) (*ChatResp, error) {
		calls++
		if calls == 1 {
			return &ChatResp{Usage: Usage{TotalTokens: 2}}, nil
		}
		require.Contains(t, req.Messages[1].Content, "no answer")
		return final, nil
	})
	require.NoError(t, err)
	require.Equal(t, 5, got.Usage.TotalTokens)
	require.Equal(t, 3, final.Usage.TotalTokens)
	require.Equal(t, "caller sentinel", backing[1].Content)
}

func TestCompletionRecovery_CancellationAndTransportErrorsAreTerminal(t *testing.T) {
	for _, tc := range []string{"before call", "after call", "transport"} {
		t.Run(tc, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc == "before call" {
				cancel()
			}
			calls := 0
			transportErr := errors.New("transport failed")
			resp, err := recoverCompletion(ctx, ChatReq{}, func(context.Context, ChatReq) (*ChatResp, error) {
				calls++
				if tc == "transport" {
					return nil, transportErr
				}
				cancel()
				return &ChatResp{Assistant: Message{Content: "buffered partial"}}, nil
			})
			require.Nil(t, resp)
			if tc == "transport" {
				require.ErrorIs(t, err, transportErr)
			} else {
				require.ErrorIs(t, err, context.Canceled)
			}
			if tc == "before call" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}

func TestTruncationRetryLimit_Bounds(t *testing.T) {
	for _, tc := range []struct{ limit, used, want int }{{0, 0, 0}, {0, -1, 0}, {0, 2048, 4096}, {0, 10000, 16384}, {0, 16384, 0}, {0, int(^uint(0) >> 1), 0}, {2048, 2048, 0}, {-1, 2048, 0}} {
		require.Equal(t, tc.want, truncationRetryLimit(tc.limit, tc.used))
	}
}

func TestCompletionStatus_CancelInFlightRecovery(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		if calls.Add(1) == 1 {
			writeCompletion(t, w, "stop", "", "", nil, 1)
			return
		}
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	client := newTestClient(t, cfg, nil)
	go func() {
		_, err := client.Chat(ctx, ChatReq{})
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("recovery request did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled completion did not return")
	}
	require.Equal(t, int32(2), calls.Load())
}

func TestCompletionRecovery_NilAndMixedFailuresAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses []*ChatResp
		wantErr   error
	}{
		{"nil response", []*ChatResp{nil}, ErrEmptyCompletion},
		{"empty then truncated", []*ChatResp{{}, {FinishReason: "length", Usage: Usage{CompletionTokens: 2048}}}, ErrOutputTruncated},
		{"truncated then empty", []*ChatResp{{FinishReason: "length", Usage: Usage{CompletionTokens: 2048}}, {}}, ErrEmptyCompletion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			resp, err := recoverCompletion(context.Background(), ChatReq{}, func(context.Context, ChatReq) (*ChatResp, error) {
				if calls >= len(tc.responses) {
					t.Fatal("unexpected retry")
				}
				r := tc.responses[calls]
				calls++
				return r, nil
			})
			require.Nil(t, resp)
			require.ErrorIs(t, err, tc.wantErr)
			require.Equal(t, len(tc.responses), calls)
		})
	}
}

type completionBudget struct {
	checks       atomic.Int32
	tokens       atomic.Int32
	rejectSecond bool
}

func (b *completionBudget) Check(context.Context, uint64, int) error {
	if b.checks.Add(1) > 1 && b.rejectSecond {
		return ErrBudgetExceeded
	}
	return nil
}
func (b *completionBudget) Record(_ context.Context, _ uint64, u Usage) error {
	b.tokens.Add(int32(u.TotalTokens))
	return nil
}

func writeCompletion(t *testing.T, w http.ResponseWriter, reason, content, refusal string, tools []any, used int) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	message := map[string]any{"role": "assistant", "content": content}
	if refusal != "" {
		message["refusal"] = refusal
	}
	if tools != nil {
		message["tool_calls"] = tools
	}
	if err := json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": message, "finish_reason": reason}},
		"usage":   map[string]int{"prompt_tokens": 50, "completion_tokens": used, "total_tokens": 50 + used},
	}); err != nil {
		t.Error(err)
	}
}
