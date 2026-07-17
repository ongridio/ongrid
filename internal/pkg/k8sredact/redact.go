package k8sredact

import (
	"regexp"
	"strings"
)

var sensitiveTextPattern = regexp.MustCompile(`(?i)(authorization|token|password|passwd|secret|api[_-]?key|client[_-]?secret)\s*[:=]\s*([^\s,;]+)`)
var credentialURLPattern = regexp.MustCompile(`(?i)(https?://[^:/\s]+:)[^@/\s]+@`)

// Text redacts common inline credentials while preserving surrounding context.
func Text(value string) string {
	value = sensitiveTextPattern.ReplaceAllString(value, "$1=[REDACTED]")
	return credentialURLPattern.ReplaceAllString(value, "$1[REDACTED]@")
}

// StringMap returns a redacted copy of labels or annotations.
func StringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return values
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if IsSensitiveKey(key) {
			out[key] = "[REDACTED]"
			continue
		}
		out[key] = Text(value)
	}
	return out
}

// IsSensitiveKey reports whether a Kubernetes field name commonly carries credentials.
func IsSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(key))
	for _, marker := range []string{"authorization", "token", "password", "passwd", "secret", "apikey", "credential", "privatekey"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
