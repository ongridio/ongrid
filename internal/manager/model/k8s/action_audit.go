package k8s

import (
	"time"
)

// ActionAuditRecord is the sanitized cross-kernel read model consumed by the
// Kubernetes Actions page. ArgsJSON must not contain one-time credentials such
// as execute_k8s_action.preflight_token.
type ActionAuditRecord struct {
	ID             string     `json:"id"`
	ClusterID      uint64     `json:"cluster_id"`
	SessionID      string     `json:"session_id,omitempty"`
	MessageID      *string    `json:"message_id,omitempty"`
	ToolCallID     *string    `json:"tool_call_id,omitempty"`
	ToolName       string     `json:"tool_name"`
	ArgsJSON       string     `json:"args_json"`
	ToolClass      string     `json:"tool_class"`
	ApprovalMode   string     `json:"approval_mode"`
	ReviewerAgent  string     `json:"reviewer_agent,omitempty"`
	ReviewerTaskID string     `json:"reviewer_task_id,omitempty"`
	Decision       string     `json:"decision"`
	Status         string     `json:"status"`
	DecisionReason string     `json:"decision_reason,omitempty"`
	OperatorUserID uint64     `json:"operator_user_id"`
	ApproverUserID *uint64    `json:"approver_user_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	ExecutedAt     *time.Time `json:"executed_at,omitempty"`
}
