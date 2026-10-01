package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	svc "github.com/ongridio/ongrid/internal/manager/service/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type fakeService struct {
	listIncidentsResp []*svc.Incident
	listIncidentsErr  error

	getIncidentResp *svc.Incident
	getIncidentErr  error

	ackIncidentResp *svc.Incident
	ackIncidentErr  error

	resolveIncidentResp *svc.Incident
	resolveIncidentErr  error

	silenceIncidentResp *svc.Incident
	silenceIncidentErr  error

	listEventsResp []*svc.Event
	listEventsErr  error

	lastSilenceInput svc.IncidentSilenceInput
	lastEventsLimit  int

	listChannelsResp []*svc.Channel
	listChannelsErr  error

	getChannelResp *svc.Channel
	getChannelErr  error

	createChannelResp *svc.Channel
	createChannelErr  error

	updateChannelResp *svc.Channel
	updateChannelErr  error

	deleteChannelErr error

	testChannelResp *svc.ChannelTestResult
	testChannelErr  error

	listRulesResp        []*svc.Rule
	listRulesErr         error
	getRuleResp          *svc.Rule
	getRuleErr           error
	createRuleResp       *svc.Rule
	createRuleErr        error
	updateRuleResp       *svc.Rule
	updateRuleErr        error
	enabledRuleResp      *svc.Rule
	enabledRuleErr       error
	deleteRuleErr        error
	lastRuleScope        string
	lastRuleID           uint64
	lastRuleInput        svc.RuleInput
	lastRuleEnabledV     bool
	previewRuleResp      *svc.PreviewResult
	previewRuleErr       error
	lastPreviewLookback  int

	lastCaller       svc.Caller
	lastFilter       svc.IncidentFilter
	lastIncidentID   uint64
	lastIncidentNote string
	lastChannelID    uint64
	lastChannelInput svc.ChannelInput
	lastPage         int
	lastPageSize     int
}

func (f *fakeService) ListRules(_ context.Context, caller svc.Caller, scope string) ([]*svc.Rule, error) {
	f.lastCaller = caller
	f.lastRuleScope = scope
	return f.listRulesResp, f.listRulesErr
}

func (f *fakeService) GetRule(_ context.Context, caller svc.Caller, id uint64) (*svc.Rule, error) {
	f.lastCaller = caller
	f.lastRuleID = id
	return f.getRuleResp, f.getRuleErr
}

func (f *fakeService) CreateRule(_ context.Context, caller svc.Caller, in svc.RuleInput) (*svc.Rule, error) {
	f.lastCaller = caller
	f.lastRuleInput = in
	return f.createRuleResp, f.createRuleErr
}

func (f *fakeService) UpdateRule(_ context.Context, caller svc.Caller, id uint64, in svc.RuleInput) (*svc.Rule, error) {
	f.lastCaller = caller
	f.lastRuleID = id
	f.lastRuleInput = in
	return f.updateRuleResp, f.updateRuleErr
}

func (f *fakeService) SetRuleEnabled(_ context.Context, caller svc.Caller, id uint64, enabled bool) (*svc.Rule, error) {
	f.lastCaller = caller
	f.lastRuleID = id
	f.lastRuleEnabledV = enabled
	return f.enabledRuleResp, f.enabledRuleErr
}

func (f *fakeService) DeleteRule(_ context.Context, caller svc.Caller, id uint64) error {
	f.lastCaller = caller
	f.lastRuleID = id
	return f.deleteRuleErr
}

func (f *fakeService) PreviewRule(_ context.Context, caller svc.Caller, in svc.RuleInput, lookbackSeconds int) (*svc.PreviewResult, error) {
	f.lastCaller = caller
	f.lastRuleInput = in
	f.lastPreviewLookback = lookbackSeconds
	return f.previewRuleResp, f.previewRuleErr
}

func (f *fakeService) ListIncidents(_ context.Context, caller svc.Caller, in svc.IncidentFilter) ([]*svc.Incident, error) {
	f.lastCaller = caller
	f.lastFilter = in
	return f.listIncidentsResp, f.listIncidentsErr
}

func (f *fakeService) CountIncidents(_ context.Context, _ svc.Caller, _ svc.IncidentFilter) (int64, error) {
	return int64(len(f.listIncidentsResp)), f.listIncidentsErr
}

func (f *fakeService) GetIncident(_ context.Context, caller svc.Caller, id uint64) (*svc.Incident, error) {
	f.lastCaller = caller
	f.lastIncidentID = id
	return f.getIncidentResp, f.getIncidentErr
}

func (f *fakeService) GetIncidentModel(_ context.Context, _ svc.Caller, _ uint64) (*alertmodel.Incident, error) {
	return nil, nil
}

func (f *fakeService) AcknowledgeIncident(_ context.Context, caller svc.Caller, id uint64, in svc.IncidentMutationInput) (*svc.Incident, error) {
	f.lastCaller = caller
	f.lastIncidentID = id
	f.lastIncidentNote = in.Note
	return f.ackIncidentResp, f.ackIncidentErr
}

