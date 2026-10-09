package knowledge

import (
	"context"
	"fmt"
	"time"

	managerbizaiops "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	managerbizllmwiki "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// LLMWikiUsageRecorder adapts Wiki compilation to the existing chat
// transcript usage model. Internal work sessions are excluded from the user
// chat list but remain included in the established global token aggregates.
type LLMWikiUsageRecorder struct {
	sessions managerbizaiops.SessionRepo
}

var _ managerbizllmwiki.TokenUsageRecorder = (*LLMWikiUsageRecorder)(nil)

// NewLLMWikiUsageRecorder creates the adapter used by the manager composition
// root. It deliberately writes through the existing AIOps session repository.
func NewLLMWikiUsageRecorder(sessions managerbizaiops.SessionRepo) managerbizllmwiki.TokenUsageRecorder {
	return &LLMWikiUsageRecorder{sessions: sessions}
}

func (r *LLMWikiUsageRecorder) Start(ctx context.Context, jobID uint64) (managerbizllmwiki.TokenUsageSink, error) {
	if r == nil || r.sessions == nil {
		return nil, fmt.Errorf("llmwiki usage: AIOps session repository is not configured")
	}
	session := &aiopsmodel.Session{
		UserID:    0,
		Title:     fmt.Sprintf("LLM Wiki compile job %d", jobID),
		Kind:      aiopsmodel.SessionKindWork,
		Initiator: aiopsmodel.SessionInitiatorScheduler,
		Audience:  aiopsmodel.SessionAudienceInternal,
	}
	if err := r.sessions.CreateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("llmwiki usage: create session: %w", err)
	}
	return &llmWikiUsageSink{sessions: r.sessions, sessionID: session.ID}, nil
}

type llmWikiUsageSink struct {
	sessions  managerbizaiops.SessionRepo
	sessionID string
}

var _ managerbizllmwiki.TokenUsageSink = (*llmWikiUsageSink)(nil)

func (s *llmWikiUsageSink) Record(ctx context.Context, usage llm.Usage) error {
	if s == nil || s.sessions == nil || s.sessionID == "" {
		return fmt.Errorf("llmwiki usage: session sink is not configured")
	}
	promptTokens := usage.PromptTokens
	completionTokens := usage.CompletionTokens
	return s.sessions.AppendMessage(ctx, &aiopsmodel.Message{
		SessionID:        s.sessionID,
		Role:             "assistant",
		PromptTokens:     &promptTokens,
		CompletionTokens: &completionTokens,
		CreatedAt:        time.Now().UTC(),
	})
}

func (s *llmWikiUsageSink) Close(ctx context.Context) error {
	if s == nil || s.sessions == nil || s.sessionID == "" {
		return fmt.Errorf("llmwiki usage: session sink is not configured")
	}
	if err := s.sessions.CloseSession(ctx, s.sessionID); err != nil {
		return fmt.Errorf("llmwiki usage: close session: %w", err)
	}
	return nil
}
