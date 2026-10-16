package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	aiopschatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	aiopstoolsdec "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	managerbizapproval "github.com/ongridio/ongrid/internal/manager/biz/approval"
)

const GenericToolApprovalKind = "agent_tool"

type genericAgentToolApprovalPayload struct {
	ExecutionToken string `json:"execution_token"`
	ToolName       string `json:"tool_name"`
	ArgsJSON       string `json:"args_json"`
	Summary        string `json:"summary"`
}

// HumanApprovalBroker is the composition-root bridge between the generic
// tool decorator and the durable approval inbox. Each pending proposal owns
// an in-memory, single-use executor that closes over the exact wrapped tool,
// arguments and request context. A manager restart loses that capability and
// therefore fails closed; it can never execute an unapproved reconstruction.
type pendingToolExecution struct {
	version aiopschatruntime.Version
	execute aiopstoolsdec.HumanApprovalExecutor
}

type HumanApprovalBroker struct {
	mu      sync.Mutex
	uc      *managerbizapproval.Usecase
	pending map[string]pendingToolExecution
}

func NewHumanApprovalBroker() *HumanApprovalBroker {
	return &HumanApprovalBroker{pending: make(map[string]pendingToolExecution)}
}

func (b *HumanApprovalBroker) SetUsecase(uc *managerbizapproval.Usecase) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.uc = uc
}

func (b *HumanApprovalBroker) ProposeAndAwait(ctx context.Context, req aiopstoolsdec.HumanApprovalRequest, execute aiopstoolsdec.HumanApprovalExecutor) (string, error) {
	if execute == nil {
		return "", fmt.Errorf("human approval: executor is required")
	}
	token := uuid.NewString()
	b.mu.Lock()
	uc := b.uc
	if uc != nil {
		b.pending[token] = pendingToolExecution{version: aiopschatruntime.VersionFromContext(ctx), execute: execute}
	}
	b.mu.Unlock()
	if uc == nil {
		return "", fmt.Errorf("human approval: approval usecase not wired")
	}
	cleanup := func() {
		b.mu.Lock()
		delete(b.pending, token)
		b.mu.Unlock()
	}
	a, err := uc.Propose(ctx, managerbizapproval.ProposeInput{
		RuntimeVersion: string(aiopschatruntime.VersionFromContext(ctx)),
		Kind:           GenericToolApprovalKind,
		Title:          truncateApprovalTitle(req.ToolName + " confirmation"),
		Summary:        req.Summary,
		Payload:        genericAgentToolApprovalPayload{ExecutionToken: token, ToolName: req.ToolName, ArgsJSON: req.ArgsJSON, Summary: req.Summary},
		Source:         "agent",
		SessionID:      req.SessionID,
		ProposedBy:     req.UserID,
	})
	if err != nil {
		cleanup()
		return "", err
	}
	if emit := aiopschatruntime.EmitFromContext(ctx); emit != nil {
		emit(aiopschatruntime.Event{
			Type: aiopschatruntime.EventApprovalPending,
			Approval: &aiopschatruntime.ApprovalPending{
				ApprovalID: a.ID,
				ToolCallID: req.ToolCallID,
				Kind:       GenericToolApprovalKind,
				ToolName:   req.ToolName,
				Command:    req.Summary,
			},
		})
	}

	deadline := time.Now().Add(ApprovalWaitTimeout)
	ticker := time.NewTicker(approvalPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cleanup()
			if err := invalidateApproval(ctx, uc, a.ID, "The originating request was cancelled; submit a new request."); err != nil {
				return "", err
			}
			return `{"status":"cancelled","message":"The approval wait was interrupted. Check the approval record for the final execution status."}`, nil
		case <-ticker.C:
			row, getErr := uc.Get(ctx, a.ID)
			if getErr != nil {
				continue
			}
			switch row.Status {
			case "executed", "failed":
				cleanup()
				if row.ResultJSON != nil {
					return *row.ResultJSON, nil
				}
				return fmt.Sprintf(`{"status":%q}`, row.Status), nil
			case "rejected":
				cleanup()
				return `{"status":"rejected","message":"The user rejected this action; it was not run. Do not retry it without new instructions."}`, nil
			}
			if time.Now().After(deadline) {
				cleanup()
				if err := invalidateApproval(ctx, uc, a.ID, "The approval wait expired; submit a new request."); err != nil {
					return "", err
				}
				return `{"status":"timeout","message":"The approval wait expired. Check the approval record for the final execution status."}`, nil
			}
		}
	}
}

func (b *HumanApprovalBroker) Execute(ctx context.Context, payloadJSON string) (string, error) {
	var payload genericAgentToolApprovalPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return "", fmt.Errorf("decode generic tool approval: %w", err)
	}
	b.mu.Lock()
	pending := b.pending[payload.ExecutionToken]
	delete(b.pending, payload.ExecutionToken)
	b.mu.Unlock()
	if pending.execute == nil {
		return "", fmt.Errorf("approved tool execution is no longer available; retry the request")
	}
	if pending.version != aiopschatruntime.VersionFromContext(ctx) {
		return "", fmt.Errorf("approved tool runtime mismatch; submit a new request")
	}
	return pending.execute(ctx)
}

func truncateApprovalTitle(title string) string {
	if len(title) > 100 {
		return title[:100] + "…"
	}
	return title
}

func invalidateApproval(ctx context.Context, uc *managerbizapproval.Usecase, id, reason string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return uc.InvalidatePending(cleanupCtx, id, reason)
}
