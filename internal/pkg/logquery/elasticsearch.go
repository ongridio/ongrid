package logquery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	oteltracing "github.com/ongridio/ongrid/internal/pkg/tracing"
)

const (
	elasticsearchBackendName     = "elasticsearch"
	defaultESIndexPattern        = "logs-ongrid.*.otel-*"
	defaultESKeepAlive           = 5 * time.Minute
	maxESResponseBytes           = 16 * 1024 * 1024
	maxESSearchResponseHardBytes = 32 * 1024 * 1024
	// Edge collectors accept a 256 KiB log body. Search responses therefore
	// scale with page size and reserve another 64 KiB per hit for the OTel
	// source envelope and sort metadata. The calculated allowance is still
	// clamped to a fixed process-memory budget below.
	maxESSearchEnvelopeBytes = 1 * 1024 * 1024
	maxESSearchHitBytes      = 320 * 1024
)

type ElasticsearchConfig struct {
	Endpoint          string
	IndexPattern      string
	APIKey            string
	AllowInsecureHTTP bool
}

// ElasticsearchClient implements the backend-neutral log Searcher by using
// the Elasticsearch REST API. It deliberately owns a fixed index pattern and
// never accepts index names or raw query DSL from callers.
type ElasticsearchClient struct {
	endpoint     string
	indexPattern string
	apiKey       string
	httpClient   *http.Client
	log          *slog.Logger
}

type elasticsearchCursor struct {
	Backend   string            `json:"backend"`
	PITID     string            `json:"pit_id"`
	Sort      []json.RawMessage `json:"sort"`
	Direction SortDirection     `json:"direction"`
}

func NewElasticsearchClient(cfg ElasticsearchConfig, hc *http.Client, log *slog.Logger) (*ElasticsearchClient, error) {
	endpoint, err := validateElasticsearchEndpoint(cfg.Endpoint, cfg.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	indexPattern := strings.TrimSpace(cfg.IndexPattern)
	if indexPattern == "" {
		indexPattern = defaultESIndexPattern
	}
	if err := validateElasticsearchIndexPattern(indexPattern); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("logquery: Elasticsearch API key is required")
	}
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	hc = oteltracing.InstrumentHTTPClient(hc, elasticsearchBackendName)
	if log == nil {
		log = slog.Default()
	}
	return &ElasticsearchClient{
		endpoint:     endpoint,
		indexPattern: indexPattern,
		apiKey:       strings.TrimSpace(cfg.APIKey),
		httpClient:   hc,
		log:          log,
	}, nil
}

