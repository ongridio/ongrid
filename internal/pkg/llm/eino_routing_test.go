package llm

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// fakeChatModel is a minimal model.ChatModel used by the routing tests.
// It records every Generate / Stream / BindTools call so the test can
// assert routing decisions without hitting a real provider.
type fakeChatModel struct {
	id string

	mu        sync.Mutex
	lastInput []*schema.Message
	lastOpts  int
	lastModel string
	tools     []*schema.ToolInfo

	generateCalls atomic.Int32
	streamCalls   atomic.Int32
	bindToolCalls atomic.Int32

	bindErr error
	genErr  error
}

func (f *fakeChatModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	f.generateCalls.Add(1)
	f.mu.Lock()
	f.lastInput = input
	f.lastOpts = len(opts)
	if m := model.GetCommonOptions(&model.Options{}, opts...).Model; m != nil {
		f.lastModel = *m
	}
	f.mu.Unlock()
	if f.genErr != nil {
		return nil, f.genErr
	}
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "from:" + f.id,
	}, nil
}

func (f *fakeChatModel) Stream(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	f.streamCalls.Add(1)
	f.mu.Lock()
	f.lastInput = input
	f.lastOpts = len(opts)
	f.mu.Unlock()
	return schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "stream:" + f.id},
	}), nil
}

func (f *fakeChatModel) BindTools(tools []*schema.ToolInfo) error {
	f.bindToolCalls.Add(1)
	f.mu.Lock()
	f.tools = tools
	f.mu.Unlock()
	return f.bindErr
}

func TestNewRoutingChatModel_Validation(t *testing.T) {
	t.Parallel()

	t.Run("empty inner", func(t *testing.T) {
		_, err := NewRoutingChatModel(RoutingChatModelConfig{
			DefaultProvider: ProviderOpenAI,
		})
		if err == nil {
			t.Fatalf("expected error for empty Inner, got nil")
		}
	})
	t.Run("missing default provider", func(t *testing.T) {
		_, err := NewRoutingChatModel(RoutingChatModelConfig{
			Inner: map[string]model.ChatModel{ProviderOpenAI: &fakeChatModel{id: "openai"}},
		})
		if err == nil {
			t.Fatalf("expected error for empty DefaultProvider, got nil")
		}
	})
	t.Run("default not in inner", func(t *testing.T) {
		_, err := NewRoutingChatModel(RoutingChatModelConfig{
			Inner:           map[string]model.ChatModel{ProviderOpenAI: &fakeChatModel{id: "openai"}},
			DefaultProvider: ProviderAnthropic,
		})
		if err == nil {
			t.Fatalf("expected error when default is not in Inner, got nil")
		}
	})
	t.Run("nil inner entry", func(t *testing.T) {
		_, err := NewRoutingChatModel(RoutingChatModelConfig{
			Inner:           map[string]model.ChatModel{ProviderOpenAI: nil},
			DefaultProvider: ProviderOpenAI,
		})
		if err == nil {
			t.Fatalf("expected error for nil inner ChatModel, got nil")
		}
	})
}

func TestRoutingChatModel_DispatchByProviderOption(t *testing.T) {
	t.Parallel()

	openai := &fakeChatModel{id: "openai"}
	anthropic := &fakeChatModel{id: "anthropic"}
	zhipu := &fakeChatModel{id: "zhipu"}
	gemini := &fakeChatModel{id: "gemini"}

	rcm, err := NewRoutingChatModel(RoutingChatModelConfig{
		Inner: map[string]model.ChatModel{
			ProviderOpenAI:    openai,
			ProviderAnthropic: anthropic,
			ProviderZhipu:     zhipu,
			ProviderGemini:    gemini,
		},
		DefaultProvider: ProviderOpenAI,
	})
	if err != nil {
		t.Fatalf("NewRoutingChatModel: %v", err)
	}

	msgs := []*schema.Message{{Role: schema.User, Content: "hi"}}
	ctx := context.Background()

	got, err := rcm.Generate(ctx, msgs, WithProvider(ProviderAnthropic))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Content != "from:anthropic" {
		t.Fatalf("expected dispatch to anthropic, got %q", got.Content)
	}
	if openai.generateCalls.Load() != 0 {
		t.Fatalf("openai should not have been called: got %d", openai.generateCalls.Load())
	}
	if anthropic.generateCalls.Load() != 1 {
		t.Fatalf("anthropic call count = %d, want 1", anthropic.generateCalls.Load())
	}

	got, err = rcm.Generate(ctx, msgs, WithProvider(ProviderZhipu))
	if err != nil {
		t.Fatalf("Generate zhipu: %v", err)
	}
	if got.Content != "from:zhipu" {
		t.Fatalf("expected dispatch to zhipu, got %q", got.Content)
	}
}

