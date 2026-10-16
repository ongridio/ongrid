package agenttrace

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tracequery"
	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

type attribute struct {
	Key   string                     `json:"key"`
	Value map[string]json.RawMessage `json:"value"`
}

type rawScope struct {
	Name       string      `json:"name"`
	Version    string      `json:"version"`
	Attributes []attribute `json:"attributes"`
}

type scopeSpans struct {
	Scope   rawScope          `json:"scope"`
	Library rawScope          `json:"instrumentationLibrary"`
	Spans   []json.RawMessage `json:"spans"`
}

type resourceSpans struct {
	Resource struct {
		Attributes []attribute `json:"attributes"`
	} `json:"resource"`
	Scopes    []scopeSpans `json:"scopeSpans"`
	Libraries []scopeSpans `json:"instrumentationLibrarySpans"`
}

type rawSpan struct {
	TraceID    string      `json:"traceId"`
	SpanID     string      `json:"spanId"`
	ParentID   string      `json:"parentSpanId"`
	Name       string      `json:"name"`
	Start      json.Number `json:"startTimeUnixNano"`
	End        json.Number `json:"endTimeUnixNano"`
	Attributes []attribute `json:"attributes"`
	Status     struct {
		Code json.RawMessage `json:"code"`
	} `json:"status"`
	Events []struct {
		Name       string      `json:"name"`
		Time       json.Number `json:"timeUnixNano"`
		Attributes []attribute `json:"attributes"`
	} `json:"events"`
	Links []struct {
		TraceID    string      `json:"traceId"`
		SpanID     string      `json:"spanId"`
		Attributes []attribute `json:"attributes"`
	} `json:"links"`
	DroppedAttributes uint32 `json:"droppedAttributesCount"`
	DroppedEvents     uint32 `json:"droppedEventsCount"`
	DroppedLinks      uint32 `json:"droppedLinksCount"`
}

