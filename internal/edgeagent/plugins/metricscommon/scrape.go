// Package metricscommon contains shared helpers for edge plugins that scrape
// Prometheus exposition endpoints and push the resulting samples through the
// existing tunnel path.
package metricscommon

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prometheus/common/expfmt"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Target is one HTTP /metrics endpoint to scrape.
type Target struct {
	ID                 string
	Name               string
	URL                string
	Enabled            bool
	Interval           time.Duration
	Timeout            time.Duration
	TLSInsecure        bool
	BearerToken        string
	BasicUsername      string
	BasicPassword      string
	SourceLabel        string
	ExtraLabels        map[string]string
	SampleLimit        int
	LabelDrop          []string
	Kind               string
	ReportScrapeStatus bool
}

const (
	DefaultInterval = 30 * time.Second
	DefaultTimeout  = 5 * time.Second
	// ScrapeUpMetricName mirrors Prometheus's synthetic scrape health metric.
	// Edge-side scrapers push it because these targets are not scraped by the
	// central Prometheus server directly.
	ScrapeUpMetricName              = "up"
	ScrapePartialMetricName         = "ongrid_scrape_partial"
	ScrapeAcceptedSamplesMetricName = "ongrid_scrape_samples_accepted"
)

// SampleLimitError reports that a streaming scrape observed at least one
// sample beyond the target limit. Observed is a lower bound because parsing
// stops immediately after the first excess sample.
type SampleLimitError struct {
	Observed int
	Limit    int
}

func (e *SampleLimitError) Error() string {
	return fmt.Sprintf("sample limit exceeded: observed at least %d limit %d", e.Observed, e.Limit)
}

// ScrapeStats describes one streaming scrape. Observed is exact for complete
// scrapes and Accepted+1 when LimitExceeded is true because parsing stops at
// the first excess sample.
type ScrapeStats struct {
	Observed      int
	Accepted      int
	LimitExceeded bool
}

// Scrape performs one GET, parses the Prometheus text response, applies
// target-side cardinality controls, and returns flat samples.
func Scrape(ctx context.Context, target Target) ([]tunnel.PromSample, error) {
	var samples []tunnel.PromSample
	stats, err := ScrapeIncremental(ctx, target, func(chunk []tunnel.PromSample) error {
		samples = append(samples, chunk...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if stats.LimitExceeded {
		return samples, &SampleLimitError{
			Observed: stats.Observed,
			Limit:    target.SampleLimit,
		}
	}
	return samples, nil
}

// ScrapeIncremental performs one GET and parses Prometheus text exposition one
// line at a time. It stops reading after the first sample beyond SampleLimit,
// so response size, parser memory, and flattened sample work stay bounded.
func ScrapeIncremental(ctx context.Context, target Target, consume func([]tunnel.PromSample) error) (ScrapeStats, error) {
	var stats ScrapeStats
	if consume == nil {
		return stats, fmt.Errorf("sample consumer required")
	}
	resp, err := openScrapeResponse(ctx, target)
	if err != nil {
		return stats, err
	}
	stats, scrapeErr := streamTextSamples(ctx, resp.Body, target, consume)
	closeErr := resp.Body.Close()
	if scrapeErr != nil {
		return stats, scrapeErr
	}
	if closeErr != nil {
		return stats, fmt.Errorf("close response body: %w", closeErr)
	}
	return stats, nil
}

func openScrapeResponse(ctx context.Context, target Target) (*http.Response, error) {
	if target.URL == "" {
		return nil, fmt.Errorf("target_url required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", string(expfmt.NewFormat(expfmt.TypeTextPlain)))
	if target.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+target.BearerToken)
	}
	if target.BasicUsername != "" || target.BasicPassword != "" {
		req.SetBasicAuth(target.BasicUsername, target.BasicPassword)
	}
	resp, err := httpClient(target).Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		_, drainErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		closeErr := resp.Body.Close()
		if drainErr != nil {
			return nil, fmt.Errorf("http status %d: drain response: %w", resp.StatusCode, drainErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("http status %d: close response: %w", resp.StatusCode, closeErr)
		}
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}
	return resp, nil
}

// ScrapeUpSample returns the synthetic target availability sample for one
// edge-side scrape. Labels are intentionally limited to low-cardinality source
// metadata; target URL and error text must not become metric labels.
func ScrapeUpSample(now time.Time, plugin string, target Target, up bool) tunnel.PromSample {
	labels := scrapeStatusLabels(plugin, target)
	value := 0.0
	if up {
		value = 1
	}
	return tunnel.PromSample{
		Name:   ScrapeUpMetricName,
		Labels: labels,
		Value:  value,
		TsMs:   now.UnixMilli(),
	}
}

// ScrapeStatusSamples exposes whether a streamed scrape was partial and the
// caller-reported accepted sample count. These travel through the same tunnel
// as the scraped data, so central Prometheus can distinguish complete and
// truncated scrapes even when the edge process /metrics endpoint is not scraped.
func ScrapeStatusSamples(now time.Time, plugin string, target Target, partial bool, accepted int) []tunnel.PromSample {
	partialValue := 0.0
	if partial {
		partialValue = 1
	}
	return []tunnel.PromSample{
		{
			Name:   ScrapePartialMetricName,
			Labels: scrapeStatusLabels(plugin, target),
			Value:  partialValue,
			TsMs:   now.UnixMilli(),
		},
		{
			Name:   ScrapeAcceptedSamplesMetricName,
			Labels: scrapeStatusLabels(plugin, target),
			Value:  float64(accepted),
			TsMs:   now.UnixMilli(),
		},
	}
}

func scrapeStatusLabels(plugin string, target Target) map[string]string {
	labels := make(map[string]string, len(target.ExtraLabels)+4)
	for k, v := range target.ExtraLabels {
		k = strings.TrimSpace(k)
		if k != "" {
			labels[k] = v
		}
	}
	if plugin != "" {
		labels["plugin"] = plugin
	}
	if target.ID != "" {
		labels["target_id"] = target.ID
	}
	if target.Name != "" {
		labels["target_name"] = target.Name
	}
	if target.Kind != "" {
		labels["kind"] = target.Kind
	}
	return labels
}

// ValidateURL checks the target URL shape early during plugin Configure.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("missing host")
	}
	return nil
}

func httpClient(target Target) *http.Client {
	tr := &http.Transport{
		MaxIdleConns:        2,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: (&net.Dialer{
			Timeout: 2 * time.Second,
		}).DialContext,
	}
	if target.TLSInsecure {
		tr.TLSClientConfig.InsecureSkipVerify = true
	}
	timeout := target.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

func applyLabelDrop(samples []tunnel.PromSample, drops []string) {
	if len(drops) == 0 {
		return
	}
	drop := make(map[string]struct{}, len(drops))
	for _, d := range drops {
		d = strings.TrimSpace(d)
		if d != "" {
			drop[d] = struct{}{}
		}
	}
	if len(drop) == 0 {
		return
	}
	for i := range samples {
		for key := range drop {
			delete(samples[i].Labels, key)
		}
	}
}
