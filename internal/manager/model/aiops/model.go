// Package aiops holds persistence entities for the manager/aiops sub-domain.
// Post-pivot rows are scoped by user_id, not org_id.
package aiops

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Role / Status constants for ChatMessage.Role and ChatToolCall.Status.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
	RoleSystem    = "system"

	StatusPending = "pending"
	StatusSuccess = "success"
	StatusError   = "error"
	StatusTimeout = "timeout"
)

// Session is a multi-turn conversation with the agent.
//
// IDs are UUIDs (string, 36 chars including dashes) so the routes are
// not enumerable and clients can use the same id when navigating before
// the server has acknowledged a write. UserID stays uint64 (FK to the
// users table). ScopeJSON is an optional JSON array of edge names used
// as a whitelist when the agent dispatches tool calls; a nil pointer
// means "no restriction".
//
// Worker / sub-agent audit columns (/ — coordinator/
// worker multi-agent path). When this row represents a sub-agent run
// spawned by the coordinator:
//
//	AgentID — the persona name (frontmatter `name`) that ran the
//	                  worker. nil for a coordinator/user-driven session.
//	ParentSessionID — the coordinator session id that requested the
//	                  spawn. nil for top-level sessions.
//	Background — true when the worker was a fire-and-forget spawn
//	                  (SpawnRequest.Background). Defaults to false so
//	                  pre-existing rows are unambiguous post-migration.
//
// Both nullable columns are indexed so the SPA worker-tree view can
// fan out from a parent session id in O(log N), and audit queries can
// filter by persona.
type Session struct {
	ID              string  `gorm:"primaryKey;type:char(36);column:id"`
	UserID          uint64  `gorm:"index;not null;column:user_id"`
	Title           string  `gorm:"size:256;not null"`
	ScopeJSON       *string `gorm:"type:text;column:scope_json"`
	AgentID         *string `gorm:"size:128;index;column:agent_id"`
	ParentSessionID *string `gorm:"size:36;index;column:parent_session_id"`
	Background      bool    `gorm:"not null;default:false;column:background"`
	// RelatedIncidentID links a chat session back to the alert incident
	// that birthed it (set by the IncidentDetail "深入诊断" button).
	// Nullable: ad-hoc chats from the home page have no incident. Indexed
	// so the per-incident agent-timeline panel can list its sessions in
	// O(log N).
	RelatedIncidentID *uint64    `gorm:"index;column:related_incident_id"`
	// Kind discriminates a user-initiated conversation from a system-
	// spawned investigation transcript. The /chat listing filters to
	// kind='user' so auto-spawned investigation sessions (one per
	// alert RCA) don't drown out the operator's own chats. The
	// `investigation_reports.audit_session_id` FK points at sessions
	// with kind='investigation'.
	Kind              string     `gorm:"size:16;not null;default:'user';column:kind;index:idx_session_kind_created,priority:1"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ClosedAt          *time.Time `gorm:"column:closed_at"`
}

const (
	SessionKindUser          = "user"
	SessionKindInvestigation = "investigation"
)

// TableName pins the SQLite table name.
func (Session) TableName() string { return "chat_sessions" }

// BeforeCreate fills ID with a fresh UUIDv4 when the caller didn't pre-set
// one. Keeps id-generation in one place so repos / tests don't have to
// remember to assign manually.
func (s *Session) BeforeCreate(*gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	return nil
}

// Message is one turn in a session.
//
// IDs are UUIDs (matching Session). Content is a *string so an assistant
// message with only tool_calls (and no textual reply) can be represented
// without conflating empty-string with absent. ToolCallID / ToolName are
// non-nil on role=tool messages.
type Message struct {
	ID               string  `gorm:"primaryKey;type:char(36);column:id"`
	SessionID        string  `gorm:"index:idx_session_msg,priority:1;type:char(36);not null;column:session_id"`
	Role             string  `gorm:"size:16;not null;check:role IN ('user','assistant','tool','system')"`
	Content          *string `gorm:"type:text"`
	ToolCallID       *string `gorm:"size:64;column:tool_call_id"`
	ToolName         *string `gorm:"size:64;column:tool_name"`
	// Model is the LLM model id that produced this message — only set on
	// role=assistant rows. Lets the SPA show per-message provenance ("the
	// answer above came from glm-4-plus; the answer below from opus") and
	// keeps the audit trail honest when the default routing switches
	// mid-session. Nullable for back-compat with rows written before the
	// column existed.
	Model            *string `gorm:"size:64"`
	PromptTokens     *int    `gorm:"column:prompt_tokens"`
	CompletionTokens *int    `gorm:"column:completion_tokens"`
	CreatedAt        time.Time

	// ToolCalls is the set of chat_tool_calls rows attached to this
	// message. Populated by SessionRepo.ListMessages so the agent's
	// buildMessages can replay role=assistant turns with content=NULL
	// but tool_calls populated — without it the LLM history sends an
	// orphan role=tool message and strict providers (DeepSeek v4+)
	// reject the request with "tool must follow tool_calls". Transient:
	// `gorm:"-"` keeps it out of the chat_messages schema.
	ToolCalls []ToolCall `gorm:"-"`
}

// TableName pins the SQLite table name.
func (Message) TableName() string { return "chat_messages" }

// BeforeCreate auto-fills ID with a UUIDv4 when missing.
func (m *Message) BeforeCreate(*gorm.DB) error {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	return nil
}

// ToolCall is one invocation requested by the assistant. ID and MessageID
// are UUIDs; DeviceID stays uint64 because the devices table is keyed by
// an auto-increment id. Renamed from EdgeID in May 2026 (entity split);
// the underlying chat_tool_calls.device_id column matches the legacy
// edge_id 1:1 because the migration reuses the integer.
type ToolCall struct {
	ID            string  `gorm:"primaryKey;type:char(36);column:id"`
	MessageID     string  `gorm:"index;type:char(36);not null;column:message_id"`
	ToolName      string  `gorm:"size:64;not null;column:tool_name"`
	ArgumentsJSON string  `gorm:"type:text;not null;column:arguments_json"`
	ResultJSON    *string `gorm:"type:text;column:result_json"`
	Status        string  `gorm:"size:16;not null;default:pending;check:status IN ('pending','success','error','timeout')"`
	Error         *string `gorm:"size:512;column:error"`
	StartedAt     time.Time
	EndedAt       *time.Time `gorm:"column:ended_at"`
	DeviceID      *uint64    `gorm:"column:device_id"`
	// LLMCallID is the call id the LLM assigned to this tool invocation
	// (e.g. "call_00_j5VjpXHsPpCS0JXjCv2O5578"). Stored so history
	// replay can emit role=assistant {content:null, tool_calls:[{id,...}]}
	// and the subsequent role=tool {tool_call_id:...} stays paired —
	// strict providers (DeepSeek v4+) reject orphan tool messages with
	// HTTP 400. NULL on rows written before this column existed; the
	// replay path falls back to pairing by order for those.
	LLMCallID *string `gorm:"size:64;column:llm_call_id"`
	CreatedAt time.Time
}

// TableName pins the SQLite table name.
func (ToolCall) TableName() string { return "chat_tool_calls" }

// BeforeCreate auto-fills ID with a UUIDv4 when missing.
func (t *ToolCall) BeforeCreate(*gorm.DB) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	return nil
}
