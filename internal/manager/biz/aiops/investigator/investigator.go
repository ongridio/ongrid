// Package investigator implements P2 — proactive AI investigation. When
// an incident first fires, the alert usecase asks the investigator to
// spawn a background goroutine that:
//
//  1. Calls the correlate_incident tool directly (no chat round-trip) to
//     gather the metric/log/trace/edge bundle.
//  2. Sends the bundle plus a fixed system prompt to the LLM in a single
//     round-trip — no multi-turn agent loop.
//  3. Persists the LLM response as a model.Event row with
//     event_type=ai_initial_diagnosis, actor_type=system. The
//     IncidentDetail SPA picks it up and renders prominently at the top.
//
// This is the "reactive AIOps → proactive AIOps" move: instead of
// waiting for on-call to type into /chat, the system investigates the
// moment the incident is born.
//
// Design notes:
//
//   - The investigator is OPTIONAL. When LLM is not configured (no
//     APIKey), main.go skips wiring it; the alert usecase tolerates a nil
//     investigator and the firing path is unchanged.
//   - Investigations are bounded by a worker pool (default 3 workers + 100
//     buffered jobs). Bursts above the buffer are dropped with a logged
//     warning — never block the alert ingestion path on a slow LLM.
//   - Each goroutine has its own 60s deadline derived from
//     context.Background(), decoupled from the firing request context, so
//     the firing HTTP/gRPC handler doesn't get held up by upstream LLM
//     latency.
//   - Failure modes (correlate failure, LLM error, persist error) all
//     log a warning and exit silently. The investigation is purely
//     additive; no incident-side state changes on failure.
package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// Defaults tuned for first customer rollout. We expect <10 incidents/min
// at peak; 3 concurrent LLM round-trips + 100 deep buffer leaves ample
// headroom while capping spend on a misconfigured rule storm.
const (
	defaultWorkers    = 3
	defaultQueueDepth = 100
	// Unified with the project-wide LLM timeout floor (see
	// internal/pkg/llm/client.go::defaultTimeout). The 60 s prior
	// default false-failed on reasoning-model defaults; 120 s gives a
	// tool-rich turn room without escaping the human-grade timescale.
	defaultLLMTimeout = 120 * time.Second
	defaultUserMsgCap = 30 * 1024 // ~30KB upper bound for the bundle text
)

// systemPrompt is the fixed system prompt sent on every investigation.
// Kept Chinese to match the operator audience and terse so the LLM
// reliably emits exactly the three asked-for paragraphs.
const systemPrompt = `你是 ongrid AIOps 平台的资深 SRE 助手。下面是一条刚触发的告警 + 它的关联数据。
请用 3 段话产出初查报告：
1. 一句话定性：现象 + 严重度 + 影响范围。
2. 可能根因：基于 metric/log/trace 数据，列 1-3 个最可能的原因，每个一句话说明依据。
3. 立即可执行的排障步骤：3-5 条具体动作（命令 / 检查项 / 升级路径），按优先级排序。

不要复述原始数据，要给判断和动作。简洁。`

// EventWriter is the narrow alert-repo seam this package needs. The
// canonical *alert.Repo satisfies it via CreateEvent. Tests inject a
// fake.
type EventWriter interface {
	CreateEvent(ctx context.Context, ev *model.Event) error
}

// ToolInvoker is the narrow tools-registry seam. *tools.Registry
// satisfies it via Invoke. Declared here so unit tests can hand in a
// fake without standing up the full registry.
type ToolInvoker interface {
	Invoke(ctx context.Context, name string, args json.RawMessage) (aiopstools.ExecuteResult, error)
}

