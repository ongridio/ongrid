package edge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	biz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type hostPluginFake struct {
	host  uint64
	edge  uint64
	saved bool
}

func (f *hostPluginFake) ListForUI(context.Context, uint64) ([]biz.PluginRow, error) {
	panic("host route used Edge config API")
}
func (f *hostPluginFake) Set(context.Context, uint64, string, biz.SetInput) (*biz.PluginRow, error) {
	panic("host route used Edge write API")
}
func (f *hostPluginFake) AutoAPMOptions(context.Context) (*biz.AutoAPMOptions, error) {
	return &biz.AutoAPMOptions{}, nil
}
func (f *hostPluginFake) CountByPlugin(context.Context) (map[string]int64, error) { return nil, nil }
func (f *hostPluginFake) ListForHost(_ context.Context, id uint64) ([]biz.PluginRow, uint64, error) {
	f.host = id
	return []biz.PluginRow{{PluginName: "autoapm", Enabled: true}}, f.edge, nil
}
func (f *hostPluginFake) SetForHost(_ context.Context, id uint64, name string, in biz.SetInput) (*biz.PluginRow, error) {
	f.host = id
	f.saved = true
	return &biz.PluginRow{PluginName: name, Enabled: in.Enabled}, nil
}
func TestHostPluginRoutesUseHostIdentityAndRequireAdmin(t *testing.T) {
	for _, role := range []string{"admin", "user"} {
		t.Run(role, func(t *testing.T) {
			cfg := &hostPluginFake{}
			router := buildRouter(NewHandler(&fakeSvc{}, newFakeDeviceRepo(), cfg), tenantctx.Tenant{UserID: 1, Role: role})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/devices/650/plugins", nil))
			if w.Code != 200 || cfg.host != 650 {
				t.Fatalf("host read: %d %s", w.Code, w.Body.String())
			}
			w = httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v1/devices/650/plugins/autoapm", strings.NewReader(`{"enabled":true,"spec":{"targets":[]}}`)))
			if role == "admin" {
				if w.Code != 200 || !cfg.saved || cfg.host != 650 {
					t.Fatalf("host write: %d %s", w.Code, w.Body.String())
				}
			} else if w.Code != 403 || cfg.saved {
				t.Fatal("unprivileged host config write")
			}
		})
	}
}
