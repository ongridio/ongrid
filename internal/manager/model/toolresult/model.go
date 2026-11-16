package toolresult

import (
	"errors"
	"time"
)

var ErrPending = errors.New("tool result awaits metadata recovery")

// Result owns the immutable payload. Messages and approvals contain bounded
// views; they never serve as the authoritative copy of an external result.
type Result struct {
	ID              string    `gorm:"column:id;primaryKey;type:char(36)" json:"id"`
	ExecutionKey    string    `gorm:"column:execution_key;type:char(64);uniqueIndex;not null" json:"-"`
	SessionID       string    `gorm:"column:session_id;type:char(36);index;not null" json:"session_id"`
	UserID          uint64    `gorm:"column:user_id;not null" json:"-"`
	ToolName        string    `gorm:"column:tool_name;size:256;not null" json:"tool_name"`
	ExecutionStatus string    `gorm:"column:execution_status;size:16;not null" json:"execution_status"`
	CaptureStatus   string    `gorm:"column:capture_status;size:16;not null" json:"capture_status"`
	CaptureReason   string    `gorm:"column:capture_reason;size:64" json:"capture_reason,omitempty"`
	StorageStatus   string    `gorm:"column:storage_status;size:16;not null" json:"storage_status"`
	ContentType     string    `gorm:"column:content_type;size:64;not null" json:"content_type"`
	CapturedBytes   int64     `gorm:"column:captured_bytes;not null" json:"captured_bytes"`
	ExpectedBytes   int64     `gorm:"column:expected_bytes;not null" json:"expected_bytes"`
	StoredBytes     int64     `gorm:"column:stored_bytes;not null" json:"stored_bytes"`
	SHA256          string    `gorm:"column:sha256;type:char(64);not null" json:"sha256"`
	StorageKey      string    `gorm:"column:storage_key;size:256" json:"-"`
	Encoding        string    `gorm:"column:encoding;size:32;not null" json:"encoding"`
	InlineBody      []byte    `gorm:"column:inline_body;type:blob" json:"-"`
	ManifestJSON    string    `gorm:"column:manifest_json;type:text;not null" json:"-"`
	CreatedAt       time.Time `gorm:"column:created_at" json:"created_at"`
	ExpiresAt       time.Time `gorm:"column:expires_at;index" json:"expires_at"`
}

func (Result) TableName() string { return "tool_results" }

// Reference is a server-created association, never inferred from tool text.
type Reference struct {
	Kind     string `gorm:"column:kind;size:32;primaryKey" json:"kind"`
	ID       string `gorm:"column:ref_id;size:128;primaryKey" json:"ref_id"`
	ResultID string `gorm:"column:result_id;type:char(36);index;not null" json:"result_id"`
	// A pinned approval or report protects its result until this time.
	RetainUntil *time.Time `gorm:"column:retain_until;index" json:"retain_until,omitempty"`
}

func (Reference) TableName() string { return "tool_result_refs" }
