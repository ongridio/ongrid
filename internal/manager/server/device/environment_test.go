package device

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/device"
	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type environmentRepo struct {
	biz.Repo
	value string
}

func (r *environmentRepo) Get(context.Context, uint64) (*model.Device, error) {
	return &model.Device{ID: 1, Environment: &r.value}, nil
}
func (r *environmentRepo) UpdateEnvironment(_ context.Context, _ uint64, value string) error {
	r.value = value
	return nil
}

func TestDeviceEnvironmentHTTP(t *testing.T) {
	repo := &environmentRepo{value: "production"}
	router := chi.NewRouter()
	NewHandler(biz.NewUsecase(repo, nil, nil)).Register(router)
	for _, tc := range []struct {
		method, role, body string
		status             int
	}{
		{"GET", "", "", 401}, {"PUT", "", "{\"environment\":\"test\"}", 401},
		{"PUT", "user", "{\"environment\":\"test\"}", 403}, {"GET", "user", "", 200},
		{"PUT", "admin", "{}", 400}, {"PUT", "admin", "{\"environment\":null}", 400},
		{"PUT", "admin", "{\"environment\":\"${ENV}\"}", 400},
		{"PUT", "admin", "{\"environment\":\"staging\"}", 200},
		{"PUT", "admin", "{\"environment\":\"\"}", 200},
	} {
		req := httptest.NewRequest(tc.method, "/v1/devices/1/environment", strings.NewReader(tc.body))
		if tc.role != "" {
			req = req.WithContext(tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 1, Role: tc.role}))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s %s %s: status %d, %s", tc.method, tc.role, tc.body, w.Code, w.Body.String())
		}
		if w.Code == http.StatusOK {
			var response struct {
				Code int
				Data biz.Environment
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Code != 0 || response.Data.Environment != repo.value {
				t.Fatalf("response: %+v", response)
			}
		}
	}
	if repo.value != "" {
		t.Fatal("clearing the environment did not persist")
	}
}
