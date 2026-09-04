package setting

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/promauth"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
	"github.com/ongridio/ongrid/internal/pkg/promwrite"
)

const maxPromProbeURLBytes = 2048

// PromProbeInput is an unsaved Prometheus-compatible backend draft. Secrets
// are used only for this probe and are never persisted or returned.
type PromProbeInput struct {
	QueryURL       string `json:"query_url"`
	RemoteWriteURL string `json:"remote_write_url"`
	BearerToken    string `json:"bearer_token"`
	BasicUser      string `json:"basic_user"`
	BasicPassword  string `json:"basic_password"`
	TLSInsecure    bool   `json:"tls_insecure"`
}

type PromConfigurationProbe struct {
	log *slog.Logger
}

func NewPromConfigurationProbe(log *slog.Logger) *PromConfigurationProbe {
	if log == nil {
		log = slog.Default()
	}
	return &PromConfigurationProbe{log: log}
}

// Probe validates the draft, runs a PromQL query, then sends an empty
// remote_write request. The write probe verifies connectivity without storing
// a metric.
func (p *PromConfigurationProbe) Probe(ctx context.Context, in PromProbeInput) error {
	queryURL := strings.TrimRight(strings.TrimSpace(in.QueryURL), "/")
	if err := validatePromProbeURL("query URL", queryURL); err != nil {
		return err
	}
	writeURL := strings.TrimSpace(in.RemoteWriteURL)
	if writeURL == "" {
		writeURL = promRemoteWriteURL(queryURL)
	}
	if err := validatePromProbeURL("remote write URL", writeURL); err != nil {
		return err
	}

	httpClient, err := promauth.BuildClient(
		promauth.TLSConfig{Insecure: in.TLSInsecure},
		promauth.NewStaticResolver(promauth.Config{
			BearerToken:   in.BearerToken,
			BasicUser:     in.BasicUser,
			BasicPassword: in.BasicPassword,
		}),
		10*time.Second,
	)
	if err != nil {
		return fmt.Errorf("build Prometheus probe client: %w", err)
	}
	if _, err := promquery.NewWithHTTPClient(queryURL, httpClient, p.log).Query(ctx, "up", time.Now()); err != nil {
		return fmt.Errorf("PromQL probe failed: %w", err)
	}
	if err := promwrite.NewWithWriteURLAndHTTPClient(writeURL, httpClient, p.log).Probe(ctx); err != nil {
		return fmt.Errorf("remote_write probe failed: %w", err)
	}
	return nil
}

func validatePromProbeURL(name, raw string) error {
	if raw == "" {
		return fmt.Errorf("%w: %s is required", errs.ErrInvalid, name)
	}
	if len(raw) > maxPromProbeURLBytes {
		return fmt.Errorf("%w: %s is too long", errs.ErrInvalid, name)
	}
	if err := validateLLMBaseURL(raw); err != nil {
		return fmt.Errorf("%w: invalid %s: %v", errs.ErrInvalid, name, err)
	}
	return nil
}
