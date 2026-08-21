package logs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

type idleStructuredSearcher struct{}

func (idleStructuredSearcher) Search(context.Context, logquery.SearchRequest) (*logquery.SearchResult, error) {
	return &logquery.SearchResult{}, nil
}

func (idleStructuredSearcher) Count(context.Context, logquery.SearchRequest) (uint64, error) {
	return 0, nil
}

func (idleStructuredSearcher) Fields(context.Context, time.Time, time.Time, logquery.Scope) ([]logquery.Field, error) {
	return nil, nil
}

func (idleStructuredSearcher) FieldValues(context.Context, logquery.FieldValuesRequest) ([]string, error) {
	return nil, nil
}

func (idleStructuredSearcher) Histogram(context.Context, logquery.SearchRequest, time.Duration) ([]logquery.HistogramBucket, error) {
	return nil, nil
}

func TestExpensiveStructuredLogEndpointsShareConcurrencyLimit(t *testing.T) {
	handler := NewHandlerWithSearcher(nil, idleStructuredSearcher{})
	handler.searchWait = 5 * time.Millisecond
	for range maxConcurrentStructuredSearches {
		if !handler.acquireSearchSlot(context.Background()) {
			t.Fatal("failed to reserve search slot")
		}
	}
	t.Cleanup(func() {
		for range maxConcurrentStructuredSearches {
			handler.releaseSearchSlot()
		}
	})

	end := time.Now().UTC()
	search := `{"start":"` + end.Add(-time.Hour).Format(time.RFC3339Nano) + `","end":"` + end.Format(time.RFC3339Nano) + `"}`
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "search", method: http.MethodPost, path: "/v1/logs/search", body: search},
		{name: "field values", method: http.MethodPost, path: "/v1/logs/field-values", body: `{}`},
		{name: "histogram", method: http.MethodPost, path: "/v1/logs/histogram", body: `{"search":` + search + `,"interval":"1m"}`},
		{name: "context", method: http.MethodPost, path: "/v1/logs/context", body: `{"timestamp":"` + end.Format(time.RFC3339Nano) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			backendTestRouter(handler).ServeHTTP(rec, req)

			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Retry-After") != "1" || !strings.Contains(rec.Body.String(), "LOG_QUERY_BUSY") {
				t.Fatalf("headers=%v body=%s", rec.Header(), rec.Body.String())
			}
		})
	}
}

func TestFieldsDoesNotConsumeStructuredSearchSlot(t *testing.T) {
	handler := NewHandlerWithSearcher(nil, idleStructuredSearcher{})
	for range maxConcurrentStructuredSearches {
		if !handler.acquireSearchSlot(context.Background()) {
			t.Fatal("failed to reserve search slot")
		}
	}
	t.Cleanup(func() {
		for range maxConcurrentStructuredSearches {
			handler.releaseSearchSlot()
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/logs/fields", nil)
	rec := httptest.NewRecorder()
	backendTestRouter(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSearchWaitsForAvailableSlot(t *testing.T) {
	handler := NewHandlerWithSearcher(nil, idleStructuredSearcher{})
	handler.searchSlots = make(chan struct{}, 1)
	handler.searchWait = 100 * time.Millisecond
	if !handler.acquireSearchSlot(context.Background()) {
		t.Fatal("failed to reserve search slot")
	}
	timer := time.AfterFunc(10*time.Millisecond, handler.releaseSearchSlot)
	t.Cleanup(func() { timer.Stop() })

	end := time.Now().UTC()
	body := `{"start":"` + end.Add(-time.Hour).Format(time.RFC3339Nano) + `","end":"` + end.Format(time.RFC3339Nano) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/logs/search", strings.NewReader(body))
	rec := httptest.NewRecorder()
	backendTestRouter(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}
