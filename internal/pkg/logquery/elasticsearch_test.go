package logquery

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestElasticsearchClient_SearchUsesPITAndOpaqueCursor(t *testing.T) {
	searchCalls := 0
	closed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "ApiKey test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/_pit"):
			if got := r.URL.Query().Get("keep_alive"); got != "5m" {
				t.Fatalf("PIT keep_alive = %q", got)
			}
			writeTestJSON(t, w, map[string]any{"id": "pit-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/_search":
			searchCalls++
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if !strings.Contains(string(body), `"resource.attributes.device_id":["42"]`) || !strings.Contains(string(body), `"resource.attributes.service.name":["api"]`) || !strings.Contains(string(body), `"severity_text":["ERROR"]`) || !strings.Contains(string(body), `"match_phrase":{"body.text":"timeout"}`) {
				t.Fatalf("search body missing scoped query: %s", body)
			}
			if strings.Contains(string(body), "simple_query_string") {
				t.Fatalf("search body exposed query-string syntax: %s", body)
			}
			if !strings.Contains(string(body), `"keep_alive":"5m"`) {
				t.Fatalf("search body missing PIT renewal: %s", body)
			}
			if searchCalls == 2 && !strings.Contains(string(body), `"search_after"`) {
				t.Fatalf("second search body missing search_after: %s", body)
			}
			hits := []any{
				testElasticsearchHit("one", "2026-08-18T12:00:00Z", "timeout", []any{"2026-08-18T12:00:00Z", 7}),
			}
			if searchCalls == 1 {
				hits = append(hits, testElasticsearchHit("two", "2026-08-18T11:59:59Z", "next", []any{"2026-08-18T11:59:59Z", 8}))
			}
			writeTestJSON(t, w, map[string]any{"took": 2, "pit_id": "pit-1", "hits": map[string]any{"hits": hits}})
		case r.Method == http.MethodDelete && r.URL.Path == "/_pit":
			closed = true
			writeTestJSON(t, w, map[string]any{"succeeded": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewElasticsearchClient(ElasticsearchConfig{
		Endpoint: server.URL, APIKey: "test-key", AllowInsecureHTTP: true,
	}, server.Client(), nil)
	if err != nil {
		t.Fatalf("NewElasticsearchClient() error = %v", err)
	}
	req := validSearchRequest()
	req.Limit = 1
	req.Scope.DeviceIDs = []uint64{42}
	req.Scope.ServiceNames = []string{"api"}
	req.Scope.Levels = []string{"ERROR"}
	req.Keywords.Include = []string{"timeout"}
	first, err := client.Search(t.Context(), req)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(first.Records) != 1 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first result = %#v", first)
	}
	if first.Records[0].ResourceAttributes["device_id"] != "42" || first.Records[0].ResourceAttributes["service_name"] != "api" {
		t.Fatalf("resource attributes = %#v", first.Records[0].ResourceAttributes)
	}
	req.Cursor = first.NextCursor
	second, err := client.Search(t.Context(), req)
	if err != nil {
		t.Fatalf("Search(cursor) error = %v", err)
	}
	if len(second.Records) != 1 || second.HasMore || second.NextCursor != "" || !closed {
		t.Fatalf("second result = %#v closed=%v", second, closed)
	}
}

func TestElasticsearchClient_ClosesPITWhenContinuationFails(t *testing.T) {
	searchCalls := 0
	closed := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/_pit"):
			writeTestJSON(t, w, map[string]any{"id": "pit-failed-continuation"})
		case r.Method == http.MethodPost && r.URL.Path == "/_search":
			searchCalls++
			if searchCalls > 1 {
				http.Error(w, `{"error":"injected"}`, http.StatusInternalServerError)
				return
			}
			writeTestJSON(t, w, map[string]any{
				"pit_id": "pit-failed-continuation",
				"hits": map[string]any{"hits": []any{
					testElasticsearchHit("one", "2026-08-18T12:00:00Z", "first", []any{"2026-08-18T12:00:00Z", 1}),
					testElasticsearchHit("two", "2026-08-18T11:59:59Z", "next", []any{"2026-08-18T11:59:59Z", 2}),
				}},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/_pit":
			closed++
			writeTestJSON(t, w, map[string]any{"succeeded": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewElasticsearchClient(ElasticsearchConfig{
		Endpoint: server.URL, APIKey: "test-key", AllowInsecureHTTP: true,
	}, server.Client(), nil)
	if err != nil {
		t.Fatalf("NewElasticsearchClient() error = %v", err)
	}
	req := validSearchRequest()
	req.Limit = 1
	first, err := client.Search(t.Context(), req)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first Search() = %#v, %v", first, err)
	}
	req.Cursor = first.NextCursor
	if _, err := client.Search(t.Context(), req); err == nil {
		t.Fatal("continuation Search() unexpectedly succeeded")
	}
	if closed != 1 {
		t.Fatalf("PIT close calls = %d, want 1", closed)
	}
}

func TestElasticsearchClient_CloseCursorReleasesAbandonedPIT(t *testing.T) {
	closed := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/_pit"):
			writeTestJSON(t, w, map[string]any{"id": "pit-abandoned"})
		case r.Method == http.MethodPost && r.URL.Path == "/_search":
			writeTestJSON(t, w, map[string]any{
				"pit_id": "pit-abandoned",
				"hits": map[string]any{"hits": []any{
					testElasticsearchHit("one", "2026-08-18T12:00:00Z", "first", []any{"2026-08-18T12:00:00Z", 1}),
					testElasticsearchHit("two", "2026-08-18T11:59:59Z", "next", []any{"2026-08-18T11:59:59Z", 2}),
				}},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/_pit":
			closed++
			writeTestJSON(t, w, map[string]any{"succeeded": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewElasticsearchClient(ElasticsearchConfig{
		Endpoint: server.URL, APIKey: "test-key", AllowInsecureHTTP: true,
	}, server.Client(), nil)
	if err != nil {
		t.Fatalf("NewElasticsearchClient() error = %v", err)
	}
	req := validSearchRequest()
	req.Limit = 1
	result, err := client.Search(t.Context(), req)
	if err != nil || result.NextCursor == "" {
		t.Fatalf("Search() = %#v, %v", result, err)
	}
	if err := client.CloseCursor(t.Context(), result.NextCursor); err != nil {
		t.Fatalf("CloseCursor() error = %v", err)
	}
	if closed != 1 {
		t.Fatalf("PIT close calls = %d, want 1", closed)
	}
}

func TestElasticsearchClient_SearchAcceptsValidPageLargerThanControlResponseLimit(t *testing.T) {
	const recordCount = 70
	message := strings.Repeat("x", 240<<10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/_pit"):
			writeTestJSON(t, w, map[string]any{"id": "pit-large-page"})
		case r.Method == http.MethodPost && r.URL.Path == "/_search":
			hits := make([]any, 0, recordCount)
			for i := 0; i < recordCount; i++ {
				hits = append(hits, testElasticsearchHit(
					fmt.Sprintf("record-%d", i),
					"2026-08-18T12:00:00Z",
					message,
					[]any{"2026-08-18T12:00:00Z", i},
				))
			}
			writeTestJSON(t, w, map[string]any{"pit_id": "pit-large-page", "hits": map[string]any{"hits": hits}})
		case r.Method == http.MethodDelete && r.URL.Path == "/_pit":
			writeTestJSON(t, w, map[string]any{"succeeded": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewElasticsearchClient(ElasticsearchConfig{
		Endpoint: server.URL, APIKey: "test-key", AllowInsecureHTTP: true,
	}, server.Client(), nil)
	if err != nil {
		t.Fatalf("NewElasticsearchClient() error = %v", err)
	}
	req := validSearchRequest()
	req.Limit = recordCount
	result, err := client.Search(t.Context(), req)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Records) != recordCount || result.Records[0].Message != message {
		t.Fatalf("Search() records = %d first message bytes=%d", len(result.Records), len(result.Records[0].Message))
	}
}

func TestMaxESSearchResponseBytesUsesFixedHardLimit(t *testing.T) {
	if got := maxESSearchResponseBytes(MaxSearchLimit); got != maxESSearchResponseHardBytes {
		t.Fatalf("maxESSearchResponseBytes(%d) = %d, want %d", MaxSearchLimit, got, maxESSearchResponseHardBytes)
	}
	if got := maxESSearchResponseBytes(1); got >= maxESSearchResponseHardBytes {
		t.Fatalf("maxESSearchResponseBytes(1) = %d, want below hard limit %d", got, maxESSearchResponseHardBytes)
	}
}

func TestElasticsearchClient_HistogramAlignsBucketsToRequestStart(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 30, 323_000, time.UTC)
	elasticsearchStart := start.Truncate(time.Millisecond)
	end := start.Add(2*time.Minute + 30*time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/_search") {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode request: %v", err)
		}
		aggs := body["aggs"].(map[string]any)
		timeline := aggs["timeline"].(map[string]any)
		histogram := timeline["date_histogram"].(map[string]any)
		if histogram["offset"] != "30s" {
			t.Fatalf("histogram offset = %#v, want 30s", histogram["offset"])
		}
		if _, ok := histogram["hard_bounds"]; !ok {
			t.Fatalf("histogram is missing hard_bounds: %#v", histogram)
		}
		writeTestJSON(t, w, map[string]any{"aggregations": map[string]any{"timeline": map[string]any{"buckets": []any{
			map[string]any{"key_as_string": elasticsearchStart.Format(time.RFC3339Nano), "doc_count": 2},
			map[string]any{"key_as_string": elasticsearchStart.Add(time.Minute).Format(time.RFC3339Nano), "doc_count": 3},
			map[string]any{"key_as_string": elasticsearchStart.Add(2 * time.Minute).Format(time.RFC3339Nano), "doc_count": 1},
		}}}})
	}))
	t.Cleanup(server.Close)
	client, err := NewElasticsearchClient(ElasticsearchConfig{
		Endpoint: server.URL, APIKey: "test-key", AllowInsecureHTTP: true,
	}, server.Client(), nil)
	if err != nil {
		t.Fatalf("NewElasticsearchClient() error = %v", err)
	}
	buckets, err := client.Histogram(t.Context(), SearchRequest{Start: start, End: end, Limit: 1}, time.Minute)
	if err != nil {
		t.Fatalf("Histogram() error = %v", err)
	}
	if len(buckets) != 3 || !buckets[0].Start.Equal(start) || buckets[2].Count != 1 {
		t.Fatalf("Histogram() = %#v", buckets)
	}
}

func TestElasticsearchClient_ProbeRequiresSupportedVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, w, map[string]any{
			"cluster_uuid": "cluster-a",
			"version":      map[string]string{"number": "8.16.3"},
		})
	}))
	t.Cleanup(server.Close)
	client, err := NewElasticsearchClient(ElasticsearchConfig{
		Endpoint: server.URL, APIKey: "test-key", AllowInsecureHTTP: true,
	}, server.Client(), nil)
	if err != nil {
		t.Fatalf("NewElasticsearchClient() error = %v", err)
	}
	version, err := client.Probe(t.Context())
	if err != nil || version != "8.16.3" {
		t.Fatalf("Probe() = %q, %v", version, err)
	}
	info, err := client.ProbeInfo(t.Context())
	if err != nil || info.Version != "8.16.3" || info.ClusterUUID != "cluster-a" {
		t.Fatalf("ProbeInfo() = %+v, %v", info, err)
	}
}

func TestDecodeElasticsearchRecordFallsBackToResourceLevel(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"@timestamp": "2026-08-18T12:00:00Z",
		"body":       map[string]any{"text": "hello"},
		"resource": map[string]any{"attributes": map[string]any{
			"device_id": "42", "cluster_id": "7", "cluster_name": "edge-fleet-a", "level": "WARN",
		}},
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	record, err := decodeElasticsearchRecord("log-1", raw)
	if err != nil {
		t.Fatalf("decodeElasticsearchRecord: %v", err)
	}
	if record.SeverityText != "warn" || record.SeverityNumber != 13 || record.ResourceAttributes["cluster_name"] != "edge-fleet-a" {
		t.Fatalf("record = %#v", record)
	}
}

func TestDecodeElasticsearchRecordDetectsHistoricalKlogLevel(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"@timestamp": "2026-08-21T13:51:44Z",
		"body":       map[string]any{"text": `E0821 13:51:44.572761 320 pod_workers.go:1324] "Error syncing pod"`},
		"resource":   map[string]any{"attributes": map[string]any{"device_id": "650"}},
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	record, err := decodeElasticsearchRecord("log-klog", raw)
	if err != nil {
		t.Fatalf("decodeElasticsearchRecord: %v", err)
	}
	if record.SeverityText != "error" || record.SeverityNumber != 17 {
		t.Fatalf("record severity = %q/%d, want error/17", record.SeverityText, record.SeverityNumber)
	}
}

func TestElasticsearchClient_RequirePrivilegesUsesFixedIndexPattern(t *testing.T) {
	const pattern = "logs-ongrid.*.otel-prod"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/_security/user/_has_privileges" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		for _, want := range []string{`"cluster":["monitor"]`, `"names":["` + pattern + `"]`, `"privileges":["read","view_index_metadata"]`} {
			if !strings.Contains(string(body), want) {
				t.Fatalf("privilege body = %s, missing %s", body, want)
			}
		}
		writeTestJSON(t, w, map[string]any{
			"has_all_requested": false,
			"cluster":           map[string]bool{"monitor": false},
			"index": map[string]any{
				pattern: map[string]bool{"read": true, "view_index_metadata": false},
			},
		})
	}))
	t.Cleanup(server.Close)
	client, err := NewElasticsearchClient(ElasticsearchConfig{
		Endpoint: server.URL, IndexPattern: pattern, APIKey: "test-key", AllowInsecureHTTP: true,
	}, server.Client(), nil)
	if err != nil {
		t.Fatalf("NewElasticsearchClient() error = %v", err)
	}
	err = client.RequirePrivileges(t.Context(), []string{"monitor"}, []string{"read", "view_index_metadata"})
	if err == nil || !strings.Contains(err.Error(), "cluster:monitor") || !strings.Contains(err.Error(), "index:view_index_metadata") {
		t.Fatalf("RequirePrivileges() error = %v", err)
	}
}

