package custommetrics

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins/metricscommon"
)

func parseSpec(spec map[string]interface{}) ([]metricscommon.Target, error) {
	rawTargets, ok := spec["targets"]
	if !ok {
		return nil, nil
	}
	items, ok := rawTargets.([]interface{})
	if !ok {
		return nil, fmt.Errorf("targets must be an array")
	}
	out := make([]metricscommon.Target, 0, len(items))
	seen := map[string]struct{}{}
	for i, raw := range items {
		m, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("targets[%d] must be an object", i)
		}
		t, err := parseTarget(i, m)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[t.ID]; exists {
			return nil, fmt.Errorf("targets[%d] duplicate id %q", i, t.ID)
		}
		seen[t.ID] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}

func parseTarget(i int, m map[string]interface{}) (metricscommon.Target, error) {
	id := stringFrom(m, "id")
	if id == "" {
		return metricscommon.Target{}, fmt.Errorf("targets[%d].id required", i)
	}
	targetURL := stringFrom(m, "target_url")
	if targetURL == "" {
		return metricscommon.Target{}, fmt.Errorf("targets[%d].target_url required", i)
	}
	if err := metricscommon.ValidateURL(targetURL); err != nil {
		return metricscommon.Target{}, fmt.Errorf("targets[%d].target_url: %w", i, err)
	}
	interval, err := durationFrom(m, "scrape_interval", metricscommon.DefaultInterval)
	if err != nil {
		return metricscommon.Target{}, fmt.Errorf("targets[%d].scrape_interval: %w", i, err)
	}
	timeout, err := durationFrom(m, "scrape_timeout", metricscommon.DefaultTimeout)
	if err != nil {
		return metricscommon.Target{}, fmt.Errorf("targets[%d].scrape_timeout: %w", i, err)
	}
	if timeout > interval {
		timeout = interval
	}
	enabled := boolFrom(m, "enabled", true)
	source := stringFrom(m, "source_label")
	if source == "" {
		source = "custom:" + id
	}
	auth := mapFrom(m, "auth")
	t := metricscommon.Target{
		ID:            id,
		Name:          firstNonEmpty(stringFrom(m, "name"), id),
		URL:           targetURL,
		Enabled:       enabled,
		Interval:      interval,
		Timeout:       timeout,
		TLSInsecure:   boolFrom(m, "tls_insecure", false),
		BearerToken:   firstNonEmpty(stringFrom(m, "bearer_token"), stringFrom(auth, "bearer_token")),
		BasicUsername: stringFrom(auth, "username"),
		BasicPassword: stringFrom(auth, "password"),
		SourceLabel:   source,
		ExtraLabels:   stringMap(m, "extra_labels"),
		SampleLimit:   intFrom(m, "sample_limit", 5000),
		LabelDrop:     stringSlice(m, "label_drop"),
		Kind:          "custom",
	}
	if t.SampleLimit < 0 {
		return metricscommon.Target{}, fmt.Errorf("targets[%d].sample_limit must be >= 0", i)
	}
	return t, nil
}

func stringFrom(m map[string]interface{}, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func boolFrom(m map[string]interface{}, key string, def bool) bool {
	raw, ok := m[key]
	if !ok {
		return def
	}
	b, ok := raw.(bool)
	if !ok {
		return def
	}
	return b
}

func intFrom(m map[string]interface{}, key string, def int) int {
	raw, ok := m[key]
	if !ok {
		return def
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return def
}

func durationFrom(m map[string]interface{}, key string, def time.Duration) (time.Duration, error) {
	v := stringFrom(m, key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be > 0")
	}
	return d, nil
}

func mapFrom(m map[string]interface{}, key string) map[string]interface{} {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	v, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	return v
}

func stringMap(m map[string]interface{}, key string) map[string]string {
	raw := mapFrom(m, key)
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = s
		}
	}
	return out
}

func stringSlice(m map[string]interface{}, key string) []string {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
