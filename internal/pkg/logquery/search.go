package logquery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultSearchLimit = 200
	MaxSearchLimit     = 1000
	MaxSearchWindow    = 30 * 24 * time.Hour
	MaxKeywordCount    = 20
	MaxFilterCount     = 20
	MaxScopeValueCount = 100
	MaxKeywordLength   = 512
)

type MatchMode string

const (
	MatchAny    MatchMode = "any"
	MatchAll    MatchMode = "all"
	MatchPhrase MatchMode = "phrase"
)

type SortDirection string

const (
	SortBackward SortDirection = "backward"
	SortForward  SortDirection = "forward"
)

type FilterOperator string

const (
	FilterEqual    FilterOperator = "eq"
	FilterNotEqual FilterOperator = "neq"
	FilterIn       FilterOperator = "in"
	FilterExists   FilterOperator = "exists"
)

// Scope contains product-level dimensions. Adapters map these stable names to
// Loki labels/structured metadata or Elasticsearch OTel document paths.
type Scope struct {
	DeviceIDs    []uint64 `json:"device_ids,omitempty"`
	ClusterIDs   []string `json:"cluster_ids,omitempty"`
	Namespaces   []string `json:"namespaces,omitempty"`
	Workloads    []string `json:"workloads,omitempty"`
	Pods         []string `json:"pods,omitempty"`
	Containers   []string `json:"containers,omitempty"`
	Nodes        []string `json:"nodes,omitempty"`
	ServiceNames []string `json:"service_names,omitempty"`
	SourceIDs    []string `json:"source_ids,omitempty"`
	Levels       []string `json:"levels,omitempty"`
	Files        []string `json:"files,omitempty"`
	Units        []string `json:"units,omitempty"`
}

type Keywords struct {
	Include []string  `json:"include,omitempty"`
	Exclude []string  `json:"exclude,omitempty"`
	Mode    MatchMode `json:"mode,omitempty"`
}

type FieldFilter struct {
	Field    string         `json:"field"`
	Operator FilterOperator `json:"operator"`
	Values   []string       `json:"values,omitempty"`
}

type SearchRequest struct {
	Start     time.Time     `json:"start"`
	End       time.Time     `json:"end"`
	Scope     Scope         `json:"scope,omitempty"`
	Keywords  Keywords      `json:"keywords,omitempty"`
	Filters   []FieldFilter `json:"filters,omitempty"`
	Limit     int           `json:"limit,omitempty"`
	Cursor    string        `json:"cursor,omitempty"`
	Direction SortDirection `json:"direction,omitempty"`
}

type Record struct {
	ID                 string            `json:"id"`
	Timestamp          time.Time         `json:"timestamp"`
	ObservedTimestamp  time.Time         `json:"observed_timestamp,omitempty"`
	Message            string            `json:"message"`
	SeverityText       string            `json:"severity_text,omitempty"`
	SeverityNumber     int32             `json:"severity_number,omitempty"`
	Backend            string            `json:"backend"`
	Attributes         map[string]string `json:"attributes,omitempty"`
	ResourceAttributes map[string]string `json:"resource_attributes,omitempty"`
	TraceID            string            `json:"trace_id,omitempty"`
	SpanID             string            `json:"span_id,omitempty"`
}

type SearchResult struct {
	Records    []Record `json:"records"`
	NextCursor string   `json:"next_cursor,omitempty"`
	HasMore    bool     `json:"has_more"`
	TookMS     int64    `json:"took_ms"`
	Backends   []string `json:"backends"`
}

type Field struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Searchable   bool   `json:"searchable"`
	Aggregatable bool   `json:"aggregatable"`
}

type HistogramBucket struct {
	Start time.Time `json:"start"`
	Count uint64    `json:"count"`
}