func (f *fakeService) ResolveIncident(_ context.Context, caller svc.Caller, id uint64, in svc.IncidentMutationInput) (*svc.Incident, error) {
	f.lastCaller = caller
	f.lastIncidentID = id
	f.lastIncidentNote = in.Note
	return f.resolveIncidentResp, f.resolveIncidentErr
}

func (f *fakeService) SilenceIncident(_ context.Context, caller svc.Caller, id uint64, in svc.IncidentSilenceInput) (*svc.Incident, error) {
	f.lastCaller = caller
	f.lastIncidentID = id
	f.lastSilenceInput = in
	return f.silenceIncidentResp, f.silenceIncidentErr
}

func (f *fakeService) ListIncidentEvents(_ context.Context, caller svc.Caller, id uint64, limit int) ([]*svc.Event, error) {
	f.lastCaller = caller
	f.lastIncidentID = id
	f.lastEventsLimit = limit
	return f.listEventsResp, f.listEventsErr
}

func (f *fakeService) ListChannels(_ context.Context, caller svc.Caller, page, pageSize int) ([]*svc.Channel, error) {
	f.lastCaller = caller
	f.lastPage = page
	f.lastPageSize = pageSize
	return f.listChannelsResp, f.listChannelsErr
}

func (f *fakeService) GetChannel(_ context.Context, caller svc.Caller, id uint64) (*svc.Channel, error) {
	f.lastCaller = caller
	f.lastChannelID = id
	return f.getChannelResp, f.getChannelErr
}

func (f *fakeService) CreateChannel(_ context.Context, caller svc.Caller, in svc.ChannelInput) (*svc.Channel, error) {
	f.lastCaller = caller
	f.lastChannelInput = in
	return f.createChannelResp, f.createChannelErr
}

func (f *fakeService) UpdateChannel(_ context.Context, caller svc.Caller, id uint64, in svc.ChannelInput) (*svc.Channel, error) {
	f.lastCaller = caller
	f.lastChannelID = id
	f.lastChannelInput = in
	return f.updateChannelResp, f.updateChannelErr
}

func (f *fakeService) DeleteChannel(_ context.Context, caller svc.Caller, id uint64) error {
	f.lastCaller = caller
	f.lastChannelID = id
	return f.deleteChannelErr
}

func (f *fakeService) TestChannel(_ context.Context, caller svc.Caller, id uint64) (*svc.ChannelTestResult, error) {
	f.lastCaller = caller
	f.lastChannelID = id
	return f.testChannelResp, f.testChannelErr
}

func buildRouter(h *Handler, tenant *tenantctx.Tenant) http.Handler {
	r := chi.NewRouter()
	if tenant != nil {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(tenantctx.With(req.Context(), *tenant)))
			})
		})
	}
	h.Register(r)
	return r
}

