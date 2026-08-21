package logquery

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	klogLevelPattern = regexp.MustCompile(`^\s*([IWEF])\d{4}\s`)
	textLevelPattern = regexp.MustCompile(`(?i)(?:^|[^[:alpha:]])(trace|debug|info(?:rmation)?|notice|warn(?:ing)?|error|err|critical|crit|fatal|panic)(?:[^[:alpha:]]|$)`)
)

func normalizeLevel(value string) string {
	level := strings.Trim(strings.ToLower(value), " \t\r\n\"'[](){}:;,")
	switch level {
	case "i", "info", "information":
		return "info"
	case "w", "warn", "warning":
		return "warn"
	case "e", "err", "error":
		return "error"
	case "crit":
		return "critical"
	case "f":
		return "fatal"
	default:
		return level
	}
}

func detectLevel(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "unknown"
	}
	if level := jsonLevel(trimmed); level != "" {
		return level
	}
	if match := klogLevelPattern.FindStringSubmatch(trimmed); len(match) == 2 {
		return normalizeLevel(match[1])
	}
	if match := textLevelPattern.FindStringSubmatch(trimmed); len(match) == 2 {
		return normalizeLevel(match[1])
	}
	return "unknown"
}

func jsonLevel(message string) string {
	if !strings.HasPrefix(message, "{") {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(message), &object); err != nil {
		return ""
	}
	for _, candidate := range []string{"level", "severity", "severity_text"} {
		for key, value := range object {
			if strings.EqualFold(key, candidate) {
				if text, ok := value.(string); ok {
					return normalizeLevel(text)
				}
			}
		}
	}
	return ""
}

func severityNumberForLevel(level string) int32 {
	switch normalizeLevel(level) {
	case "trace":
		return 1
	case "debug":
		return 5
	case "info", "notice":
		return 9
	case "warn":
		return 13
	case "error":
		return 17
	case "critical", "fatal", "panic":
		return 21
	default:
		return 0
	}
}
