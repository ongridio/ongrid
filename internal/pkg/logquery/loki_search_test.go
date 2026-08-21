package logquery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLokiCountUsesInstantStructuredQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query().Get("query")
		for _, want := range []string{`sum(count_over_time(`, `namespace="prod"`, `|~ "(?i)panic"`, `[5m]))`} {
			if !strings.Contains(query, want) {
				t.Fatalf("query = %q, missing %q", query, want)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "vector", "result": []any{
				map[string]any{"metric": map[string]string{}, "value": []any{1787054400, "7"}},
			}},
		})
	}))
	t.Cleanup(server.Close)
	client := NewWithHTTPClient(server.URL, server.Client(), nil)
	req := validSearchRequest()
	req.Start = req.End.Add(-5 * time.Minute)
	req.Scope.Namespaces = []string{"prod"}
	req.Keywords = Keywords{Include: []string{"panic"}, Mode: MatchPhrase}
	count, err := client.Count(t.Context(), req)
	if err != nil || count != 7 {
		t.Fatalf("Count() = %d, %v", count, err)
	}
}

func TestLokiHistogramAlignsFullBucketsAndCountsPartialTail(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	end := start.Add(5*time.Minute + 30*time.Second)
	interval := 2 * time.Minute
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loki/api/v1/query_range":
			if got := r.URL.Query().Get("start"); got != strconv.FormatInt(start.Add(interval).UnixNano(), 10) {
				t.Fatalf("query_range start = %s", got)
			}
			if got := r.URL.Query().Get("end"); got != strconv.FormatInt(start.Add(2*interval).UnixNano(), 10) {
				t.Fatalf("query_range end = %s", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"resultType": "matrix", "result": []any{
					map[string]any{"metric": map[string]string{}, "values": []any{
						[]any{float64(start.Add(interval).Unix()), "2"},
						[]any{float64(start.Add(2 * interval).Unix()), "3"},
					}},
				}},
			})
		case "/loki/api/v1/query":
			if !strings.Contains(r.URL.Query().Get("query"), "[90s]") {
				t.Fatalf("partial count query = %q", r.URL.Query().Get("query"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"resultType": "vector", "result": []any{
					map[string]any{"metric": map[string]string{}, "value": []any{float64(end.Unix()), "4"}},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := NewWithHTTPClient(server.URL, server.Client(), nil)
	buckets, err := client.Histogram(t.Context(), SearchRequest{Start: start, End: end, Limit: 1}, interval)
	if err != nil {
		t.Fatalf("Histogram() error = %v", err)
	}
	want := []HistogramBucket{
		{Start: start, Count: 2},
		{Start: start.Add(interval), Count: 3},
		{Start: start.Add(2 * interval), Count: 4},
	}
	if !reflect.DeepEqual(buckets, want) {
		t.Fatalf("Histogram() = %#v, want %#v", buckets, want)
	}
}

func TestLokiHistogramOffsetsEpochAlignedEvaluationsToRequestStart(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 30, 0, time.UTC)
	end := start.Add(5*time.Minute + 30*time.Second)
	interval := 2 * time.Minute
	firstEvaluation := time.Date(2026, 8, 18, 10, 4, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loki/api/v1/query_range":
			if got := r.URL.Query().Get("start"); got != strconv.FormatInt(firstEvaluation.UnixNano(), 10) {
				t.Fatalf("query_range start = %s, want %d", got, firstEvaluation.UnixNano())
			}
			if got := r.URL.Query().Get("end"); got != strconv.FormatInt(firstEvaluation.Add(interval).UnixNano(), 10) {
				t.Fatalf("query_range end = %s, want %d", got, firstEvaluation.Add(interval).UnixNano())
			}
			if query := r.URL.Query().Get("query"); !strings.Contains(query, "[2m] offset 90s") {
				t.Fatalf("query = %q, want exact bucket offset", query)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"resultType": "matrix", "result": []any{
					map[string]any{"metric": map[string]string{}, "values": []any{
						[]any{float64(firstEvaluation.Unix()), "2"},
						[]any{float64(firstEvaluation.Add(interval).Unix()), "3"},
					}},
				}},
			})
		case "/loki/api/v1/query":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"resultType": "vector", "result": []any{
					map[string]any{"metric": map[string]string{}, "value": []any{float64(end.Unix()), "4"}},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := NewWithHTTPClient(server.URL, server.Client(), nil)
	buckets, err := client.Histogram(t.Context(), SearchRequest{Start: start, End: end, Limit: 1}, interval)
	if err != nil {
		t.Fatalf("Histogram() error = %v", err)
	}
	want := []HistogramBucket{
		{Start: start, Count: 2},
		{Start: start.Add(interval), Count: 3},
		{Start: start.Add(2 * interval), Count: 4},
	}
	if !reflect.DeepEqual(buckets, want) {
		t.Fatalf("Histogram() = %#v, want %#v", buckets, want)
	}
}

func TestLokiHistogramCountsSingleBucketWithoutRangeQuery(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query" {
			t.Fatalf("unexpected histogram path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "vector", "result": []any{
				map[string]any{"metric": map[string]string{}, "value": []any{float64(end.Unix()), "5"}},
			}},
		})
	}))
	t.Cleanup(server.Close)
	client := NewWithHTTPClient(server.URL, server.Client(), nil)
	buckets, err := client.Histogram(t.Context(), SearchRequest{Start: start, End: end, Limit: 1}, time.Minute)
	if err != nil {
		t.Fatalf("Histogram() error = %v", err)
	}
	want := []HistogramBucket{{Start: start, Count: 5}}
	if !reflect.DeepEqual(buckets, want) {
		t.Fatalf("Histogram() = %#v, want %#v", buckets, want)
	}
}

