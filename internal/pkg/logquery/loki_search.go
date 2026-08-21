package logquery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const lokiBackendName = "loki"

const maxLokiFieldValueScanRecords = 10_000

type lokiCursor struct {
	Backend   string        `json:"backend"`
	Timestamp int64         `json:"timestamp"`
	Skip      int           `json:"skip,omitempty"`
	Direction SortDirection `json:"direction"`
}

// Search implements the backend-neutral Searcher interface on top of Loki.
func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResult, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	var cursor lokiCursor
	if req.Cursor != "" {
		if err := decodeCursor(req.Cursor, &cursor); err != nil {
			return nil, err
		}
		if cursor.Backend != lokiBackendName || cursor.Direction != req.Direction || cursor.Timestamp <= 0 || cursor.Skip < 0 || cursor.Skip > 10000 {
			return nil, errInvalidCursor
		}
		point := time.Unix(0, cursor.Timestamp)
		if req.Direction == SortBackward && point.Before(req.End) {
			// Loki's log range end is exclusive. Keep the cursor timestamp in
			// the next query so Skip can remove records already returned at that
			// timestamp without dropping the remaining records there.
			req.End = point.Add(time.Nanosecond)
		}
		if req.Direction == SortForward && point.After(req.Start) {
			// Apply the same one-nanosecond overlap at the exclusive start edge
			// for forward pagination.
			req.Start = point.Add(-time.Nanosecond)
		}
	}

	query, err := compileLogQL(req)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	queryLimit := req.Limit + cursor.Skip + 1
	out, err := c.QueryRange(ctx, QueryRangeOptions{
		Query:     query,
		Start:     req.Start,
		End:       req.End,
		Limit:     queryLimit,
		Direction: string(req.Direction),
	})
	if err != nil {
		return nil, err
	}
	records, err := decodeLokiRecords(out)
	if err != nil {
		return nil, err
	}
	sortRecords(records, req.Direction)
	if cursor.Skip > 0 {
		if len(records) < cursor.Skip {
			return nil, errInvalidCursor
		}
		for i := 0; i < cursor.Skip; i++ {
			if records[i].Timestamp.UnixNano() != cursor.Timestamp {
				return nil, errInvalidCursor
			}
		}
		records = records[cursor.Skip:]
	}
	hasMore := len(records) > req.Limit
	if hasMore {
		records = records[:req.Limit]
	}
	var next string
	if hasMore && len(records) > 0 {
		last := records[len(records)-1]
		cursorTimestamp := last.Timestamp.UnixNano()
		skip := recordsAtTimestamp(records, cursorTimestamp)
		if cursor.Timestamp == cursorTimestamp {
			skip += cursor.Skip
		}
		next, err = encodeCursor(lokiCursor{
			Backend:   lokiBackendName,
			Timestamp: cursorTimestamp,
			Skip:      skip,
			Direction: req.Direction,
		})
		if err != nil {
			return nil, err
		}
	}
	return &SearchResult{
		Records:    records,
		NextCursor: next,
		HasMore:    hasMore,
		TookMS:     time.Since(started).Milliseconds(),
		Backends:   []string{lokiBackendName},
	}, nil
}

func recordsAtTimestamp(records []Record, timestamp int64) int {
	count := 0
	for _, record := range records {
		if record.Timestamp.UnixNano() == timestamp {
			count++
		}
	}
	return count
}

// Count evaluates a single count_over_time expression at req.End. Using
// Loki's instant-query endpoint avoids overlapping range-vector buckets and
// gives alert evaluation an exact count for (Start, End]. Elasticsearch Count
// uses the same boundary convention so adjacent product histogram buckets do
// not count an event twice.
func (c *Client) Count(ctx context.Context, req SearchRequest) (uint64, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return 0, err
	}
	query, err := compileLogQL(req)
	if err != nil {
		return 0, err
	}
	expr := fmt.Sprintf("sum(count_over_time(%s[%s]))", query, logQLDuration(req.End.Sub(req.Start)))
	params := url.Values{}
	params.Set("query", expr)
	params.Set("time", strconv.FormatInt(req.End.UnixNano(), 10))
	body, err := c.do(ctx, "/loki/api/v1/query", params)
	if err != nil {
		return 0, err
	}
	var envelope struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return 0, fmt.Errorf("logquery: decode Loki count: %w", err)
	}
	if envelope.Status != "success" {
		return 0, fmt.Errorf("logquery: %s", envelope.Error)
	}
	if envelope.Data.ResultType != "vector" {
		return 0, fmt.Errorf("logquery: expected vector count result, got %q", envelope.Data.ResultType)
	}
	var total uint64
	for _, item := range envelope.Data.Result {
		if len(item.Value) != 2 {
			continue
		}
		var raw string
		if err := json.Unmarshal(item.Value[1], &raw); err != nil {
			return 0, fmt.Errorf("logquery: decode Loki count value: %w", err)
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 {
			return 0, fmt.Errorf("logquery: invalid Loki count value %q", raw)
		}
		total += uint64(value)
	}
	return total, nil
}

