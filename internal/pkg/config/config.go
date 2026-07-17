// Package config loads runtime configuration from environment variables.
//
// MVP uses plain os.Getenv + sensible defaults (no YAML/viper dep yet).
// See .env.example at the repo root for the full list of variables.
//
// Field grouping reflects the post-pivot stack: HTTP / metrics, DB (MySQL
// default, SQLite opt-in), JWT (iam), OpenAI (llm), Admin bootstrap (cloud
// only), Edge (ongrid-edge).
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the top-level runtime configuration shared by ongrid and
// ongrid-edge. Fields unused by one binary are simply ignored.
type Config struct {
	HTTPAddr    string
	MetricsAddr string
	TunnelAddr  string

	// PublicURL is the canonical https URL operators use to reach this
	// manager from outside the docker network. Used to compose data
	// plane endpoints handed out to edges:
	// e.g. logs plugin pushes to PublicURL + "/loki/api/v1/push".
	// env: ONGRID_PUBLIC_URL (no default — empty disables data plane
	// plugin endpoints, which then prevents edges from being able to
	// push logs/traces).
	PublicURL string

	// K8sEventRetention caps how long manager keeps Kubernetes Events in its
	// local snapshot store. The event's Kubernetes timestamp is used before
	// last_seen_at so old Events do not live forever just because the API still
	// returns them.
	// env: ONGRID_K8S_EVENT_RETENTION; default 24h.
	K8sEventRetention time.Duration
	// K8sEventMaxPerCluster caps retained Kubernetes Events per cluster.
	// env: ONGRID_K8S_EVENT_MAX_PER_CLUSTER; default 5000.
	K8sEventMaxPerCluster int
	// K8sEventCleanupInterval controls how often the manager applies the two
	// Kubernetes Event retention guards.
	// env: ONGRID_K8S_EVENT_CLEANUP_INTERVAL; default 1h.
	K8sEventCleanupInterval time.Duration

	DB             DBConfig
	JWT            JWTConfig
	OpenAI         OpenAIConfig
	LLM            LLMConfig
	Admin          AdminConfig
	Edge           EdgeConfig
	FrontierClient FrontierClientConfig
	Prom           PromConfig
	Grafana        GrafanaConfig
	Notification   NotificationConfig
	Alert          AlertConfig
	Logs           LogsConfig
	Traces         TracesConfig
	Skills         SkillsConfig
}

// SkillsConfig wires the manager-side subprocess skill loader. The
// loader scans each directory for skill.json manifests and registers
// them as ScopeManager SubprocessSkills (see internal/skill/loader.go).
//
// Default ExternalDirs is empty unless ONGRID_SKILLS_EXTERNAL_DIRS is
// set; on the deployed image the compose env block points it at
// /etc/ongrid/skills.
type SkillsConfig struct {
	// ExternalDirs is the comma-/colon-separated list of allowlist
	// roots the loader walks at startup. Each must be an absolute
	// path; relative or non-existent entries are skipped with a log
	// line.
	// env: ONGRID_SKILLS_EXTERNAL_DIRS; default empty.
	ExternalDirs []string
}

// LogsConfig wires the manager-side Loki query proxy. When
// URL is empty the Logs page returns 503 and the SPA shows a "logs
// disabled" state. Built-in deployments default URL to
// http://loki:3100 via the manager docker-compose env block.
type LogsConfig struct {
	// URL is the Loki API root the manager talks to for query_range /
	// labels / label values. env: ONGRID_LOG_QUERY_URL; defaults from
	// docker-compose env block to http://loki:3100.
	URL string
}

// TracesConfig wires the manager-side Tempo query proxy. Mirrors
// LogsConfig — same role for the trace signal. When URL is empty the
// Traces page returns 503 and the SPA shows a "traces disabled" state.
// Built-in deployments default URL to http://tempo:3200 via the manager
// docker-compose env block.
type TracesConfig struct {
	// URL is the Tempo HTTP listener root the manager talks to for
	// /api/search, /api/traces/<id>, and /api/search/tag/<tag>/values.
	// env: ONGRID_TRACE_QUERY_URL; defaults from docker-compose env
	// block to http://tempo:3200.
	URL string
}

