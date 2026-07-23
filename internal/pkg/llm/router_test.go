package llm

import (
	"context"
	"errors"
	"testing"
)

// stubClient records the last ChatReq and returns a canned response.
type stubClient struct {
	id    string
	last  ChatReq
	calls int
}

func (s *stubClient) Chat(_ context.Context, req ChatReq) (*ChatResp, error) {
	s.calls++
	s.last = req
	c := "from-" + s.id
	return &ChatResp{Assistant: Message{Role: "assistant", Content: c}}, nil
}

type stubProvidersResolver struct {
	providers []ProviderConfig
	def       string
	err       error
}

func (s stubProvidersResolver) ResolveProviders(context.Context) ([]ProviderConfig, string, error) {
	return s.providers, s.def, s.err
}

func TestMultiClient_RoutesByProvider(t *testing.T) {
	openai := &stubClient{id: "openai"}
	zhipu := &stubClient{id: "zhipu"}
	mc := &MultiClient{
		staticSubs:  map[string]Client{"openai": openai, "zhipu": zhipu},
		staticInfos: []ProviderInfo{{ID: "openai"}, {ID: "zhipu"}},
		staticDefID: "openai",
	}

	resp, err := mc.Chat(context.Background(), ChatReq{Provider: "zhipu", Model: "glm-4-plus"})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Assistant.Content != "from-zhipu" {
		t.Errorf("routed to wrong sub: %q", resp.Assistant.Content)
	}
	if zhipu.last.Model != "glm-4-plus" {
		t.Errorf("model passthrough: %q", zhipu.last.Model)
	}
}

func TestMultiClient_DefaultsWhenProviderEmpty(t *testing.T) {
	openai := &stubClient{id: "openai"}
	mc := &MultiClient{
		staticSubs:  map[string]Client{"openai": openai},
		staticInfos: []ProviderInfo{{ID: "openai"}},
		staticDefID: "openai",
	}
	resp, err := mc.Chat(context.Background(), ChatReq{})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Assistant.Content != "from-openai" {
		t.Errorf("expected default provider; got %q", resp.Assistant.Content)
	}
}

func TestMultiClient_FallbackWhenNoDefault(t *testing.T) {
	fb := &stubClient{id: "fallback"}
	mc := &MultiClient{
		staticSubs:  map[string]Client{},
		staticInfos: nil,
		fallback:    fb,
	}
	resp, err := mc.Chat(context.Background(), ChatReq{})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Assistant.Content != "from-fallback" {
		t.Errorf("expected fallback; got %q", resp.Assistant.Content)
	}
}

func TestMultiClient_UnknownProviderErrors(t *testing.T) {
	openai := &stubClient{id: "openai"}
	mc := &MultiClient{
		staticSubs:  map[string]Client{"openai": openai},
		staticInfos: []ProviderInfo{{ID: "openai"}},
		staticDefID: "openai",
	}
	_, err := mc.Chat(context.Background(), ChatReq{Provider: "anthropic"})
	if err == nil || !errors.Is(err, err) {
		t.Fatalf("expected provider-not-configured error; got %v", err)
	}
}

func TestNewMultiClient_SkipsEmptyAPIKey(t *testing.T) {
	mc := NewMultiClient([]ProviderConfig{
		{ID: "openai", Label: "OpenAI", APIKey: "sk-test", Model: "gpt-4o"},
		{ID: "anthropic", Label: "Anthropic", APIKey: "", Model: "claude-3-5-sonnet"},
	}, "", nil)

	infos := mc.Providers()
	if len(infos) != 1 || infos[0].ID != "openai" {
		t.Fatalf("expected openai only; got %+v", infos)
	}
	if !mc.HasProvider("openai") || mc.HasProvider("anthropic") {
		t.Errorf("HasProvider mismatch")
	}
	defID, defModel := mc.Default()
	if defID != "openai" || defModel != "gpt-4o" {
		t.Errorf("default = %q/%q", defID, defModel)
	}
}

func TestNewMultiClient_ExplicitDefault(t *testing.T) {
	mc := NewMultiClient([]ProviderConfig{
		{ID: "openai", APIKey: "k1", Model: "gpt-4o"},
		{ID: "zhipu", APIKey: "k2", Model: "glm-4-plus"},
	}, "zhipu", nil)
	id, _ := mc.Default()
	if id != "zhipu" {
		t.Errorf("default = %q, want zhipu", id)
	}
}

func TestMultiClient_EmptySuccessfulResolverDisablesStaticAndFallbackClients(t *testing.T) {
	fallback := &stubClient{id: "fallback"}
	mc := NewMultiClient([]ProviderConfig{
		{ID: "openai", APIKey: "env-key", Model: "env-model"},
	}, "openai", fallback)
	mc.SetProvidersResolver(stubProvidersResolver{providers: []ProviderConfig{}})

	if providers := mc.Providers(); len(providers) != 0 {
		t.Fatalf("providers = %+v, want empty authoritative catalog", providers)
	}
	if _, err := mc.Chat(context.Background(), ChatReq{}); err == nil || err.Error() != "llm: no providers configured" {
		t.Fatalf("Chat error = %v, want no providers configured", err)
	}
	if fallback.calls != 0 {
		t.Fatalf("legacy fallback called %d times", fallback.calls)
	}
}

func TestMultiClient_ResolverErrorStillUsesLegacyFallback(t *testing.T) {
	fallback := &stubClient{id: "fallback"}
	mc := NewMultiClient(nil, "", fallback)
	mc.SetProvidersResolver(stubProvidersResolver{err: errors.New("database unavailable")})

	resp, err := mc.Chat(context.Background(), ChatReq{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Assistant.Content != "from-fallback" || fallback.calls != 1 {
		t.Fatalf("fallback response = %+v calls=%d", resp, fallback.calls)
	}
}
