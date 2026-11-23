package aiops

import (
	"context"
	"errors"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatprotocol"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

func TestResolveSessionRunOptions(t *testing.T) {
	provider, selectedModel := "deepseek", "deepseek-chat"
	sess := &model.Session{Provider: &provider, Model: &selectedModel}

	got := resolveSessionRunOptions(sess, chatprotocol.RunOptions{})
	if got.Provider != provider || got.Model != selectedModel {
		t.Fatalf("session fallback = %q/%q", got.Provider, got.Model)
	}

	got = resolveSessionRunOptions(sess, chatprotocol.RunOptions{Provider: "custom", Model: "qwen"})
	if got.Provider != "custom" || got.Model != "qwen" {
		t.Fatalf("request override = %q/%q", got.Provider, got.Model)
	}

	got = resolveSessionRunOptions(&model.Session{}, chatprotocol.RunOptions{})
	if got.Provider != "" || got.Model != "" {
		t.Fatalf("legacy session should fall through: %+v", got)
	}
}

func TestResolveSessionReasoningEffort(t *testing.T) {
	provider, modelName := "openai", "gpt-6.1-terra"
	sess := &model.Session{Provider: &provider, Model: &modelName, ReasoningEffort: "high"}
	inherited := resolveSessionRunOptions(sess, chatprotocol.RunOptions{})
	if inherited.ReasoningEffort == nil || *inherited.ReasoningEffort != "high" {
		t.Fatalf("lost stored effort: %+v", inherited)
	}
	defaultEffort := ""
	explicitDefault := resolveSessionRunOptions(sess, chatprotocol.RunOptions{ReasoningEffort: &defaultEffort})
	if explicitDefault.ReasoningEffort == nil || *explicitDefault.ReasoningEffort != "" {
		t.Fatal("default was replaced by saved effort")
	}
	switched := resolveSessionRunOptions(sess, chatprotocol.RunOptions{Provider: "deepseek", Model: "deepseek-v4-flash"})
	if switched.ReasoningEffort != nil {
		t.Fatal("effort leaked across models")
	}
}

func TestCreateSessionRejectsUnsupportedReasoningBeforeStorage(t *testing.T) {
	service := &Service{}
	effort := "medium"
	_, err := service.CreateSession(context.Background(), Caller{UserID: 1}, CreateSessionInput{
		Provider: "deepseek", Model: "deepseek-v4-flash", ReasoningEffort: &effort,
	})
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("invalid effort accepted: %v", err)
	}
}

type sessionDefaultCatalog struct{ info llm.ProviderInfo }

func (c *sessionDefaultCatalog) Providers() []llm.ProviderInfo { return []llm.ProviderInfo{c.info} }
func (c *sessionDefaultCatalog) Default() (string, string)     { return c.info.ID, c.info.Model }

func TestCreateSessionSnapshotsDefaultEffort(t *testing.T) {
	ctx := context.Background()
	repo := &memSessions{sessions: map[string]*model.Session{}}
	catalog := &sessionDefaultCatalog{info: llm.ProviderInfo{ID: "openai", Model: "gpt-6.1-terra", DefaultReasoningEffort: "high"}}
	service := &Service{sessions: repo, modelCatalog: catalog}
	first, err := service.CreateSession(ctx, Caller{UserID: 1}, CreateSessionInput{})
	if err != nil || first.ReasoningEffort != "high" || first.Provider == nil || *first.Provider != "openai" {
		t.Fatalf("inherit: %+v %v", first, err)
	}
	catalog.info.DefaultReasoningEffort = "low"
	second, err := service.CreateSession(ctx, Caller{UserID: 1}, CreateSessionInput{})
	if err != nil || second.ReasoningEffort != "low" || first.ReasoningEffort != "high" {
		t.Fatalf("snapshot changed: %+v %+v %v", first, second, err)
	}
	providerDefault := ""
	explicit, err := service.CreateSession(ctx, Caller{UserID: 1}, CreateSessionInput{ReasoningEffort: &providerDefault})
	if err != nil || explicit.ReasoningEffort != "" {
		t.Fatalf("provider default overridden: %+v %v", explicit, err)
	}
	catalog.info.Model = "gpt-4o" // An old configured effort must never reach a non-reasoning model.
	plain, err := service.CreateSession(ctx, Caller{UserID: 1}, CreateSessionInput{})
	if err != nil || plain.ReasoningEffort != "" {
		t.Fatalf("unsupported default: %+v %v", plain, err)
	}
}

func TestSessionContextWindowUsesDeploymentCapacity(t *testing.T) {
	ctx := context.Background()
	catalog := &sessionDefaultCatalog{info: llm.ProviderInfo{ID: "custom", Model: "alias", ContextWindows: map[string]llm.WindowCapability{"alias": {Total: 128000, Source: "configuration"}}}}
	service := &Service{sessions: &memSessions{sessions: map[string]*model.Session{}}, modelCatalog: catalog}
	for _, n := range []int{-1, 258000, 64001} {
		if _, e := service.CreateSession(ctx, Caller{UserID: 1}, CreateSessionInput{ContextWindow: n}); !errors.Is(e, errs.ErrInvalid) {
			t.Fatalf("accepted %d: %v", n, e)
		}
	}
	for _, n := range []int{0, 64000, 128000} {
		s, e := service.CreateSession(ctx, Caller{UserID: 1}, CreateSessionInput{ContextWindow: n})
		if e != nil || s.ContextWindow != n {
			t.Fatalf("window %d: %+v %v", n, s, e)
		}
	}
	if e := service.validateWindow(ctx, "v2", "custom", "alias", 64000); !errors.Is(e, errs.ErrInvalid) {
		t.Fatal("legacy runtime accepted a v3 window", e)
	}
}