// GrafanaConfig holds first-boot seed values for the Grafana integration.
// Runtime values (root URL, SA token) are read from system_settings and
// can be edited live in the UI; this struct only sets defaults applied
// when the corresponding rows are missing on first start.
type GrafanaConfig struct {
	// InternalRootURL is the URL the manager uses to reach Grafana via
	// the docker network. The compose-shipped Grafana lives at
	// http://grafana:3000/grafana (the /grafana sub-path comes from
	// GF_SERVER_SERVE_FROM_SUB_PATH=true). Operators pointing at a
	// real external Grafana override this in the UI on first run.
	// env: ONGRID_GRAFANA_INTERNAL_URL; default http://grafana:3000/grafana.
	InternalRootURL string

	// BootstrapUser / BootstrapPassword are the admin credentials the
	// manager uses ONCE at first boot to auto-create the ongrid SA +
	// token via the Grafana admin API. Only set for the embedded Grafana
	// shipped in our compose; for a customer-supplied external Grafana
	// these stay empty and bootstrap is skipped — the operator pastes
	// a manually-created token into the UI.
	// env: ONGRID_GRAFANA_BOOTSTRAP_USER (default admin) and
	//      ONGRID_GRAFANA_BOOTSTRAP_PASSWORD (no default — empty disables).
	BootstrapUser     string
	BootstrapPassword string

	// TLSInsecure skips cert verification when calling Grafana from the
	// manager. Symmetric with PromConfig.TLSInsecure — needed when
	// pointing at an external Grafana behind a self-signed cert.
	// env: ONGRID_GRAFANA_TLS_INSECURE; default false.
	TLSInsecure bool
}

// NotificationConfig controls outbound notifications for alerts, scheduled
// tasks, and future AIOps proactive recommendations.
//
// The concrete delivery adapters live in internal/pkg/notify. Keeping only
// plain configuration here avoids coupling config loading to any transport
// client.
type NotificationConfig struct {
	// Enabled gates all outbound notification sends.
	// env: ONGRID_NOTIFY_ENABLED; default true (configured channels deliver
	// out of the box; set false to mute all notifications).
	Enabled bool
	// DefaultChannels is the ordered channel name list used when a caller
	// does not specify explicit destinations.
	// env: ONGRID_NOTIFY_DEFAULT_CHANNELS; comma-separated; default empty.
	DefaultChannels []string
	// Timeout is the per-channel send timeout.
	// env: ONGRID_NOTIFY_TIMEOUT; default 10s.
	Timeout time.Duration

	// "log" channel was removed in 2026-05; alert_events table is the
	// authoritative audit. Webhook / IM channels remain.
	Webhook  NotifyWebhookConfig
	Slack    NotifyWebhookConfig
	Feishu   NotifyWebhookConfig
	DingTalk NotifyWebhookConfig
}

// NotifyWebhookConfig covers webhook-style channels. Slack, Feishu, and
// DingTalk use different payload shapes, but the endpoint/secret material
// they need is the same at config level.
type NotifyWebhookConfig struct {
	Enabled bool
	Name    string
	URL     string
	Secret  string
}

