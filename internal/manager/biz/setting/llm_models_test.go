package setting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func TestFetchModelsCatalog(t *testing.T) {
	for _, count := range []int{0, 3, 40} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-only-key" {
					t.Error("incorrect upstream request")
				}
				data := []map[string]string{{"id": " "}, {"id": strings.Repeat("x", 257)}}
				for i := count - 1; i >= 0; i-- {
					data = append(data,
						map[string]string{"id": fmt.Sprintf(" model-%02d ", i)},
						map[string]string{"id": fmt.Sprintf("model-%02d", i)},
					)
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			svc := NewLLMConfigurationService(map[string]EnvProviderDefaults{
				"openai": {BaseURL: srv.URL + "/v1/"},
			}, nil)
			got, err := svc.FetchModels(context.Background(), LLMProbeInput{
				Provider: "openai",
				APIKey:   "test-only-key",
			})
			if err != nil {
				t.Fatal(err)
			}
			want := min(count, maxLLMModels)
			if len(got.Models) != want || got.Models == nil || got.Truncated != (count > maxLLMModels) {
				t.Fatalf("unexpected catalog: %+v", got)
			}
			for i, id := range got.Models {
				if id != fmt.Sprintf("model-%02d", i) {
					t.Errorf("unexpected id %q", id)
				}
			}
		})
	}
}

func TestFetchModelsValidation(t *testing.T) {
	svc := NewLLMConfigurationService(nil, nil)
	for _, in := range []LLMProbeInput{
		{Provider: "bad", APIKey: "x"},
		{Provider: "openai"},
		{Provider: "custom", APIKey: "x"},
		{Provider: "custom", APIKey: "x", BaseURL: "file:///tmp/a"},
		{Provider: "custom", APIKey: "x", BaseURL: "http://user:secret@localhost"},
		{Provider: "openai", APIKey: strings.Repeat("x", maxLLMAPIKeyBytes+1)},
		{Provider: "openai", APIKey: "x", TLSInsecure: true},
	} {
		if _, err := svc.FetchModels(context.Background(), in); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("expected invalid: %v", err)
		}
	}
}

func TestFetchModelsFailuresAreSecretFree(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError, http.StatusFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(status)
				if _, err := fmt.Fprint(w, `{"error":{"message":"test-only-key","type":"bad"}}`); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			svc := NewLLMConfigurationService(nil, nil)
			_, err := svc.FetchModels(context.Background(), LLMProbeInput{
				Provider: "custom",
				APIKey:   "test-only-key",
				BaseURL:  srv.URL,
			})
			if !errors.Is(err, ErrLLMModelsUnavailable) || strings.Contains(err.Error(), "test-only-key") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestFetchModelsDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	svc := NewLLMConfigurationService(nil, nil)
	svc.probe.timeout = 20 * time.Millisecond
	_, err := svc.FetchModels(context.Background(), LLMProbeInput{
		Provider: "custom",
		APIKey:   "test-only-key",
		BaseURL:  srv.URL,
	})
	if !errors.Is(err, ErrLLMModelsUnavailable) || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout: %v", err)
	}
}

func TestCustomTLSDiscoveryProbePersistenceAndRuntime(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"data":[{"id":"test-model"}]}`
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			body = `{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`
		}
		if _, err := fmt.Fprint(w, body); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	settings := New(newFakeRepo(), nil)
	svc := NewLLMConfigurationService(nil, settings)
	in := LLMProbeInput{
		Provider:     "custom",
		APIKey:       "test-only-key",
		BaseURL:      srv.URL + "/v1",
		DefaultModel: "test-model",
		Models:       []string{"test-model"},
	}
	if _, err := svc.FetchModels(context.Background(), in); err == nil {
		t.Fatal("self-signed certificate was accepted by default")
	}
	in.TLSInsecure = true
	if _, err := svc.FetchModels(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	result, err := svc.Save(context.Background(), in)
	if err != nil || !result.Saved {
		t.Fatalf("save: %+v %v", result, err)
	}

	resolver := NewLLMSettingsResolver(settings, nil, "custom")
	providers, _, err := resolver.ResolveProviders(context.Background())
	if err != nil || len(providers) != 1 || !providers[0].TLSInsecure {
		t.Fatalf("TLS setting not resolved: %v", err)
	}
	client := llm.NewMultiClient(providers, "custom", nil)
	if _, err = client.Chat(context.Background(), llm.ChatReq{
		Messages: []llm.Message{{Role: "user", Content: "OK"}},
	}); err != nil {
		t.Fatalf("runtime chat TLS: %v", err)
	}
}
