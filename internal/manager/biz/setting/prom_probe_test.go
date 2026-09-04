package setting

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang/snappy"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestPromConfigurationProbe_UsesUnsavedDraftForQueryAndWrite(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer draft-token" {
			t.Errorf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/select/0/prometheus/api/v1/query":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		case "/insert/0/prometheus/api/v1/write":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read write probe: %v", err)
			}
			decoded, err := snappy.Decode(nil, body)
			if err != nil || len(decoded) != 0 {
				t.Errorf("write probe body = %v, err = %v", decoded, err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	probe := NewPromConfigurationProbe(nil)
	err := probe.Probe(context.Background(), PromProbeInput{
		QueryURL:       srv.URL + "/select/0/prometheus",
		RemoteWriteURL: srv.URL + "/insert/0/prometheus/api/v1/write",
		BearerToken:    "draft-token",
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got := strings.Join(paths, ","); got != "/select/0/prometheus/api/v1/query,/insert/0/prometheus/api/v1/write" {
		t.Fatalf("paths = %q", got)
	}
}

func TestPromConfigurationProbe_RejectsInvalidDraftURL(t *testing.T) {
	err := NewPromConfigurationProbe(nil).Probe(context.Background(), PromProbeInput{QueryURL: "file:///tmp/prom"})
	if err == nil || !strings.Contains(err.Error(), "query URL") || !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
}
