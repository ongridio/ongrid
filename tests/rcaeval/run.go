package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph"
	toolkit "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/manager/biz/alert/investigator"
	aiopsstore "github.com/ongridio/ongrid/internal/manager/data/aiops/store"
	alertstore "github.com/ongridio/ongrid/internal/manager/data/alert/store"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	aiopsservice "github.com/ongridio/ongrid/internal/manager/service/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

type runConfig struct {
	Mode          string        `json:"mode"`
	Model         string        `json:"model"`
	BaseURL       string        `json:"base_url,omitempty"`
	MaxModelCalls int           `json:"max_model_calls"`
	MaxToolCalls  int           `json:"max_tool_calls"`
	MaxTokens     int           `json:"token_stop_threshold"`
	Timeout       time.Duration `json:"timeout_ns"`
	Temperature   float32       `json:"temperature"`
}

type runResult struct {
	ID                  string                          `json:"id"`
	CaseID              string                          `json:"case_id"`
	Title               string                          `json:"title"`
	Repeat              int                             `json:"repeat"`
	CaseHash            string                          `json:"case_sha256"`
	Status              string                          `json:"status"`
	Config              runConfig                       `json:"config"`
	ElapsedMS           int64                           `json:"elapsed_ms"`
	Usage               llm.Usage                       `json:"usage"`
	UsageComplete       bool                            `json:"usage_complete"`
	ModelCalls          int                             `json:"model_calls"`
	ToolCalls           int                             `json:"tool_calls"`
	ToolExecutions      int                             `json:"tool_executions"`
	RepeatedQueries     int                             `json:"repeated_queries"`
	Sessions            int                             `json:"sessions"`
	Issues              []string                        `json:"issues"`
	MissingObservations []string                        `json:"missing_observations"`
	Queries             []replayCall                    `json:"queries"`
	Report              *alertmodel.InvestigationReport `json:"report,omitempty"`
	Rubric              rubric                          `json:"rubric"`
}

// Signal only after the production repository has stored the terminal result.
type completionRepo struct {
	*alertstore.InvestigationRepo
	done chan error
	once sync.Once
}

func (r *completionRepo) MarkReady(ctx context.Context, id string, fields investigator.ReadyFields) error {
	err := r.InvestigationRepo.MarkReady(ctx, id, fields)
	r.once.Do(func() { r.done <- err })
	return err
}
func (r *completionRepo) UpdateStatus(ctx context.Context, id, status, reason string) error {
	err := r.InvestigationRepo.UpdateStatus(ctx, id, status, reason)
	if status == "failed" || status == "skipped" {
		r.once.Do(func() { r.done <- err })
	}
	return err
}

// Both the top-level worker and production AgentTool use the same runtime.
type workerAdapter struct{ rt *chatruntime.Runtime }

type evaluationSpawner struct {
	*chatruntime.Runtime
	root context.Context
}

func (s evaluationSpawner) SpawnWorker(ctx context.Context, req contract.SpawnRequest) (*contract.Worker, error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.root, cancel)
	defer func() { stop(); cancel() }()
	// The isolated tool bag contains no infrastructure writes. This gate admits
	// the production AgentTool so the corpus can exercise specialist evidence.
	return s.Runtime.SpawnWorker(basetool.WithAgentWriteAllowed(ctx, true), req)
}

func (a workerAdapter) SpawnWorker(ctx context.Context, req toolkit.SpawnWorkerRequest) (*toolkit.WorkerHandle, error) {
	w, err := a.rt.SpawnWorker(ctx, contract.SpawnRequest{AgentName: req.AgentName, Prompt: req.Prompt,
		ParentSession: req.ParentSession, Locale: req.Locale, Provider: req.Provider, Model: req.Model, ReasoningEffort: req.ReasoningEffort})
	if err != nil {
		return nil, err
	}
	return workerHandle(w), nil
}
func (a workerAdapter) SendToWorker(ctx context.Context, id, message string) error {
	return a.rt.SendToWorker(ctx, id, message)
}
func (a workerAdapter) StopWorker(ctx context.Context, id string) error {
	return a.rt.StopWorker(ctx, id)
}
func (a workerAdapter) GetWorker(id string) (*toolkit.WorkerHandle, bool) {
	w, ok := a.rt.GetWorker(id)
	if !ok {
		return nil, false
	}
	return workerHandle(w), true
}
func workerHandle(w *contract.Worker) *toolkit.WorkerHandle {
	return &toolkit.WorkerHandle{ID: w.ID, AgentName: w.AgentName, Status: string(w.Status), Result: w.Result, Err: w.Err}
}