func (c *ElasticsearchClient) Search(ctx context.Context, req SearchRequest) (_ *SearchResult, retErr error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	started := time.Now()
	var cursor elasticsearchCursor
	if req.Cursor != "" {
		if err := decodeCursor(req.Cursor, &cursor); err != nil {
			return nil, err
		}
		if cursor.Backend != elasticsearchBackendName || cursor.PITID == "" || cursor.Direction != req.Direction || len(cursor.Sort) == 0 {
			return nil, errInvalidCursor
		}
	} else {
		pitID, err := c.openPIT(ctx)
		if err != nil {
			return nil, err
		}
		cursor = elasticsearchCursor{Backend: elasticsearchBackendName, PITID: pitID, Direction: req.Direction}
	}
	defer func() {
		// A failed continuation is abandoned by the HTTP/tool caller, so close
		// its PIT as well as a PIT opened by this call. This keeps canceled or
		// failed pagination from accumulating server-side search contexts.
		if retErr != nil {
			if err := c.closePIT(context.WithoutCancel(ctx), cursor.PITID); err != nil {
				retErr = errors.Join(retErr, err)
			}
		}
	}()

	body, err := buildElasticsearchSearchBody(req, cursor)
	if err != nil {
		return nil, err
	}
	var response struct {
		Took int64  `json:"took"`
		PIT  string `json:"pit_id"`
		Hits struct {
			Hits []struct {
				ID     string            `json:"_id"`
				Source json.RawMessage   `json:"_source"`
				Sort   []json.RawMessage `json:"sort"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := c.doJSONWithLimit(ctx, http.MethodPost, "/_search", nil, body, &response, maxESSearchResponseBytes(req.Limit)); err != nil {
		return nil, err
	}
	if response.PIT != "" {
		cursor.PITID = response.PIT
	}
	records := make([]Record, 0, min(len(response.Hits.Hits), req.Limit))
	for i, hit := range response.Hits.Hits {
		if i >= req.Limit {
			break
		}
		record, err := decodeElasticsearchRecord(hit.ID, hit.Source)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	hasMore := len(response.Hits.Hits) > req.Limit
	var next string
	if hasMore && len(response.Hits.Hits) >= req.Limit {
		cursor.Sort = response.Hits.Hits[req.Limit-1].Sort
		next, err = encodeCursor(cursor)
		if err != nil {
			return nil, err
		}
	} else {
		if err := c.closePIT(ctx, cursor.PITID); err != nil {
			return nil, err
		}
	}
	took := response.Took
	if took == 0 {
		took = time.Since(started).Milliseconds()
	}
	return &SearchResult{
		Records:    records,
		NextCursor: next,
		HasMore:    hasMore,
		TookMS:     took,
		Backends:   []string{elasticsearchBackendName},
	}, nil
}

// CloseCursor releases the Elasticsearch PIT embedded in an opaque cursor.
// Callers use it when pagination is intentionally abandoned.
func (c *ElasticsearchClient) CloseCursor(ctx context.Context, encoded string) error {
	var cursor elasticsearchCursor
	if err := decodeCursor(encoded, &cursor); err != nil {
		return err
	}
	if cursor.Backend != elasticsearchBackendName || cursor.PITID == "" {
		return errInvalidCursor
	}
	return c.closePIT(ctx, cursor.PITID)
}

// Count uses Elasticsearch's count API with the same allowlisted query
// compiler as Search. No caller-controlled index or raw DSL is accepted.
func (c *ElasticsearchClient) Count(ctx context.Context, req SearchRequest) (uint64, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return 0, err
	}
	query, err := buildElasticsearchQueryWithStart(req, "gt")
	if err != nil {
		return 0, err
	}
	body := map[string]any{"query": query}
	var response struct {
		Count uint64 `json:"count"`
	}
	path := "/" + c.indexPattern + "/_count"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, body, &response); err != nil {
		return 0, err
	}
	return response.Count, nil
}

// CountGrouped uses a composite aggregation so alert evaluation can enumerate
// every matching group without relying on a backend-specific terms size or
// silently truncating high-cardinality results.
func (c *ElasticsearchClient) CountGrouped(ctx context.Context, req SearchRequest, groupBy []string) ([]CountGroup, error) {
	fields, err := NormalizeGroupBy(groupBy)
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		count, err := c.Count(ctx, req)
		if err != nil {
			return nil, err
		}
		return []CountGroup{{Count: count}}, nil
	}
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	query, err := buildElasticsearchQueryWithStart(req, "gt")
	if err != nil {
		return nil, err
	}
	sources := make([]any, 0, len(fields))
	for _, field := range fields {
		def, _ := LookupField(field)
		sources = append(sources, map[string]any{
			field: map[string]any{"terms": map[string]any{
				"field": def.ElasticsearchPath, "missing_bucket": true,
			}},
		})
	}

	const pageSize = 500
	path := "/" + c.indexPattern + "/_search"
	groups := make([]CountGroup, 0, min(pageSize, MaxCountGroups))
	var after map[string]json.RawMessage
	previousAfter := ""
	for {
		composite := map[string]any{"size": pageSize, "sources": sources}
		if len(after) > 0 {
			composite["after"] = after
		}
		body := map[string]any{
			"size":  0,
			"query": query,
			"aggs":  map[string]any{"groups": map[string]any{"composite": composite}},
		}
		var response struct {
			Aggregations struct {
				Groups struct {
					AfterKey map[string]json.RawMessage `json:"after_key"`
					Buckets  []struct {
						Key      map[string]json.RawMessage `json:"key"`
						DocCount uint64                     `json:"doc_count"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"aggregations"`
		}
		if err := c.doJSON(ctx, http.MethodPost, path, nil, body, &response); err != nil {
			return nil, err
		}
		for _, bucket := range response.Aggregations.Groups.Buckets {
			labels := make(map[string]string, len(fields))
			for _, field := range fields {
				value, err := elasticsearchGroupValue(bucket.Key[field])
				if err != nil {
					return nil, fmt.Errorf("logquery: decode Elasticsearch group %q: %w", field, err)
				}
				if value, ok := normalizeGroupedLabelValue(field, value); ok {
					labels[field] = value
				}
			}
			groups = append(groups, CountGroup{Labels: labels, Count: bucket.DocCount})
			if len(groups) > MaxCountGroups {
				return nil, fmt.Errorf("logquery: Elasticsearch grouped count exceeds %d buckets", MaxCountGroups)
			}
		}
		if len(response.Aggregations.Groups.Buckets) == 0 || len(response.Aggregations.Groups.AfterKey) == 0 {
			break
		}
		encodedAfter, err := json.Marshal(response.Aggregations.Groups.AfterKey)
		if err != nil {
			return nil, fmt.Errorf("logquery: encode Elasticsearch composite cursor: %w", err)
		}
		if string(encodedAfter) == previousAfter {
			return nil, errors.New("logquery: Elasticsearch composite cursor did not advance")
		}
		previousAfter = string(encodedAfter)
		after = response.Aggregations.Groups.AfterKey
	}
	return groups, nil
}

func elasticsearchGroupValue(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func (c *ElasticsearchClient) Fields(_ context.Context, _, _ time.Time, _ Scope) ([]Field, error) {
	return AllowedFields(), nil
}

func (c *ElasticsearchClient) FieldValues(ctx context.Context, req FieldValuesRequest) ([]string, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	def, _ := LookupField(req.Field)
	if !def.Aggregatable {
		return nil, fmt.Errorf("logquery: field %q is not aggregatable", req.Field)
	}
	filters, err := elasticsearchFilterClauses(SearchRequest{Start: req.Start, End: req.End, Scope: req.Scope})
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"size": 0,
		"query": map[string]any{
			"bool": map[string]any{"filter": filters},
		},
		"aggs": map[string]any{
			"values": map[string]any{
				"terms": map[string]any{"field": def.ElasticsearchPath, "size": req.Limit},
			},
		},
	}
	var response struct {
		Aggregations struct {
			Values struct {
				Buckets []struct {
					Key any `json:"key"`
				} `json:"buckets"`
			} `json:"values"`
		} `json:"aggregations"`
	}
	path := "/" + c.indexPattern + "/_search"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, body, &response); err != nil {
		return nil, err
	}
	values := make([]string, 0, len(response.Aggregations.Values.Buckets))
	for _, bucket := range response.Aggregations.Values.Buckets {
		values = append(values, fmt.Sprint(bucket.Key))
	}
	return values, nil
}

func (c *ElasticsearchClient) Histogram(ctx context.Context, req SearchRequest, interval time.Duration) ([]HistogramBucket, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	if interval <= 0 || interval > MaxSearchWindow {
		return nil, errors.New("logquery: histogram interval is invalid")
	}
	query, err := buildElasticsearchQueryWithStart(req, "gt")
	if err != nil {
		return nil, err
	}
	elasticsearchStart := req.Start.Truncate(time.Millisecond)
	maxBound := elasticsearchStart.Add(req.End.Sub(req.Start)).Add(-time.Nanosecond).UTC().Format(time.RFC3339Nano)
	minBound := elasticsearchStart.UTC().Format(time.RFC3339Nano)
	dateHistogram := map[string]any{
		"field":          "@timestamp",
		"fixed_interval": elasticsearchDuration(interval),
		"min_doc_count":  0,
		"extended_bounds": map[string]any{
			"min": minBound,
			"max": maxBound,
		},
		"hard_bounds": map[string]any{
			"min": minBound,
			"max": maxBound,
		},
	}
	if offset := elasticsearchHistogramOffset(elasticsearchStart, interval); offset > 0 {
		dateHistogram["offset"] = elasticsearchDuration(offset)
	}
	body := map[string]any{
		"size":  0,
		"query": query,
		"aggs": map[string]any{
			"timeline": map[string]any{
				"date_histogram": dateHistogram,
			},
		},
	}
	var response struct {
		Aggregations struct {
			Timeline struct {
				Buckets []struct {
					KeyAsString string `json:"key_as_string"`
					DocCount    uint64 `json:"doc_count"`
				} `json:"buckets"`
			} `json:"timeline"`
		} `json:"aggregations"`
	}
	path := "/" + c.indexPattern + "/_search"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, body, &response); err != nil {
		return nil, err
	}
	buckets := make([]HistogramBucket, 0, len(response.Aggregations.Timeline.Buckets))
	for _, bucket := range response.Aggregations.Timeline.Buckets {
		ts, err := time.Parse(time.RFC3339Nano, bucket.KeyAsString)
		if err != nil {
			return nil, fmt.Errorf("logquery: decode Elasticsearch histogram timestamp: %w", err)
		}
		buckets = append(buckets, HistogramBucket{Start: req.Start.Add(ts.Sub(elasticsearchStart)).UTC(), Count: bucket.DocCount})
	}
	return buckets, nil
}

// ElasticsearchInfo identifies the cluster behind an Elasticsearch endpoint.
// ClusterUUID is used to prevent split-brain configurations where Edge writes
// and Manager queries target different clusters that happen to run the same
// Elasticsearch version.
type ElasticsearchInfo struct {
	Version     string
	ClusterUUID string
}

// ProbeInfo validates the supported Elasticsearch version and returns the
// stable cluster identity advertised by the root endpoint.
func (c *ElasticsearchClient) ProbeInfo(ctx context.Context) (ElasticsearchInfo, error) {
	var info struct {
		ClusterUUID string `json:"cluster_uuid"`
		Version     struct {
			Number string `json:"number"`
		} `json:"version"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/", nil, nil, &info); err != nil {
		return ElasticsearchInfo{}, err
	}
	if info.Version.Number == "" {
		return ElasticsearchInfo{}, errors.New("logquery: Elasticsearch version missing")
	}
	major, minor, err := parseElasticsearchVersion(info.Version.Number)
	if err != nil {
		return ElasticsearchInfo{}, err
	}
	if major < 8 || (major == 8 && minor < 16) {
		return ElasticsearchInfo{}, fmt.Errorf("logquery: Elasticsearch 8.16+ required, got %s", info.Version.Number)
	}
	return ElasticsearchInfo{Version: info.Version.Number, ClusterUUID: strings.TrimSpace(info.ClusterUUID)}, nil
}

// Probe preserves the existing version-only API for callers that only need a
// compatibility check.
func (c *ElasticsearchClient) Probe(ctx context.Context) (string, error) {
	info, err := c.ProbeInfo(ctx)
	if err != nil {
		return "", err
	}
	return info.Version, nil
}

// RequirePrivileges verifies the API key against the fixed product index
// pattern owned by this client. Elasticsearch permits an API key to inspect
// its own effective privileges, so this check does not require granting the
// Edge write credential any cluster-wide monitoring permission.
func (c *ElasticsearchClient) RequirePrivileges(ctx context.Context, clusterPrivileges, indexPrivileges []string) error {
	if len(clusterPrivileges) == 0 && len(indexPrivileges) == 0 {
		return errors.New("logquery: at least one Elasticsearch privilege is required")
	}
	type indexPrivilegesRequest struct {
		Names      []string `json:"names"`
		Privileges []string `json:"privileges"`
	}
	request := struct {
		Cluster []string                 `json:"cluster,omitempty"`
		Index   []indexPrivilegesRequest `json:"index,omitempty"`
	}{Cluster: clusterPrivileges}
	if len(indexPrivileges) != 0 {
		request.Index = []indexPrivilegesRequest{{
			Names:      []string{c.indexPattern},
			Privileges: indexPrivileges,
		}}
	}
	var response struct {
		HasAllRequested bool                       `json:"has_all_requested"`
		Cluster         map[string]bool            `json:"cluster"`
		Index           map[string]map[string]bool `json:"index"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/_security/user/_has_privileges", nil, request, &response); err != nil {
		return err
	}
	if response.HasAllRequested {
		return nil
	}
	missing := make([]string, 0, len(clusterPrivileges)+len(indexPrivileges))
	for _, privilege := range clusterPrivileges {
		if !response.Cluster[privilege] {
			missing = append(missing, "cluster:"+privilege)
		}
	}
	grantedIndex := response.Index[c.indexPattern]
	for _, privilege := range indexPrivileges {
		if !grantedIndex[privilege] {
			missing = append(missing, "index:"+privilege)
		}
	}
	sort.Strings(missing)
	if len(missing) == 0 {
		return errors.New("logquery: Elasticsearch API key lacks required privileges")
	}
	return fmt.Errorf("logquery: Elasticsearch API key lacks required privileges: %s", strings.Join(missing, ", "))
}

func (c *ElasticsearchClient) openPIT(ctx context.Context) (string, error) {
	query := url.Values{"keep_alive": []string{elasticsearchDuration(defaultESKeepAlive)}}
	path := "/" + c.indexPattern + "/_pit"
	var response struct {
		ID string `json:"id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, path, query, nil, &response); err != nil {
		return "", err
	}
	if response.ID == "" {
		return "", errors.New("logquery: Elasticsearch PIT id missing")
	}
	return response.ID, nil
}

func (c *ElasticsearchClient) closePIT(ctx context.Context, pitID string) error {
	if pitID == "" {
		return nil
	}
	var response struct {
		Succeeded bool `json:"succeeded"`
	}
	if err := c.doJSON(ctx, http.MethodDelete, "/_pit", nil, map[string]string{"id": pitID}, &response); err != nil {
		return err
	}
	if !response.Succeeded {
		return errors.New("logquery: Elasticsearch did not close PIT")
	}
	return nil
}

func buildElasticsearchSearchBody(req SearchRequest, cursor elasticsearchCursor) (map[string]any, error) {
	query, err := buildElasticsearchQuery(req)
	if err != nil {
		return nil, err
	}
	direction := "desc"
	if req.Direction == SortForward {
		direction = "asc"
	}
	body := map[string]any{
		"size":             req.Limit + 1,
		"track_total_hits": false,
		"query":            query,
		"pit": map[string]any{
			"id":         cursor.PITID,
			"keep_alive": elasticsearchDuration(defaultESKeepAlive),
		},
		"sort": []any{
			map[string]any{"@timestamp": direction},
			map[string]any{"_shard_doc": direction},
		},
		"_source": []string{
			"@timestamp", "observed_timestamp", "body", "severity_text", "severity_number",
			"attributes", "resource", "trace_id", "span_id",
		},
	}
	if len(cursor.Sort) > 0 {
		searchAfter := make([]any, 0, len(cursor.Sort))
		for _, raw := range cursor.Sort {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, errInvalidCursor
			}
			searchAfter = append(searchAfter, value)
		}
		body["search_after"] = searchAfter
	}
	return body, nil
}

func buildElasticsearchQuery(req SearchRequest) (map[string]any, error) {
	return buildElasticsearchQueryWithStart(req, "gte")
}

func buildElasticsearchQueryWithStart(req SearchRequest, startOperator string) (map[string]any, error) {
	filters, err := elasticsearchFilterClausesWithStart(req, startOperator)
	if err != nil {
		return nil, err
	}
	must := make([]any, 0, len(req.Keywords.Include))
	mustNot := make([]any, 0, len(req.Keywords.Exclude)+len(req.Filters))
	include := trimmedStrings(req.Keywords.Include)
	switch req.Keywords.Mode {
	case MatchAny:
		if len(include) > 0 {
			should := make([]any, 0, len(include))
			for _, keyword := range include {
				should = append(should, map[string]any{"match_phrase": map[string]any{"body.text": keyword}})
			}
			must = append(must, map[string]any{"bool": map[string]any{
				"should": should, "minimum_should_match": 1,
			}})
		}
	case MatchAll, MatchPhrase:
		for _, keyword := range include {
			must = append(must, map[string]any{"match_phrase": map[string]any{"body.text": keyword}})
		}
	}
	for _, keyword := range trimmedStrings(req.Keywords.Exclude) {
		mustNot = append(mustNot, map[string]any{"match_phrase": map[string]any{"body.text": keyword}})
	}
	for _, filter := range req.Filters {
		def, _ := LookupField(filter.Field)
		var clause any
		if filter.Field == "message" {
			switch filter.Operator {
			case FilterEqual:
				clause = map[string]any{"match_phrase": map[string]any{def.ElasticsearchPath: filter.Values[0]}}
			case FilterIn, FilterNotEqual:
				should := make([]any, 0, len(filter.Values))
				for _, value := range filter.Values {
					should = append(should, map[string]any{"match_phrase": map[string]any{def.ElasticsearchPath: value}})
				}
				clause = map[string]any{"bool": map[string]any{"should": should, "minimum_should_match": 1}}
			case FilterExists:
				clause = map[string]any{"exists": map[string]any{"field": def.ElasticsearchPath}}
			case FilterPrefix:
				clause = map[string]any{"match_phrase_prefix": map[string]any{def.ElasticsearchPath: filter.Values[0]}}
			}
		} else {
			switch filter.Operator {
			case FilterEqual:
				clause = map[string]any{"term": map[string]any{def.ElasticsearchPath: filter.Values[0]}}
				if filter.Values[0] == "" {
					clause = map[string]any{"bool": map[string]any{"minimum_should_match": 1, "should": []any{clause, map[string]any{"bool": map[string]any{"must_not": []any{map[string]any{"exists": map[string]any{"field": def.ElasticsearchPath}}}}}}}}
				}
			case FilterIn:
				clause = map[string]any{"terms": map[string]any{def.ElasticsearchPath: filter.Values}}
			case FilterExists:
				clause = map[string]any{"exists": map[string]any{"field": def.ElasticsearchPath}}
			case FilterNotEqual:
				clause = map[string]any{"terms": map[string]any{def.ElasticsearchPath: filter.Values}}
			case FilterPrefix:
				clause = map[string]any{"prefix": map[string]any{def.ElasticsearchPath: filter.Values[0]}}
			}
		}
		if filter.Operator == FilterNotEqual {
			mustNot = append(mustNot, clause)
		} else {
			filters = append(filters, clause)
		}
	}
	return map[string]any{
		"bool": map[string]any{
			"filter":   filters,
			"must":     must,
			"must_not": mustNot,
		},
	}, nil
}

func elasticsearchFilterClauses(req SearchRequest) ([]any, error) {
	return elasticsearchFilterClausesWithStart(req, "gte")
}

func elasticsearchFilterClausesWithStart(req SearchRequest, startOperator string) ([]any, error) {
	if startOperator != "gte" && startOperator != "gt" {
		return nil, errors.New("logquery: invalid Elasticsearch start boundary")
	}
	filters := []any{
		map[string]any{"range": map[string]any{"@timestamp": map[string]any{
			startOperator: req.Start.UTC().Format(time.RFC3339Nano),
			"lte":         req.End.UTC().Format(time.RFC3339Nano),
			"format":      "strict_date_optional_time_nanos",
		}}},
	}
	addTerms := func(logical string, values []string) {
		if len(values) == 0 {
			return
		}
		def, _ := LookupField(logical)
		filters = append(filters, map[string]any{"terms": map[string]any{def.ElasticsearchPath: values}})
	}
	if len(req.Scope.DeviceIDs) > 0 {
		values := make([]string, 0, len(req.Scope.DeviceIDs))
		for _, id := range req.Scope.DeviceIDs {
			if id == 0 {
				return nil, errors.New("logquery: device_id must be greater than zero")
			}
			values = append(values, strconv.FormatUint(id, 10))
		}
		addTerms("device_id", values)
	}
	addTerms("cluster_id", req.Scope.ClusterIDs)
	addTerms("namespace", req.Scope.Namespaces)
	addTerms("workload", req.Scope.Workloads)
	addTerms("pod", req.Scope.Pods)
	addTerms("container", req.Scope.Containers)
	addTerms("node", req.Scope.Nodes)
	addTerms("service_name", req.Scope.ServiceNames)
	addTerms("source_id", req.Scope.SourceIDs)
	addTerms("level", req.Scope.Levels)
	addTerms("file", req.Scope.Files)
	addTerms("unit", req.Scope.Units)
	return filters, nil
}

func decodeElasticsearchRecord(id string, raw json.RawMessage) (Record, error) {
	var source map[string]any
	if err := json.Unmarshal(raw, &source); err != nil {
		return Record{}, fmt.Errorf("logquery: decode Elasticsearch source: %w", err)
	}
	timestamp, err := parseElasticsearchTime(lookupPath(source, "@timestamp"))
	if err != nil {
		return Record{}, fmt.Errorf("logquery: decode Elasticsearch timestamp: %w", err)
	}
	observed, _ := parseElasticsearchTime(lookupPath(source, "observed_timestamp"))
	attrs := scalarMap(lookupPath(source, "attributes"))
	rawResources := scalarMap(lookupPath(source, "resource.attributes"))
	resources := make(map[string]string)
	for _, logical := range []string{
		"device_id", "cluster_id", "namespace", "workload", "pod", "container",
		"node", "service_name", "service_namespace", "environment", "source_id", "file", "unit",
	} {
		def, _ := LookupField(logical)
		name := strings.TrimPrefix(def.ElasticsearchPath, "resource.attributes.")
		if value := rawResources[name]; value != "" {
			resources[logical] = value
		}
	}
	if clusterName := rawResources["cluster_name"]; clusterName != "" {
		resources["cluster_name"] = clusterName
	}
	message := stringify(lookupPath(source, "body.text"))
	severityText := normalizeLevel(firstNonEmpty(
		stringify(lookupPath(source, "severity_text")),
		attrs["level"],
		attrs["severity"],
		attrs["severity_text"],
		rawResources["level"],
	))
	if severityText == "" {
		severityText = detectLevel(message)
	}
	severityNumber := int32Value(lookupPath(source, "severity_number"))
	if severityNumber == 0 {
		severityNumber = severityNumberForLevel(severityText)
	}
	return Record{
		ID:                 id,
		Timestamp:          timestamp,
		ObservedTimestamp:  observed,
		Message:            message,
		SeverityText:       severityText,
		SeverityNumber:     severityNumber,
		Backend:            elasticsearchBackendName,
		Attributes:         attrs,
		ResourceAttributes: resources,
		TraceID:            stringify(lookupPath(source, "trace_id")),
		SpanID:             stringify(lookupPath(source, "span_id")),
	}, nil
}

func (c *ElasticsearchClient) doJSON(ctx context.Context, method, path string, query url.Values, input, output any) error {
	return c.doJSONWithLimit(ctx, method, path, query, input, output, maxESResponseBytes)
}

func (c *ElasticsearchClient) doJSONWithLimit(ctx context.Context, method, path string, query url.Values, input, output any, maxResponseBytes int64) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("logquery: encode Elasticsearch request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	requestURL := c.endpoint + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return fmt.Errorf("logquery: build Elasticsearch request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "ApiKey "+c.apiKey)
	req.Header.Set("User-Agent", "ongrid-logquery/0.2")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("logquery: Elasticsearch request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			c.log.WarnContext(ctx, "close Elasticsearch response", slog.Any("err", closeErr))
		}
	}()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("logquery: read Elasticsearch response: %w", err)
	}
	if int64(len(responseBody)) > maxResponseBytes {
		return fmt.Errorf("logquery: Elasticsearch response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return elasticsearchHTTPError(resp.StatusCode, responseBody)
	}
	if output == nil || len(responseBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("logquery: decode Elasticsearch response: %w", err)
	}
	return nil
}

