package setting

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	openai "github.com/sashabaranov/go-openai"

	settingmodel "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

const (
	LLMProbeCodeOK                  = "ok"
	LLMProbeCodeDisabled            = "disabled"
	LLMProbeCodeUnsupportedProvider = "unsupported-provider"
	LLMProbeCodeMissingAPIKey       = "missing-api-key"
	LLMProbeCodeMissingModel        = "missing-model"
	LLMProbeCodeMissingBaseURL      = "missing-base-url"
	LLMProbeCodeInvalidBaseURL      = "invalid-base-url"
	LLMProbeCodeAuthentication      = "authentication-failed"
	LLMProbeCodePermission          = "permission-denied"
	LLMProbeCodeModelNotFound       = "model-not-found"
	LLMProbeCodeQuotaExceeded       = "quota-exceeded"
	LLMProbeCodeRateLimited         = "rate-limited"
	LLMProbeCodeTimeout             = "timeout"
	LLMProbeCodeCanceled            = "request-canceled"
	LLMProbeCodeDNS                 = "dns-failed"
	LLMProbeCodeConnection          = "connection-failed"
	LLMProbeCodeTLS                 = "tls-failed"
	LLMProbeCodeEndpointNotFound    = "endpoint-not-found"
	LLMProbeCodeProviderUnavailable = "provider-unavailable"
	LLMProbeCodeInvalidRequest      = "invalid-request"
	LLMProbeCodeInvalidResponse     = "invalid-response"
	LLMProbeCodeUpstream            = "upstream-error"
)

const (
	defaultLLMProbeTimeout = 20 * time.Second
	maxLLMAPIKeyBytes      = 16 << 10
	maxLLMBaseURLBytes     = 2048
	maxLLMModelBytes       = 256
	maxLLMModels           = 32
	maxLLMProbeDetailRunes = 240
)

// LLMProbeInput is a provider draft supplied by an administrator. APIKey may be
// persisted only by LLMConfigurationService.Save; it must never be logged or
// copied into LLMProbeResult.
type LLMProbeInput struct {
	Provider     string   `json:"provider"`
	APIKey       string   `json:"api_key"`
	BaseURL      string   `json:"base_url"`
	DefaultModel string   `json:"default_model"`
	Models       []string `json:"models"`
}

// LLMProbeResult is a stable, language-neutral validation result. The SPA maps
// Code to localized guidance and may show Detail as a bounded upstream hint.
type LLMProbeResult struct {
	Valid     bool   `json:"valid"`
	Code      string `json:"code"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Detail    string `json:"detail,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Saved     bool   `json:"saved"`
	Disabled  bool   `json:"disabled"`
}

type llmProbeCall func(context.Context, llm.Config) (*llm.ProbeResult, error)

// LLMConfigProbe validates a draft against the provider before it is saved.
// defaults is the same env-backed provider map used by LLMSettingsResolver, so
// an empty named-provider Base URL means exactly what it means in production.
type LLMConfigProbe struct {
	defaults map[string]EnvProviderDefaults
	timeout  time.Duration
	call     llmProbeCall
}

func NewLLMConfigProbe(defaults map[string]EnvProviderDefaults) *LLMConfigProbe {
	cloned := make(map[string]EnvProviderDefaults, len(defaults))
	for provider, def := range defaults {
		cloned[provider] = def
	}
	return &LLMConfigProbe{
		defaults: cloned,
		timeout:  defaultLLMProbeTimeout,
		call:     llm.ProbeChatCompletion,
	}
}

type validatedLLMConfig struct {
	provider         string
	apiKey           string
	storedBaseURL    string
	effectiveBaseURL string
	defaultModel     string
	models           []string
}

// LLMConfigurationService binds the exact draft that was probed to the exact
// tuple persisted by Service.SetBatch. This server-side boundary prevents a UI
// or API client from validating one value and then saving a different one.
type LLMConfigurationService struct {
	probe    *LLMConfigProbe
	settings *Service
}

// NewLLMConfigurationService builds the validation and persistence boundary.
func NewLLMConfigurationService(defaults map[string]EnvProviderDefaults, settings *Service) *LLMConfigurationService {
	return &LLMConfigurationService{
		probe:    NewLLMConfigProbe(defaults),
		settings: settings,
	}
}

