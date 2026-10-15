package tracing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ongridio/ongrid/internal/pkg/k8sredact"
)

const AgentContentMaxBytes = 8 * 1024
const AgentContentSchema = "ongrid.trace.content/v1"

var bearerCredential = regexp.MustCompile(`(?i)\bBearer\s+[^\s"',;]+`)

// AgentContentEnabled is opt-in because traces use deployment-wide telemetry access.
func AgentContentEnabled() bool {
	enabled, _ := strconv.ParseBool(os.Getenv("ONGRID_AGENT_TRACE_CAPTURE_CONTENT"))
	return enabled
}

// RecordAgentContent snapshots execution data; readers never need the chat database.
func RecordAgentContent(span trace.Span, key string, value any) {
	if !span.IsRecording() || !AgentContentEnabled() {
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		span.SetAttributes(attribute.String("ongrid.agent.content.error", "serialization_failed"))
		return
	}
	span.SetAttributes(attribute.String(key, MaskAgentContent(string(encoded))))
}

// MaskAgentContent is also the official Eino callback's pre-export mask.
// shortcut: credential masking recognizes common fields and inline forms; use a policy-specific scrubber for other sensitive data.
func MaskAgentContent(raw string) string {
	if !AgentContentEnabled() {
		return ""
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	wasJSON := json.Valid([]byte(raw))
	if !wasJSON || decoder.Decode(&value) != nil {
		value = raw
	}
	if value == nil {
		return ""
	}
	omitted := []string{}
	value = cleanAgentContent(value, &omitted, 0)
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	if len(omitted) == 0 && len(encoded) <= AgentContentMaxBytes {
		if !wasJSON {
			return value.(string)
		}
		return string(encoded)
	}
	snapshot := map[string]any{
		"schema": AgentContentSchema, "value": value,
		"original_bytes": len(raw), "omitted": omitted,
	}
	encoded, _ = json.Marshal(snapshot)
	if len(encoded) <= AgentContentMaxBytes {
		return string(encoded)
	}
	preview, _ := json.Marshal(value)
	if text, ok := value.(string); ok {
		preview = []byte(text)
	}
	snapshot["truncated"] = true
	// Keep valid JSON and UTF-8 even when the original value is a large structure.
	for size := AgentContentMaxBytes / 2; size >= 0; size /= 2 {
		prefix := preview[:min(size, len(preview))]
		for !utf8.Valid(prefix) {
			prefix = prefix[:len(prefix)-1]
		}
		snapshot["value"] = string(prefix)
		encoded, _ = json.Marshal(snapshot)
		if len(encoded) <= AgentContentMaxBytes {
			return string(encoded)
		}
		if size == 0 {
			return ""
		}
	}
	return ""
}

func cleanAgentContent(value any, omitted *[]string, depth int) any {
	omit := func(reason string) {
		if !slices.Contains(*omitted, reason) {
			*omitted = append(*omitted, reason)
		}
	}
	if depth > 20 {
		omit("depth_limit")
		return "[omitted]"
	}
	switch v := value.(type) {
	case map[string]any:
		role, _ := v["role"].(string)
		if role == "system" || role == "developer" {
			body, _ := json.Marshal(v)
			hash := sha256.Sum256(body)
			omit("system_prompt")
			return map[string]any{"role": role, "content": "[system instructions omitted]", "original_bytes": len(body), "sha256": hex.EncodeToString(hash[:])}
		}
		for key, item := range v {
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			switch {
			case normalized == "reasoningcontent" || normalized == "reasoning" || normalized == "thinking":
				delete(v, key)
				if item != nil && item != "" {
					omit("reasoning")
				}
			case normalized == "images" || strings.Contains(normalized, "multicontent") || normalized == "audio" || normalized == "video" || normalized == "imageurl":
				delete(v, key)
				if item != nil {
					omit("media")
				}
			case k8sredact.IsSensitiveKey(key) && normalized != "tokenusage" && !strings.HasSuffix(normalized, "tokens") && !strings.HasSuffix(normalized, "tokencount"), normalized == "cookie" || normalized == "setcookie":
				v[key] = "[REDACTED]"
				omit("credentials")
			default:
				v[key] = cleanAgentContent(item, omitted, depth+1)
			}
		}
		return v
	case []any:
		messageArray := len(v) > 0
		for _, item := range v {
			message, ok := item.(map[string]any)
			if !ok || message["role"] == nil {
				messageArray = false
				break
			}
		}
		if messageArray && len(v) > 12 {
			system, messages := []any{}, []any{}
			for _, item := range v {
				role := item.(map[string]any)["role"]
				if role == "system" || role == "developer" {
					system = append(system, item)
				} else {
					messages = append(messages, item)
				}
			}
			if len(messages) > 12 {
				omit("earlier_messages")
				messages = messages[len(messages)-12:]
			}
			v = append(system, messages...)
		}
		for i := range v {
			v[i] = cleanAgentContent(v[i], omitted, depth+1)
		}
		return v
	case string:
		// Tool arguments/results are frequently JSON encoded inside a message string.
		if depth < 20 && (strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[")) && json.Valid([]byte(v)) {
			var nested any
			decoder := json.NewDecoder(bytes.NewBufferString(v))
			decoder.UseNumber()
			if decoder.Decode(&nested) == nil {
				encoded, _ := json.Marshal(cleanAgentContent(nested, omitted, depth+1))
				return string(encoded)
			}
		}
		clean := k8sredact.Text(bearerCredential.ReplaceAllString(v, "Bearer [REDACTED]"))
		if clean != v {
			omit("credentials")
		}
		if strings.HasPrefix(v, "data:") {
			omit("media")
			return "[media omitted]"
		}
		return clean
	default:
		return value
	}
}
