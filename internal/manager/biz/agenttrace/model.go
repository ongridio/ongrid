// Package agenttrace reads Agent telemetry from the existing Tempo query plane.
package agenttrace

import (
	"encoding/json"
	"time"
)

type Identity struct {
	ServiceName      string `json:"service_name"`
	ServiceNamespace string `json:"service_namespace"`
	Environment      string `json:"environment"`
}

// Attributes contain decoded OTLP AnyValues; SDK payloads have no fixed schema.
type Attributes map[string]any

type Content struct {
	State         string   `json:"state"`
	Source        string   `json:"source,omitempty"`
	Value         any      `json:"value,omitempty"`
	Truncated     bool     `json:"truncated,omitempty"`
	OriginalBytes int      `json:"original_bytes,omitempty"`
	Omitted       []string `json:"omitted,omitempty"`
}

type Usage struct {
	InputTokens  *int64   `json:"input_tokens"`
	OutputTokens *int64   `json:"output_tokens"`
	TotalTokens  *int64   `json:"total_tokens"`
	CachedTokens *int64   `json:"cached_tokens"`
	ReportedCost *float64 `json:"reported_cost"`
	Currency     string   `json:"currency,omitempty"`
	Conflict     bool     `json:"conflict"`
	Complete     bool     `json:"complete"`
}

type Scope struct {
	Name       string     `json:"name"`
	Version    string     `json:"version"`
	Attributes Attributes `json:"attributes,omitempty"`
}

type Event struct {
	Name         string     `json:"name"`
	TimeUnixNano string     `json:"time_unix_nano"`
	Attributes   Attributes `json:"attributes"`
}

type Link struct {
	TraceID    string     `json:"trace_id"`
	SpanID     string     `json:"span_id"`
	Attributes Attributes `json:"attributes"`
}

type Step struct {
	SpanID        string          `json:"span_id"`
	ParentSpanID  string          `json:"parent_span_id"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	SDK           string          `json:"sdk"`
	Identity      Identity        `json:"identity"`
	Start         time.Time       `json:"start"`
	DurationMS    float64         `json:"duration_ms"`
	Status        string          `json:"status"`
	Model         string          `json:"model"`
	Provider      string          `json:"provider"`
	SessionID     string          `json:"session_id"`
	Input         Content         `json:"input"`
	Output        Content         `json:"output"`
	Usage         Usage           `json:"usage"`
	Scope         Scope           `json:"scope"`
	Attributes    Attributes      `json:"attributes"`
	Resource      Attributes      `json:"resource"`
	Events        []Event         `json:"events"`
	Links         []Link          `json:"links"`
	Issues        []string        `json:"issues"`
	Raw           json.RawMessage `json:"raw"`
	ChatSessionID string          `json:"chat_session_id,omitempty"`
}

type Run struct {
	TraceID          string    `json:"trace_id"`
	EntrySpanID      string    `json:"entry_span_id"`
	Identity         Identity  `json:"identity"`
	Name             string    `json:"name"`
	SDKs             []string  `json:"sdks"`
	Start            time.Time `json:"start"`
	DurationMS       float64   `json:"duration_ms"`
	Status           string    `json:"status"`
	Models           []string  `json:"models"`
	Kinds            []string  `json:"kinds"`
	SessionID        string    `json:"session_id"`
	StepCount        int       `json:"step_count"`
	ErrorCount       int       `json:"error_count"`
	InputPreview     string    `json:"input_preview"`
	OutputPreview    string    `json:"output_preview"`
	ContentAvailable bool      `json:"content_available"`
	InputState       string    `json:"input_state"`
	OutputState      string    `json:"output_state"`
	ChatSessionID    string    `json:"chat_session_id,omitempty"`
	Usage            Usage     `json:"usage"`
	Issues           []string  `json:"issues"`
}

type Trace struct {
	TraceID string   `json:"trace_id"`
	Runs    []Run    `json:"runs"`
	Steps   []Step   `json:"steps"`
	Issues  []string `json:"issues"`
}
