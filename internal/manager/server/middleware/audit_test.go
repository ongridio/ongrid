package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/audit"
	model "github.com/ongridio/ongrid/internal/manager/model/audit"
	"github.com/ongridio/ongrid/internal/pkg/auditctx"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type auditRecorder struct{ rows []model.Log }

func (r *auditRecorder) Insert(_ context.Context, row *model.Log) error {
	r.rows = append(r.rows, *row)
	return nil
}
func (*auditRecorder) List(context.Context, audit.ListFilters) ([]model.Log, int64, error) {
	return nil, 0, nil
}
func (*auditRecorder) DeleteOlderThan(context.Context, time.Time) (int64, error) { return 0, nil }

// 跨域 Handler 只引用共享事件契约；原中间件仍必须写入相同的审计行。
func TestSharedAuditEventSurvivesWrappedRequest(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		repo := &auditRecorder{}
		handler := AuditMiddleware(audit.New(repo, nil))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantctx.SetOnSlot(r.Context(), tenantctx.Tenant{UserID: 17, Email: "audit@example.test", Role: "admin"})
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			if explicit {
				auditctx.SetAuditEvent(r.WithContext(ctx), auditctx.Event{Action: auditctx.ActionUserUpdate, ResourceType: auditctx.ResourceUser, ResourceID: "42"})
			}
			w.WriteHeader(http.StatusForbidden)
		}))
		req := httptest.NewRequest(http.MethodPost, "/users/42", nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)
		if !explicit {
			if len(repo.rows) != 0 {
				t.Fatal("unannotated request audited")
			}
			continue
		}
		if len(repo.rows) != 1 {
			t.Fatalf("rows=%d", len(repo.rows))
		}
		got := repo.rows[0]
		if got.Action != model.ActionUserUpdate || got.Status != model.StatusDenied || got.UserID == nil || *got.UserID != 17 || got.ResourceID != "42" {
			t.Fatalf("audit changed: %+v", got)
		}
	}
}
