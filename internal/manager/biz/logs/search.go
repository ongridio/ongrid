package logs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	model "github.com/ongridio/ongrid/internal/manager/model/logs"
	apperrs "github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

const maxHistogramBuckets = 500

const maxSearchCursorEnvelopeBytes = 8 * 1024

type searchCursorEnvelope struct {
	Backend           string `json:"backend"`
	BackendID         uint64 `json:"backend_id"`
	BackendGeneration uint64 `json:"backend_generation"`
	Cursor            string `json:"cursor"`
}

func (s *Service) Search(ctx context.Context, req logquery.SearchRequest) (result *logquery.SearchResult, retErr error) {
	ctx, span := otel.Tracer("github.com/ongridio/ongrid/internal/manager/biz/logs").Start(ctx, "logs.Search")
	defer func() { tracing.EndSpan(span, retErr) }()
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	backend, err := s.selectedBackend(ctx)
	if err != nil {
		return nil, err
	}
	searcher := s.loki
	backendName := "loki"
	if backend != nil {
		backendName = "elasticsearch"
		searcher, err = s.elasticsearchClient(ctx, backend)
		if err != nil {
			return nil, err
		}
		if req.Cursor != "" {
			envelope, decodeErr := decodeSearchCursorEnvelope(req.Cursor)
			if decodeErr != nil {
				return nil, decodeErr
			}
			if envelope.Backend != string(model.BackendTypeElasticsearch) || envelope.BackendID != backend.ID ||
				envelope.BackendGeneration != backend.Generation || envelope.Cursor == "" {
				return nil, fmt.Errorf("%w: log cursor does not belong to the selected backend", apperrs.ErrInvalid)
			}
			req.Cursor = envelope.Cursor
		}
	} else if searcher == nil {
		return nil, errors.New("current Loki backend is unavailable")
	}
	span.SetAttributes(attribute.String("logs.backend", backendName))
	// Product search windows own (start, end]. Backend search APIs use an
	// inclusive lower bound, so exclude the start boundary explicitly.
	req.Start = req.Start.Add(time.Nanosecond)
	result, err = searcher.Search(ctx, req)
	if err != nil || backend == nil || result.NextCursor == "" {
		return result, err
	}
	innerCursor := result.NextCursor
	result.NextCursor, err = encodeSearchCursorEnvelope(searchCursorEnvelope{
		Backend: string(model.BackendTypeElasticsearch), BackendID: backend.ID,
		BackendGeneration: backend.Generation, Cursor: innerCursor,
	})
	if err != nil {
		if closeErr := logquery.CloseCursor(context.WithoutCancel(ctx), searcher, innerCursor); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close Elasticsearch cursor after envelope failure: %w", closeErr))
		}
		return nil, err
	}
	return result, nil
}

// CloseCursor releases backend resources when a caller abandons pagination.
// It is safe for the built-in Loki path, whose cursors are stateless.
func (s *Service) CloseCursor(ctx context.Context, cursor string) error {
	if cursor == "" {
		return nil
	}
	envelope, err := decodeSearchCursorEnvelope(cursor)
	if err != nil {
		return err
	}
	if envelope.Backend == "loki" {
		return nil
	}
	if envelope.Backend != string(model.BackendTypeElasticsearch) || envelope.BackendID == 0 ||
		envelope.BackendGeneration == 0 || envelope.Cursor == "" {
		return fmt.Errorf("%w: invalid log cursor envelope", apperrs.ErrInvalid)
	}
	backend, err := s.repo.GetBackend(ctx, envelope.BackendID)
	if err != nil {
		return fmt.Errorf("load log cursor backend: %w", err)
	}
	if backend.Type != model.BackendTypeElasticsearch || backend.Generation != envelope.BackendGeneration {
		return fmt.Errorf("%w: log cursor backend generation changed", apperrs.ErrConflict)
	}
	searcher, err := s.elasticsearchClient(ctx, backend)
	if err != nil {
		return err
	}
	return logquery.CloseCursor(ctx, searcher, envelope.Cursor)
}