func (c *Client) Fields(_ context.Context, _, _ time.Time, _ Scope) ([]Field, error) {
	return AllowedFields(), nil
}

func (c *Client) FieldValues(ctx context.Context, req FieldValuesRequest) ([]string, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	def, _ := LookupField(req.Field)
	if def.LokiName == "" {
		return []string{}, nil
	}
	if def.LokiIndexed && scopeIsEmpty(req.Scope) {
		values, err := c.LabelValues(ctx, def.LokiName, req.Start, req.End)
		if err != nil {
			return nil, err
		}
		if len(values) > req.Limit {
			values = values[:req.Limit]
		}
		return values, nil
	}

	values := make(map[string]struct{})
	search := SearchRequest{
		Start: req.Start, End: req.End, Scope: req.Scope,
		Limit: MaxSearchLimit, Direction: SortBackward,
	}
	scanned := 0
	for scanned < maxLokiFieldValueScanRecords {
		result, err := c.Search(ctx, search)
		if err != nil {
			return nil, err
		}
		for _, record := range result.Records {
			if scanned >= maxLokiFieldValueScanRecords {
				break
			}
			scanned++
			if value := lokiRecordFieldValue(record, req.Field, def); value != "" {
				values[value] = struct{}{}
			}
		}
		if result.NextCursor == "" || result.NextCursor == search.Cursor {
			break
		}
		search.Cursor = result.NextCursor
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) > req.Limit {
		out = out[:req.Limit]
	}
	return out, nil
}

func scopeIsEmpty(scope Scope) bool {
	return len(scope.DeviceIDs) == 0 && len(scope.ClusterIDs) == 0 && len(scope.Namespaces) == 0 &&
		len(scope.Workloads) == 0 && len(scope.Pods) == 0 && len(scope.Containers) == 0 &&
		len(scope.Nodes) == 0 && len(scope.ServiceNames) == 0 && len(scope.SourceIDs) == 0 &&
		len(scope.Levels) == 0 && len(scope.Files) == 0 && len(scope.Units) == 0
}

func lokiRecordFieldValue(record Record, logical string, def FieldDefinition) string {
	switch logical {
	case "trace_id":
		return strings.TrimSpace(record.TraceID)
	case "span_id":
		return strings.TrimSpace(record.SpanID)
	case "level":
		if value := strings.TrimSpace(record.SeverityText); value != "" {
			return value
		}
	}
	if value := strings.TrimSpace(record.ResourceAttributes[logical]); value != "" {
		return value
	}
	if value := strings.TrimSpace(record.Attributes[def.LokiName]); value != "" {
		return value
	}
	return strings.TrimSpace(record.Attributes[logical])
}

func (c *Client) Histogram(ctx context.Context, req SearchRequest, interval time.Duration) ([]HistogramBucket, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	if interval <= 0 || interval > MaxSearchWindow {
		return nil, errors.New("logquery: histogram interval is invalid")
	}
	span := req.End.Sub(req.Start)
	fullBucketCount := int(span / interval)
	buckets := make([]HistogramBucket, 0, fullBucketCount+1)
	if fullBucketCount == 1 {
		countReq := req
		countReq.End = req.Start.Add(interval)
		count, err := c.Count(ctx, countReq)
		if err != nil {
			return nil, err
		}
		buckets = append(buckets, HistogramBucket{Start: req.Start.UTC(), Count: count})
	} else if fullBucketCount > 1 {
		query, err := compileLogQL(req)
		if err != nil {
			return nil, err
		}
		// Loki aligns query_range evaluation timestamps to the epoch-based step
		// grid even when start carries seconds or milliseconds. Evaluate at the
		// next aligned point and offset the range selector backwards so every
		// count still owns the exact product bucket (req.Start+i*interval,
		// req.Start+(i+1)*interval].
		firstBucketEnd := req.Start.Add(interval)
		evaluationStart := firstBucketEnd.Truncate(interval)
		if evaluationStart.Before(firstBucketEnd) {
			evaluationStart = evaluationStart.Add(interval)
		}
		evaluationOffset := evaluationStart.Sub(firstBucketEnd)
		offsetClause := ""
		if evaluationOffset > 0 {
			offsetClause = " offset " + logQLDuration(evaluationOffset)
		}
		metricQuery := fmt.Sprintf("sum(count_over_time(%s[%s]%s))", query, logQLDuration(interval), offsetClause)
		out, err := c.QueryRange(ctx, QueryRangeOptions{
			Query: metricQuery,
			Start: evaluationStart,
			End:   evaluationStart.Add(time.Duration(fullBucketCount-1) * interval),
			Step:  interval,
			Limit: MaxSearchLimit,
		})
		if err != nil {
			return nil, err
		}
		fullBuckets, err := decodeLokiHistogram(out)
		if err != nil {
			return nil, err
		}
		for i := range fullBuckets {
			fullBuckets[i].Start = fullBuckets[i].Start.Add(-interval - evaluationOffset).UTC()
		}
		buckets = append(buckets, fullBuckets...)
	}

	partialStart := req.Start.Add(time.Duration(fullBucketCount) * interval)
	if req.End.After(partialStart) {
		partialReq := req
		partialReq.Start = partialStart
		count, err := c.Count(ctx, partialReq)
		if err != nil {
			return nil, err
		}
		buckets = append(buckets, HistogramBucket{Start: partialStart.UTC(), Count: count})
	}
	return buckets, nil
}