func validateElasticsearchEndpoint(raw string, allowHTTP bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("logquery: invalid Elasticsearch endpoint")
	}
	if parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http") {
		return "", errors.New("logquery: Elasticsearch endpoint must use HTTPS")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("logquery: Elasticsearch endpoint must not contain a path")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validateElasticsearchIndexPattern(pattern string) error {
	if !strings.HasPrefix(pattern, "logs-ongrid.") || !strings.Contains(pattern, ".otel-") {
		return errors.New("logquery: Elasticsearch index pattern must stay under logs-ongrid.*.otel-*")
	}
	if strings.ContainsAny(pattern, " ,\\/#?") || strings.Contains(pattern, "..") {
		return errors.New("logquery: invalid Elasticsearch index pattern")
	}
	return nil
}

func elasticsearchHTTPError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && (envelope.Error.Type != "" || envelope.Error.Reason != "") {
		return fmt.Errorf("logquery: Elasticsearch returned %d (%s): %s", status, envelope.Error.Type, truncate(envelope.Error.Reason, 256))
	}
	return fmt.Errorf("logquery: Elasticsearch returned %d", status)
}

func parseElasticsearchVersion(raw string) (int, int, error) {
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("logquery: invalid Elasticsearch version %q", raw)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("logquery: invalid Elasticsearch version %q", raw)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("logquery: invalid Elasticsearch version %q", raw)
	}
	return major, minor, nil
}