func encodeSearchCursorEnvelope(envelope searchCursorEnvelope) (string, error) {
	body, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode log cursor envelope: %w", err)
	}
	if len(body) > maxSearchCursorEnvelopeBytes {
		return "", fmt.Errorf("%w: log cursor envelope is too large", apperrs.ErrInvalid)
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func decodeSearchCursorEnvelope(raw string) (searchCursorEnvelope, error) {
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(body) == 0 || len(body) > maxSearchCursorEnvelopeBytes {
		return searchCursorEnvelope{}, fmt.Errorf("%w: invalid log cursor envelope", apperrs.ErrInvalid)
	}
	var envelope searchCursorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return searchCursorEnvelope{}, fmt.Errorf("%w: invalid log cursor envelope", apperrs.ErrInvalid)
	}
	return envelope, nil
}

// Count uses only the selected backend. Retained data in an inactive backend
// is intentionally outside the product query surface.
func (s *Service) Count(ctx context.Context, req logquery.SearchRequest) (uint64, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return 0, err
	}
	searcher, err := s.selectedSearcher(ctx)
	if err != nil {
		return 0, err
	}
	req.Cursor = ""
	return searcher.Count(ctx, req)
}

// CountGrouped routes backend-neutral alert aggregation to the currently
// selected backend. Product search windows own (start, end], matching Count.
func (s *Service) CountGrouped(ctx context.Context, req logquery.SearchRequest, groupBy []string) ([]logquery.CountGroup, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	searcher, err := s.selectedSearcher(ctx)
	if err != nil {
		return nil, err
	}
	req.Cursor = ""
	return logquery.CountGrouped(ctx, searcher, req, groupBy)
}

func (s *Service) Fields(_ context.Context, _, _ time.Time, _ logquery.Scope) ([]logquery.Field, error) {
	return logquery.AllowedFields(), nil
}

func (s *Service) FieldValues(ctx context.Context, req logquery.FieldValuesRequest) ([]string, error) {
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	searcher, err := s.selectedSearcher(ctx)
	if err != nil {
		return nil, err
	}
	return searcher.FieldValues(ctx, req)
}

func (s *Service) Histogram(ctx context.Context, req logquery.SearchRequest, interval time.Duration) (result []logquery.HistogramBucket, retErr error) {
	ctx, span := otel.Tracer("github.com/ongridio/ongrid/internal/manager/biz/logs").Start(ctx, "logs.Histogram")
	defer func() { tracing.EndSpan(span, retErr) }()
	if err := req.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	if interval <= 0 || interval > logquery.MaxSearchWindow {
		return nil, errors.New("logquery: histogram interval is invalid")
	}
	window := req.End.Sub(req.Start)
	bucketCount := int((window-1)/interval) + 1
	if bucketCount > maxHistogramBuckets {
		return nil, fmt.Errorf("logquery: histogram exceeds %d buckets; increase interval", maxHistogramBuckets)
	}
	searcher, err := s.selectedSearcher(ctx)
	if err != nil {
		return nil, err
	}
	req.Cursor = ""
	buckets, err := searcher.Histogram(ctx, req, interval)
	if err != nil {
		return nil, err
	}

	// Backend adapters return buckets aligned to the request start. Normalize
	// sparse backend results onto the product grid so both backends expose the
	// same zero-filled bucket positions.
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
		// A record exactly at an interval-aligned end belongs to the final
		// product bucket even when Elasticsearch returns a bucket keyed by end.
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

func (s *Service) selectedBackend(ctx context.Context) (*model.Backend, error) {
	backend, err := s.repo.SelectedBackend(ctx)
	if errors.Is(err, apperrs.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return backend, nil
}

func (s *Service) selectedSearcher(ctx context.Context) (logquery.Searcher, error) {
	backend, err := s.selectedBackend(ctx)
	if err != nil {
		return nil, err
	}
	if backend == nil {
		if s.loki == nil {
			return nil, errors.New("current Loki backend is unavailable")
		}
		return s.loki, nil
	}
	return s.elasticsearchClient(ctx, backend)
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
	apiKey, err := s.credential(ctx, backend.QueryCredentialRef, backendAuthType(backend))
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