func TestElasticsearchClient_CountUsesFixedIndexAndStructuredQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/_count") {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/logs-ongrid.") {
			t.Fatalf("count path = %q", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		for _, want := range []string{`"resource.attributes.namespace":["prod"]`, `"body.text"`, `"panic"`, `"gt":"2026-08-18T11:00:00Z"`} {
			if !strings.Contains(string(body), want) {
				t.Fatalf("count body = %s, missing %s", body, want)
			}
		}
		writeTestJSON(t, w, map[string]any{"count": 9})
	}))
	t.Cleanup(server.Close)
	client, err := NewElasticsearchClient(ElasticsearchConfig{
		Endpoint: server.URL, APIKey: "test-key", AllowInsecureHTTP: true,
	}, server.Client(), nil)
	if err != nil {
		t.Fatalf("NewElasticsearchClient() error = %v", err)
	}
	req := validSearchRequest()
	req.Scope.Namespaces = []string{"prod"}
	req.Keywords = Keywords{Include: []string{"panic"}, Mode: MatchPhrase}
	count, err := client.Count(t.Context(), req)
	if err != nil || count != 9 {
		t.Fatalf("Count() = %d, %v", count, err)
	}
}

func TestElasticsearchClient_CountGroupedPagesCompositeAggregation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/_search") {
			http.NotFound(w, r)
			return
		}
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		aggs := body["aggs"].(map[string]any)
		groups := aggs["groups"].(map[string]any)
		composite := groups["composite"].(map[string]any)
		sources := composite["sources"].([]any)
		encodedSources, _ := json.Marshal(sources)
		for _, want := range []string{"resource.attributes.device_id", "resource.attributes.ongrid_source", "missing_bucket"} {
			if !strings.Contains(string(encodedSources), want) {
				t.Fatalf("sources = %s, missing %q", encodedSources, want)
			}
		}
		if calls == 1 {
			if _, ok := composite["after"]; ok {
				t.Fatalf("first composite unexpectedly has after: %#v", composite)
			}
			writeTestJSON(t, w, map[string]any{"aggregations": map[string]any{"groups": map[string]any{
				"after_key": map[string]any{"device_id": "42", "source_id": "journald"},
				"buckets":   []any{map[string]any{"key": map[string]any{"device_id": "42", "source_id": "journald"}, "doc_count": 6}},
			}}})
			return
		}
		after, ok := composite["after"].(map[string]any)
		if !ok || after["device_id"] != "42" || after["source_id"] != "journald" {
			t.Fatalf("second composite after = %#v", composite["after"])
		}
		writeTestJSON(t, w, map[string]any{"aggregations": map[string]any{"groups": map[string]any{
			"buckets": []any{map[string]any{"key": map[string]any{"device_id": "43", "source_id": "kubernetes:pod"}, "doc_count": 2}},
		}}})
	}))
	t.Cleanup(server.Close)
	client, err := NewElasticsearchClient(ElasticsearchConfig{
		Endpoint: server.URL, APIKey: "test-key", AllowInsecureHTTP: true,
	}, server.Client(), nil)
	if err != nil {
		t.Fatalf("NewElasticsearchClient() error = %v", err)
	}
	groups, err := client.CountGrouped(t.Context(), validSearchRequest(), []string{"device_id", "source_id"})
	if err != nil {
		t.Fatalf("CountGrouped() error = %v", err)
	}
	if calls != 2 || len(groups) != 2 || groups[0].Count != 6 || groups[1].Labels["device_id"] != "43" {
		t.Fatalf("calls=%d groups=%#v", calls, groups)
	}
}

