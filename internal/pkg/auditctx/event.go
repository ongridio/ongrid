// Package auditctx 提供跨域审计事件契约，不依赖任何业务或存储包。
package auditctx

type Event struct {
	// Actor — filled by the middleware from JWT claims, by handlers
	// for failed-auth or anon paths.
	UserID    *uint64
	UserEmail string
	Role      string
	IP        string
	UserAgent string
	RequestID string

	// Action — must be one of the canonical model.Action* constants.
	Action       string
	ResourceType string
	ResourceID   string
	ResourceName string

	// Outcome.
	Status       string // success|failure|denied
	ErrorCode    string
	ErrorMessage string

	// Free-form structured detail. Caller is responsible for redacting
	// secrets BEFORE passing in (LLM keys, passwords, tokens). Pass a
	// map or struct; the usecase JSON-encodes.
	Payload any
}

// Status enum — bounded set used by both the writer and the list filter.
const (
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusDenied  = "denied"
)

// Canonical actions. The naming convention is verb_resource in
// snake_case — kept deliberately small. Sub-flavours (enable vs disable,
// role-change vs password-reset, single vs bulk) live in the payload,
// not the action name, so the UI's action dropdown stays short and the
// audit row's payload tells the operator exactly what changed.
//
// Operator feedback 2026-05-20: the prior 38-action enum mixed
// CRUD verbs with state-transition flavours (rule_enable / rule_disable)
// and per-setting names (llm_key_set / grafana_config_set), which made
// the action filter sprawl. The current set collapses those to the
// underlying verb + a payload that carries the specifics.
const (
	// Note 2026-05-21: auth_login / auth_logout / audit_view dropped.
	// Operator flagged read-only / session-bookkeeping rows as drowning
	// out the mutation signal. We keep auth_login_failed because a
	// brute-force pattern still wants to be visible.
	ActionAuthLoginFailed = "auth_login_failed"

	// User CRUD. role / password / profile field changes all surface as
	// user_update — the payload field carries which field flipped.
	ActionUserCreate = "user_create"
	ActionUserUpdate = "user_update"
	ActionUserDelete = "user_delete"
	ActionUserExport = "user_export"

	// Device CRUD. enable / disable / bulk-delete fold into update /
	// delete + a payload (e.g. {"enabled": false, "count": 3}).
	ActionDeviceUpdate = "device_update"
	ActionDeviceDelete = "device_delete"

	// Alert rule CRUD. enable / disable fold into update with payload
	// {"enabled": <bool>}.
	ActionRuleCreate = "rule_create"
	ActionRuleUpdate = "rule_update"
	ActionRuleDelete = "rule_delete"

	ActionIncidentAck     = "incident_ack"
	ActionIncidentResolve = "incident_resolve"
	ActionIncidentSilence = "incident_silence"

	// Settings umbrella. LLM key / Grafana config / SSH key writes all
	// land here; payload carries {"key": "...", "category": "..."}.
	// Sensitive values are redacted upstream.
	ActionSettingUpdate = "setting_update"
	ActionSettingDelete = "setting_delete"

	ActionChannelCreate = "channel_create"
	ActionChannelUpdate = "channel_update"
	ActionChannelDelete = "channel_delete"

	ActionRepoCreate = "repo_create"
	ActionRepoUpdate = "repo_update"
	ActionRepoDelete = "repo_delete"
	ActionRepoSync   = "repo_sync"

	ActionSkillInstall   = "skill_install"
	ActionSkillUninstall = "skill_uninstall"
)

// ResourceType buckets used in the resource_type column. Same flat-list
// convention as Action — group in the UI, not the data.
const (
	ResourceUser     = "user"
	ResourceDevice   = "device"
	ResourceIncident = "incident"
	ResourceSetting  = "setting"
	ResourceRule     = "rule"
	ResourceChannel  = "channel"
	ResourceRepo     = "repo"
	ResourceSkill    = "skill"
	ResourceLLM      = "llm"
	ResourceGitKey   = "git_ssh_key"
	ResourceGrafana  = "grafana"
	ResourceRAG      = "rag"
	ResourceAudit    = "audit"
	ResourceAuth     = "auth"
)