// Config tunes the investigator. Zero values pick the package defaults.
type Config struct {
	// Model is the LLM model slug used in the chat request. Empty means
	// use the router/catalog default so background diagnosis follows the
	// same default provider+model as the home-page picker.
	Model string
	// Workers is the goroutine pool size. Default 3.
	Workers int
	// QueueDepth is the buffered channel depth. Default 100.
	QueueDepth int
	// LLMTimeout caps the LLM round-trip. Default 60s.
	LLMTimeout time.Duration
	// UserMsgCap caps the bundle JSON byte length passed as the user
	// message. Default 30KB. The correlate_incident tool already trims
	// to ~100KB; this is the secondary cap so we don't blow the LLM
	// context on a verbose bundle.
	UserMsgCap int
}

// Investigator is the long-lived service. Construct via New, call
// InvestigateAsync from the alert usecase whenever a fresh incident is
// born. Drain the worker pool on shutdown via Close.
type Investigator struct {
	llmClient llm.Client
	tools     ToolInvoker
	events    EventWriter
	cfg       Config
	log       *slog.Logger

	jobs     chan job
	wg       sync.WaitGroup
	stopOnce sync.Once
	stopped  chan struct{}
}

type job struct {
	incident *model.Incident
}

// New constructs an Investigator and starts cfg.Workers goroutines that
// drain the job queue. The returned value must be closed via Close to
// drain the queue gracefully on shutdown.
//
// Any of llmClient / tools / events being nil renders the Investigator a
// no-op: InvestigateAsync drops the job silently. main.go is expected
// to gate construction on an available LLM provider so the no-op shape is
// only hit in tests.
func New(llmClient llm.Client, tools ToolInvoker, events EventWriter, cfg Config, log *slog.Logger) *Investigator {
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.QueueDepth <= 0 {
		cfg.QueueDepth = defaultQueueDepth
	}
	if cfg.LLMTimeout <= 0 {
		cfg.LLMTimeout = defaultLLMTimeout
	}
	if cfg.UserMsgCap <= 0 {
		cfg.UserMsgCap = defaultUserMsgCap
	}
	if log == nil {
		log = slog.Default()
	}
	inv := &Investigator{
		llmClient: llmClient,
		tools:     tools,
		events:    events,
		cfg:       cfg,
		log:       log.With(slog.String("comp", "ai-investigator")),
		jobs:      make(chan job, cfg.QueueDepth),
		stopped:   make(chan struct{}),
	}
	for i := 0; i < cfg.Workers; i++ {
		inv.wg.Add(1)
		go inv.workerLoop()
	}
	return inv
}

// InvestigateAsync enqueues an investigation for the given incident.
// Returns immediately; the LLM round-trip happens on a worker goroutine
// with its own deadline derived from context.Background().
//
// When the queue is full (the worker pool is already saturated), the
// job is dropped with a logged warning rather than blocking — the alert
// firing path must never wait on AI investigation.
//
// Safe to call on a nil receiver: returns immediately. This lets the
// alert usecase do `if u.investigator != nil` without a runtime nil
// check inside InvestigateAsync (kept for symmetry with the rest of the
// usecase's nil-safe deps).
func (i *Investigator) InvestigateAsync(incident *model.Incident) {
	if i == nil || incident == nil {
		return
	}
	if i.llmClient == nil || i.tools == nil || i.events == nil {
		return
	}
	select {
	case <-i.stopped:
		// Closed; drop silently.
		return
	default:
	}
	select {
	case i.jobs <- job{incident: incident}:
	default:
		i.log.Warn("AI investigator backlog full, skipping",
			slog.Uint64("incident_id", incident.ID),
			slog.Int("queue_depth", i.cfg.QueueDepth),
		)
	}
}

// Close drains the worker pool. Blocks until every in-flight
// investigation finishes (each capped at LLMTimeout). Safe to call
// multiple times; subsequent calls are no-ops.
func (i *Investigator) Close() {
	if i == nil {
		return
	}
	i.stopOnce.Do(func() {
		close(i.stopped)
		close(i.jobs)
	})
	i.wg.Wait()
}

func (i *Investigator) workerLoop() {
	defer i.wg.Done()
	for j := range i.jobs {
		i.runOne(j.incident)
	}
}

