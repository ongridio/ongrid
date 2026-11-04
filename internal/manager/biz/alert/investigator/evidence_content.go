package investigator

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/k8sredact"
)

var evidenceBearer = regexp.MustCompile(`(?i)\bBearer\s+[^\s"',;]+`)

// shortcut: common credential forms are masked; raw evidence retains the
// transcript's owner/admin permission boundary for unstructured sensitive data.
func redactEvidence(raw string) string {
	var value any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if !json.Valid([]byte(raw)) || dec.Decode(&value) != nil {
		return k8sredact.Text(evidenceBearer.ReplaceAllString(raw, "Bearer [REDACTED]"))
	}
	var clean func(any, int) any
	clean = func(v any, depth int) any {
		if depth > 20 {
			return "[omitted]"
		}
		switch x := v.(type) {
		case map[string]any:
			for key, item := range x {
				if k8sredact.IsSensitiveKey(key) || strings.EqualFold(key, "cookie") || strings.EqualFold(key, "set-cookie") {
					x[key] = "[REDACTED]"
				} else {
					x[key] = clean(item, depth+1)
				}
			}
		case []any:
			for i := range x {
				x[i] = clean(x[i], depth+1)
			}
		case string:
			var nested any
			d := json.NewDecoder(strings.NewReader(x))
			d.UseNumber()
			if (strings.HasPrefix(x, "{") || strings.HasPrefix(x, "[")) && json.Valid([]byte(x)) && d.Decode(&nested) == nil {
				b, _ := json.Marshal(clean(nested, depth+1))
				return string(b)
			}
			return k8sredact.Text(evidenceBearer.ReplaceAllString(x, "Bearer [REDACTED]"))
		}
		return v
	}
	b, err := json.Marshal(clean(value, 0))
	if err != nil {
		return "[unavailable]"
	}
	return string(b)
}

// These are observed sample bounds, not an inferred query window. In particular,
// empty results have no observation timestamp and do not prove service health.
func observationBounds(entries []json.RawMessage, target string) (string, string, string) {
	var first, last time.Time
	targets := []string{}
	addTime := func(raw json.RawMessage) {
		s := strings.Trim(string(raw), `"`)
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			if n, e := strconv.ParseInt(s, 10, 64); e == nil && n > 1e15 {
				t = time.Unix(0, n)
			} else if seconds, e := strconv.ParseFloat(s, 64); e == nil && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) && seconds > 0 && seconds < 253402300799 {
				whole, fraction := math.Modf(seconds)
				t = time.Unix(int64(whole), int64(fraction*1e9))
			} else {
				return
			}
		}
		if first.IsZero() || t.Before(first) {
			first = t
		}
		if last.IsZero() || t.After(last) {
			last = t
		}
	}
	for _, entry := range entries {
		var row map[string]json.RawMessage
		if json.Unmarshal(entry, &row) != nil {
			continue
		}
		labels := row
		for _, key := range []string{"metric", "stream"} {
			if row[key] != nil {
				_ = json.Unmarshal(row[key], &labels)
				break
			}
		}
		for _, key := range []string{"device_id", "service_name", "pod", "instance"} {
			if v := strings.Trim(string(labels[key]), `"`); v != "" && v != "null" {
				targets = appendIssue(targets, key+"="+clipEvidence(redactEvidence(v), 80))
			}
		}
		if row["timestamp"] != nil {
			addTime(row["timestamp"])
		}
		var values [][]json.RawMessage
		_ = json.Unmarshal(row["values"], &values)
		var single []json.RawMessage
		if json.Unmarshal(row["value"], &single) == nil && len(single) > 0 {
			values = append(values, single)
		}
		for _, sample := range values {
			if len(sample) > 0 {
				addTime(sample[0])
			}
		}
	}
	if len(targets) > 0 {
		target = clipEvidence(strings.Join(targets, ", "), 300)
	}
	if first.IsZero() {
		return target, "", ""
	}
	return target, first.UTC().Format(time.RFC3339Nano), last.UTC().Format(time.RFC3339Nano)
}