// Probe validates an unsaved provider draft without persisting it.
func (s *LLMConfigurationService) Probe(ctx context.Context, in LLMProbeInput) (LLMProbeResult, error) {
	if s == nil || s.probe == nil {
		return LLMProbeResult{}, fmt.Errorf("llm configuration service not wired")
	}
	return s.probe.Probe(ctx, in)
}

// Save validates every exposed model and atomically persists the provider
// tuple. An empty API key is a deliberate disable override and skips upstream
// calls so a broken credential can always be removed.
func (s *LLMConfigurationService) Save(ctx context.Context, in LLMProbeInput) (LLMProbeResult, error) {
	if s == nil || s.probe == nil || s.settings == nil {
		return LLMProbeResult{}, fmt.Errorf("llm configuration service not wired")
	}
	operational := strings.TrimSpace(in.APIKey) != ""
	cfg, result, ok := s.probe.validateInput(in, operational)
	if !ok {
		return result, nil
	}
	if operational {
		result = s.probe.probeValidated(ctx, cfg)
		if !result.Valid {
			return result, nil
		}
	} else {
		cfg.apiKey = ""
		result.Valid = true
		result.Code = LLMProbeCodeDisabled
		result.Disabled = true
	}

	keys, ok := providerKeysByID(cfg.provider)
	if !ok {
		result.Valid = false
		result.Code = LLMProbeCodeUnsupportedProvider
		return result, nil
	}
	modelsJSON, err := EncodeModelsList(cfg.models)
	if err != nil {
		return result, fmt.Errorf("encode llm models: %w", err)
	}
	if err := s.settings.SetBatch(ctx, []settingmodel.Setting{
		{Category: settingmodel.CategoryLLM, Key: keys.apiKey, Value: cfg.apiKey, Sensitive: true},
		{Category: settingmodel.CategoryLLM, Key: keys.baseURL, Value: cfg.storedBaseURL},
		{Category: settingmodel.CategoryLLM, Key: keys.defaultModel, Value: cfg.defaultModel},
		{Category: settingmodel.CategoryLLM, Key: keys.models, Value: modelsJSON},
	}); err != nil {
		return result, fmt.Errorf("save llm provider %s: %w", cfg.provider, err)
	}
	result.Saved = true
	return result, nil
}

func providerKeysByID(provider string) (providerKeys, bool) {
	for _, keys := range allProviderKeys() {
		if keys.id == provider {
			return keys, true
		}
	}
	return providerKeys{}, false
}

// Probe performs one bounded validation across every model that would be
// exposed after saving. Expected configuration and upstream failures are
// returned as Valid=false rather than Go errors because validation completed.
func (p *LLMConfigProbe) Probe(ctx context.Context, in LLMProbeInput) (LLMProbeResult, error) {
	if p == nil || p.call == nil {
		return LLMProbeResult{}, fmt.Errorf("llm config probe not wired")
	}
	cfg, result, ok := p.validateInput(in, true)
	if !ok {
		return result, nil
	}
	return p.probeValidated(ctx, cfg), nil
}