// runOne executes a single investigation. All errors log + return; we
// never panic out of a worker, never write a partial event. The
// dispatch context is decoupled from the caller — the alert HTTP/gRPC
// handler that triggered this firing has long since returned by the
// time we get here.
func (i *Investigator) runOne(incident *model.Incident) {
	ctx, cancel := context.WithTimeout(context.Background(), i.cfg.LLMTimeout)
	defer cancel()

	logCtx := i.log.With(
		slog.Uint64("incident_id", incident.ID),
		slog.String("rule_key", incident.Rule),
		slog.String("severity", incident.Severity),
	)

	bundleJSON, err := i.gatherBundle(ctx, incident.ID)
	if err != nil {
		logCtx.Warn("AI investigation: correlate_incident failed", slog.Any("err", err))
		return
	}
	if len(bundleJSON) == 0 {
		logCtx.Warn("AI investigation: empty bundle from correlate_incident")
		return
	}

	bundleJSON = capUserMessage(bundleJSON, i.cfg.UserMsgCap)

	resp, err := i.llmClient.Chat(ctx, llm.ChatReq{
		Model: i.cfg.Model,
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: string(bundleJSON)},
		},
		Temperature: 0.2,
	})
	if err != nil {
		// ErrNoAPIKey is the known-benign case (LLM disabled mid-flight
		// after wiring), log at INFO-ish; everything else is WARN.
		if errors.Is(err, llm.ErrNoAPIKey) {
			logCtx.Info("AI investigation skipped: LLM not configured")
			return
		}
		logCtx.Warn("AI investigation: llm.Chat failed", slog.Any("err", err))
		return
	}
	if resp == nil || resp.Assistant.Content == "" {
		logCtx.Warn("AI investigation: empty LLM response")
		return
	}

	msg := resp.Assistant.Content
	now := time.Now().UTC()
	ev := &model.Event{
		IncidentID:  incident.ID,
		EventType:   model.EventTypeAIInitialDiagnosis,
		StatusAfter: incident.Status,
		Severity:    incident.Severity,
		Title:       "AI 初查",
		Message:     &msg,
		ActorType:   model.ActorTypeSystem,
		OccurredAt:  now,
	}
	if err := i.events.CreateEvent(ctx, ev); err != nil {
		logCtx.Warn("AI investigation: persist event failed", slog.Any("err", err))
		return
	}
	logCtx.Info("AI investigation: initial diagnosis written",
		slog.Int("prompt_tokens", resp.Usage.PromptTokens),
		slog.Int("completion_tokens", resp.Usage.CompletionTokens),
		slog.Int("response_chars", len(msg)),
	)
}

// gatherBundle calls the correlate_incident tool directly (without
// going through the chat session abstraction) and returns its raw JSON
// result. Failures are surfaced so runOne can log + abort.
func (i *Investigator) gatherBundle(ctx context.Context, incidentID uint64) ([]byte, error) {
	args, err := json.Marshal(map[string]any{
		"incident_id": incidentID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal correlate args: %w", err)
	}
	res, err := i.tools.Invoke(ctx, aiopstools.ToolNameCorrelateIncident, args)
	if err != nil {
		return nil, fmt.Errorf("invoke %s: %w", aiopstools.ToolNameCorrelateIncident, err)
	}
	if len(res.ResultJSON) == 0 {
		return nil, errors.New("correlate_incident returned empty result")
	}
	return []byte(res.ResultJSON), nil
}

// capUserMessage trims the bundle JSON if it exceeds the configured
// cap. We don't try to preserve JSON validity on truncation — the LLM
// can read a slightly chopped JSON tail just fine, and the alternative
// (parse + re-trim) is overkill for v1. We append an explicit marker so
// the model knows the tail was cut.
func capUserMessage(b []byte, cap int) []byte {
	if cap <= 0 || len(b) <= cap {
		return b
	}
	out := make([]byte, 0, cap+32)
	out = append(out, b[:cap]...)
	out = append(out, []byte("\n...(truncated)")...)
	return out
}