func runCase(ctx context.Context, root, dir string, c evalCase, repeat int, cfg runConfig, client llm.Client) (*runResult, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "sessions.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	if err := aiopsstore.Migrate(db); err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&alertmodel.InvestigationReport{}); err != nil {
		return nil, err
	}
	sessions := aiopsstore.NewSessionRepoWithAttachmentRoot(db, filepath.Join(dir, "attachments"))
	repo := &completionRepo{InvestigationRepo: alertstore.NewInvestigationRepo(db), done: make(chan error, 1)}
	reg := chatruntime.NewAgentRegistry()
	if err := reg.Load(filepath.Join(root, "agents")); err != nil {
		return nil, err
	}
	for _, name := range []string{"incident-investigator", "specialist-sre"} {
		if _, ok := reg.ByName(name); !ok {
			return nil, fmt.Errorf("required agent %s not loaded: %v", name, reg.Warnings())
		}
	}
	if cfg.Mode == "smoke" {
		client = &smokeClient{caseData: c, positions: map[string]int{}}
	}
	m := &meter{root: ctx, inner: client, gate: make(chan struct{}, 1), maxCalls: cfg.MaxModelCalls, maxTokens: cfg.MaxTokens}
	cm, err := llm.NewClientChatModel(llm.ClientChatModelConfig{Client: meteredClient{m, "investigation"}, Model: cfg.Model})
	if err != nil {
		return nil, err
	}
	replay, err := newReplay(c, cfg.MaxToolCalls)
	if err != nil {
		return nil, err
	}
	bag, err := replay.tools(ctx)
	if err != nil {
		return nil, err
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt, err := chatruntime.NewRuntime(chatruntime.Config{Sessions: sessions, AgentRegistry: reg,
		ChatModel: cm.(einomodel.ToolCallingChatModel), ToolBag: bag, BasePrompt: aiopsservice.BasePrompt(),
		GraphCfg: graph.Config{Model: cfg.Model, Temperature: cfg.Temperature}, Logger: log})
	if err != nil {
		return nil, err
	}
	agentTool := toolkit.NewAgentTool(workerAdapter{rt}, nil, log)
	info, err := agentTool.Info(ctx)
	if err != nil {
		return nil, err
	}
	if err := rt.AppendToolBag([]basetool.BaseTool{&replayTool{info: info, state: replay, delegate: agentTool}}); err != nil {
		return nil, err
	}
	uc := investigator.NewUsecase(repo, evaluationSpawner{rt, ctx}, meteredClient{m, "extraction"}, investigator.Config{
		Enabled: true, WorkerTimeout: cfg.Timeout, SummarizerTimeout: cfg.Timeout, SummarizerModel: cfg.Model,
		MaxConcurrent: 1, DefaultLocale: "zh"}, log).WithEvidenceReader(sessions).WithMessageReader(sessions)
	if err := uc.ForceEnqueue(ctx, c.Incident.model()); err != nil {
		return nil, err
	}
	// Detached production work has its own deadline; the model wrapper cancels
	// active requests at our earlier deadline, then we await its final DB write.
	select {
	case err = <-repo.done:
	case <-time.After(cfg.Timeout + 15*time.Second):
		return nil, fmt.Errorf("pipeline did not terminate after its timeout")
	}
	if err != nil {
		return nil, err
	}
	rep, err := repo.GetByIncident(context.Background(), c.Incident.ID)
	if err != nil {
		return nil, err
	}
	result := &runResult{ID: filepath.Base(dir), CaseID: c.ID, Title: c.Title, Repeat: repeat,
		Config: cfg, Status: "awaiting_review", Report: rep, Rubric: c.Rubric,
		ElapsedMS: time.Since(started).Milliseconds(), CaseHash: hashValue(c)}
	replay.mu.Lock()
	result.Queries = append([]replayCall(nil), replay.calls...)
	toolBudget := replay.exceeded
	replay.mu.Unlock()
	m.mu.Lock()
	calls := append([]modelCall(nil), m.calls...)
	modelBudget := m.exceeded
	m.mu.Unlock()
	result.ModelCalls, result.ToolExecutions = len(calls), len(result.Queries)
	result.UsageComplete = cfg.Mode == "live"
	seen := map[string]bool{}
	for _, call := range calls {
		if call.Error != "" {
			result.Issues = append(result.Issues, "model_error: "+call.Error)
			result.Status = "model_error"
		}
		if call.Response == nil {
			result.UsageComplete = false
			continue
		}
		for _, tc := range call.Response.Assistant.ToolCalls {
			result.ToolCalls++
			key, _ := queryKey(tc.Name, tc.Args)
			if seen[key] {
				result.RepeatedQueries++
			}
			seen[key] = true
		}
		if call.Response.Usage.TotalTokens == 0 {
			result.UsageComplete = false
		}
		result.Usage.PromptTokens += call.Response.Usage.PromptTokens
		result.Usage.CompletionTokens += call.Response.Usage.CompletionTokens
		result.Usage.TotalTokens += call.Response.Usage.TotalTokens
	}
	observed := map[string]bool{}
	for _, q := range result.Queries {
		observed[q.Fixture] = true
		if q.Error == "fixture_coverage_gap" && result.Status == "awaiting_review" {
			result.Status = "fixture_gap"
		}
	}
	for _, id := range c.Rubric.Required {
		if !observed[id] {
			result.MissingObservations = append(result.MissingObservations, id)
		}
	}
	if rep.Status != "ready" {
		result.Issues = append(result.Issues, "pipeline: "+rep.StatusReason)
		if result.Status == "awaiting_review" {
			result.Status = "pipeline_error"
		}
	}
	if modelBudget || toolBudget {
		result.Status = "budget_exceeded"
	}
	var rows []*aiopsmodel.Session
	if err := db.Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result.Sessions = len(rows)
	transcript := map[string]any{}
	for _, s := range rows {
		messages, err := sessions.ListMessages(context.Background(), s.ID, 0)
		if err != nil {
			return nil, err
		}
		transcript[s.ID] = map[string]any{"session": s, "messages": messages}
	}
	for i := range calls {
		if calls[i].Phase == "extraction" && rep.AuditSessionID != nil {
			calls[i].SessionID = *rep.AuditSessionID
		}
		if calls[i].SessionID == "" {
			result.Issues = append(result.Issues, "model_session_unresolved")
		}
	}
	if rep.EvidenceCheckJSON != nil {
		var check alertmodel.EvidenceCheck
		if err := json.Unmarshal([]byte(*rep.EvidenceCheckJSON), &check); err != nil {
			return nil, err
		}
		result.Issues = append(result.Issues, check.Issues...)
		result.Issues = append(result.Issues, check.RootCause.Issues...)
		for _, source := range check.Sources {
			if _, err := investigator.ReadEvidenceDetail(context.Background(), sessions, rep, rep.ID, source.ID, 1, true); err != nil {
				result.Issues = append(result.Issues, "source_read_failed: "+source.ID)
				result.Status = "pipeline_error"
			}
		}
		if rep.RootCause == "" && result.Status == "awaiting_review" {
			result.Status = "pipeline_error"
		}
	} else if rep.Status == "ready" {
		result.Issues = append(result.Issues, "evidence_check_missing")
		result.Status = "pipeline_error"
	}
	for name, value := range map[string]any{"result.json": result, "model_calls.json": calls, "sessions.json": transcript, "case.json": c,
		"review.json": review{RunID: result.ID, CaseHash: result.CaseHash, ResultHash: hashValue(result), Verdict: "pending"}} {
		if err := writeJSON(filepath.Join(dir, name), value); err != nil {
			return nil, err
		}
	}
	return result, nil
}
