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
	"sort"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/ongridio/ongrid/internal/edgeagent/collector"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Target is one HTTP /metrics endpoint to scrape.
type Target struct {
	ID            string
	Name          string
	URL           string
	Enabled       bool
	Interval      time.Duration
	Timeout       time.Duration
	TLSInsecure   bool
	BearerToken   string
	BasicUsername string
	BasicPassword string
	SourceLabel   string
	ExtraLabels   map[string]string
	SampleLimit   int
	LabelDrop     []string
	Kind          string
}

const (
	DefaultInterval = 30 * time.Second
	DefaultTimeout  = 5 * time.Second
)

// Scrape performs one GET, parses the Prometheus text response, applies
// target-side cardinality controls, and returns flat samples.
func Scrape(ctx context.Context, target Target) ([]tunnel.PromSample, error) {
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
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	samples := collector.FlattenSamples(time.Now(), target.SourceLabel, familiesToSlice(families), target.ExtraLabels)
	applyLabelDrop(samples, target.LabelDrop)
	if target.SampleLimit > 0 && len(samples) > target.SampleLimit {
		return nil, fmt.Errorf("sample limit exceeded: got %d limit %d", len(samples), target.SampleLimit)
	}
	return samples, nil
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

func familiesToSlice(in map[string]*dto.MetricFamily) []*dto.MetricFamily {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*dto.MetricFamily, 0, len(keys))
	for _, k := range keys {
		out = append(out, in[k])
	}
	return out
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