// Normalize accepts both Tempo's historical batches and OTLP resourceSpans.
// Unknown attributes, events, links, scope, and original spans remain inspectable.
func Normalize(traceID string, body []byte) (*Trace, error) {
	if len(body) > tracequery.MaxResponseBytes {
		return nil, tracequery.ErrResponseTooLarge
	}
	var envelope struct {
		Resources []resourceSpans `json:"resourceSpans"`
		Batches   []resourceSpans `json:"batches"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("agenttrace: decode trace: %w", err)
	}
	resources := append(envelope.Resources, envelope.Batches...)
	if len(resources) > 1000 {
		return nil, fmt.Errorf("agenttrace: resource limit exceeded")
	}
	out := &Trace{TraceID: traceID, Runs: []Run{}, Steps: []Step{}, Issues: []string{}}
	seen := map[string]bool{}
	for _, resource := range resources {
		attrs := decodeAttributes(resource.Resource.Attributes)
		identity := Identity{stringAttr(attrs, "service.name"), stringAttr(attrs, "service.namespace"), stringAttr(attrs, "deployment.environment.name", "deployment.environment")}
		for _, group := range append(resource.Scopes, resource.Libraries...) {
			scope := group.Scope
			if scope.Name == "" {
				scope = group.Library
			}
			for _, raw := range group.Spans {
				if len(out.Steps) >= 20000 {
					return nil, fmt.Errorf("agenttrace: span limit exceeded")
				}
				var span rawSpan
				if err := json.Unmarshal(raw, &span); err != nil {
					return nil, fmt.Errorf("agenttrace: decode span: %w", err)
				}
				id := canonicalID(span.SpanID, 8)
				if id == "" || (span.TraceID != "" && canonicalID(span.TraceID, 16) != traceID) {
					out.Issues = appendUnique(out.Issues, "invalid_span_identity")
					continue
				}
				if seen[id] {
					out.Issues = appendUnique(out.Issues, "duplicate_span_id")
					continue
				}
				seen[id] = true
				a := decodeAttributes(span.Attributes)
				step := Step{SpanID: id, ParentSpanID: canonicalID(span.ParentID, 8), Name: span.Name, Identity: identity,
					Attributes: a, Resource: attrs, Scope: Scope{scope.Name, scope.Version, decodeAttributes(scope.Attributes)}, Raw: raw,
					Events: []Event{}, Links: []Link{}, Issues: []string{}}
				start, startErr := span.Start.Int64()
				end, endErr := span.End.Int64()
				if startErr == nil && start > 0 {
					step.Start = time.Unix(0, start).UTC()
				} else {
					step.Issues = append(step.Issues, "invalid_start_time")
				}
				if endErr == nil && end >= start && start > 0 {
					step.DurationMS = float64(end-start) / 1e6
				} else {
					step.Issues = append(step.Issues, "invalid_end_time")
				}
				step.Status = "unset"
				switch string(span.Status.Code) {
				case "2", `"STATUS_CODE_ERROR"`:
					step.Status = "error"
				case "1", `"STATUS_CODE_OK"`:
					step.Status = "ok"
				}
				if stringAttr(a, "langfuse.observation.level") == "ERROR" {
					step.Status = "error"
				}
				if span.ParentID != "" && step.ParentSpanID == "" && !zeroID(span.ParentID) {
					step.Issues = append(step.Issues, "invalid_parent_id")
				}
				if span.DroppedAttributes+span.DroppedEvents+span.DroppedLinks > 0 {
					step.Issues = append(step.Issues, "dropped_telemetry")
				}
				for _, event := range span.Events {
					step.Events = append(step.Events, Event{event.Name, string(event.Time), decodeAttributes(event.Attributes)})
				}
				for _, link := range span.Links {
					step.Links = append(step.Links, Link{canonicalID(link.TraceID, 16), canonicalID(link.SpanID, 8), decodeAttributes(link.Attributes)})
				}
				adapt(&step)
				out.Steps = append(out.Steps, step)
			}
		}
	}
	sort.Slice(out.Steps, func(i, j int) bool {
		if out.Steps[i].Start.Equal(out.Steps[j].Start) {
			return out.Steps[i].SpanID < out.Steps[j].SpanID
		}
		return out.Steps[i].Start.Before(out.Steps[j].Start)
	})
	buildRuns(out)
	return out, nil
}

func canonicalID(value string, size int) string {
	if len(value) == size*2 {
		if b, err := hex.DecodeString(value); err == nil && !zeroBytes(b) {
			return strings.ToLower(value)
		}
	}
	if b, err := base64.StdEncoding.DecodeString(value); err == nil && len(b) == size && !zeroBytes(b) {
		return hex.EncodeToString(b)
	}
	return ""
}

func zeroBytes(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
func zeroID(s string) bool {
	if b, err := hex.DecodeString(s); err == nil {
		return zeroBytes(b)
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return zeroBytes(b)
	}
	return false
}

func decodeAttributes(attrs []attribute) Attributes {
	out := Attributes{}
	for _, attr := range attrs {
		out[attr.Key] = decodeValue(attr.Value)
	}
	return out
}

func decodeValue(value map[string]json.RawMessage) any {
	for key, raw := range value {
		switch key {
		case "arrayValue":
			var array struct {
				Values []map[string]json.RawMessage `json:"values"`
			}
			if json.Unmarshal(raw, &array) == nil {
				values := make([]any, 0, len(array.Values))
				for _, v := range array.Values {
					values = append(values, decodeValue(v))
				}
				return values
			}
		case "kvlistValue":
			var list struct {
				Values []attribute `json:"values"`
			}
			if json.Unmarshal(raw, &list) == nil {
				return decodeAttributes(list.Values)
			}
		default:
			return decodeJSON(raw)
		}
	}
	return nil
}

func decodeJSON(raw []byte) any {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) == nil {
		return value
	}
	return string(raw)
}

func stringAttr(a Attributes, keys ...string) string {
	for _, key := range keys {
		if v, ok := a[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func appendUnique(values []string, value string) []string {
	if value != "" && !slices.Contains(values, value) {
		return append(values, value)
	}
	return values
}

func content(a Attributes, keys ...string) Content {
	for _, key := range keys {
		if v, ok := a[key]; ok {
			if s, ok := v.(string); ok && json.Valid([]byte(s)) {
				v = decodeJSON([]byte(s))
			}
			out := Content{State: "available", Source: key, Value: v}
			if snapshot, ok := v.(map[string]any); ok && snapshot["schema"] == tracing.AgentContentSchema {
				out.Value = snapshot["value"]
				out.Truncated, _ = snapshot["truncated"].(bool)
				if n, ok := snapshot["original_bytes"].(json.Number); ok {
					bytes, _ := n.Int64()
					out.OriginalBytes = int(bytes)
				}
				if omitted, ok := snapshot["omitted"].([]any); ok {
					for _, item := range omitted {
						if reason, ok := item.(string); ok {
							out.Omitted = append(out.Omitted, reason)
						}
					}
				}
			}
			return out
		}
	}
	return Content{State: "not_recorded"}
}

func preview(c Content) string {
	if c.State != "available" {
		return ""
	}
	s, ok := c.Value.(string)
	if !ok {
		b, _ := json.Marshal(c.Value)
		s = string(b)
	}
	r := []rune(s)
	if len(r) > 180 {
		return string(r[:180]) + "…"
	}
	return s
}

func numeric(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(fmt.Sprint(v), 64)
	return n, err == nil
}