func compileLogQL(req SearchRequest) (string, error) {
	matchers := make([]string, 0, 12)
	lineFilters := make([]string, 0, len(req.Keywords.Include)+len(req.Keywords.Exclude)+len(req.Filters))
	addScopeMatchers := func(fieldName string, values []string) error {
		if len(values) == 0 {
			return nil
		}
		def, ok := LookupField(fieldName)
		if !ok || def.LokiName == "" {
			return fmt.Errorf("logquery: Loki field mapping missing for %q", fieldName)
		}
		matcher := logQLMultiMatcher(def.LokiName, values, false)
		if def.LokiIndexed {
			matchers = append(matchers, matcher)
		} else {
			lineFilters = append(lineFilters, "| "+matcher)
		}
		return nil
	}
	if len(req.Scope.DeviceIDs) > 0 {
		values := make([]string, 0, len(req.Scope.DeviceIDs))
		for _, id := range req.Scope.DeviceIDs {
			if id == 0 {
				return "", errors.New("logquery: device_id must be greater than zero")
			}
			values = append(values, strconv.FormatUint(id, 10))
		}
		if err := addScopeMatchers("device_id", values); err != nil {
			return "", err
		}
	}
	for _, item := range []struct {
		field  string
		values []string
	}{
		{"cluster_id", req.Scope.ClusterIDs},
		{"namespace", req.Scope.Namespaces},
		{"workload", req.Scope.Workloads},
		{"pod", req.Scope.Pods},
		{"container", req.Scope.Containers},
		{"node", req.Scope.Nodes},
		{"service_name", req.Scope.ServiceNames},
		{"source_id", req.Scope.SourceIDs},
		{"level", req.Scope.Levels},
		{"file", req.Scope.Files},
		{"unit", req.Scope.Units},
	} {
		if err := addScopeMatchers(item.field, item.values); err != nil {
			return "", err
		}
	}

	for _, filter := range req.Filters {
		def, _ := LookupField(filter.Field)
		if filter.Field == "message" {
			switch filter.Operator {
			case FilterEqual:
				lineFilters = append(lineFilters, `|~ "(?i)`+escapeLogQLString(regexp.QuoteMeta(filter.Values[0]))+`"`)
			case FilterIn:
				parts := make([]string, 0, len(filter.Values))
				for _, value := range trimmedStrings(filter.Values) {
					parts = append(parts, regexp.QuoteMeta(value))
				}
				lineFilters = append(lineFilters, `|~ "(?i)(`+escapeLogQLString(strings.Join(parts, "|"))+`)"`)
			case FilterNotEqual:
				for _, value := range filter.Values {
					lineFilters = append(lineFilters, `!~ "(?i)`+escapeLogQLString(regexp.QuoteMeta(value))+`"`)
				}
			case FilterExists:
				lineFilters = append(lineFilters, `|~ ".+"`)
			}
			continue
		}
		if def.LokiName == "" {
			return "", fmt.Errorf("logquery: Loki field mapping missing for %q", filter.Field)
		}
		switch filter.Operator {
		case FilterEqual, FilterIn:
			matcher := logQLMultiMatcher(def.LokiName, filter.Values, false)
			if def.LokiIndexed {
				matchers = append(matchers, matcher)
			} else {
				lineFilters = append(lineFilters, "| "+matcher)
			}
		case FilterNotEqual:
			matcher := logQLMultiMatcher(def.LokiName, filter.Values, true)
			if def.LokiIndexed {
				matchers = append(matchers, matcher)
			} else {
				lineFilters = append(lineFilters, "| "+matcher)
			}
		case FilterExists:
			matcher := def.LokiName + `=~".+"`
			if def.LokiIndexed {
				matchers = append(matchers, matcher)
			} else {
				lineFilters = append(lineFilters, "| "+matcher)
			}
		}
	}
	if len(matchers) == 0 {
		// Every Edge log stream is enriched with device_id. ongrid_source is
		// optional and is absent from ordinary journald/file/Kubernetes streams,
		// so using it as the required non-empty Loki matcher would leave only
		// connection-check probe logs visible in an unfiltered search.
		matchers = append(matchers, `device_id=~".+"`)
	}

	include := trimmedStrings(req.Keywords.Include)
	switch req.Keywords.Mode {
	case MatchAny:
		if len(include) > 0 {
			parts := make([]string, 0, len(include))
			for _, keyword := range include {
				parts = append(parts, regexp.QuoteMeta(keyword))
			}
			lineFilters = append(lineFilters, `|~ "(?i)(`+escapeLogQLString(strings.Join(parts, "|"))+`)"`)
		}
	case MatchAll, MatchPhrase:
		for _, keyword := range include {
			lineFilters = append(lineFilters, `|~ "(?i)`+escapeLogQLString(regexp.QuoteMeta(keyword))+`"`)
		}
	}
	for _, keyword := range trimmedStrings(req.Keywords.Exclude) {
		lineFilters = append(lineFilters, `!~ "(?i)`+escapeLogQLString(regexp.QuoteMeta(keyword))+`"`)
	}
	return "{" + strings.Join(matchers, ",") + "} " + strings.Join(lineFilters, " "), nil
}

