package model

import "time"

// IntegrationToken stores only the digest of the random credential.
type IntegrationToken struct {
	ID          uint64     `gorm:"primaryKey;autoIncrement" json:"-"`
	PublicID    string     `gorm:"size:32;not null;uniqueIndex" json:"public_id"`
	OwnerUserID uint64     `gorm:"not null;index" json:"-"`
	Name        string     `gorm:"size:128;not null" json:"name"`
	SecretHash  []byte     `gorm:"size:32;not null" json:"-"`
	Audience    string     `gorm:"size:32;not null" json:"-"`
	ScopesJSON  string     `gorm:"type:text;not null" json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	// Keep legacy revocations invalid when upgrading to token deletion.
	RevokedAt *time.Time `json:"-"`
}

func (IntegrationToken) TableName() string { return "iam_integration_tokens" }