func (p *LLMConfigProbe) validateInput(in LLMProbeInput, operational bool) (validatedLLMConfig, LLMProbeResult, bool) {
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	defaultModel := strings.TrimSpace(in.DefaultModel)
	result := LLMProbeResult{Code: LLMProbeCodeOK, Provider: provider, Model: defaultModel}
	cfg := validatedLLMConfig{
		provider:      provider,
		apiKey:        in.APIKey,
		storedBaseURL: strings.TrimSpace(in.BaseURL),
		defaultModel:  defaultModel,
	}

	if !isKnownLLMProvider(provider) {
		result.Code = LLMProbeCodeUnsupportedProvider
		return cfg, result, false
	}
	if operational && strings.TrimSpace(in.APIKey) == "" {
		result.Code = LLMProbeCodeMissingAPIKey
		return cfg, result, false
	}
	if len(in.APIKey) > maxLLMAPIKeyBytes {
		result.Code = LLMProbeCodeInvalidRequest
		result.Detail = "api key is too long"
		return cfg, result, false
	}
	if len(cfg.storedBaseURL) > maxLLMBaseURLBytes {
		result.Code = LLMProbeCodeInvalidBaseURL
		result.Detail = "base URL is too long"
		return cfg, result, false
	}

	seen := make(map[string]struct{}, len(in.Models))
	for _, rawModel := range in.Models {
		modelName := strings.TrimSpace(rawModel)
		if modelName == "" {
			continue
		}
		if len(modelName) > maxLLMModelBytes {
			result.Code = LLMProbeCodeInvalidRequest
			result.Model = modelName
			result.Detail = "model name is too long"
			return cfg, result, false
		}
		if _, exists := seen[modelName]; exists {
			continue
		}
		seen[modelName] = struct{}{}
		cfg.models = append(cfg.models, modelName)
		if len(cfg.models) > maxLLMModels {
			result.Code = LLMProbeCodeInvalidRequest
			result.Detail = "too many models"
			return cfg, result, false
		}
	}
	if !operational && cfg.defaultModel == "" && len(cfg.models) > 0 {
		cfg.defaultModel = cfg.models[0]
		result.Model = cfg.defaultModel
	}
	if len(cfg.defaultModel) > maxLLMModelBytes {
		result.Code = LLMProbeCodeInvalidRequest
		result.Detail = "default model name is too long"
		return cfg, result, false
	}
	if operational {
		if cfg.defaultModel == "" || len(cfg.models) == 0 {
			result.Code = LLMProbeCodeMissingModel
			return cfg, result, false
		}
		if !containsString(cfg.models, cfg.defaultModel) {
			result.Code = LLMProbeCodeInvalidRequest
			result.Detail = "default model must be included in models"
			return cfg, result, false
		}
	}

	cfg.effectiveBaseURL = cfg.storedBaseURL
	if cfg.effectiveBaseURL == "" {
		cfg.effectiveBaseURL = strings.TrimSpace(p.defaults[provider].BaseURL)
	}
	if operational && provider == settingmodel.LLMProviderCustom && cfg.effectiveBaseURL == "" {
		result.Code = LLMProbeCodeMissingBaseURL
		return cfg, result, false
	}
	if operational && cfg.effectiveBaseURL != "" {
		if err := validateLLMBaseURL(cfg.effectiveBaseURL); err != nil {
			result.Code = LLMProbeCodeInvalidBaseURL
			result.Detail = sanitizeLLMProbeDetail(err.Error(), in.APIKey)
			return cfg, result, false
		}
	}
	return cfg, result, true
}

func (p *LLMConfigProbe) probeValidated(ctx context.Context, cfg validatedLLMConfig) LLMProbeResult {
	result := LLMProbeResult{
		Code:     LLMProbeCodeOK,
		Provider: cfg.provider,
		Model:    cfg.defaultModel,
	}
	startedAt := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	models := make([]string, 0, len(cfg.models))
	models = append(models, cfg.defaultModel)
	for _, modelName := range cfg.models {
		if modelName != cfg.defaultModel {
			models = append(models, modelName)
		}
	}
	for _, modelName := range models {
		_, err := p.call(probeCtx, llm.Config{
			APIKey:  cfg.apiKey,
			Model:   modelName,
			BaseURL: cfg.effectiveBaseURL,
			Timeout: p.timeout,
		})
		if err != nil {
			result.Model = modelName
			result.Code, result.Detail = classifyLLMProbeError(err, cfg.apiKey)
			result.LatencyMS = time.Since(startedAt).Milliseconds()
			return result
		}
	}
	result.Valid = true
	result.LatencyMS = time.Since(startedAt).Milliseconds()
	return result
}

func isKnownLLMProvider(provider string) bool {
	switch provider {
	case settingmodel.LLMProviderOpenAI,
		settingmodel.LLMProviderAnthropic,
		settingmodel.LLMProviderZhipu,
		settingmodel.LLMProviderGemini,
		settingmodel.LLMProviderDeepSeek,
		settingmodel.LLMProviderKimi,
		settingmodel.LLMProviderCustom:
		return true
	default:
		return false
	}
}

func validateLLMBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base URL scheme must be http or https")
	}
	if u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("base URL host is required")
	}
	if u.User != nil {
		return fmt.Errorf("base URL must not contain userinfo")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base URL must not contain query or fragment")
	}
	return nil
}