func TestLokiFieldValuesUsesScopeAndKeepsGlobalIndexedFastPath(t *testing.T) {
	window := validSearchRequest()
	queryRangeCalls := 0
	labelValueCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loki/api/v1/query_range":
			queryRangeCalls++
			if query := r.URL.Query().Get("query"); !strings.Contains(query, `device_id="42"`) {
				t.Fatalf("scoped field-values query = %q", query)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"resultType": "streams", "result": []any{
					map[string]any{"stream": map[string]string{"device_id": "42", "cluster_id": "7"}, "values": []any{[]any{strconv.FormatInt(window.End.Add(-time.Minute).UnixNano(), 10), "first"}}},
					map[string]any{"stream": map[string]string{"device_id": "42", "cluster_id": "12"}, "values": []any{[]any{strconv.FormatInt(window.End.Add(-2*time.Minute).UnixNano(), 10), "second"}}},
				}},
			})
		case "/loki/api/v1/label/cluster_id/values":
			labelValueCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": []string{"global-a", "global-b"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := NewWithHTTPClient(server.URL, server.Client(), nil)

	scoped, err := client.FieldValues(t.Context(), FieldValuesRequest{
		Field: "cluster_id", Start: window.Start, End: window.End,
		Scope: Scope{DeviceIDs: []uint64{42}}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("FieldValues(scoped): %v", err)
	}
	if want := []string{"12", "7"}; !reflect.DeepEqual(scoped, want) {
		t.Fatalf("FieldValues(scoped) = %#v, want %#v", scoped, want)
	}
	if queryRangeCalls != 1 || labelValueCalls != 0 {
		t.Fatalf("scoped calls: query_range=%d label_values=%d", queryRangeCalls, labelValueCalls)
	}

	global, err := client.FieldValues(t.Context(), FieldValuesRequest{
		Field: "cluster_id", Start: window.Start, End: window.End, Limit: 10,
	})
	if err != nil {
		t.Fatalf("FieldValues(global): %v", err)
	}
	if want := []string{"global-a", "global-b"}; !reflect.DeepEqual(global, want) {
		t.Fatalf("FieldValues(global) = %#v, want %#v", global, want)
	}
	if queryRangeCalls != 1 || labelValueCalls != 1 {
		t.Fatalf("global calls: query_range=%d label_values=%d", queryRangeCalls, labelValueCalls)
	}
}