// AlertConfig controls built-in host monitoring alert evaluation. The
// evaluator consumes the closed-set HostMetricPoint fast path, so these
// rules work in both embedded and scrape collector modes.
type AlertConfig struct {
	// Enabled gates built-in host alert evaluation.
	// env: ONGRID_ALERT_ENABLED; default true.
	Enabled bool
	// Cooldown suppresses duplicate notifications for the same edge+rule.
	// env: ONGRID_ALERT_COOLDOWN; default 10m.
	Cooldown time.Duration
	// CPUPercent fires when cpu_pct >= threshold. 0 disables the rule.
	// env: ONGRID_ALERT_CPU_PERCENT; default 90.
	CPUPercent float64
	// MemPercent fires when mem_pct >= threshold. 0 disables the rule.
	// env: ONGRID_ALERT_MEM_PERCENT; default 90.
	MemPercent float64
	// DiskUsedPercent fires when disk_used_pct >= threshold. 0 disables the rule.
	// env: ONGRID_ALERT_DISK_USED_PERCENT; default 90.
	DiskUsedPercent float64
	// Load1 fires when load1 >= threshold. 0 disables the rule.
	// env: ONGRID_ALERT_LOAD1; default 0.
	Load1 float64
	// EvaluatorInterval is how often the pipeline evaluator scans edges
	// and queries Prom for `up` / remote_write health.
	// env: ONGRID_ALERT_EVAL_INTERVAL; default 5m (was 30s pre-2026-05-31 —
	// too noisy on long-firing rules, an always-true rule racked up
	// ~3900 alert_events in 32h). Notification cadence is independently
	// bounded by Alert.Cooldown so stretching this doesn't multiply
	// notifications — only the storage + evaluator-CPU side.
	EvaluatorInterval time.Duration
	// EdgeOfflineThreshold is the heartbeat staleness above which an edge
	// counts as offline.
	// env: ONGRID_ALERT_EDGE_OFFLINE_THRESHOLD; default 90s.
	EdgeOfflineThreshold time.Duration
	// PromIngestFailLimit is the consecutive remote_write failure count at
	// or above which the prom_ingest_fail rule fires.
	// env: ONGRID_ALERT_PROM_INGEST_FAIL_LIMIT; default 5.
	PromIngestFailLimit int
}

// PromConfig points the manager at a cloud-side Prometheus instance for
// remote_write ingestion (open-set edge samples) and PromQL queries
// (driven by the AI tool registry).
//
// When Enabled is false the manager runs without Prom: the
// push_prom_samples tunnel handler is still installed but silently drops
// every batch (so edges keep working), and the query_promql AI tool is
// not registered.
type PromConfig struct {
	// Enabled gates all Prom wiring. env: ONGRID_PROM_ENABLED; default false.
	Enabled bool
	// URL is the Prom server root, e.g. "http://prometheus:9090".
	// env: ONGRID_PROM_URL; default "http://prometheus:9090".
	URL string
	// RemoteWriteURL, when set, is the exact remote_write endpoint. This
	// supports Prometheus-compatible TSDBs whose write path is not rooted at
	// /api/v1/write (for example Mimir/Cortex-style gateways).
	// env: ONGRID_PROM_REMOTE_WRITE_URL; default empty.
	RemoteWriteURL string
	// QueryURL is the Prometheus-compatible HTTP API root used by query_promql.
	// If empty it falls back to URL.
	// env: ONGRID_PROM_QUERY_URL; default empty.
	QueryURL string

	// TLSInsecure skips TLS cert verification when talking to the TSDB.
	// env: ONGRID_PROM_TLS_INSECURE; default false.
	TLSInsecure bool

	// TLSCAPath is the path to a PEM file with the root CA used to verify
	// the TSDB's cert. Empty = use system roots.
	// env: ONGRID_PROM_TLS_CA_FILE; default empty.
	TLSCAPath string
}

// FrontierClientConfig drives the manager-side service-end SDK that dials
// the upstream github.com/singchia/frontier broker. The broker itself is
// a separate docker container; this struct only describes how to reach it.
type FrontierClientConfig struct {
	// Addr is the frontier service-bound listen, reached over the docker
	// network, e.g. "frontier:40011".
	Addr string
	// ServiceName is the identifier reported to the frontier on connect
	// via fbsvc.OptionServiceName. Defaults to "ongrid-manager".
	ServiceName string
	// Disabled skips the long-lived service-end dial to the frontier
	// broker entirely. Set by ONGRID_FRONTIER_DISABLED=true. The e2e
	// harness uses it to bring the manager up without a real geminio
	// broker — features that require fbClient (webssh, edge reverse
	// calls) error at call site rather than failing manager startup.
	Disabled bool
}

// DBConfig selects the backend (MySQL by default, SQLite opt-in) and
// carries the parameters for whichever is active. Only the fields matching
// Dialect are consulted at Open time; the others may be empty.
type DBConfig struct {
	// Dialect selects the backend: "mysql" (default) or "sqlite".
	// An empty string is treated as "mysql" for defensive defaults.
	Dialect string
	// DSN is the MySQL Data Source Name used when Dialect == "mysql".
	// Example: "ongrid:ongrid@tcp(127.0.0.1:3306)/ongrid?parseTime=true&charset=utf8mb4&loc=Local".
	DSN string
	// Path is the sqlite database file path used when Dialect == "sqlite".
	// The special value ":memory:" is accepted for tests.
	Path string
}

