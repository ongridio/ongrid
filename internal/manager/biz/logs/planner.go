package logs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/logs"
	apperrs "github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

const (
	plannerBackendName  = "manager"
	maxHistogramBuckets = 500
)

type plannerCursor struct {
	Backend    string `json:"backend"`
	PlanSum    string `json:"plan_sum"`
	Phase      string `json:"phase"`
	Cursor     string `json:"cursor,omitempty"`
	RequestSum string `json:"request_sum"`
}

type queryPhase struct {
	name       string
	start, end time.Time
	backend    *model.Backend // nil means built-in Loki
}

func (s *Service) Search(ctx context.Context, req logquery.SearchRequest) (*logquery.SearchResult, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	phases, planSum, err := s.plan(ctx, req.Start, req.End, req.Direction)
	if err != nil {
		return nil, err
	}
	requestSum, err := plannerRequestSum(req)
	if err != nil {
		return nil, err
	}
	phaseIndex := 0
	backendCursor := ""
	if req.Cursor != "" {
		var cursor plannerCursor
		if err := decodePlannerCursor(req.Cursor, &cursor); err != nil {
			return nil, err
		}
		if cursor.Backend != plannerBackendName || cursor.PlanSum != planSum || cursor.RequestSum != requestSum {
			return nil, errors.New("logquery: invalid cursor")
		}
		phaseIndex = phasePosition(phases, cursor.Phase)
		if phaseIndex < 0 {
			return nil, errors.New("logquery: invalid cursor")
		}
		backendCursor = cursor.Cursor
	}

	started := time.Now()
	result := &logquery.SearchResult{Records: []logquery.Record{}, Backends: []string{}}
	remaining := req.Limit
	for phaseIndex < len(phases) && remaining > 0 {
		phase := phases[phaseIndex]
		phaseReq := req
		// Query phases use the same (start, end] ownership convention as
		// Count. Backend search APIs accept an inclusive lower bound, so move
		// it by one nanosecond to prevent duplicates at a backend switch.
		phaseReq.Start, phaseReq.End = phase.start.Add(time.Nanosecond), phase.end
		phaseReq.Limit, phaseReq.Cursor = remaining, backendCursor
		searcher, err := s.searcherForPhase(ctx, phase)
		if err != nil {
			return nil, err
		}
		page, err := searcher.Search(ctx, phaseReq)
		if err != nil {
			return nil, err
		}
		result.Records = append(result.Records, page.Records...)
		result.Backends = appendUnique(result.Backends, page.Backends...)
		remaining = req.Limit - len(result.Records)
		if page.HasMore {
			result.HasMore = true
			result.NextCursor, err = encodePlannerCursor(plannerCursor{
				Backend: plannerBackendName, PlanSum: planSum, Phase: phase.name,
				Cursor: page.NextCursor, RequestSum: requestSum,
			})
			if err != nil {
				return nil, err
			}
			break
		}
		phaseIndex++
		backendCursor = ""
		if phaseIndex < len(phases) && remaining == 0 {
			result.HasMore = true
			result.NextCursor, err = encodePlannerCursor(plannerCursor{
				Backend: plannerBackendName, PlanSum: planSum,
				Phase: phases[phaseIndex].name, RequestSum: requestSum,
			})
			if err != nil {
				return nil, err
			}
		}
	}
	result.TookMS = time.Since(started).Milliseconds()
	return result, nil
}

// Count uses the same single selected backend as Search. Data retained in an
// inactive backend is intentionally outside the product query surface.
func (s *Service) Count(ctx context.Context, req logquery.SearchRequest) (uint64, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return 0, err
	}
	phases, _, err := s.plan(ctx, req.Start, req.End, logquery.SortForward)
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, phase := range phases {
		phaseReq := req
		phaseReq.Start, phaseReq.End, phaseReq.Cursor = phase.start, phase.end, ""
		searcher, err := s.searcherForPhase(ctx, phase)
		if err != nil {
			return 0, err
		}
		count, err := searcher.Count(ctx, phaseReq)
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func (s *Service) Fields(_ context.Context, _, _ time.Time, _ logquery.Scope) ([]logquery.Field, error) {
	return logquery.AllowedFields(), nil
}

func (s *Service) FieldValues(ctx context.Context, req logquery.FieldValuesRequest) ([]string, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	phases, _, err := s.plan(ctx, req.Start, req.End, logquery.SortForward)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{})
	for _, phase := range phases {
		phaseReq := req
		phaseReq.Start, phaseReq.End = phase.start, phase.end
		searcher, err := s.searcherForPhase(ctx, phase)
		if err != nil {
			return nil, err
		}
		values, err := searcher.FieldValues(ctx, phaseReq)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			set[value] = struct{}{}
		}
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	if len(values) > req.Limit {
		values = values[:req.Limit]
	}
	return values, nil
}

