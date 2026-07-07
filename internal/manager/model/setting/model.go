// Package setting holds the persistence entity for the system_settings
// key/value store. The table backs DB-driven runtime configuration that
// admins can edit through the UI without restarting the manager (e.g.
// LLM credentials, model, base URL).
//
// Scope: flat / single-tenant. When multi-tenancy lands the
// uniqueness should expand to (org_id, category, key); the current shape
// stays forward-compatible because Category lets us namespace by feature
// area (llm, notification, ...).
package setting

import "time"

// Setting is one (category, key) -> value row.
//
// Sensitive=true marks rows whose Value should never be returned in the
// clear by list endpoints; the service layer is responsible for masking
// before sending to the client.
type Setting struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	Category  string    `gorm:"size:32;not null;uniqueIndex:idx_settings_cat_key,priority:1"`
	Key       string    `gorm:"size:128;not null;uniqueIndex:idx_settings_cat_key,priority:2"`
	// MySQL 拒绝 TEXT 列的 DEFAULT 值；Go 零值已经是 ""，业务层 Set 总是
	// 显式写入，schema 不必声明默认。
	Value     string    `gorm:"type:text;not null"`
	Sensitive bool      `gorm:"not null;default:false"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
}

// TableName pins the table name so future package renames don't accidentally
// create a new schema.
func (Setting) TableName() string { return "system_settings" }

// Categories used today. Add to this list as new feature areas adopt
// DB-backed config.
const (
	CategoryLLM       = "llm"
	CategoryProm      = "prom"      // external Prometheus / VictoriaMetrics / Mimir / Thanos
	CategoryGrafana   = "grafana"   // external Grafana root URL + service-account token
	CategoryLoki      = "loki"      // external Loki / VictoriaLogs URL + auth
	CategoryTempo     = "tempo"     // external Tempo OTLP HTTP endpoint + auth
	CategoryWebSearch = "websearch" // built-in web_search skill: Tavily key + future provider knobs
	CategoryAgent     = "agent"     // AI agent behaviour toggles (write-action gate, …)
)

// Well-known keys under CategoryAgent.
const (
	// KeyAgentWriteEnabled gates whether the chat agent may use write/mutating
	// tools. Only "true" enables write tools; unset / any other value keeps the
	// agent read-only (only Class=="read" tools exposed to the LLM).
	KeyAgentWriteEnabled = "write_enabled"
)

// (CategoryGit + KeyGitGitHubToken removed HTTPS git
// auth will return via the credential.helper-backed table in P3;
// SSH lives in ssh_identities. Any pre-existing `git.github_token`
// row in system_settings is now dead data — safe to leave or DELETE
// out-of-band; nothing reads it.)

// Well-known keys under CategoryLLM. The Resolver in pkg/llm reads exactly
// these names; keep the strings stable across releases.
//
// 2026-05 expansion: per-provider keys for OpenAI / Anthropic / Zhipu /
// Gemini, each carrying api_key + base_url + JSON-encoded models list +
// default_model. The legacy openai_* triple is still honoured (the
// LLMSettingsResolver in biz/setting/llm.go falls back to the legacy
// openai_model field when openai_default_model is empty), so existing
// deployments survive migration cleanly.
//
// Naming pattern: <provider>_<field>. The provider id is the same one
// the SPA's model dropdown sends (openai / anthropic / zhipu / gemini),
// keeping the wire shape symmetric with the catalog endpoint.
const (
	KeyOpenAIAPIKey  = "openai_api_key"
	KeyOpenAIModel   = "openai_model"
	KeyOpenAIBaseURL = "openai_base_url"

	KeyOpenAIModels       = "openai_models"        // JSON array of model slugs
	KeyOpenAIDefaultModel = "openai_default_model" // single slug

	KeyAnthropicAPIKey       = "anthropic_api_key" // sensitive
	KeyAnthropicBaseURL      = "anthropic_base_url"
	KeyAnthropicModels       = "anthropic_models"
	KeyAnthropicDefaultModel = "anthropic_default_model"

	KeyZhipuAPIKey       = "zhipu_api_key" // sensitive
	KeyZhipuBaseURL      = "zhipu_base_url"
	KeyZhipuModels       = "zhipu_models"
	KeyZhipuDefaultModel = "zhipu_default_model"

	KeyGeminiAPIKey       = "gemini_api_key" // sensitive
	KeyGeminiBaseURL      = "gemini_base_url"
	KeyGeminiModels       = "gemini_models"
	KeyGeminiDefaultModel = "gemini_default_model"

	KeyDeepSeekAPIKey       = "deepseek_api_key" // sensitive
	KeyDeepSeekBaseURL      = "deepseek_base_url"
	KeyDeepSeekModels       = "deepseek_models"
	KeyDeepSeekDefaultModel = "deepseek_default_model"

	KeyKimiAPIKey       = "kimi_api_key" // sensitive
	KeyKimiBaseURL      = "kimi_base_url"
	KeyKimiModels       = "kimi_models"
	KeyKimiDefaultModel = "kimi_default_model"

	// Custom = a generic OpenAI-compatible endpoint (Ollama / vLLM /
	// OpenRouter / LM Studio / Together / Groq / any self-hosted gateway).
	// Unlike the named providers it has no default endpoint, so base_url is
	// required — the resolver skips it until one is supplied.
	KeyCustomAPIKey       = "custom_api_key" // sensitive
	KeyCustomBaseURL      = "custom_base_url"
	KeyCustomModels       = "custom_models"
	KeyCustomDefaultModel = "custom_default_model"

	// KeyLLMDefaultProvider stores the cluster-wide default provider id.
	// Empty → first provider (alphabetical) is used.
	KeyLLMDefaultProvider = "default_provider"
)

// LLMProviderID enumerates the providers the multi-provider router
// understands. Keep the strings stable; they are the public wire ids.
const (
	LLMProviderOpenAI    = "openai"
	LLMProviderAnthropic = "anthropic"
	LLMProviderZhipu     = "zhipu"
	LLMProviderGemini    = "gemini"
	LLMProviderDeepSeek  = "deepseek"
	LLMProviderKimi      = "kimi"
	LLMProviderCustom    = "custom"
)

// Well-known keys under CategoryProm. internal/pkg/promauth reads bearer/
// basic on every request via the Resolver; URLs are read at startup (env
// seed → DB) and changes require a manager restart.
const (
	KeyPromQueryURL       = "query_url"
	KeyPromRemoteWriteURL = "remote_write_url"
	KeyPromBearerToken    = "bearer_token"     // sensitive
	KeyPromBasicUser      = "basic_user"
	KeyPromBasicPassword  = "basic_password"   // sensitive
	KeyPromTLSInsecure    = "tls_insecure"     // "true" / "false"
	KeyPromTLSCAPEM       = "tls_ca_pem"       // PEM text
)

// Well-known keys under CategoryGrafana. PR-2 wires these into a Grafana
// API client; today only RootURL drives the UI's "open in Grafana" links.
//
// SAToken is the bearer credential we mint at bootstrap for the embedded
// Grafana. APIKey is the operator-pasted equivalent for an external
// Grafana — semantically identical (both feed the Authorization: Bearer
// header), exposed as a separate UI field because customers who run their
// own Grafana usually don't have admin access to mint a fresh SA token,
// and pasting an existing API key is the path of least resistance.
//
// OrgID mirrors the per-browser observability store value into the
// backend so the dashboard-fetch proxy can default it without making the
// SPA pass it on every call.
const (
	KeyGrafanaRootURL = "root_url"
	KeyGrafanaSAToken = "sa_token" // sensitive — Grafana service-account token
	KeyGrafanaAPIKey  = "api_key"  // sensitive — alternative bearer for external Grafana
	KeyGrafanaOrgID   = "org_id"
)

// Well-known keys under CategoryLoki. The PluginConfigUC reads these on
// every FetchForEdge call so a UI edit takes effect on the edge's next
// reload (push or 60s safety-net poll). When the URL is empty the
// manager falls back to its env-seeded default (ONGRID_LOG_URL), which
// in built-in deployments points at the docker-internal loki:3100 (the
// edge then pushes to manager nginx /loki/api/v1/push, which proxies on).
const (
	KeyLokiURL          = "url"             // e.g. https://loki.customer.com or http://loki:3100 (default)
	KeyLokiBasicUser    = "basic_user"
	KeyLokiBasicPassword = "basic_password" // sensitive
	KeyLokiTLSInsecure  = "tls_insecure"    // "true" / "false"
)

// Well-known keys under CategoryTempo. Mirrors CategoryLoki; URL is the
// OTLP HTTP push endpoint (e.g. https://tempo.customer.com/v1/traces).
const (
	KeyTempoURL          = "url"
	KeyTempoBasicUser    = "basic_user"
	KeyTempoBasicPassword = "basic_password" // sensitive
	KeyTempoTLSInsecure  = "tls_insecure"
)

// Well-known keys under CategoryWebSearch. Read by the manager-scoped
// `web_search` skill (internal/skill/builtin/web_search.go) every time
// the AI agent decides to look something up on the public web.
//
// Provider selection (KeyWebSearchProvider): which backend to dispatch to.
//
//   - "searxng" (default): self-hosted meta-search aggregator running in
//     ongrid's docker-compose alongside Loki/Tempo/Prom. Zero key,
//     zero quota; reachable at http://searxng:8080.
//   - "tavily": commercial search API with 1000 free calls/month;
//     requires KeyTavilyAPIKey.
//   - "brave": commercial search API with 2000 free calls/month;
//     requires KeyBraveAPIKey.
//
// Empty / unset provider falls through to "searxng" (the zero-config
// baseline). Tavily / Brave are opt-in via UI.
const (
	KeyWebSearchProvider = "provider"        // "searxng" | "tavily" | "brave"
	KeySearxngURL        = "searxng_url"     // e.g. http://searxng:8080 (default)
	KeyTavilyAPIKey      = "tavily_api_key"  // sensitive — Tavily Search API key
	KeyBraveAPIKey       = "brave_api_key"   // sensitive — Brave Search API key
)

// WebSearch provider names, exported as constants so callers don't pass
// raw strings. Comparisons inside the skill are case-insensitive but the
// canonical wire form is lowercase.
const (
	ProviderSearxng = "searxng"
	ProviderTavily  = "tavily"
	ProviderBrave   = "brave"
)

// DefaultSearxngURL is the docker-internal URL the embedded SearXNG
// service answers on. Used as a fallback when the operator hasn't
// pointed at an external instance.
const DefaultSearxngURL = "http://searxng:8080"

// Note: server/setting/http.go's default-mask policy treats any *_api_key,
// *_secret, *_token, *_password suffix as sensitive — bearer_token /
// basic_password / sa_token all match automatically, no allowlist needed.