// JWTConfig holds JWT signing / expiry parameters used by the iam bounded context.
type JWTConfig struct {
	Secret     string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// OpenAIConfig holds credentials / endpoint for the LLM client.
type OpenAIConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// LLMProviderConfig is one configured LLM upstream beyond OpenAI. The
// SPA chat input's per-message model selector is populated from the
// configured-and-non-empty subset of these. All fields env-derived to
// keep the bootstrap path uniform with OpenAI; any provider with an
// empty APIKey is silently dropped from the catalog.
type LLMProviderConfig struct {
	APIKey  string
	Model   string   // default model
	BaseURL string   // optional base URL override
	Models  []string // closed-set of models exposed via /v1/aiops/models
}

// LLMConfig groups the multi-provider router config. OpenAI lives in
// its own top-level field for legacy reasons (see OpenAIConfig); the
// non-OpenAI providers cluster here.
type LLMConfig struct {
	Anthropic LLMProviderConfig
	Zhipu     LLMProviderConfig
	Gemini    LLMProviderConfig
	DeepSeek  LLMProviderConfig
	Kimi      LLMProviderConfig
	// Default is the provider id used when a chat-send request does not
	// specify Provider. Empty → first configured provider (deterministic
	// alphabetical ordering, see llm.NewMultiClient).
	Default string
	// DailyTokenLimit is the global per-UTC-day token ceiling enforced
	// against the BudgetChecker callback. <=0 means unlimited (the
	// historical MVP default). Single-tenant scope — when the platform
	// goes multi-tenant the limit moves into per-org settings and this
	// stays as a safety-net global cap.
	// env: ONGRID_LLM_DAILY_TOKEN_LIMIT; default 0 (unlimited).
	DailyTokenLimit int
}

// AdminConfig holds bootstrap admin credentials. Used only by the cloud
// binary at startup to seed the first admin user in a self-managed deployment.
// If either field is empty, no bootstrap is attempted.
type AdminConfig struct {
	Email    string
	Password string
}

// EdgeConfig is consumed only by ongrid-edge to dial the cloud tunnel.
type EdgeConfig struct {
	CloudAddr string
	AccessKey string
	SecretKey string

	// CollectorMode selects how the edge's periodic metric-push path
	// behaves. Defaults to "off" because the hostmetrics + procmetrics
	// plugins now expose host / per-process metrics to the manager via
	// direct Prometheus scrape — pushing duplicate samples via tunnel
	// produces noisy "ongrid_source=embedded" extra series in panel
	// legends.
	//
	// Values:
	//	off / "" — no periodic push; on-demand RPCs still work
	//	auto — legacy: embedded (gopsutil push) + optional scraper
	//	embedded — embedded push only
	//	scrape — multi-target HTTP scraper that polls /metrics
	//
	// env: ONGRID_EDGE_COLLECTOR_MODE
	CollectorMode string

	// ScrapeConfigFile is the absolute path to the yaml file describing
	// scrape targets. Only consulted when CollectorMode == "scrape".
	//
	// env: ONGRID_EDGE_SCRAPE_CONFIG_FILE; default /etc/ongrid-edge/scrape.yaml
	ScrapeConfigFile string

	// CollectorInterval is how often the embedded collector takes a
	// snapshot. Scrape mode ignores this — each target carries its own
	// per-target interval in the yaml.
	//
	// env: ONGRID_EDGE_COLLECTOR_INTERVAL; default 10s
	CollectorInterval time.Duration
}

// Load reads env vars and returns a Config with defaults applied.
// It never returns a non-nil error in MVP; the signature leaves room
// for future validation (e.g. required fields).
func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:                getEnv("ONGRID_HTTP_ADDR", ":8080"),
		MetricsAddr:             getEnv("ONGRID_METRICS_ADDR", ":9100"),
		TunnelAddr:              getEnv("ONGRID_TUNNEL_ADDR", ":40012"),
		PublicURL:               getEnv("ONGRID_PUBLIC_URL", ""),
		K8sEventRetention:       getEnvDuration("ONGRID_K8S_EVENT_RETENTION", 24*time.Hour),
		K8sEventMaxPerCluster:   getEnvInt("ONGRID_K8S_EVENT_MAX_PER_CLUSTER", 5000),
		K8sEventCleanupInterval: getEnvDuration("ONGRID_K8S_EVENT_CLEANUP_INTERVAL", time.Hour),
		Logs:                    LogsConfig{URL: getEnv("ONGRID_LOG_QUERY_URL", "http://loki:3100")},
		Traces:                  TracesConfig{URL: getEnv("ONGRID_TRACE_QUERY_URL", "http://tempo:3200")},
	}

	c.DB.Dialect = getEnv("ONGRID_DB_DIALECT", "mysql")
	c.DB.DSN = getEnv(
		"ONGRID_DB_DSN",
		"ongrid:ongrid@tcp(127.0.0.1:3306)/ongrid?parseTime=true&charset=utf8mb4&loc=Local",
	)
	c.DB.Path = getEnv("ONGRID_DB_PATH", "./data/ongrid.db")

	c.JWT.Secret = getEnv("ONGRID_JWT_SECRET", "dev-insecure-secret-change-me")
	c.JWT.AccessTTL = getEnvDuration("ONGRID_JWT_ACCESS_TTL", 15*time.Minute)
	c.JWT.RefreshTTL = getEnvDuration("ONGRID_JWT_REFRESH_TTL", 7*24*time.Hour)

	c.OpenAI.APIKey = getEnv("ONGRID_OPENAI_API_KEY", "")
	c.OpenAI.Model = getEnv("ONGRID_OPENAI_MODEL", "gpt-5.4")
	c.OpenAI.BaseURL = getEnv("ONGRID_OPENAI_BASE_URL", "")

	// Multi-provider LLM config (HLD: ChatInput model selector). Each
	// provider is gated by its own API key — empty key = provider not
	// surfaced to the SPA's selector. BaseURL defaults are pre-baked at
	// the wiring site so operators can drop in just the API key.
	c.LLM.Anthropic.APIKey = getEnv("ONGRID_ANTHROPIC_API_KEY", "")
	c.LLM.Anthropic.Model = getEnv("ONGRID_ANTHROPIC_MODEL", "claude-sonnet-4-6")
	c.LLM.Anthropic.BaseURL = getEnv("ONGRID_ANTHROPIC_BASE_URL", "")
	c.LLM.Anthropic.Models = splitProviderModels(getEnv("ONGRID_ANTHROPIC_MODELS", "claude-opus-4-7,claude-sonnet-4-6,claude-haiku-4-5"))
	c.LLM.Zhipu.APIKey = getEnv("ONGRID_ZHIPU_API_KEY", "")
	c.LLM.Zhipu.Model = getEnv("ONGRID_ZHIPU_MODEL", "glm-4.7")
	c.LLM.Zhipu.BaseURL = getEnv("ONGRID_ZHIPU_BASE_URL", "")
	c.LLM.Zhipu.Models = splitProviderModels(getEnv("ONGRID_ZHIPU_MODELS", "glm-5.1,glm-5,glm-4.7,glm-4.7-flash"))
	c.LLM.Gemini.APIKey = getEnv("ONGRID_GEMINI_API_KEY", "")
	c.LLM.Gemini.Model = getEnv("ONGRID_GEMINI_MODEL", "gemini-2.5-pro")
	c.LLM.Gemini.BaseURL = getEnv("ONGRID_GEMINI_BASE_URL", "")
	c.LLM.Gemini.Models = splitProviderModels(getEnv("ONGRID_GEMINI_MODELS", "gemini-3.5-flash,gemini-2.5-pro,gemini-2.5-flash"))
	c.LLM.DeepSeek.APIKey = getEnv("ONGRID_DEEPSEEK_API_KEY", "")
	c.LLM.DeepSeek.Model = getEnv("ONGRID_DEEPSEEK_MODEL", "deepseek-v4-flash")
	c.LLM.DeepSeek.BaseURL = getEnv("ONGRID_DEEPSEEK_BASE_URL", "")
	c.LLM.DeepSeek.Models = splitProviderModels(getEnv("ONGRID_DEEPSEEK_MODELS", "deepseek-v4-pro,deepseek-v4-flash,deepseek-reasoner"))
	c.LLM.Kimi.APIKey = getEnv("ONGRID_KIMI_API_KEY", "")
	c.LLM.Kimi.Model = getEnv("ONGRID_KIMI_MODEL", "kimi-k2.6")
	c.LLM.Kimi.BaseURL = getEnv("ONGRID_KIMI_BASE_URL", "")
	c.LLM.Kimi.Models = splitProviderModels(getEnv("ONGRID_KIMI_MODELS", "kimi-k2.6,kimi-k2.5,moonshot-v1-128k"))
	c.LLM.Default = getEnv("ONGRID_LLM_DEFAULT_PROVIDER", "")
	c.LLM.DailyTokenLimit = getEnvInt("ONGRID_LLM_DAILY_TOKEN_LIMIT", 0)

	c.Admin.Email = getEnv("ONGRID_ADMIN_EMAIL", "")
	c.Admin.Password = getEnv("ONGRID_ADMIN_PASSWORD", "")

	c.Edge.CloudAddr = getEnv("ONGRID_EDGE_CLOUD_ADDR", "127.0.0.1:40012")
	c.Edge.AccessKey = getEnv("ONGRID_EDGE_ACCESS_KEY", "")
	c.Edge.SecretKey = getEnv("ONGRID_EDGE_SECRET_KEY", "")
	c.Edge.CollectorMode = getEnv("ONGRID_EDGE_COLLECTOR_MODE", "off")
	c.Edge.ScrapeConfigFile = getEnv("ONGRID_EDGE_SCRAPE_CONFIG_FILE", "/etc/ongrid-edge/scrape.yaml")
	c.Edge.CollectorInterval = getEnvDuration("ONGRID_EDGE_COLLECTOR_INTERVAL", 10*time.Second)

	c.FrontierClient.Addr = getEnv("ONGRID_FRONTIER_ADDR", "frontier:40011")
	c.FrontierClient.ServiceName = getEnv("ONGRID_FRONTIER_SERVICE_NAME", "ongrid-manager")
	c.FrontierClient.Disabled = getEnvBool("ONGRID_FRONTIER_DISABLED", false)

	c.Prom.Enabled = getEnvBool("ONGRID_PROM_ENABLED", false)
	c.Prom.URL = getEnv("ONGRID_PROM_URL", "http://prometheus:9090")
	c.Prom.RemoteWriteURL = getEnv("ONGRID_PROM_REMOTE_WRITE_URL", "")
	c.Prom.QueryURL = getEnv("ONGRID_PROM_QUERY_URL", "")
	c.Prom.TLSInsecure = getEnvBool("ONGRID_PROM_TLS_INSECURE", false)
	c.Prom.TLSCAPath = getEnv("ONGRID_PROM_TLS_CA_FILE", "")
	c.Grafana.InternalRootURL = getEnv("ONGRID_GRAFANA_INTERNAL_URL", "http://grafana:3000/grafana")
	c.Grafana.BootstrapUser = getEnv("ONGRID_GRAFANA_BOOTSTRAP_USER", "admin")
	c.Grafana.BootstrapPassword = getEnv("ONGRID_GRAFANA_BOOTSTRAP_PASSWORD", "")
	c.Grafana.TLSInsecure = getEnvBool("ONGRID_GRAFANA_TLS_INSECURE", false)

	// Default ON: notifications are allowed out of the box, so any channel
	// the operator configures (UI or env) delivers without flipping a
	// master switch. Per-channel env enables (Slack/Feishu/... below) stay
	// OFF by default on purpose — seeding an enabled-but-URL-less channel
	// would clutter the UI; UI-created channels carry their own enabled flag.
	c.Notification.Enabled = getEnvBool("ONGRID_NOTIFY_ENABLED", true)
	// Default channels list defaults empty — operators can pin specific
	// channel names per env if desired. The legacy "log" entry was
	// removed alongside the log channel type.
	c.Notification.DefaultChannels = getEnvCSV("ONGRID_NOTIFY_DEFAULT_CHANNELS", nil)
	c.Notification.Timeout = getEnvDuration("ONGRID_NOTIFY_TIMEOUT", 10*time.Second)
	c.Notification.Webhook.Enabled = getEnvBool("ONGRID_NOTIFY_WEBHOOK_ENABLED", false)
	c.Notification.Webhook.Name = getEnv("ONGRID_NOTIFY_WEBHOOK_NAME", "webhook")
	c.Notification.Webhook.URL = getEnv("ONGRID_NOTIFY_WEBHOOK_URL", "")
	c.Notification.Webhook.Secret = getEnv("ONGRID_NOTIFY_WEBHOOK_SECRET", "")
	c.Notification.Slack.Enabled = getEnvBool("ONGRID_NOTIFY_SLACK_ENABLED", false)
	c.Notification.Slack.Name = getEnv("ONGRID_NOTIFY_SLACK_NAME", "slack")
	c.Notification.Slack.URL = getEnv("ONGRID_NOTIFY_SLACK_WEBHOOK_URL", "")
	c.Notification.Feishu.Enabled = getEnvBool("ONGRID_NOTIFY_FEISHU_ENABLED", false)
	c.Notification.Feishu.Name = getEnv("ONGRID_NOTIFY_FEISHU_NAME", "feishu")
	c.Notification.Feishu.URL = getEnv("ONGRID_NOTIFY_FEISHU_WEBHOOK_URL", "")
	c.Notification.Feishu.Secret = getEnv("ONGRID_NOTIFY_FEISHU_SECRET", "")
	c.Notification.DingTalk.Enabled = getEnvBool("ONGRID_NOTIFY_DINGTALK_ENABLED", false)
	c.Notification.DingTalk.Name = getEnv("ONGRID_NOTIFY_DINGTALK_NAME", "dingtalk")
	c.Notification.DingTalk.URL = getEnv("ONGRID_NOTIFY_DINGTALK_WEBHOOK_URL", "")
	c.Notification.DingTalk.Secret = getEnv("ONGRID_NOTIFY_DINGTALK_SECRET", "")

	c.Alert.Enabled = getEnvBool("ONGRID_ALERT_ENABLED", true)
	c.Alert.Cooldown = getEnvDuration("ONGRID_ALERT_COOLDOWN", 10*time.Minute)
	c.Alert.CPUPercent = getEnvFloat("ONGRID_ALERT_CPU_PERCENT", 90)
	c.Alert.MemPercent = getEnvFloat("ONGRID_ALERT_MEM_PERCENT", 90)
	c.Alert.DiskUsedPercent = getEnvFloat("ONGRID_ALERT_DISK_USED_PERCENT", 90)
	c.Alert.Load1 = getEnvFloat("ONGRID_ALERT_LOAD1", 0)
	c.Alert.EvaluatorInterval = getEnvDuration("ONGRID_ALERT_EVAL_INTERVAL", 5*time.Minute)
	c.Alert.EdgeOfflineThreshold = getEnvDuration("ONGRID_ALERT_EDGE_OFFLINE_THRESHOLD", 90*time.Second)
	c.Alert.PromIngestFailLimit = getEnvInt("ONGRID_ALERT_PROM_INGEST_FAIL_LIMIT", 5)

	c.Skills.ExternalDirs = getEnvCSV("ONGRID_SKILLS_EXTERNAL_DIRS", nil)

	return c, nil
}

// getEnvBool parses a boolean env var. Accepts the usual strconv.ParseBool
// values (1/0, t/f, true/false, TRUE/FALSE …); any other value returns def.
func getEnvBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return def
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// splitProviderModels parses a comma-separated list of model slugs into
// a trimmed, dedup'd slice. Empty input returns nil so the wiring site
// can decide whether to fall back to the default model only.
func splitProviderModels(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';'
	})
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func getEnvCSV(key string, def []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	parts := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ';'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func getEnvInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return def
}

func getEnvFloat(key string, def float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return def
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	// Fall back to integer seconds for convenience.
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}
