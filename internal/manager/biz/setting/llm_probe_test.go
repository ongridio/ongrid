package setting

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	openai "github.com/sashabaranov/go-openai"

	settingmodel "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func TestLLMConfigProbe_WhenValid_UsesEffectiveConfiguration(t *testing.T) {
	t.Parallel()

	var got llm.Config
	p := NewLLMConfigProbe(map[string]EnvProviderDefaults{
		settingmodel.LLMProviderDeepSeek: {BaseURL: "https://api.deepseek.example/v1"},
	})
	p.call = func(_ context.Context, cfg llm.Config) (*llm.ProbeResult, error) {
		got = cfg
		return &llm.ProbeResult{}, nil
	}

	res, err := p.Probe(context.Background(), LLMProbeInput{
		Provider: " DeepSeek ",
		APIKey:   "secret-key",
		Model:    " deepseek-chat ",
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.Valid || res.Code != LLMProbeCodeOK {
		t.Fatalf("result = %+v", res)
	}
	if got.APIKey != "secret-key" || got.Model != "deepseek-chat" || got.BaseURL != "https://api.deepseek.example/v1" {
		t.Errorf("effective config = %+v", got)
	}
	if got.Timeout != defaultLLMProbeTimeout {
		t.Errorf("timeout = %s", got.Timeout)
	}
}

func TestLLMConfigProbe_WhenInputInvalid_DoesNotCallProvider(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   LLMProbeInput
		code string
	}{
		{name: "unsupported provider", in: LLMProbeInput{Provider: "other", APIKey: "key", Model: "m"}, code: LLMProbeCodeUnsupportedProvider},
		{name: "missing key", in: LLMProbeInput{Provider: "openai", Model: "m"}, code: LLMProbeCodeMissingAPIKey},
		{name: "missing model", in: LLMProbeInput{Provider: "openai", APIKey: "key"}, code: LLMProbeCodeMissingModel},
		{name: "custom missing URL", in: LLMProbeInput{Provider: "custom", APIKey: "key", Model: "m"}, code: LLMProbeCodeMissingBaseURL},
		{name: "unsupported scheme", in: LLMProbeInput{Provider: "custom", APIKey: "key", Model: "m", BaseURL: "file:///tmp/model"}, code: LLMProbeCodeInvalidBaseURL},
		{name: "missing host", in: LLMProbeInput{Provider: "custom", APIKey: "key", Model: "m", BaseURL: "http:///v1"}, code: LLMProbeCodeInvalidBaseURL},
		{name: "userinfo", in: LLMProbeInput{Provider: "custom", APIKey: "key", Model: "m", BaseURL: "https://user:pass@example.com/v1"}, code: LLMProbeCodeInvalidBaseURL},
		{name: "query", in: LLMProbeInput{Provider: "custom", APIKey: "key", Model: "m", BaseURL: "https://example.com/v1?token=x"}, code: LLMProbeCodeInvalidBaseURL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewLLMConfigProbe(nil)
			p.call = func(context.Context, llm.Config) (*llm.ProbeResult, error) {
				t.Fatal("provider must not be called")
				return nil, nil
			}
			res, err := p.Probe(context.Background(), tc.in)
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if res.Valid || res.Code != tc.code {
				t.Errorf("result = %+v, want code %q", res, tc.code)
			}
		})
	}
}

func TestClassifyLLMProbeError_DistinguishesFailureReasons(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		code string
	}{
		{name: "authentication", err: apiError(http.StatusUnauthorized, "invalid_api_key", "invalid api key"), code: LLMProbeCodeAuthentication},
		{name: "permission", err: apiError(http.StatusForbidden, "permission_denied", "project has no access"), code: LLMProbeCodePermission},
		{name: "model not found", err: apiError(http.StatusNotFound, "model_not_found", "model does not exist"), code: LLMProbeCodeModelNotFound},
		{name: "quota", err: apiError(http.StatusTooManyRequests, "insufficient_quota", "credit balance exhausted"), code: LLMProbeCodeQuotaExceeded},
		{name: "rate limit", err: apiError(http.StatusTooManyRequests, "rate_limit", "too many requests"), code: LLMProbeCodeRateLimited},
		{name: "provider unavailable", err: apiError(http.StatusServiceUnavailable, "", "temporarily unavailable"), code: LLMProbeCodeProviderUnavailable},
		{name: "invalid request", err: apiError(http.StatusBadRequest, "invalid_request", "unsupported parameter"), code: LLMProbeCodeInvalidRequest},
		{name: "endpoint not found", err: &openai.RequestError{HTTPStatusCode: http.StatusNotFound, Body: []byte("not found")}, code: LLMProbeCodeEndpointNotFound},
		{name: "timeout", err: context.DeadlineExceeded, code: LLMProbeCodeTimeout},
		{name: "canceled", err: context.Canceled, code: LLMProbeCodeCanceled},
		{name: "dns", err: &url.Error{Op: "Post", URL: "https://bad.invalid", Err: &net.DNSError{Name: "bad.invalid", Err: "no such host"}}, code: LLMProbeCodeDNS},
		{name: "connection", err: &url.Error{Op: "Post", URL: "http://127.0.0.1:1", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}, code: LLMProbeCodeConnection},
		{name: "tls", err: &url.Error{Op: "Post", URL: "https://example.com", Err: x509.UnknownAuthorityError{}}, code: LLMProbeCodeTLS},
		{name: "invalid response", err: errors.New("llm probe: empty choices in response"), code: LLMProbeCodeInvalidResponse},
		{name: "unknown", err: errors.New("unexpected provider failure"), code: LLMProbeCodeUpstream},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, _ := classifyLLMProbeError(fmt.Errorf("probe: %w", tc.err), "secret-key")
			if code != tc.code {
				t.Errorf("code = %q, want %q", code, tc.code)
			}
		})
	}
}

func TestClassifyLLMProbeError_RedactsAPIKeyAndBoundsDetail(t *testing.T) {
	t.Parallel()

	key := "secret-key-value"
	message := "provider echoed " + key + " " + strings.Repeat("x", 400)
	_, detail := classifyLLMProbeError(apiError(http.StatusBadRequest, "invalid", message), key)
	if strings.Contains(detail, key) {
		t.Fatalf("detail leaked key: %q", detail)
	}
	if len([]rune(detail)) > maxLLMProbeDetailRunes+1 {
		t.Fatalf("detail rune count = %d", len([]rune(detail)))
	}
}

func TestLLMConfigProbe_WhenProviderFails_ReturnsTypedResult(t *testing.T) {
	t.Parallel()

	p := NewLLMConfigProbe(nil)
	p.timeout = time.Second
	p.call = func(context.Context, llm.Config) (*llm.ProbeResult, error) {
		return nil, apiError(http.StatusUnauthorized, "invalid_api_key", "bad key")
	}
	res, err := p.Probe(context.Background(), LLMProbeInput{
		Provider: settingmodel.LLMProviderOpenAI,
		APIKey:   "secret-key",
		Model:    "gpt-test",
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Valid || res.Code != LLMProbeCodeAuthentication {
		t.Fatalf("result = %+v", res)
	}
}

func apiError(status int, code, message string) error {
	return &openai.APIError{HTTPStatusCode: status, Code: code, Message: message}
}