func logQLMultiMatcher(label string, values []string, negate bool) string {
	clean := trimmedStrings(values)
	if len(clean) == 1 {
		op := "="
		if negate {
			op = "!="
		}
		return label + op + `"` + escapeLogQLString(clean[0]) + `"`
	}
	parts := make([]string, 0, len(clean))
	for _, value := range clean {
		parts = append(parts, regexp.QuoteMeta(value))
	}
	op := "=~"
	if negate {
		op = "!~"
	}
	return label + op + `"(?:` + escapeLogQLString(strings.Join(parts, "|")) + `)"`
}

func escapeLogQLString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`)
	return replacer.Replace(value)
}

func trimmedStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func decodeLokiRecords(result *QueryRangeResult) ([]Record, error) {
	if result.ResultType != "streams" {
		return nil, fmt.Errorf("logquery: expected streams result, got %q", result.ResultType)
	}
	var streams []struct {
		Stream map[string]string   `json:"stream"`
		Values [][]json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(result.Result, &streams); err != nil {
		return nil, fmt.Errorf("logquery: decode Loki streams: %w", err)
	}
	records := make([]Record, 0)
	for _, stream := range streams {
		for _, value := range stream.Values {
			if len(value) < 2 {
				return nil, errors.New("logquery: invalid Loki log entry")
			}
			var rawTimestamp, message string
			if err := json.Unmarshal(value[0], &rawTimestamp); err != nil {
				return nil, fmt.Errorf("logquery: decode Loki timestamp: %w", err)
			}
			if err := json.Unmarshal(value[1], &message); err != nil {
				return nil, fmt.Errorf("logquery: decode Loki message: %w", err)
			}
			nanos, err := strconv.ParseInt(rawTimestamp, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("logquery: parse Loki timestamp: %w", err)
			}
			labels := cloneStringMap(stream.Stream)
			if len(value) >= 3 && string(value[2]) != "null" {
				var metadata map[string]string
				if err := json.Unmarshal(value[2], &metadata); err != nil {
					return nil, fmt.Errorf("logquery: decode Loki structured metadata: %w", err)
				}
				for key, item := range metadata {
					labels[key] = item
				}
			}
			for _, alias := range []struct {
				canonical string
				legacy    string
			}{
				{canonical: "device_id", legacy: "attributes_device_id"},
				{canonical: "cluster_id", legacy: "attributes_cluster_id"},
				{canonical: "ongrid_source", legacy: "attributes_ongrid_source"},
				{canonical: "level", legacy: "attributes_level"},
			} {
				if labels[alias.canonical] == "" {
					labels[alias.canonical] = labels[alias.legacy]
				}
			}
			attrs := cloneStringMap(labels)
			// Loki exposes the intrinsic OTel severity fields as structured
			// metadata and older OTLP payloads may contain an attributes_ prefix.
			// These implementation fields must not leak into the backend-neutral
			// attribute/display-field namespace.
			delete(attrs, "severity_text")
			delete(attrs, "severity_number")
			for key := range attrs {
				if strings.HasPrefix(key, "attributes_") || strings.HasPrefix(key, "resource_attributes_") {
					delete(attrs, key)
				}
			}
			if strings.EqualFold(strings.TrimSpace(attrs["service_name"]), "unknown_service") {
				delete(attrs, "service_name")
			}
			resources := map[string]string{}
			for _, logical := range []string{"device_id", "cluster_id", "namespace", "workload", "pod", "container", "node", "service_name", "source_id"} {
				def, _ := LookupField(logical)
				if v := labels[def.LokiName]; v != "" && !(logical == "service_name" && strings.EqualFold(strings.TrimSpace(v), "unknown_service")) {
					resources[logical] = v
				}
			}
			if clusterName := labels["cluster_name"]; clusterName != "" {
				resources["cluster_name"] = clusterName
			}
			severityText := normalizeLevel(firstNonEmpty(labels["level"], labels["detected_level"], labels["severity_text"]))
			if severityText == "" {
				severityText = detectLevel(message)
			}
			records = append(records, Record{
				ID:                 stableLokiRecordID(rawTimestamp, labels, message),
				Timestamp:          time.Unix(0, nanos).UTC(),
				Message:            message,
				SeverityText:       severityText,
				SeverityNumber:     severityNumberForLevel(severityText),
				Backend:            lokiBackendName,
				Attributes:         attrs,
				ResourceAttributes: resources,
				TraceID:            labels["trace_id"],
				SpanID:             labels["span_id"],
			})
		}
	}
	return records, nil
}

func decodeLokiHistogram(result *QueryRangeResult) ([]HistogramBucket, error) {
	if result.ResultType != "matrix" {
		return nil, fmt.Errorf("logquery: expected matrix result, got %q", result.ResultType)
	}
	var series []struct {
		Values [][]json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(result.Result, &series); err != nil {
		return nil, fmt.Errorf("logquery: decode Loki histogram: %w", err)
	}
	counts := map[int64]uint64{}
	for _, item := range series {
		for _, pair := range item.Values {
			if len(pair) != 2 {
				continue
			}
			var seconds float64
			var rawCount string
			if err := json.Unmarshal(pair[0], &seconds); err != nil {
				return nil, fmt.Errorf("logquery: decode histogram timestamp: %w", err)
			}
			if err := json.Unmarshal(pair[1], &rawCount); err != nil {
				return nil, fmt.Errorf("logquery: decode histogram value: %w", err)
			}
			count, err := strconv.ParseFloat(rawCount, 64)
			if err != nil {
				return nil, fmt.Errorf("logquery: parse histogram value: %w", err)
			}
			counts[int64(math.Round(seconds*1000))] += uint64(count)
		}
	}
	keys := make([]int64, 0, len(counts))
	for ts := range counts {
		keys = append(keys, ts)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	buckets := make([]HistogramBucket, 0, len(keys))
	for _, ts := range keys {
		buckets = append(buckets, HistogramBucket{Start: time.UnixMilli(ts).UTC(), Count: counts[ts]})
	}
	return buckets, nil
}

func sortRecords(records []Record, direction SortDirection) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Timestamp.Equal(records[j].Timestamp) {
			return records[i].ID < records[j].ID
		}
		if direction == SortForward {
			return records[i].Timestamp.Before(records[j].Timestamp)
		}
		return records[i].Timestamp.After(records[j].Timestamp)
	})
}

func stableLokiRecordID(ts string, labels map[string]string, message string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	builder := strings.Builder{}
	builder.WriteString(ts)
	for _, key := range keys {
		builder.WriteByte(0)
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(labels[key])
	}
	builder.WriteByte(0)
	builder.WriteString(message)
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:16])
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func logQLDuration(d time.Duration) string {
	if d%time.Hour == 0 {
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	if d%time.Minute == 0 {
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	if d%time.Second == 0 {
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
	// Loki range selectors accept integer duration components, but Go's
	// Duration.String emits fractional seconds for cutover-split buckets
	// (for example, "1.337s"). Loki rejects that form with
	// `unknown unit "."`. Product query boundaries are millisecond-aligned;
	// round any finer caller input up to Loki's minimum supported unit so the
	// range never becomes shorter than the requested interval.
	milliseconds := d / time.Millisecond
	if d%time.Millisecond != 0 {
		milliseconds++
	}
	if milliseconds < 1 {
		milliseconds = 1
	}
	return strconv.FormatInt(int64(milliseconds), 10) + "ms"
}
