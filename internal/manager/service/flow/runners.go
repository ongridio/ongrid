package flow

import (
	"context"
	"fmt"
	"strings"
	"time"

	aiopschatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	managerbizalert "github.com/ongridio/ongrid/internal/manager/biz/alert"
	managermodelalert "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

type channelGetter interface {
	GetChannelByID(context.Context, uint64) (*managermodelalert.Channel, error)
}

// AgentRunner implements bizflow.AgentRunner over the chatruntime —
// one synchronous worker per agent node (mirrors the RCA investigator's
// WorkerSpawner usage).
type agentWorkerSpawner interface {
	SpawnWorker(context.Context, aiopschatruntime.SpawnRequest) (*aiopschatruntime.Worker, error)
}
type AgentRunner struct{ Runtime agentWorkerSpawner }

func (s AgentRunner) RunAgent(ctx context.Context, persona, prompt string) (string, error) {
	w, err := s.Runtime.SpawnWorker(ctx, aiopschatruntime.SpawnRequest{
		AgentName:   persona,
		Prompt:      prompt,
		Background:  false, // sync — the flow engine owns concurrency
		SessionKind: "flow",
	})
	if err != nil {
		return "", err
	}
	if w == nil {
		return "", fmt.Errorf("agent runner: nil worker")
	}
	if w.Status != aiopschatruntime.WorkerStatusCompleted {
		reason := w.Err
		if reason == "" {
			reason = string(w.Status)
		}
		return "", fmt.Errorf("agent worker %s: %s", w.ID, reason)
	}
	return w.Result, nil
}

// LLMRunner implements bizflow.LLMRunner over the routing llm.Client
// — one chat completion, no tools, no agent loop. Provider/Model left
// empty so the call follows the configured default (DefaultResolver),
// same as the report extractor / RCA summarizer.
type LLMRunner struct{ Client llm.Client }

func (s LLMRunner) RunLLM(ctx context.Context, system, user string) (string, error) {
	if s.Client == nil {
		return "", fmt.Errorf("llm client not configured")
	}
	msgs := make([]llm.Message, 0, 2)
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, llm.Message{Role: "system", Content: system})
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: user})
	resp, err := s.Client.Chat(ctx, llm.ChatReq{Messages: msgs})
	if err != nil {
		return "", err
	}
	return resp.Assistant.Content, nil
}

// Notifier implements bizflow.Notifier over the alert channel
// store + notify router — same BuildSenderFromChannel path the alert
// notifier and report deliverer use.
type Notifier struct {
	Channels channelGetter
	Router   *notify.Router
}

func (s Notifier) Notify(ctx context.Context, channelIDs []uint64, title, message string) error {
	var firstErr error
	sent := 0
	for _, id := range channelIDs {
		ch, err := s.Channels.GetChannelByID(ctx, id)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("channel %d: not found", id)
			}
			continue
		}
		if !ch.Enabled {
			if firstErr == nil {
				firstErr = fmt.Errorf("channel %d: disabled", id)
			}
			continue
		}
		sender, err := managerbizalert.BuildSenderFromChannel(ch)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("channel %d: %w", id, err)
			}
			continue
		}
		msg := notify.Message{
			Subject:    title,
			Body:       message,
			Severity:   notify.SeverityInfo,
			Source:     "flow",
			OccurredAt: time.Now().UTC(),
		}
		if err := s.Router.SendVia(ctx, msg, sender); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("channel %d: %w", id, err)
			}
			continue
		}
		sent++
	}
	if sent == 0 && firstErr != nil {
		return firstErr
	}
	return nil
}