type FieldValuesRequest struct {
	Field string    `json:"field"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Scope Scope     `json:"scope,omitempty"`
	Limit int       `json:"limit,omitempty"`
}

// Searcher is the backend-neutral surface consumed by the Manager logs HTTP
// service, alerting and AIOps. Implementations must enforce the product data
// stream boundary themselves; callers never pass an arbitrary index name.
type Searcher interface {
	Search(ctx context.Context, req SearchRequest) (*SearchResult, error)
	// Count returns the exact number of records in (Start, End]. This shared
	// boundary convention lets callers compose adjacent buckets without
	// double-counting their common timestamp.
	// Alerting uses this instead of fetching pages or summing UI histogram
	// buckets, which may overlap on Loki window boundaries.
	Count(ctx context.Context, req SearchRequest) (uint64, error)
	Fields(ctx context.Context, start, end time.Time, scope Scope) ([]Field, error)
	FieldValues(ctx context.Context, req FieldValuesRequest) ([]string, error)
	Histogram(ctx context.Context, req SearchRequest, interval time.Duration) ([]HistogramBucket, error)
}

// FieldDefinition is the sole allowlist for user-addressable log fields.
// LokiName may identify an indexed label or structured metadata field;
// ElasticsearchPath always stays within the product OTel document schema.
type FieldDefinition struct {
	Field
	LokiName          string
	LokiIndexed       bool
	ElasticsearchPath string
}

var errInvalidCursor = errors.New("logquery: invalid cursor")

func (r *SearchRequest) NormalizeAndValidate() error {
	if r.Start.IsZero() || r.End.IsZero() {
		return errors.New("logquery: start and end are required")
	}
	if !r.End.After(r.Start) {
		return errors.New("logquery: end must be after start")
	}
	if r.End.Sub(r.Start) > MaxSearchWindow {
		return fmt.Errorf("logquery: time window exceeds %s", MaxSearchWindow)
	}
	if r.Limit == 0 {
		r.Limit = DefaultSearchLimit
	}
	if r.Limit < 1 || r.Limit > MaxSearchLimit {
		return fmt.Errorf("logquery: limit must be between 1 and %d", MaxSearchLimit)
	}
	if r.Direction == "" {
		r.Direction = SortBackward
	}
	if r.Direction != SortBackward && r.Direction != SortForward {
		return errors.New("logquery: direction must be backward or forward")
	}
	if r.Keywords.Mode == "" {
		r.Keywords.Mode = MatchAny
	}
	if r.Keywords.Mode != MatchAny && r.Keywords.Mode != MatchAll && r.Keywords.Mode != MatchPhrase {
		return errors.New("logquery: keyword mode must be any, all, or phrase")
	}
	deviceIDs, err := normalizeDeviceIDs(r.Scope.DeviceIDs)
	if err != nil {
		return err
	}
	r.Scope.DeviceIDs = deviceIDs
	if err := validateScope(r.Scope); err != nil {
		return err
	}
	if len(r.Keywords.Include) > MaxKeywordCount || len(r.Keywords.Exclude) > MaxKeywordCount {
		return fmt.Errorf("logquery: at most %d include and exclude keywords are allowed", MaxKeywordCount)
	}
	if err := validateStrings("keyword", append(append([]string{}, r.Keywords.Include...), r.Keywords.Exclude...), MaxKeywordLength); err != nil {
		return err
	}
	if len(r.Filters) > MaxFilterCount {
		return fmt.Errorf("logquery: at most %d filters are allowed", MaxFilterCount)
	}
	for i := range r.Filters {
		f := &r.Filters[i]
		f.Field = strings.TrimSpace(f.Field)
		if _, ok := LookupField(f.Field); !ok {
			return fmt.Errorf("logquery: filter field %q is not allowed", f.Field)
		}
		switch f.Operator {
		case FilterEqual:
			if len(f.Values) != 1 {
				return fmt.Errorf("logquery: equal filter %q requires exactly one value", f.Field)
			}
		case FilterNotEqual, FilterIn:
			if len(f.Values) == 0 {
				return fmt.Errorf("logquery: filter %q requires values", f.Field)
			}
		case FilterExists:
			if len(f.Values) != 0 {
				return fmt.Errorf("logquery: exists filter %q must not have values", f.Field)
			}
		default:
			return fmt.Errorf("logquery: unsupported filter operator %q", f.Operator)
		}
		if len(f.Values) > MaxKeywordCount {
			return fmt.Errorf("logquery: filter %q has too many values", f.Field)
		}
		if err := validateStrings("filter value", f.Values, MaxKeywordLength); err != nil {
			return err
		}
	}
	if r.Cursor != "" {
		var probe map[string]json.RawMessage
		if err := decodeCursor(r.Cursor, &probe); err != nil {
			return err
		}
	}
	return nil
}

func (r *FieldValuesRequest) NormalizeAndValidate() error {
	r.Field = strings.TrimSpace(r.Field)
	if _, ok := LookupField(r.Field); !ok {
		return fmt.Errorf("logquery: field %q is not allowed", r.Field)
	}
	deviceIDs, err := normalizeDeviceIDs(r.Scope.DeviceIDs)
	if err != nil {
		return err
	}
	r.Scope.DeviceIDs = deviceIDs
	if err := validateScope(r.Scope); err != nil {
		return err
	}
	if r.Start.IsZero() || r.End.IsZero() || !r.End.After(r.Start) {
		return errors.New("logquery: valid start and end are required")
	}
	if r.End.Sub(r.Start) > MaxSearchWindow {
		return fmt.Errorf("logquery: time window exceeds %s", MaxSearchWindow)
	}
	if r.Limit == 0 {
		r.Limit = 100
	}
	if r.Limit < 1 || r.Limit > 500 {
		return errors.New("logquery: field value limit must be between 1 and 500")
	}
	return nil
}

func LookupField(name string) (FieldDefinition, bool) {
	switch name {
	case "device_id":
		return field(name, "keyword", "device_id", "resource.attributes.device_id"), true
	case "cluster_id":
		return field(name, "keyword", "cluster_id", "resource.attributes.cluster_id"), true
	case "namespace":
		return field(name, "keyword", "namespace", "resource.attributes.namespace"), true
	case "workload":
		return structuredField(name, "keyword", "workload", "resource.attributes.workload"), true
	case "pod":
		return structuredField(name, "keyword", "pod", "resource.attributes.pod"), true
	case "container":
		return structuredField(name, "keyword", "container", "resource.attributes.container"), true
	case "node":
		return structuredField(name, "keyword", "node", "resource.attributes.node"), true
	case "service_name":
		return field(name, "keyword", "service_name", "resource.attributes.service_name"), true
	case "source_id":
		return field(name, "keyword", "ongrid_source", "resource.attributes.ongrid_source"), true
	case "level":
		return structuredField(name, "keyword", "level", "severity_text"), true
	case "file":
		return structuredField(name, "keyword", "filename", "resource.attributes.filename"), true
	case "unit":
		return structuredField(name, "keyword", "unit", "resource.attributes.unit"), true
	case "trace_id":
		return structuredField(name, "keyword", "trace_id", "trace_id"), true
	case "span_id":
		return structuredField(name, "keyword", "span_id", "span_id"), true
	case "message":
		return FieldDefinition{
			Field:             Field{Name: name, Type: "text", Searchable: true, Aggregatable: false},
			LokiName:          "",
			ElasticsearchPath: "body.text",
		}, true
	default:
		return FieldDefinition{}, false
	}
}

func AllowedFields() []Field {
	names := []string{
		"device_id", "cluster_id", "namespace", "workload", "pod", "container", "node",
		"service_name", "source_id", "level", "file", "unit", "trace_id", "span_id", "message",
	}
	out := make([]Field, 0, len(names))
	for _, name := range names {
		def, _ := LookupField(name)
		out = append(out, def.Field)
	}
	return out
}

func field(name, fieldType, loki, elastic string) FieldDefinition {
	return FieldDefinition{
		Field: Field{
			Name:         name,
			Type:         fieldType,
			Searchable:   true,
			Aggregatable: true,
		},
		LokiName:          loki,
		LokiIndexed:       true,
		ElasticsearchPath: elastic,
	}
}

func structuredField(name, fieldType, loki, elastic string) FieldDefinition {
	return FieldDefinition{
		Field: Field{
			Name:         name,
			Type:         fieldType,
			Searchable:   true,
			Aggregatable: true,
		},
		LokiName:          loki,
		LokiIndexed:       false,
		ElasticsearchPath: elastic,
	}
}

func validateStrings(kind string, values []string, maxLength int) error {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return fmt.Errorf("logquery: %s must not be empty", kind)
		}
		if len(trimmed) > maxLength {
			return fmt.Errorf("logquery: %s exceeds %d bytes", kind, maxLength)
		}
	}
	return nil
}

func validateScope(scope Scope) error {
	for _, id := range scope.DeviceIDs {
		if id == 0 {
			return errors.New("logquery: device_id must be greater than zero")
		}
	}
	for _, item := range []struct {
		name   string
		values []string
	}{
		{"cluster_id", scope.ClusterIDs},
		{"namespace", scope.Namespaces},
		{"workload", scope.Workloads},
		{"pod", scope.Pods},
		{"container", scope.Containers},
		{"node", scope.Nodes},
		{"service_name", scope.ServiceNames},
		{"source_id", scope.SourceIDs},
		{"level", scope.Levels},
		{"file", scope.Files},
		{"unit", scope.Units},
	} {
		if len(item.values) > MaxScopeValueCount {
			return fmt.Errorf("logquery: scope %s has too many values", item.name)
		}
		if err := validateStrings("scope "+item.name, item.values, 256); err != nil {
			return err
		}
	}
	return nil
}

func normalizeDeviceIDs(values []uint64) ([]uint64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[uint64]struct{}, min(len(values), MaxScopeValueCount))
	out := make([]uint64, 0, min(len(values), MaxScopeValueCount))
	for _, id := range values {
		if id == 0 {
			return nil, errors.New("logquery: device_id must be greater than zero")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) > MaxScopeValueCount {
			return nil, errors.New("logquery: scope device_id has too many values")
		}
	}
	return out, nil
}

func encodeCursor(v any) (string, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("logquery: encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func decodeCursor(raw string, dst any) error {
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return errInvalidCursor
	}
	if len(body) > 4096 {
		return errInvalidCursor
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return errInvalidCursor
	}
	return nil
}
