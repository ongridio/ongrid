package externalmcp

import "time"

// ExecutionLock prevents overlapping external writes by token or target.
type ExecutionLock struct {
	Key         string    `gorm:"primaryKey;size:255"`
	OperationID string    `gorm:"size:36;not null;index"`
	ExpiresAt   time.Time `gorm:"not null;index"`
}

func (ExecutionLock) TableName() string { return "external_mcp_execution_locks" }

// Preflight persists one-use Kubernetes grants across manager replicas.
type Preflight struct {
	ID          string    `gorm:"primaryKey;size:64"`
	Fingerprint []byte    `gorm:"size:32;not null"`
	OwnerID     uint64    `gorm:"not null"`
	TokenID     string    `gorm:"size:32;not null"`
	ExpiresAt   time.Time `gorm:"not null;index"`
	ConsumedAt  *time.Time
}

func (Preflight) TableName() string { return "external_mcp_preflights" }
