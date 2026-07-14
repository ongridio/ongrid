package metrics

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
	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// specView is the parsed-and-defaulted shape of PluginConfig.Spec.
//
// URLs holds 1..N scrape targets. The plugin loops over them every
// tick and pushes each one's samples in a separate push_prom_samples
// RPC. Single-URL deployments leave len(URLs)==1; the default fans
// out to both node_exporter (:9102) and process_exporter (:9256) so a
// fresh edge produces both host- and process-level series via the
// tunnel without any operator config.
type specView struct {
	URLs                      []string
	Interval                  time.Duration
	Timeout                   time.Duration
	TLSInsecure               bool
	BearerToken               string
	ExtraLabels               map[string]string
	SourceLabel               string // value emitted on the wire as PushPromSamplesRequest.Source
	DedupeFilesystemsByDevice bool
}

// Defaults match the host/proc-metrics plugins' subprocesses
// (node_exporter on :9102, process_exporter on :9256 — see
// internal/edgeagent/plugins/hostmetrics, .../procmetrics).
// Localhost only because all processes live in the same systemd unit
// on the edge.
var defaultURLs = []string{
	"http://127.0.0.1:9102/metrics",
	"http://127.0.0.1:9256/metrics",
}

const (
	defaultInterval = 15 * time.Second
	defaultTimeout  = 5 * time.Second
)

// parseSpec reads PluginConfig.Spec into a typed view, applying defaults
// for missing keys. Returns an error only on shapes the operator can fix
// (bad duration string, malformed URL); silently ignores unknown keys.
//
// Three target shapes accepted (first one set wins):
//
//	target_urls: ["http://...", "http://..."]
//	target_url: "http://..." (legacy single-URL form)
//	<missing> → defaultURLs
func parseSpec(spec map[string]interface{}) (specView, error) {
	out := specView{
		URLs:     append([]string(nil), defaultURLs...),
		Interval: defaultInterval,
		Timeout:  defaultTimeout,
	}
	if spec == nil {
		// Empty source label = manager-side Ingester won't attach an
		// `ongrid_source` label, so push samples land looking identical
		// to old direct-scrape series. This is the desired default after
		// retired host.docker.internal scrape — there is
		// only one source now, no need to disambiguate.
		return out, nil
	}

	if urls := stringSlice(spec, "target_urls"); len(urls) > 0 {
		out.URLs = urls
	} else if v := stringFrom(spec, "target_url"); v != "" {
		out.URLs = []string{v}
	}
	if v := stringFrom(spec, "scrape_interval"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return out, fmt.Errorf("scrape_interval %q: %w", v, err)
		}
		if d <= 0 {
			return out, fmt.Errorf("scrape_interval must be > 0; got %v", d)
		}
		out.Interval = d
	}
	if v := stringFrom(spec, "scrape_timeout"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return out, fmt.Errorf("scrape_timeout %q: %w", v, err)
		}
		if d <= 0 {
			return out, fmt.Errorf("scrape_timeout must be > 0; got %v", d)
		}
		out.Timeout = d
	}
	if raw, ok := spec["tls_insecure"]; ok {
		if b, ok := raw.(bool); ok {
			out.TLSInsecure = b
		}
	}
	out.BearerToken = stringFrom(spec, "bearer_token")
	out.ExtraLabels = stringMap(spec, "extra_labels")
	out.DedupeFilesystemsByDevice = boolFrom(spec, "dedupe_filesystems_by_device")

	// Prevent obvious misconfig: timeout must not exceed interval, else
	// scrapes overlap themselves and hammer the target.
	if out.Timeout > out.Interval {
		out.Timeout = out.Interval
	}

	// Validate every URL early so bad config surfaces in HealthSnapshot
	// rather than as an HTTP error every tick.
	for _, u := range out.URLs {
		if _, err := url.Parse(u); err != nil {
			return out, fmt.Errorf("target_url %q: %w", u, err)
		}
	}
	// SourceLabel is opt-in via spec.source_label. Default empty so
	// push samples don't carry an ongrid_source label (see comment in
	// the spec==nil branch). Operators can still set one explicitly
	// when running multiple metrics plugins side-by-side.
	if v := stringFrom(spec, "source_label"); v != "" {
		out.SourceLabel = v
	}
	return out, nil
}

// sourceLabelForURL builds the wire-side `source` label. We use
// "metrics:<host>:<port>" so multiple metrics plugins (future) stay
// distinguishable in `ongrid_source` queries.
func sourceLabelForURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "metrics:unknown"
	}
	return "metrics:" + u.Host
}

