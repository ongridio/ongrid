package imbridge

import "time"

// ApprovalTarget freezes the original conversation for one Ongrid session.
// SecretContext is encrypted; it is only needed for expiring DingTalk webhooks.
type ApprovalTarget struct {
	ConnectionIdentity string `gorm:"size:64"`
	DeliverySpace      string `gorm:"size:255"`
	CallbackChatID     string `gorm:"size:128"`
	PlatformThreadID   string `gorm:"size:128"`
	SessionID          string `gorm:"primaryKey;size:64"`
	AppID              uint64 `gorm:"index;not null"`
	ThreadID           uint64
	Provider           string `gorm:"size:16"`
	AppIdentifier      string `gorm:"size:128"`
	ChatID             string `gorm:"size:128;not null"`
	ReceiveIDType      string `gorm:"size:32"`
	Locale             string `gorm:"size:8"`
	SecretContext      string `gorm:"type:text"`
	ExpiresAt          *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (ApprovalTarget) TableName() string { return "im_approval_targets" }

// ApprovalNotification is a delivery projection, never an approval decision.
type ApprovalNotification struct {
	NativeCard      bool
	RefreshAt       *time.Time
	ActionKind      string `gorm:"size:64"`
	ApprovalID      string `gorm:"primaryKey;size:64"`
	SessionID       string `gorm:"size:64;index;not null"`
	MessageID       string `gorm:"size:255"`
	DeliveredStatus string `gorm:"size:16"`
	Attempts        int
	NextAt          time.Time `gorm:"index"`
	LeaseOwner      string    `gorm:"size:64"`
	LeaseUntil      *time.Time
	Done            bool   `gorm:"index"`
	LastError       string `gorm:"size:64"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (ApprovalNotification) TableName() string { return "im_approval_notifications" }

// DeliveryGate 将平台 Retry-After 应用于同一机器人所有通知，而不是只延后单条消息。
type DeliveryGate struct {
	AppID     uint64 `gorm:"primaryKey"`
	NotBefore time.Time
}

func (DeliveryGate) TableName() string { return "im_delivery_gates" }