func TestRoutingChatModel_FallsBackToDefault(t *testing.T) {
	t.Parallel()

	openai := &fakeChatModel{id: "openai"}
	anthropic := &fakeChatModel{id: "anthropic"}

	rcm, err := NewRoutingChatModel(RoutingChatModelConfig{
		Inner: map[string]model.ChatModel{
			ProviderOpenAI:    openai,
			ProviderAnthropic: anthropic,
		},
		DefaultProvider: ProviderAnthropic,
	})
	if err != nil {
		t.Fatalf("NewRoutingChatModel: %v", err)
	}

	got, err := rcm.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Content != "from:anthropic" {
		t.Fatalf("expected dispatch to default (anthropic), got %q", got.Content)
	}
	if openai.generateCalls.Load() != 0 {
		t.Fatalf("non-default provider should not have been called")
	}
	if rcm.DefaultProvider() != ProviderAnthropic {
		t.Fatalf("DefaultProvider() = %q, want %q", rcm.DefaultProvider(), ProviderAnthropic)
	}
}

// TestRoutingChatModel_DynamicDefault verifies that DefaultResolver lets the
// configured default change live: a call that omits WithProvider routes to the
// resolver's provider (and gets its model injected), an explicit provider /
// model still wins, and a resolved provider with no inner falls back to the
// boot default. This is what makes the RCA investigator track the home-page
// model selection without a restart.
func TestRoutingChatModel_DynamicDefault(t *testing.T) {
	t.Parallel()

	openai := &fakeChatModel{id: "openai"}
	anthropic := &fakeChatModel{id: "anthropic"}
	resolved, mdl := ProviderAnthropic, "claude-opus-4-7"
	rcm, err := NewRoutingChatModel(RoutingChatModelConfig{
		Inner: map[string]model.ChatModel{
			ProviderOpenAI:    openai,
			ProviderAnthropic: anthropic,
		},
		DefaultProvider: ProviderOpenAI, // boot default differs from resolved
		DefaultResolver: func(context.Context) (string, string) { return resolved, mdl },
	})
	if err != nil {
		t.Fatalf("NewRoutingChatModel: %v", err)
	}
	msgs := []*schema.Message{{Role: schema.User, Content: "hi"}}

	// (1) No WithProvider → dynamic default routes to anthropic + injects model.
	got, err := rcm.Generate(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Content != "from:anthropic" {
		t.Fatalf("dynamic default routed to %q, want from:anthropic", got.Content)
	}
	if anthropic.lastModel != mdl {
		t.Fatalf("dynamic default model = %q, want %q", anthropic.lastModel, mdl)
	}

	// (2) Explicit WithProvider wins over the resolver.
	got, err = rcm.Generate(context.Background(), msgs, WithProvider(ProviderOpenAI))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Content != "from:openai" {
		t.Fatalf("explicit provider routed to %q, want from:openai", got.Content)
	}

	// (3) Explicit model is not clobbered by the resolver's model.
	openai.lastModel = ""
	if _, err := rcm.Generate(context.Background(), msgs, WithProvider(ProviderOpenAI), model.WithModel("gpt-4o")); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if openai.lastModel != "gpt-4o" {
		t.Fatalf("explicit model = %q, want gpt-4o", openai.lastModel)
	}

	// (4) Resolver naming an unconfigured provider falls back to the boot default.
	resolved = ProviderDeepSeek // not in inner
	got, err = rcm.Generate(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Content != "from:openai" {
		t.Fatalf("unknown resolved provider routed to %q, want boot default from:openai", got.Content)
	}
}

func TestRoutingChatModel_UnknownProvider(t *testing.T) {
	t.Parallel()

	openai := &fakeChatModel{id: "openai"}
	rcm, err := NewRoutingChatModel(RoutingChatModelConfig{
		Inner:           map[string]model.ChatModel{ProviderOpenAI: openai},
		DefaultProvider: ProviderOpenAI,
	})
	if err != nil {
		t.Fatalf("NewRoutingChatModel: %v", err)
	}

	_, err = rcm.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		WithProvider("does-not-exist"),
	)
	if err == nil {
		t.Fatalf("expected error for unknown provider, got nil")
	}
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("expected ErrUnknownProvider, got %v", err)
	}
}

