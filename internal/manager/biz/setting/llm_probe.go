package setting

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
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
	maxLLMProbeDetailRunes = 240
)

// LLMProbeInput is an unpersisted provider draft supplied by an administrator.
// APIKey must never be logged or copied into LLMProbeResult.
type LLMProbeInput struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
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

// Probe performs one bounded upstream call. Expected configuration and
// upstream failures are returned as Valid=false rather than Go errors because
// the validation itself completed successfully. A non-nil error means the
// probe service is not wired and should map to an HTTP 5xx.
func (p *LLMConfigProbe) Probe(ctx context.Context, in LLMProbeInput) (LLMProbeResult, error) {
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	modelName := strings.TrimSpace(in.Model)
	result := LLMProbeResult{Code: LLMProbeCodeOK, Provider: provider, Model: modelName}

	if p == nil || p.call == nil {
		return result, fmt.Errorf("llm config probe not wired")
	}
	if !isKnownLLMProvider(provider) {
		result.Code = LLMProbeCodeUnsupportedProvider
		return result, nil
	}
	if strings.TrimSpace(in.APIKey) == "" {
		result.Code = LLMProbeCodeMissingAPIKey
		return result, nil
	}
	if len(in.APIKey) > maxLLMAPIKeyBytes {
		result.Code = LLMProbeCodeInvalidRequest
		result.Detail = "api key is too long"
		return result, nil
	}
	if modelName == "" {
		result.Code = LLMProbeCodeMissingModel
		return result, nil
	}
	if len(modelName) > maxLLMModelBytes {
		result.Code = LLMProbeCodeInvalidRequest
		result.Detail = "model name is too long"
		return result, nil
	}

	baseURL := strings.TrimSpace(in.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(p.defaults[provider].BaseURL)
	}
	if provider == settingmodel.LLMProviderCustom && baseURL == "" {
		result.Code = LLMProbeCodeMissingBaseURL
		return result, nil
	}
	if len(baseURL) > maxLLMBaseURLBytes {
		result.Code = LLMProbeCodeInvalidBaseURL
		result.Detail = "base URL is too long"
		return result, nil
	}
	if baseURL != "" {
		if err := validateLLMBaseURL(baseURL); err != nil {
			result.Code = LLMProbeCodeInvalidBaseURL
			result.Detail = sanitizeLLMProbeDetail(err.Error(), in.APIKey)
			return result, nil
		}
	}

	startedAt := time.Now()
	_, err := p.call(ctx, llm.Config{
		APIKey:  in.APIKey,
		Model:   modelName,
		BaseURL: baseURL,
		Timeout: p.timeout,
	})
	result.LatencyMS = time.Since(startedAt).Milliseconds()
	if err == nil {
		result.Valid = true
		return result, nil
	}
	result.Code, result.Detail = classifyLLMProbeError(err, in.APIKey)
	return result, nil
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
