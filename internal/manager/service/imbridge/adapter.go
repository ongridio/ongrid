package imbridge

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatprotocol"
	svcaiops "github.com/ongridio/ongrid/internal/manager/service/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// LLMDefaultProvider returns the cluster-wide default LLM provider id +
// model that web/SPA chats would pick. The IM path uses this so it
// stays in sync with whatever the operator selected in the UI picker;
// otherwise a stale hard-coded fallback would route IM
// traffic to a model that's not in the catalog and the LLM router
// rejects it ("this model has beta-limitations…"), which surfaces in
// chat apps as "助手执行失败".
//
// Implemented by managerbizsetting.LLMSettingsResolver — the same
// resolver wired into the multi-provider LLM router (see main.go
// llmSettingsResolver) so the IM and HTTP paths converge on one source
// of truth for "default model".
type LLMDefaultProvider interface {
	ResolveProviders(ctx context.Context) ([]llm.ProviderConfig, string, error)
}

// AiopsServiceAdapter wires the imbridge's AgentSession interface to
// the existing aiops.Service. Both EnsureSession and StreamMessage
// authenticate as a fixed "service account" user (configured at boot)
// until per-IM-user binding lands.
type AiopsServiceAdapter struct {
	svc           *svcaiops.Service
	serviceUserID uint64
	defaults      LLMDefaultProvider
	log           *slog.Logger
}

// NewAiopsAdapter wires the adapter. defaults is optional — when nil
// the adapter uses empty RunOptions and follows the router default. Pass the LLMSettingsResolver
// so the IM path picks up the same default the SPA picker writes.
func NewAiopsAdapter(svc *svcaiops.Service, serviceUserID uint64, defaults LLMDefaultProvider, log *slog.Logger) *AiopsServiceAdapter {
	if log == nil {
		log = slog.Default()
	}
	return &AiopsServiceAdapter{
		svc:           svc,
		serviceUserID: serviceUserID,
		defaults:      defaults,
		log:           log.With(slog.String("comp", "imbridge.adapter")),
	}
}

func (a *AiopsServiceAdapter) caller() svcaiops.Caller {
	// Role left blank — backend uses caller.UserID for ownership
	// checks; admin gating doesn't apply on the IM path.
	return svcaiops.Caller{UserID: a.serviceUserID}
}

// EnsureSession just creates a fresh session per inbound thread. We
// don't yet dedupe by label because the bridge already memoises via
// the im_threads table — duplicate calls only happen the first time
// after manager restart, which is acceptable for now.
func (a *AiopsServiceAdapter) EnsureSession(ctx context.Context, ownerUserID uint64, label string) (string, error) {
	caller := a.caller()
	if ownerUserID != 0 {
		caller.UserID = ownerUserID
	}
	sess, err := a.svc.CreateSession(ctx, caller, svcaiops.CreateSessionInput{
		Title: label,
	})
	if err != nil {
		return "", fmt.Errorf("imbridge adapter: create session: %w", err)
	}
	return sess.ID, nil
}

// StreamMessage posts user content to the session and forwards each
// chatprotocol.Event to emit. The agent loop runs synchronously on the
// caller's goroutine — the bridge calls this from its own goroutine
// (the webhook handler returns 200 immediately).
func (a *AiopsServiceAdapter) StreamMessage(ctx context.Context, sessionID string, userContent string, emit chatprotocol.Emit) error {
	opts := a.runOptions(ctx)
	_, err := a.svc.PostMessageStreamWithOpts(ctx, a.caller(), sessionID, userContent, emit, opts)
	return err
}

// runOptions resolves the cluster default provider+model so IM traffic
// uses the same LLM the SPA picker writes. On any resolver error or
// missing catalog, returns zero RunOptions and lets the agent layer
// resolve the configured default.
func (a *AiopsServiceAdapter) runOptions(ctx context.Context) chatprotocol.RunOptions {
	if a.defaults == nil {
		return chatprotocol.RunOptions{}
	}
	providers, defID, err := a.defaults.ResolveProviders(ctx)
	if err != nil || len(providers) == 0 {
		if err != nil {
			a.log.Warn("imbridge: llm resolver failed; using agent fallback model",
				slog.Any("err", err))
		}
		return chatprotocol.RunOptions{}
	}
	// If a default provider is set, look it up; otherwise take the
	// first catalog entry (alphabetical by id — same order the SPA
	// shows). Both paths fall back to the first provider when the
	// configured default points at a provider that was removed.
	var pick *llm.ProviderConfig
	if defID != "" {
		for i := range providers {
			if providers[i].ID == defID {
				pick = &providers[i]
				break
			}
		}
	}
	if pick == nil {
		pick = &providers[0]
	}
	return chatprotocol.RunOptions{
		Provider: pick.ID,
		Model:    pick.Model,
	}
}