func TestRoutingChatModel_StreamRoutes(t *testing.T) {
	t.Parallel()

	openai := &fakeChatModel{id: "openai"}
	gemini := &fakeChatModel{id: "gemini"}

	rcm, err := NewRoutingChatModel(RoutingChatModelConfig{
		Inner: map[string]model.ChatModel{
			ProviderOpenAI: openai,
			ProviderGemini: gemini,
		},
		DefaultProvider: ProviderOpenAI,
	})
	if err != nil {
		t.Fatalf("NewRoutingChatModel: %v", err)
	}

	sr, err := rcm.Stream(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		WithProvider(ProviderGemini),
	)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	var got string
	for {
		chunk, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		got += chunk.Content
	}
	if got != "stream:gemini" {
		t.Fatalf("stream content = %q, want stream:gemini", got)
	}
	if gemini.streamCalls.Load() != 1 {
		t.Fatalf("gemini stream count = %d, want 1", gemini.streamCalls.Load())
	}
	if openai.streamCalls.Load() != 0 {
		t.Fatalf("openai stream should not have been called")
	}
}

func TestRoutingChatModel_BindToolsFanout(t *testing.T) {
	t.Parallel()

	openai := &fakeChatModel{id: "openai"}
	anthropic := &fakeChatModel{id: "anthropic"}
	zhipu := &fakeChatModel{id: "zhipu"}
	gemini := &fakeChatModel{id: "gemini"}

	rcm, err := NewRoutingChatModel(RoutingChatModelConfig{
		Inner: map[string]model.ChatModel{
			ProviderOpenAI:    openai,
			ProviderAnthropic: anthropic,
			ProviderZhipu:     zhipu,
			ProviderGemini:    gemini,
		},
		DefaultProvider: ProviderOpenAI,
	})
	if err != nil {
		t.Fatalf("NewRoutingChatModel: %v", err)
	}

	tools := []*schema.ToolInfo{{Name: "search", Desc: "do a search"}}
	if err := rcm.BindTools(tools); err != nil {
		t.Fatalf("BindTools: %v", err)
	}
	for name, fk := range map[string]*fakeChatModel{
		"openai":    openai,
		"anthropic": anthropic,
		"zhipu":     zhipu,
		"gemini":    gemini,
	} {
		if fk.bindToolCalls.Load() != 1 {
			t.Errorf("provider %s BindTools count = %d, want 1", name, fk.bindToolCalls.Load())
		}
		if len(fk.tools) != 1 || fk.tools[0].Name != "search" {
			t.Errorf("provider %s did not receive tool list", name)
		}
	}
}

func TestRoutingChatModel_BindToolsBubblesError(t *testing.T) {
	t.Parallel()

	bad := &fakeChatModel{id: "bad", bindErr: errors.New("nope")}
	good := &fakeChatModel{id: "good"}

	rcm, err := NewRoutingChatModel(RoutingChatModelConfig{
		Inner: map[string]model.ChatModel{
			ProviderOpenAI:    good,
			ProviderAnthropic: bad,
		},
		DefaultProvider: ProviderOpenAI,
	})
	if err != nil {
		t.Fatalf("NewRoutingChatModel: %v", err)
	}

	if err := rcm.BindTools([]*schema.ToolInfo{{Name: "t"}}); err == nil {
		t.Fatalf("expected BindTools to bubble inner error, got nil")
	}
}

func TestRoutingChatModel_Providers(t *testing.T) {
	t.Parallel()

	rcm, err := NewRoutingChatModel(RoutingChatModelConfig{
		Inner: map[string]model.ChatModel{
			ProviderOpenAI: &fakeChatModel{id: "openai"},
			ProviderZhipu:  &fakeChatModel{id: "zhipu"},
		},
		DefaultProvider: ProviderOpenAI,
	})
	if err != nil {
		t.Fatalf("NewRoutingChatModel: %v", err)
	}
	got := rcm.Providers()
	if len(got) != 2 {
		t.Fatalf("Providers() returned %v, want 2 entries", got)
	}
}

// ---- clientChatModel adapter tests --------------------------------------

// einoStubClient is a llm.Client used by the adapter tests.
type einoStubClient struct {
	resp    *ChatResp
	err     error
	lastReq ChatReq
	callCnt atomic.Int32
}

func (s *einoStubClient) Chat(_ context.Context, req ChatReq) (*ChatResp, error) {
	s.callCnt.Add(1)
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	if s.resp != nil {
		return s.resp, nil
	}
	return &ChatResp{
		Assistant: Message{Role: "assistant", Content: "ok"},
		Usage:     Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10},
	}, nil
}

func TestNewClientChatModel_NilClient(t *testing.T) {
	t.Parallel()
	if _, err := NewClientChatModel(ClientChatModelConfig{}); err == nil {
		t.Fatalf("expected error when Client is nil")
	}
}