func (s *Service) Histogram(ctx context.Context, req logquery.SearchRequest, interval time.Duration) ([]logquery.HistogramBucket, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	if interval <= 0 || interval > logquery.MaxSearchWindow {
		return nil, errors.New("logquery: histogram interval is invalid")
	}
	span := req.End.Sub(req.Start)
	bucketCount := int((span-1)/interval) + 1
	if bucketCount > maxHistogramBuckets {
		return nil, fmt.Errorf("logquery: histogram exceeds %d buckets; increase interval", maxHistogramBuckets)
	}
	phases, _, err := s.plan(ctx, req.Start, req.End, logquery.SortForward)
	if err != nil {
		return nil, err
	}
	if len(phases) != 1 {
		return nil, errors.New("logquery: histogram requires exactly one selected backend")
	}
	searcher, err := s.searcherForPhase(ctx, phases[0])
	if err != nil {
		return nil, err
	}
	phaseReq := req
	phaseReq.Cursor = ""
	buckets, err := searcher.Histogram(ctx, phaseReq, interval)
	if err != nil {
		return nil, err
	}

	// Backend adapters return buckets aligned to the request start. Normalize
	// sparse backend results onto the product grid so both Loki and
	// Elasticsearch expose identical zero-filled bucket positions.
	out := make([]logquery.HistogramBucket, bucketCount)
	for i := range out {
		out[i].Start = req.Start.Add(time.Duration(i) * interval).UTC()
	}
	for _, bucket := range buckets {
		delta := bucket.Start.Sub(req.Start)
		if delta < 0 || delta%interval != 0 {
			return nil, fmt.Errorf("logquery: backend histogram bucket %s is not aligned to request start %s", bucket.Start, req.Start)
		}
		index := int(delta / interval)
		// Search/count phases own (start, end]. Elasticsearch date_histogram
		// buckets are left-closed, so a record exactly at an interval-aligned
		// request end is returned in a bucket whose key equals end. Fold that
		// boundary bucket into the final product bucket to preserve the inclusive
		// end without growing the requested grid by one bucket.
		if index == len(out) && bucket.Start.Equal(req.End) {
			out[len(out)-1].Count += bucket.Count
			continue
		}
		if index >= len(out) {
			return nil, fmt.Errorf("logquery: backend histogram bucket %s is outside the request window", bucket.Start)
		}
		out[index].Count += bucket.Count
	}
	return out, nil
}

func (s *Service) plan(ctx context.Context, start, end time.Time, _ logquery.SortDirection) ([]queryPhase, string, error) {
	backend, err := s.repo.SelectedBackend(ctx)
	if err != nil && !errors.Is(err, apperrs.ErrNotFound) {
		return nil, "", err
	}
	if errors.Is(err, apperrs.ErrNotFound) {
		backend = nil
	}
	phases := []queryPhase{buildSelectedQueryPhase(start, end, backend)}
	planSum, err := queryPlanSum(phases)
	if err != nil {
		return nil, "", err
	}
	return phases, planSum, nil
}

func buildSelectedQueryPhase(start, end time.Time, backend *model.Backend) queryPhase {
	if backend == nil {
		return queryPhase{name: "loki", start: start, end: end}
	}
	return queryPhase{
		name: fmt.Sprintf("elasticsearch:%d", backend.ID), start: start, end: end, backend: backend,
	}
}

func (s *Service) searcherForPhase(ctx context.Context, phase queryPhase) (logquery.Searcher, error) {
	if phase.backend == nil {
		if s.loki == nil {
			return nil, errors.New("current Loki backend is unavailable")
		}
		return s.loki, nil
	}
	return s.elasticsearchClient(ctx, phase.backend)
}

func (s *Service) elasticsearchClient(ctx context.Context, backend *model.Backend) (*logquery.ElasticsearchClient, error) {
	cacheKey := fmt.Sprintf("%d/%d/%s/%s/%s", backend.ID, backend.Generation, backend.QueryEndpoint, backend.QueryCredentialRef, backend.IndexPattern)
	s.mu.RLock()
	if s.cacheKey == cacheKey && s.cachedES != nil {
		client := s.cachedES
		s.mu.RUnlock()
		return client, nil
	}
	s.mu.RUnlock()
	apiKey, err := s.apiKey(ctx, backend.QueryCredentialRef)
	if err != nil {
		return nil, err
	}
	client, err := s.newESClient(backend.QueryEndpoint, backend.IndexPattern, apiKey, backend)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cacheKey, s.cachedES = cacheKey, client
	s.mu.Unlock()
	return client, nil
}

func queryPlanSum(phases []queryPhase) (string, error) {
	type fingerprint struct {
		Name       string    `json:"name"`
		Start      time.Time `json:"start"`
		End        time.Time `json:"end"`
		Generation uint64    `json:"generation,omitempty"`
	}
	items := make([]fingerprint, 0, len(phases))
	for _, phase := range phases {
		item := fingerprint{Name: phase.name, Start: phase.start, End: phase.end}
		if phase.backend != nil {
			item.Generation = phase.backend.Generation
		}
		items = append(items, item)
	}
	body, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("encode log query plan fingerprint: %w", err)
	}
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func phasePosition(phases []queryPhase, name string) int {
	for i := range phases {
		if phases[i].name == name {
			return i
		}
	}
	return -1
}

func plannerRequestSum(req logquery.SearchRequest) (string, error) {
	req.Cursor = ""
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("encode log query fingerprint: %w", err)
	}
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func encodePlannerCursor(cursor plannerCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode log query cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func decodePlannerCursor(raw string, cursor *plannerCursor) error {
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(body) > 8192 || json.Unmarshal(body, cursor) != nil {
		return errors.New("logquery: invalid cursor")
	}
	return nil
}

func appendUnique(base []string, values ...string) []string {
	seen := make(map[string]struct{}, len(base)+len(values))
	for _, value := range base {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		base = append(base, value)
	}
	return base
}