func lookupPath(root map[string]any, path string) any {
	parts := strings.Split(path, ".")
	var current any = root
	for i := 0; i < len(parts); i++ {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		if value, exists := object[strings.Join(parts[i:], ".")]; exists {
			return value
		}
		value, exists := object[parts[i]]
		if !exists {
			return nil
		}
		current = value
	}
	return current
}

func scalarMap(value any) map[string]string {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(object))
	for key, item := range object {
		switch item.(type) {
		case string, float64, bool, json.Number:
			out[key] = stringify(item)
		}
	}
	return out
}

func stringify(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

func int32Value(value any) int32 {
	switch typed := value.(type) {
	case float64:
		return int32(typed)
	case json.Number:
		n, _ := typed.Int64()
		return int32(n)
	case string:
		n, _ := strconv.ParseInt(typed, 10, 32)
		return int32(n)
	default:
		return 0
	}
}

func parseElasticsearchTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return time.Time{}, errors.New("missing timestamp")
		}
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		if err != nil {
			return time.Time{}, err
		}
		return parsed.UTC(), nil
	case float64:
		return time.UnixMilli(int64(typed)).UTC(), nil
	case json.Number:
		n, err := typed.Int64()
		if err != nil {
			return time.Time{}, err
		}
		return time.UnixMilli(n).UTC(), nil
	default:
		return time.Time{}, errors.New("missing timestamp")
	}
}

func elasticsearchDuration(d time.Duration) string {
	if d%time.Hour == 0 {
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	if d%time.Minute == 0 {
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	if d%time.Second == 0 {
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
	return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
}

func elasticsearchHistogramOffset(start time.Time, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	remainder := start.UnixNano() % int64(interval)
	if remainder < 0 {
		remainder += int64(interval)
	}
	return time.Duration(remainder)
}

func maxESSearchResponseBytes(limit int) int64 {
	if limit < 1 {
		limit = DefaultSearchLimit
	}
	calculated := maxESSearchEnvelopeBytes + int64(limit+1)*maxESSearchHitBytes
	return min(calculated, int64(maxESSearchResponseHardBytes))
}
