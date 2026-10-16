package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	managerbizaiops "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	manageraiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	managerapprovalmodel "github.com/ongridio/ongrid/internal/manager/model/approval"
	k8smodel "github.com/ongridio/ongrid/internal/manager/model/k8s"
	managersvcaiops "github.com/ongridio/ongrid/internal/manager/service/aiops"
)

const (
	k8sActionAuditSourcePageSize = 500
	// The UI intentionally presents recent audit history. Keep aggregation
	// bounded even if a long-lived installation accumulates millions of rows;
	// the normalized table/index can replace this compatibility projection in
	// a future migration without changing the HTTP contract.
	k8sActionAuditMaxSourceRows = 5000
)

type k8sActionProposalReader interface {
	ListMutatingProposals(context.Context, managerbizaiops.MutatingProposalFilter) ([]*manageraiopsmodel.MutatingProposal, error)
}

type k8sActionApprovalReader interface {
	ListKind(context.Context, string, int, int) ([]*managerapprovalmodel.Approval, error)
}

// ActionAuditReader merges and sanitizes both Kubernetes approval sources.
type ActionAuditReader struct {
	proposals k8sActionProposalReader
	approvals k8sActionApprovalReader
}

func (r ActionAuditReader) ListK8sActionAudits(ctx context.Context, clusterID uint64, limit, offset int) ([]k8smodel.ActionAuditRecord, int, error) {
	if r.proposals == nil || r.approvals == nil {
		return nil, 0, fmt.Errorf("kubernetes action audit readers are not configured")
	}
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	records := make([]k8smodel.ActionAuditRecord, 0, limit)
	for sourceOffset := 0; sourceOffset < k8sActionAuditMaxSourceRows; sourceOffset += k8sActionAuditSourcePageSize {
		rows, err := r.proposals.ListMutatingProposals(ctx, managerbizaiops.MutatingProposalFilter{
			ToolName: aiopstools.ToolNameExecuteK8sAction,
			Limit:    k8sActionAuditSourcePageSize,
			Offset:   sourceOffset,
		})
		if err != nil {
			return nil, 0, fmt.Errorf("list ReviewGate Kubernetes actions: %w", err)
		}
		for _, row := range rows {
			if record, ok := k8sActionAuditFromProposal(row, clusterID); ok {
				records = append(records, record)
			}
		}
		if len(rows) < k8sActionAuditSourcePageSize {
			break
		}
	}
	for sourceOffset := 0; sourceOffset < k8sActionAuditMaxSourceRows; sourceOffset += k8sActionAuditSourcePageSize {
		rows, err := r.approvals.ListKind(ctx, aiopstools.ToolNameExecuteK8sAction, k8sActionAuditSourcePageSize, sourceOffset)
		if err != nil {
			return nil, 0, fmt.Errorf("list human-approved Kubernetes actions: %w", err)
		}
		for _, row := range rows {
			if record, ok := k8sActionAuditFromApproval(row, clusterID); ok {
				records = append(records, record)
			}
		}
		if len(rows) < k8sActionAuditSourcePageSize {
			break
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID > records[j].ID
		}
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	total := len(records)
	if offset >= total {
		return []k8smodel.ActionAuditRecord{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return records[offset:end], total, nil
}

func k8sActionAuditFromProposal(row *manageraiopsmodel.MutatingProposal, clusterID uint64) (k8smodel.ActionAuditRecord, bool) {
	if row == nil {
		return k8smodel.ActionAuditRecord{}, false
	}
	args, argsJSON, ok := sanitizedK8sActionArgs(row.ArgsJSON)
	if !ok || args.ClusterID != clusterID {
		return k8smodel.ActionAuditRecord{}, false
	}
	status := "pending"
	switch row.Decision {
	case manageraiopsmodel.DecisionApprove:
		status = "approved"
		if row.ExecutedAt != nil {
			status = "executed"
		}
	case manageraiopsmodel.DecisionReject:
		status = "rejected"
	}
	return k8smodel.ActionAuditRecord{
		ID: row.ID, ClusterID: args.ClusterID, SessionID: row.SessionID,
		MessageID: row.MessageID, ToolCallID: row.ToolCallID,
		ToolName: row.ToolName, ArgsJSON: argsJSON, ToolClass: row.ToolClass,
		ApprovalMode: "review_gate", ReviewerAgent: row.ReviewerAgent,
		ReviewerTaskID: row.ReviewerTaskID, Decision: row.Decision, Status: status,
		DecisionReason: stringValue(row.DecisionReason), OperatorUserID: row.OperatorUserID,
		ApproverUserID: row.ApproverUserID, CreatedAt: row.CreatedAt,
		DecidedAt: row.DecidedAt, ExecutedAt: row.ExecutedAt,
	}, true
}

func k8sActionAuditFromApproval(row *managerapprovalmodel.Approval, clusterID uint64) (k8smodel.ActionAuditRecord, bool) {
	if row == nil {
		return k8smodel.ActionAuditRecord{}, false
	}
	var payload managersvcaiops.K8sActionPayload
	if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil || payload.Args.ClusterID != clusterID {
		return k8smodel.ActionAuditRecord{}, false
	}
	payload.Args.PreflightToken = ""
	argsJSON, err := json.Marshal(payload.Args)
	if err != nil {
		return k8smodel.ActionAuditRecord{}, false
	}
	decision := manageraiopsmodel.DecisionPending
	switch row.Status {
	case managerapprovalmodel.StatusRejected:
		decision = manageraiopsmodel.DecisionReject
	case managerapprovalmodel.StatusApproved, managerapprovalmodel.StatusExecuted, managerapprovalmodel.StatusFailed:
		decision = manageraiopsmodel.DecisionApprove
	}
	return k8smodel.ActionAuditRecord{
		ID: row.ID, ClusterID: payload.Args.ClusterID, SessionID: row.SessionID,
		ToolName: aiopstools.ToolNameExecuteK8sAction, ArgsJSON: string(argsJSON), ToolClass: "write",
		ApprovalMode: "human", Decision: decision, Status: row.Status,
		DecisionReason: stringValue(row.Reason), OperatorUserID: row.ProposedBy,
		ApproverUserID: row.ApprovedBy, CreatedAt: row.CreatedAt,
		DecidedAt: row.DecidedAt, ExecutedAt: row.ExecutedAt,
	}, true
}

func sanitizedK8sActionArgs(raw string) (aiopstools.ExecuteK8sActionArgs, string, bool) {
	var args aiopstools.ExecuteK8sActionArgs
	if err := json.Unmarshal([]byte(raw), &args); err != nil || args.ClusterID == 0 {
		return aiopstools.ExecuteK8sActionArgs{}, "", false
	}
	args.PreflightToken = ""
	encoded, err := json.Marshal(args)
	if err != nil {
		return aiopstools.ExecuteK8sActionArgs{}, "", false
	}
	return args, string(encoded), true
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func NewActionAuditReader(proposals k8sActionProposalReader, approvals k8sActionApprovalReader) ActionAuditReader {
	return ActionAuditReader{proposals: proposals, approvals: approvals}
}