func TestCompileLogQL_MapsScopeAndKeywords(t *testing.T) {
	req := validSearchRequest()
	req.Scope = Scope{
		DeviceIDs:    []uint64{42},
		Namespaces:   []string{"prod"},
		Nodes:        []string{"node-a"},
		ServiceNames: []string{"api", "worker"},
		Levels:       []string{"ERROR"},
		Files:        []string{"/var/log/app.log"},
		Units:        []string{"sshd.service"},
	}
	req.Filters = []FieldFilter{{Field: "message", Operator: FilterIn, Values: []string{"connection refused", "broken pipe"}}}
	req.Keywords = Keywords{Include: []string{"timeout", "refused"}, Exclude: []string{"healthcheck"}, Mode: MatchAny}
	if err := req.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate() error = %v", err)
	}
	got, err := compileLogQL(req)
	if err != nil {
		t.Fatalf("compileLogQL() error = %v", err)
	}
	for _, want := range []string{
		`device_id="42"`,
		`namespace="prod"`,
		`| node="node-a"`,
		`service_name=~"(?:api|worker)"`,
		`| level="ERROR"`,
		`| filename="/var/log/app.log"`,
		`| unit="sshd.service"`,
		`|~ "(?i)(connection refused|broken pipe)"`,
		`|~ "(?i)(timeout|refused)"`,
		`!~ "(?i)healthcheck"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compileLogQL() = %q, missing %q", got, want)
		}
	}
}

func TestCompileLogQL_DefaultSelectorDoesNotRequireOptionalSourceLabel(t *testing.T) {
	req := validSearchRequest()
	if err := req.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate() error = %v", err)
	}
	got, err := compileLogQL(req)
	if err != nil {
		t.Fatalf("compileLogQL() error = %v", err)
	}
	if !strings.Contains(got, `device_id=~".+"`) {
		t.Fatalf("compileLogQL() = %q, want device-wide default selector", got)
	}
	if strings.Contains(got, "ongrid_source") {
		t.Fatalf("compileLogQL() = %q, optional source label must not gate unfiltered search", got)
	}
}

func TestDecodeLokiRecords_ProducesStableRecord(t *testing.T) {
	raw, err := json.Marshal([]map[string]any{{
		"stream": map[string]string{
			"cluster_id": "7", "cluster_name": "edge-fleet-a",
		},
		"values": []any{[]any{
			"1787054400000000000", "connection refused",
			map[string]string{
				"filename":                 "/var/log/app.log",
				"trace_id":                 "abc123",
				"severity_text":            "ERROR",
				"severity_number":          "17",
				"service_name":             "unknown_service",
				"attributes_device_id":     "42",
				"attributes_ongrid_source": "journald",
			},
		}},
	}})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result := &QueryRangeResult{ResultType: "streams", Result: raw}
	first, err := decodeLokiRecords(result)
	if err != nil {
		t.Fatalf("decodeLokiRecords() error = %v", err)
	}
	second, err := decodeLokiRecords(result)
	if err != nil {
		t.Fatalf("decodeLokiRecords() second error = %v", err)
	}
	if len(first) != 1 || first[0].ID == "" || first[0].ID != second[0].ID {
		t.Fatalf("records do not have a stable id: %#v %#v", first, second)
	}
	if first[0].Timestamp.IsZero() || first[0].Message != "connection refused" || first[0].SeverityText != "error" || first[0].SeverityNumber != 17 {
		t.Fatalf("decoded record = %#v", first[0])
	}
	if first[0].ResourceAttributes["device_id"] != "42" || first[0].ResourceAttributes["cluster_id"] != "7" || first[0].ResourceAttributes["cluster_name"] != "edge-fleet-a" {
		t.Fatalf("resource attributes = %#v", first[0].ResourceAttributes)
	}
	if first[0].Attributes["filename"] != "/var/log/app.log" || first[0].TraceID != "abc123" {
		t.Fatalf("structured metadata = %#v trace=%q", first[0].Attributes, first[0].TraceID)
	}
	if _, ok := first[0].Attributes["severity_text"]; ok {
		t.Fatalf("severity_text leaked into attributes: %#v", first[0].Attributes)
	}
	if _, ok := first[0].Attributes["severity_number"]; ok {
		t.Fatalf("severity_number leaked into attributes: %#v", first[0].Attributes)
	}
	for _, name := range []string{"service_name", "attributes_device_id", "attributes_ongrid_source"} {
		if _, ok := first[0].Attributes[name]; ok {
			t.Fatalf("internal Loki field %q leaked into attributes: %#v", name, first[0].Attributes)
		}
	}
	if _, ok := first[0].ResourceAttributes["service_name"]; ok {
		t.Fatalf("unknown service leaked into resource attributes: %#v", first[0].ResourceAttributes)
	}
}

func TestDecodeLokiRecordsFallsBackToDetectedAndBodyLevels(t *testing.T) {
	raw, err := json.Marshal([]map[string]any{
		{
			"stream": map[string]string{"device_id": "42"},
			"values": []any{[]any{"1787054400000000000", "probe", map[string]string{"detected_level": "WARN"}}},
		},
		{
			"stream": map[string]string{"device_id": "43"},
			"values": []any{[]any{"1787054401000000000", `I0821 13:51:44.571438 320 scope.go:122] "RemoveContainer"`}},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	records, err := decodeLokiRecords(&QueryRangeResult{ResultType: "streams", Result: raw})
	if err != nil {
		t.Fatalf("decodeLokiRecords() error = %v", err)
	}
	if len(records) != 2 || records[0].SeverityText != "warn" || records[1].SeverityText != "info" {
		t.Fatalf("decoded records = %#v", records)
	}
}

func TestLokiSearchCursorDoesNotSkipRecordsSharingTimestamp(t *testing.T) {
	const timestamp = "1787054400000000000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "streams", "result": []any{
				map[string]any{"stream": map[string]string{"device_id": "42", "source": "a"}, "values": []any{[]any{timestamp, "alpha"}}},
				map[string]any{"stream": map[string]string{"device_id": "42", "source": "b"}, "values": []any{[]any{timestamp, "bravo"}}},
				map[string]any{"stream": map[string]string{"device_id": "42", "source": "c"}, "values": []any{[]any{timestamp, "charlie"}}},
			}},
		})
	}))
	t.Cleanup(server.Close)
	client := NewWithHTTPClient(server.URL, server.Client(), nil)
	req := validSearchRequest()
	req.Limit = 1
	seen := map[string]bool{}
	for page := 0; page < 3; page++ {
		result, err := client.Search(t.Context(), req)
		if err != nil {
			t.Fatalf("Search(page %d): %v", page, err)
		}
		if len(result.Records) != 1 || seen[result.Records[0].ID] {
			t.Fatalf("page %d records = %#v, seen=%#v", page, result.Records, seen)
		}
		seen[result.Records[0].ID] = true
		req.Cursor = result.NextCursor
	}
	if len(seen) != 3 {
		t.Fatalf("seen = %#v", seen)
	}
}

func TestLokiSearchCursorIncludesExclusiveEndBoundary(t *testing.T) {
	newest := time.Date(2026, 8, 21, 4, 0, 3, 0, time.UTC)
	boundary := newest.Add(-time.Second)
	oldest := boundary.Add(-time.Second)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		calls++
		values := []any{
			[]any{strconv.FormatInt(newest.UnixNano(), 10), "newest"},
			[]any{strconv.FormatInt(boundary.UnixNano(), 10), "boundary"},
			[]any{strconv.FormatInt(oldest.UnixNano(), 10), "oldest"},
		}
		if calls == 2 {
			wantEnd := boundary.Add(time.Nanosecond).UnixNano()
			gotEnd, err := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
			if err != nil || gotEnd != wantEnd {
				t.Fatalf("second page end = %q, want %d", r.URL.Query().Get("end"), wantEnd)
			}
			// Loki treats the range end as exclusive for log queries. Nudging the
			// bound by one nanosecond keeps the cursor record available for the
			// skip count and prevents the next page from being rejected.
			values = values[1:]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "streams", "result": []any{
				map[string]any{"stream": map[string]string{"device_id": "42", "source": "edge"}, "values": values},
			}},
		})
	}))
	t.Cleanup(server.Close)
	client := NewWithHTTPClient(server.URL, server.Client(), nil)
	req := validSearchRequest()
	req.Start = oldest.Add(-time.Second)
	req.End = newest.Add(time.Second)
	req.Limit = 2

	first, err := client.Search(t.Context(), req)
	if err != nil {
		t.Fatalf("Search(first): %v", err)
	}
	if len(first.Records) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	req.Cursor = first.NextCursor
	second, err := client.Search(t.Context(), req)
	if err != nil {
		t.Fatalf("Search(second): %v", err)
	}
	if len(second.Records) != 1 || second.Records[0].Message != "oldest" || second.HasMore {
		t.Fatalf("second page = %#v", second)
	}
}

func TestLogQLDuration_UsesSupportedUnits(t *testing.T) {
	cases := map[time.Duration]string{
		time.Hour:                          "1h",
		5 * time.Minute:                    "5m",
		30 * time.Second:                   "30s",
		1337 * time.Millisecond:            "1337ms",
		time.Millisecond + time.Nanosecond: "2ms",
	}
	for input, want := range cases {
		if got := logQLDuration(input); got != want {
			t.Fatalf("logQLDuration(%s) = %q, want %q", input, got, want)
		}
	}
}
