package alert

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ongridio/ongrid/internal/manager/biz/alert/investigator"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	svc "github.com/ongridio/ongrid/internal/manager/service/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type evidenceHTTPRepo struct {
	report *alertmodel.InvestigationReport
	call   investigator.EvidenceCall
	reads  int
}

func (f *evidenceHTTPRepo) GetByIncident(context.Context, uint64) (*alertmodel.InvestigationReport, error) {
	return f.report, nil
}
func (f *evidenceHTTPRepo) ListInvestigationCalls(context.Context, string, time.Time) (investigator.EvidenceRecords, error) {
	return investigator.EvidenceRecords{}, nil
}
func (f *evidenceHTTPRepo) GetInvestigationCall(_ context.Context, root, call string, _ time.Time) (investigator.EvidenceCall, uint64, error) {
	f.reads++
	if root != "root" || call != "call" {
		return investigator.EvidenceCall{}, 0, errs.ErrNotFound
	}
	return f.call, 0, nil
}

func TestEvidenceHTTPAuthReportScopeAndWireCompatibility(t *testing.T) {
	now := time.Now().UTC()
	root := "root"
	result := `{"records":[]}`
	source := alertmodel.EvidenceSource{ID: "E1", ToolCallID: "call", SessionID: root, Tool: "query_logql", State: "empty", StartedAt: now, EndedAt: &now}
	b, _ := json.Marshal(alertmodel.EvidenceCheck{Version: 1, Status: "traceable", Sources: []alertmodel.EvidenceSource{source}})
	check := string(b)
	repo := &evidenceHTTPRepo{report: &alertmodel.InvestigationReport{ID: "report", IncidentID: 42, AuditSessionID: &root, ReadyAt: &now, EvidenceCheckJSON: &check},
		call: investigator.EvidenceCall{SessionID: root, ToolCall: aiopsmodel.ToolCall{ID: "call", ToolName: "query_logql", Status: "success", ResultJSON: &result, ArgumentsJSON: `{}`, EndedAt: &now}}}
	incidents := &fakeService{getIncidentResp: &svc.Incident{ID: 42}}
	h := NewHandler(incidents, nil, nil).WithInvestigations(repo).WithEvidenceReader(repo)
	r := chi.NewRouter()
	h.Register(r)
	for _, tt := range []struct {
		name, path, role string
		status           int
	}{
		{"unauthenticated", "report/evidence/E1", "", 401},
		{"system transcript restricted", "report/evidence/E1", "viewer", 403},
		{"admin", "report/evidence/E1", "admin", 200},
		{"stale report", "old-report/evidence/E1", "admin", 404},
		{"invented reference", "report/evidence/E9", "admin", 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/alerts/incidents/42/investigation/"+tt.path, nil)
			if tt.role != "" {
				req = req.WithContext(tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 7, Role: tt.role}))
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tt.status == 200 && (rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "records")) {
				t.Fatalf("source response=%s", rec.Body.String())
			}
		})
	}
	incidents.getIncidentErr = errs.ErrNotFound
	req := httptest.NewRequest("GET", "/v1/alerts/incidents/42/investigation/report/evidence/E1", nil)
	req = req.WithContext(tenantctx.With(req.Context(), tenantctx.Tenant{UserID: 7, Role: "admin"}))
	before := repo.reads
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 404 || repo.reads != before {
		t.Fatalf("incident denial did not stop evidence read: %d", rec.Code)
	}
	old, _ := json.Marshal(investigationReportToWire(&alertmodel.InvestigationReport{ID: "old"}))
	if strings.Contains(string(old), "evidence_check") {
		t.Fatalf("legacy report marked checked: %s", old)
	}
}