func TestClientChatModel_GenerateTranslatesUsage(t *testing.T) {
	t.Parallel()
	stub := &einoStubClient{}
	cm, err := NewClientChatModel(ClientChatModelConfig{
		Client: stub,
		Model:  "gpt-test",
		UserID: 42,
	})
	if err != nil {
		t.Fatalf("NewClientChatModel: %v", err)
	}
	got, err := cm.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hello world"}},
		model.WithTemperature(0.5),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Role != schema.Assistant {
		t.Fatalf("role = %q, want assistant", got.Role)
	}
	if got.Content != "ok" {
		t.Fatalf("content = %q, want ok", got.Content)
	}
	if got.ResponseMeta == nil || got.ResponseMeta.Usage == nil {
		t.Fatalf("expected ResponseMeta.Usage to be set")
	}
	if got.ResponseMeta.Usage.TotalTokens != 10 {
		t.Fatalf("TotalTokens = %d, want 10", got.ResponseMeta.Usage.TotalTokens)
	}

	// Verify req translation.
	if stub.lastReq.Model != "gpt-test" {
		t.Errorf("req.Model = %q, want gpt-test", stub.lastReq.Model)
	}
	if stub.lastReq.UserID != 42 {
		t.Errorf("req.UserID = %d, want 42", stub.lastReq.UserID)
	}
	if stub.lastReq.Temperature != 0.5 {
		t.Errorf("req.Temperature = %v, want 0.5", stub.lastReq.Temperature)
	}
	if len(stub.lastReq.Messages) != 1 || stub.lastReq.Messages[0].Content != "hello world" {
		t.Errorf("req messages = %+v", stub.lastReq.Messages)
	}
}

func TestClientChatModelPreservesMultimodalInput(t *testing.T) {
	t.Parallel()
	stub := &einoStubClient{}
	cm, err := NewClientChatModel(ClientChatModelConfig{Client: stub})
	if err != nil {
		t.Fatalf("NewClientChatModel: %v", err)
	}
	data := "aGVsbG8="
	_, err = cm.Generate(context.Background(), []*schema.Message{{
		Role:    schema.User,
		Content: "describe",
		UserInputMultiContent: []schema.MessageInputPart{{
			Type:  schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: "image/png"}},
		}},
	}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(stub.lastReq.Messages) != 1 || len(stub.lastReq.Messages[0].Images) != 1 {
		t.Fatalf("multimodal input was not preserved: %+v", stub.lastReq.Messages)
	}
	if got := stub.lastReq.Messages[0].Images[0]; got.MIMEType != "image/png" || got.Data != data {
		t.Fatalf("unexpected image: %+v", got)
	}
}

func TestClientChatModel_BindToolsAttaches(t *testing.T) {
	t.Parallel()
	stub := &einoStubClient{}
	cm, err := NewClientChatModel(ClientChatModelConfig{Client: stub, Model: "gpt-test"})
	if err != nil {
		t.Fatalf("NewClientChatModel: %v", err)
	}
	if err := cm.BindTools([]*schema.ToolInfo{{Name: "search", Desc: "do a search"}}); err != nil {
		t.Fatalf("BindTools: %v", err)
	}
	if _, err := cm.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
	); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(stub.lastReq.Tools) != 1 {
		t.Fatalf("expected 1 tool forwarded, got %d", len(stub.lastReq.Tools))
	}
	if stub.lastReq.Tools[0].Name != "search" {
		t.Errorf("tool name = %q, want search", stub.lastReq.Tools[0].Name)
	}
	if len(stub.lastReq.Tools[0].Parameters) == 0 {
		t.Errorf("expected non-empty Parameters JSON")
	}
}

func TestClientChatModel_WithToolsImmutable(t *testing.T) {
	t.Parallel()
	stub := &einoStubClient{}
	cm, err := NewClientChatModel(ClientChatModelConfig{Client: stub})
	if err != nil {
		t.Fatalf("NewClientChatModel: %v", err)
	}
	tcm, ok := cm.(model.ToolCallingChatModel)
	if !ok {
		t.Fatalf("clientChatModel does not satisfy model.ToolCallingChatModel")
	}
	derived, err := tcm.WithTools([]*schema.ToolInfo{{Name: "calc"}})
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	if any(derived) == any(cm) {
		t.Fatalf("WithTools should return a new instance, not the receiver")
	}
	// Original adapter still has no bound tools.
	if _, err := cm.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
	); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(stub.lastReq.Tools) != 0 {
		t.Fatalf("original adapter should not have tools, got %d", len(stub.lastReq.Tools))
	}
	// Derived adapter forwards them.
	if _, err := derived.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
	); err != nil {
		t.Fatalf("derived.Generate: %v", err)
	}
	if len(stub.lastReq.Tools) != 1 || stub.lastReq.Tools[0].Name != "calc" {
		t.Fatalf("derived adapter did not forward tools: got %+v", stub.lastReq.Tools)
	}
}