func classifyLLMProbeError(err error, apiKey string) (string, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return LLMProbeCodeTimeout, "provider did not respond before the probe deadline"
	}
	if errors.Is(err, context.Canceled) {
		return LLMProbeCodeCanceled, "probe request was canceled"
	}
	if errors.Is(err, llm.ErrNoAPIKey) {
		return LLMProbeCodeMissingAPIKey, ""
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return LLMProbeCodeInvalidResponse, "provider returned an invalid chat completion response"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return LLMProbeCodeDNS, sanitizeLLMProbeDetail(dnsErr.Error(), apiKey)
	}
	if isLLMTLSError(err) {
		return LLMProbeCodeTLS, sanitizeLLMProbeDetail(err.Error(), apiKey)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return LLMProbeCodeTimeout, "provider did not respond before the probe deadline"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return LLMProbeCodeConnection, sanitizeLLMProbeDetail(opErr.Error(), apiKey)
	}

	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return classifyLLMHTTPError(apiErr.HTTPStatusCode, fmt.Sprint(apiErr.Code), apiErr.Type, apiErr.Message, apiKey, false)
	}
	var requestErr *openai.RequestError
	if errors.As(err, &requestErr) {
		return classifyLLMHTTPError(requestErr.HTTPStatusCode, "", "", "", apiKey, true)
	}

	msg := strings.ToLower(err.Error())
	switch {
	case containsAny(msg, "empty choices", "decode", "decoding response", "unmarshal", "invalid character"):
		return LLMProbeCodeInvalidResponse, "provider returned an invalid chat completion response"
	case strings.Contains(msg, "invalid api key"), strings.Contains(msg, "invalid_api_key"):
		return LLMProbeCodeAuthentication, "provider rejected the API key"
	case strings.Contains(msg, "model") && containsAny(msg, "not found", "does not exist", "unknown", "invalid model"):
		return LLMProbeCodeModelNotFound, sanitizeLLMProbeDetail(err.Error(), apiKey)
	default:
		return LLMProbeCodeUpstream, sanitizeLLMProbeDetail(err.Error(), apiKey)
	}
}

func classifyLLMHTTPError(status int, rawCode, rawType, rawMessage, apiKey string, unstructured bool) (string, string) {
	message := strings.TrimSpace(rawMessage)
	searchable := strings.ToLower(strings.Join([]string{rawCode, rawType, message}, " "))
	detail := sanitizeLLMProbeDetail(message, apiKey)

	switch {
	case containsAny(searchable, "insufficient_quota", "quota exceeded", "billing", "payment required", "credit balance"):
		return LLMProbeCodeQuotaExceeded, detail
	case status == 401 || containsAny(searchable, "invalid_api_key", "invalid api key", "authentication", "unauthorized"):
		return LLMProbeCodeAuthentication, detail
	case status == 403:
		return LLMProbeCodePermission, detail
	case containsAny(searchable, "model_not_found", "model not found", "model does not exist", "unknown model", "invalid model"):
		return LLMProbeCodeModelNotFound, detail
	case status == 402:
		return LLMProbeCodeQuotaExceeded, detail
	case status == 408 || status == 504:
		return LLMProbeCodeTimeout, detail
	case status == 429:
		return LLMProbeCodeRateLimited, detail
	case status == 404 && unstructured:
		return LLMProbeCodeEndpointNotFound, "provider endpoint did not expose Chat Completions"
	case status == 404:
		return LLMProbeCodeEndpointNotFound, detail
	case status >= 500:
		return LLMProbeCodeProviderUnavailable, detail
	case status == 400 || status == 422:
		return LLMProbeCodeInvalidRequest, detail
	case unstructured:
		return LLMProbeCodeInvalidResponse, "provider returned an unstructured error response"
	default:
		return LLMProbeCodeUpstream, detail
	}
}

func isLLMTLSError(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return true
	}
	var certificateErr x509.CertificateInvalidError
	if errors.As(err, &certificateErr) {
		return true
	}
	var recordHeaderErr tls.RecordHeaderError
	return errors.As(err, &recordHeaderErr)
}

func sanitizeLLMProbeDetail(detail, apiKey string) string {
	detail = strings.TrimSpace(detail)
	if apiKey != "" {
		detail = strings.ReplaceAll(detail, apiKey, "[redacted]")
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if utf8.RuneCountInString(detail) <= maxLLMProbeDetailRunes {
		return detail
	}
	runes := []rune(detail)
	return string(runes[:maxLLMProbeDetailRunes]) + "…"
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
