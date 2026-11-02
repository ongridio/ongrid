package imbridge

import "time"

// BindingChallenge 两端确认的短期证明；平台身份加密，随机口令仅存摘要。
type BindingChallenge struct {
	ID            string    `gorm:"primaryKey;size:64"`
	CodeHash      string    `gorm:"size:64;uniqueIndex"`
	UserID        uint64    `gorm:"index"`
	AppID         uint64    `gorm:"index"`
	AppIdentity   string    `gorm:"size:64"`
	SubjectHash   string    `gorm:"size:64"`
	SubjectCipher string    `gorm:"type:text"`
	ExpiresAt     time.Time `gorm:"index"`
	Consumed      bool
	CreatedAt     time.Time
}

func (BindingChallenge) TableName() string { return "im_binding_challenges" }

type IdentityBinding struct {
	ID            string `gorm:"primaryKey;size:64"`
	UserID        uint64 `gorm:"index"`
	AppID         uint64 `gorm:"uniqueIndex:uk_im_subject,priority:1"`
	AppIdentity   string `gorm:"size:64"`
	SubjectHash   string `gorm:"size:64;uniqueIndex:uk_im_subject,priority:2"`
	SubjectCipher string `gorm:"type:text"`
	CreatedAt     time.Time
}

func (IdentityBinding) TableName() string { return "im_identity_bindings" }

// ActionToken 仅授权提交审批意图，不能直接执行；令牌绑定原消息和目标。
type ActionToken struct {
	Hash        string `gorm:"primaryKey;size:64"`
	ApprovalID  string `gorm:"size:64;index"`
	SessionID   string `gorm:"size:64"`
	AppID       uint64
	AppIdentity string    `gorm:"size:64"`
	ChatID      string    `gorm:"size:128"`
	MessageID   string    `gorm:"size:255"`
	Action      string    `gorm:"size:16"`
	ExpiresAt   time.Time `gorm:"index"`
	CreatedAt   time.Time
}

func (ActionToken) TableName() string { return "im_approval_action_tokens" }

// DecisionCommand 是回调收件箱而非审批状态机。running 不自动重放，避免不确定副作用。
type DecisionCommand struct {
	Provider      string `gorm:"size:16"`
	ChatID        string `gorm:"size:128"`
	MessageID     string `gorm:"size:255"`
	SubjectCipher string `gorm:"type:text"`
	ApprovalID    string `gorm:"primaryKey;size:64"`
	TokenHash     string `gorm:"size:64"`
	BindingID     string `gorm:"size:64"`
	UserID        uint64
	AppID         uint64
	AppIdentity   string `gorm:"size:64"`
	Action        string `gorm:"size:16"`
	State         string `gorm:"size:16;index"`
	Outcome       string `gorm:"size:64"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (DecisionCommand) TableName() string { return "im_approval_commands" }
