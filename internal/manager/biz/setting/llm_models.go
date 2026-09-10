package setting

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	settingmodel "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	openai "github.com/sashabaranov/go-openai"
)

// ErrLLMModelsUnavailable hides untrusted upstream response details while
// still allowing the HTTP layer to return a stable gateway error.
var ErrLLMModelsUnavailable = errors.New("provider models unavailable")

// LLMModelsResult is a provider catalog fetched from an unsaved draft. It does
// not imply that every returned model supports chat completions.
type LLMModelsResult struct {
	Models    []string `json:"models"`
	Truncated bool     `json:"truncated"`
}

// FetchModels queries only the supplied draft and never persists credentials.
func (s *LLMConfigurationService) FetchModels(ctx context.Context, in LLMProbeInput) (LLMModelsResult, error) {
	if s == nil || s.probe == nil {
		return LLMModelsResult{}, errors.New("llm configuration service not wired")
	}
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if in.TLSInsecure && provider != settingmodel.LLMProviderCustom {
		return LLMModelsResult{}, fmt.Errorf("TLS override is only allowed for custom providers: %w", errs.ErrInvalid)
	}
	if !isKnownLLMProvider(provider) {
		return LLMModelsResult{}, fmt.Errorf("unsupported provider: %w", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.APIKey) == "" || len(in.APIKey) > maxLLMAPIKeyBytes {
		return LLMModelsResult{}, fmt.Errorf("valid api key is required: %w", errs.ErrInvalid)
	}
	baseURL := strings.TrimSpace(in.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(s.probe.defaults[provider].BaseURL)
	}
	if provider == settingmodel.LLMProviderCustom && baseURL == "" {
		return LLMModelsResult{}, fmt.Errorf("base URL is required: %w", errs.ErrInvalid)
	}
	if baseURL != "" && (len(baseURL) > maxLLMBaseURLBytes || validateLLMBaseURL(baseURL) != nil) {
		return LLMModelsResult{}, fmt.Errorf("invalid base URL: %w", errs.ErrInvalid)
	}

	config := openai.DefaultConfig(in.APIKey)
	if baseURL != "" {
		config.BaseURL = strings.TrimRight(baseURL, "/")
	}
	transport := llm.NewHTTPTransport(in.TLSInsecure)
	defer transport.CloseIdleConnections()
	config.HTTPClient = &http.Client{
		Transport: transport,
		Timeout:   defaultLLMProbeTimeout,
		// Never forward draft credentials to a redirected endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("model endpoint redirects are not allowed")
		},
	}
	queryCtx, cancel := context.WithTimeout(ctx, defaultLLMProbeTimeout)
	defer cancel()
	catalog, err := openai.NewClientWithConfig(config).ListModels(queryCtx)
	if err != nil {
		code, _ := classifyLLMProbeError(err, in.APIKey)
		return LLMModelsResult{}, fmt.Errorf("fetch models failed (%s): %w", code, ErrLLMModelsUnavailable)
	}

	models := make([]string, 0, len(catalog.Models))
	seen := make(map[string]bool)
	for _, item := range catalog.Models {
		id := strings.TrimSpace(item.ID)
		if id == "" || len(id) > maxLLMModelBytes || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
	}
	sort.Strings(models)
	truncated := len(models) > maxLLMModels
	if truncated {
		models = models[:maxLLMModels]
	}
	return LLMModelsResult{Models: models, Truncated: truncated}, nil
}
