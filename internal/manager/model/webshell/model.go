// Package webshell holds the audit row schema for WebSSH sessions.
// Every session emits one INSERT on open and one UPDATE on close, with
// byte counters and the termination cause. Passwords NEVER land here.
package webshell

import "time"

// Termination reasons. Kept as constants so handlers don't drift.
const (
	TerminatedByUser          = "user"          // browser closed
	TerminatedByIdle          = "idle"          // no input >N minutes
	TerminatedByDisconnect    = "disconnect"    // tunnel / WS dropped
	TerminatedByAdminKill     = "admin_kill"    // admin kicked the session
	TerminatedBySSHAuthFail   = "ssh_auth_fail" // sshd refused creds
	TerminatedBySSHHostKey    = "ssh_host_key"  // unknown / changed host key
	TerminatedBySSHExit       = "ssh_exit"      // user typed exit / shell ended
	TerminatedByDeviceOffline = "device_offline"
)

// Session is one audit row for an opened WebSSH session.
type Session struct {
	ID              string     `gorm:"primaryKey;size:64"`
	OngridUserID    uint64     `gorm:"not null;column:ongrid_user_id;index"`
	SSHUser         string     `gorm:"size:64;not null;column:ssh_user"`
	SSHPort         uint16     `gorm:"not null;default:22;column:ssh_port"`
	CredentialID    *uint64    `gorm:"column:credential_id"`
	HostFingerprint string     `gorm:"size:128;column:host_fingerprint"`
	DeviceID        uint64     `gorm:"not null;column:device_id;index"`
	EdgeID          uint64     `gorm:"not null;column:edge_id"`
	ClientIP        string     `gorm:"size:64;column:client_ip"`
	StartedAt       time.Time  `gorm:"not null;column:started_at"`
	EndedAt         *time.Time `gorm:"column:ended_at"`
	BytesStdin      uint64     `gorm:"not null;default:0;column:bytes_stdin"`
	BytesStdout     uint64     `gorm:"not null;default:0;column:bytes_stdout"`
	ExitCode        int        `gorm:"not null;default:0;column:exit_code"`
	TerminatedBy    string     `gorm:"size:32;column:terminated_by"`
}

// TableName pins the table name.
func (Session) TableName() string { return "webshell_sessions" }

// Credential is a personal, device-bound SSH login. PasswordCiphertext is
// write-only at the API boundary and encrypted before it reaches the DB.
type Credential struct {
	ID                 uint64 `gorm:"primaryKey;autoIncrement"`
	OngridUserID       uint64 `gorm:"not null;uniqueIndex:uk_webshell_credential_owner_label,priority:1"`
	DeviceID           uint64 `gorm:"not null;uniqueIndex:uk_webshell_credential_owner_label,priority:2;index"`
	Label              string `gorm:"size:64;not null;uniqueIndex:uk_webshell_credential_owner_label,priority:3"`
	SSHUser            string `gorm:"size:64;not null"`
	SSHPort            uint16 `gorm:"not null;default:22"`
	PasswordCiphertext string `gorm:"type:text;not null" json:"-"`
	LastUsedAt         *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (Credential) TableName() string { return "webshell_credentials" }

// KnownHost pins one SSH host key per user/device/port (TOFU).
type KnownHost struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	OngridUserID uint64 `gorm:"not null;uniqueIndex:uk_webshell_known_host,priority:1"`
	DeviceID     uint64 `gorm:"not null;uniqueIndex:uk_webshell_known_host,priority:2"`
	SSHPort      uint16 `gorm:"not null;uniqueIndex:uk_webshell_known_host,priority:3"`
	Fingerprint  string `gorm:"size:128;not null"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (KnownHost) TableName() string { return "webshell_known_hosts" }