func TestElasticsearchClient_CountGroupedOmitsUnknownService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/_search") {
			http.NotFound(w, r)
			return
		}
		writeTestJSON(t, w, map[string]any{"aggregations": map[string]any{"groups": map[string]any{
			"buckets": []any{map[string]any{
				"key":       map[string]any{"device_id": "42", "service_name": "unknown_service"},
				"doc_count": 2,
			}},
		}}})
	}))
	t.Cleanup(server.Close)
	client, err := NewElasticsearchClient(ElasticsearchConfig{
		Endpoint: server.URL, APIKey: "test-key", AllowInsecureHTTP: true,
	}, server.Client(), nil)
	if err != nil {
		t.Fatalf("NewElasticsearchClient() error = %v", err)
	}
	groups, err := client.CountGrouped(t.Context(), validSearchRequest(), []string{"device_id", "service_name"})
	if err != nil {
		t.Fatalf("CountGrouped() error = %v", err)
	}
	if len(groups) != 1 || groups[0].Labels["device_id"] != "42" {
		t.Fatalf("groups = %#v", groups)
	}
	if _, exists := groups[0].Labels["service_name"]; exists {
		t.Fatalf("unknown service sentinel was retained: %#v", groups[0].Labels)
	}
}

func TestNewElasticsearchClient_RejectsUnsafeEndpointAndIndexPattern(t *testing.T) {
	cases := []ElasticsearchConfig{
		{Endpoint: "http://es.example", APIKey: "key"},
		{Endpoint: "https://user:pass@es.example", APIKey: "key"},
		{Endpoint: "https://es.example", APIKey: "key", IndexPattern: "*"},
		{Endpoint: "https://es.example", APIKey: ""},
		{Endpoint: "https://es.example", BasicUser: "reader"},
		{Endpoint: "https://es.example", BasicPassword: "test-password"},
		{Endpoint: "https://es.example", BasicUser: "reader:other", BasicPassword: "test-password"},
		{Endpoint: "https://es.example", APIKey: "key", BasicUser: "reader", BasicPassword: "test-password"},
	}
	for _, cfg := range cases {
		if _, err := NewElasticsearchClient(cfg, nil, nil); err == nil {
			t.Fatalf("NewElasticsearchClient(%#v) unexpectedly succeeded", cfg)
		}
	}
}

func testElasticsearchHit(id, timestamp, message string, sort []any) map[string]any {
	return map[string]any{
		"_id": id,
		"_source": map[string]any{
			"@timestamp":    timestamp,
			"body":          map[string]any{"text": message},
			"severity_text": "ERROR",
			"resource": map[string]any{
				"attributes": map[string]any{"device_id": "42", "service.name": "api"},
			},
		},
		"sort": sort,
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
}

func TestElasticsearchDuration_UsesFixedIntervalSyntax(t *testing.T) {
	cases := map[time.Duration]string{time.Hour: "1h", 5 * time.Minute: "5m", 250 * time.Millisecond: "250ms"}
	for input, want := range cases {
		if got := elasticsearchDuration(input); got != want {
			t.Fatalf("elasticsearchDuration(%s) = %q, want %q", input, got, want)
		}
	}
}