// scrapeOnce performs one HTTP GET against targetURL, parses the
// Prometheus text response, and returns a flat sample slice ready for
// push_prom_samples plus the source label. Each entry in spec.URLs is
// scraped separately so a 200 from one target doesn't get masked by a
// failure from another.
func scrapeOnce(ctx context.Context, spec specView, targetURL string) ([]tunnel.PromSample, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, spec.SourceLabel, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", string(expfmt.NewFormat(expfmt.TypeTextPlain)))
	if spec.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+spec.BearerToken)
	}
	resp, err := newClient(spec).Do(req)
	if err != nil {
		return nil, spec.SourceLabel, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, spec.SourceLabel, fmt.Errorf("http status %d", resp.StatusCode)
	}

	var p expfmt.TextParser
	families, err := p.TextToMetricFamilies(resp.Body)
	if err != nil {
		return nil, spec.SourceLabel, fmt.Errorf("parse: %w", err)
	}
	mfs := familiesToSlice(families)
	now := time.Now()
	// FlattenSamples lives in internal/edgeagent/collector — it already
	// handles counter / gauge / histogram / summary fan-out plus
	// extraLabels merge. We don't reimplement.
	samples := collector.FlattenSamples(now, spec.SourceLabel, mfs, spec.ExtraLabels)
	if spec.DedupeFilesystemsByDevice {
		samples = dedupeFilesystemSamplesByDevice(samples)
	}
	return samples, spec.SourceLabel, nil
}

// dedupeFilesystemSamplesByDevice keeps one filesystem mountpoint per
// physical block device. Container and VM runtimes frequently expose the
// same /dev/... filesystem through many bind mounts, which otherwise
// multiplies every node_filesystem_* series without adding capacity
// information. Virtual devices such as tmpfs and virtiofs are preserved:
// identical device labels there can still describe independent filesystems.
func dedupeFilesystemSamplesByDevice(samples []tunnel.PromSample) []tunnel.PromSample {
	preferred := make(map[string]string)
	for _, sample := range samples {
		if !strings.HasPrefix(sample.Name, "node_filesystem_") {
			continue
		}
		device, mountpoint := sample.Labels["device"], sample.Labels["mountpoint"]
		if !isPhysicalBlockDevice(device) || mountpoint == "" {
			continue
		}
		if current, ok := preferred[device]; !ok || preferMountpoint(mountpoint, current) {
			preferred[device] = mountpoint
		}
	}

	out := samples[:0]
	for _, sample := range samples {
		if strings.HasPrefix(sample.Name, "node_filesystem_") {
			device, mountpoint := sample.Labels["device"], sample.Labels["mountpoint"]
			if isPhysicalBlockDevice(device) {
				if want, ok := preferred[device]; ok && mountpoint != want {
					continue
				}
			}
		}
		out = append(out, sample)
	}
	return out
}

func isPhysicalBlockDevice(device string) bool {
	return strings.HasPrefix(device, "/dev/")
}

func preferMountpoint(candidate, current string) bool {
	if candidate == "/" {
		return current != "/"
	}
	if current == "/" {
		return false
	}
	if len(candidate) != len(current) {
		return len(candidate) < len(current)
	}
	return candidate < current
}

// newClient builds a per-scrape HTTP client. We don't pool per-target
// because there's typically one scrape target per metrics plugin instance
// and the keep-alive savings don't justify the lifecycle complexity.
func newClient(spec specView) *http.Client {
	tr := &http.Transport{
		MaxIdleConns:        2,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		// Localhost scrape: short dial timeout so the overall scrape
		// timeout is dominated by the read, not by TCP backoff.
		DialContext: (&net.Dialer{
			Timeout: 2 * time.Second,
		}).DialContext,
	}
	if spec.TLSInsecure {
		tr.TLSClientConfig.InsecureSkipVerify = true
	}
	return &http.Client{
		Transport: tr,
		Timeout:   spec.Timeout,
	}
}

// familiesToSlice flattens the (deterministic) name→family map returned
// by expfmt into a slice with stable ordering — keeps tests reproducible.
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

// stringFrom extracts spec[key] as string, tolerating both string and
// fmt-stringable shapes that arrive from JSON decoding (rare for our
// schema but harmless to handle).
func stringFrom(spec map[string]interface{}, key string) string {
	raw, ok := spec[key]
	if !ok {
		return ""
	}
	if s, ok := raw.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func boolFrom(spec map[string]interface{}, key string) bool {
	raw, ok := spec[key]
	if !ok {
		return false
	}
	value, _ := raw.(bool)
	return value
}

// stringSlice extracts a []string from spec[key], tolerating both
// []string and the JSON-decoded []interface{} (whose elements may be
// strings). Empty / wrong-shape returns nil so callers can fall through
// to legacy single-URL form or the default.
func stringSlice(spec map[string]interface{}, key string) []string {
	raw, ok := spec[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	}
	return nil
}

// stringMap extracts a map[string]string from spec[key], tolerating the
// JSON-decoded map[string]interface{} shape.
func stringMap(spec map[string]interface{}, key string) map[string]string {
	raw, ok := spec[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case map[string]string:
		return v
	case map[string]interface{}:
		out := make(map[string]string, len(v))
		for k, val := range v {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
		return out
	}
	return nil
}

// Compile-time guard: the Plugin must satisfy plugins.Plugin.
var _ plugins.Plugin = (*Plugin)(nil)