func TestListIncidentsHappyPath(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	f := &fakeService{
		listIncidentsResp: []*svc.Incident{{
			ID:         9,
			RuleKey:    "cpu_high",
			RuleName:   "CPU High",
			Severity:   "warning",
			Status:     "open",
			Summary:    "CPU > 90%",
			TargetType: "server",
			TargetID:   "2",
			TargetName: "self-loop",
			FiredAt:    now,
			UpdatedAt:  now,
		}},
	}
	h := NewHandler(f, f, f)
	router := buildRouter(h, &tenantctx.Tenant{UserID: 7, Role: "user"})

	req := httptest.NewRequest(http.MethodGet, "/v1/alerts/incidents?status=open&page=2&page_size=50", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body listIncidentsResp
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || len(body.Items) != 1 || body.Items[0].RuleKey != "cpu_high" {
		t.Fatalf("body = %+v", body)
	}
	if f.lastCaller.UserID != 7 || f.lastFilter.Status != "open" || f.lastFilter.Page != 2 || f.lastFilter.PageSize != 50 {
		t.Fatalf("caller/filter = %+v %+v", f.lastCaller, f.lastFilter)
	}
}

func TestGetIncidentNotWired(t *testing.T) {
	t.Parallel()

	f := &fakeService{getIncidentErr: errs.ErrNotWiredYet}
	h := NewHandler(f, f, f)
	router := buildRouter(h, &tenantctx.Tenant{UserID: 1, Role: "user"})

	req := httptest.NewRequest(http.MethodGet, "/v1/alerts/incidents/3", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", w.Code)
	}
	var body errorBody
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Code != "not-wired-yet" {
		t.Fatalf("code = %q", body.Code)
	}
}

func TestAckIncidentHappyPath(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	f := &fakeService{
		ackIncidentResp: &svc.Incident{
			ID:             11,
			RuleKey:        "edge_offline",
			RuleName:       "Edge Offline",
			Severity:       "critical",
			Status:         "acknowledged",
			Summary:        "offline 5m",
			TargetType:     "server",
			TargetID:       "2",
			TargetName:     "self-loop",
			FiredAt:        now,
			UpdatedAt:      now,
			AcknowledgedAt: &now,
		},
	}
	h := NewHandler(f, f, f)
	router := buildRouter(h, &tenantctx.Tenant{UserID: 99, Role: "user"})

	req := httptest.NewRequest(http.MethodPost, "/v1/alerts/incidents/11/ack", strings.NewReader(`{"note":"owner claimed"}`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if f.lastIncidentID != 11 || f.lastIncidentNote != "owner claimed" {
		t.Fatalf("ack input = id:%d note:%q", f.lastIncidentID, f.lastIncidentNote)
	}
}

func TestCreateChannelAdminHappyPath(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	f := &fakeService{
		createChannelResp: &svc.Channel{
			ID:             5,
			Name:           "feishu-primary",
			Type:           "feishu",
			Enabled:        true,
			EndpointMasked: "https://open.feishu.cn/***",
			CreatedAt:      now,
			UpdatedAt:      now,
		},
	}
	h := NewHandler(f, f, f)
	router := buildRouter(h, &tenantctx.Tenant{UserID: 9, Role: "admin"})

	req := httptest.NewRequest(http.MethodPost, "/v1/notification-channels", strings.NewReader(`{"name":"feishu-primary","type":"feishu","endpoint":"https://open.feishu.cn/hook","enabled":true}`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if f.lastChannelInput.Name != "feishu-primary" || f.lastChannelInput.Type != "feishu" {
		t.Fatalf("input = %+v", f.lastChannelInput)
	}
}

func TestCreateChannelForbiddenForNonAdmin(t *testing.T) {
	t.Parallel()

	f := &fakeService{}
	h := NewHandler(f, f, f)
	router := buildRouter(h, &tenantctx.Tenant{UserID: 9, Role: "user"})

	req := httptest.NewRequest(http.MethodPost, "/v1/notification-channels", strings.NewReader(`{"name":"x"}`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestListIncidentEventsHappyPath(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	f := &fakeService{
		listEventsResp: []*svc.Event{{
			ID:         42,
			IncidentID: 11,
			EventType:  "firing",
			Severity:   "critical",
			Title:      "edge 2 offline ≥ 1m",
			ActorType:  "system",
			OccurredAt: now,
			CreatedAt:  now,
		}},
	}
	h := NewHandler(f, f, f)
	router := buildRouter(h, &tenantctx.Tenant{UserID: 7, Role: "user"})

	req := httptest.NewRequest(http.MethodGet, "/v1/alerts/incidents/11/events?limit=50", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body listIncidentEventsResp
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || len(body.Items) != 1 || body.Items[0].EventType != "firing" {
		t.Fatalf("body = %+v", body)
	}
	if f.lastIncidentID != 11 || f.lastEventsLimit != 50 {
		t.Fatalf("inputs = id:%d limit:%d", f.lastIncidentID, f.lastEventsLimit)
	}
}

func TestSilenceIncidentHappyPath(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	f := &fakeService{
		silenceIncidentResp: &svc.Incident{
			ID:        11,
			RuleKey:   "cpu_high",
			Status:    "silenced",
			Summary:   "CPU > 90%",
			FiredAt:   now,
			UpdatedAt: now,
		},
	}
	h := NewHandler(f, f, f)
	router := buildRouter(h, &tenantctx.Tenant{UserID: 33, Role: "user"})

	req := httptest.NewRequest(http.MethodPost, "/v1/alerts/incidents/11/silence", strings.NewReader(`{"until":"30m","reason":"deploy in flight"}`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if f.lastIncidentID != 11 || f.lastSilenceInput.Until != "30m" || f.lastSilenceInput.Reason != "deploy in flight" {
		t.Fatalf("silence input = id:%d %+v", f.lastIncidentID, f.lastSilenceInput)
	}
}

func TestListChannelsRequiresAuth(t *testing.T) {
	t.Parallel()

	f := &fakeService{}
	h := NewHandler(f, f, f)
	router := buildRouter(h, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/notification-channels", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestCreateSMTPChannelPassesStructuredConfig(t *testing.T) {
	f := &fakeService{createChannelResp: &svc.Channel{ID: 1, Name: "email", Type: "smtp"}}
	router := buildRouter(NewHandler(f, f, f), &tenantctx.Tenant{UserID: 9, Role: "admin"})
	req := httptest.NewRequest(http.MethodPost, "/v1/notification-channels", strings.NewReader(`{"name":"email","type":"smtp","enabled":true,"secret":"test-password","smtp":{"host":"smtp.example.test","port":587,"from":"alerts@example.test","to":["ops@example.test"],"tls_mode":"starttls"}}`))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d: %s", recorder.Code, recorder.Body.String())
	}
	input := f.lastChannelInput
	if input.SMTP == nil || input.SMTP.Host != "smtp.example.test" || input.SMTP.Port != 587 || len(input.SMTP.To) != 1 || input.Secret != "test-password" {
		t.Fatal("SMTP request fields were not forwarded")
	}
	if strings.Contains(recorder.Body.String(), "test-password") {
		t.Fatal("password leaked into response")
	}
}
