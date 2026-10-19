package tools

import (
	"context"
	"sync"

	alertbiz "github.com/ongridio/ongrid/internal/manager/biz/alert"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
)

// fakeAlertUC is a minimal AlertUsecase used by the BaseTool tests for
// the alert-flavoured tools (query_incidents / get_incident_detail /
// query_alert_rules / get_edge_summary / correlate_incident).
//
// Each method records the last filter / id and returns a planted
// response. Errors plant per-method via the *Err fields.
type fakeAlertUC struct {
	mu sync.Mutex

	// Planted responses.
	incidentByID    map[uint64]*alertmodel.Incident
	listIncidents   []*alertmodel.Incident
	listIncidentsCb func(f alertbiz.IncidentFilter) []*alertmodel.Incident
	listEvents      []*alertmodel.Event
	listRules       []*alertmodel.Rule

	// Planted errors.
	getIncidentErr   error
	listIncidentsErr error
	listEventsErr    error
	listRulesErr     error

	// Last call recorders.
	lastIncidentFilter alertbiz.IncidentFilter
	lastEventsFor      uint64
	lastListScope      string
}

func (f *fakeAlertUC) GetIncident(_ context.Context, id uint64) (*alertmodel.Incident, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getIncidentErr != nil {
		return nil, f.getIncidentErr
	}
	if inc, ok := f.incidentByID[id]; ok {
		return inc, nil
	}
	return nil, nil
}

func (f *fakeAlertUC) ListIncidents(_ context.Context, filter alertbiz.IncidentFilter) ([]*alertmodel.Incident, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastIncidentFilter = filter
	if f.listIncidentsErr != nil {
		return nil, f.listIncidentsErr
	}
	if f.listIncidentsCb != nil {
		return f.listIncidentsCb(filter), nil
	}
	return f.listIncidents, nil
}

func (f *fakeAlertUC) ListEvents(_ context.Context, incidentID uint64, _ int) ([]*alertmodel.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastEventsFor = incidentID
	if f.listEventsErr != nil {
		return nil, f.listEventsErr
	}
	return f.listEvents, nil
}

func (f *fakeAlertUC) ListRules(_ context.Context, scope string) ([]*alertmodel.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastListScope = scope
	if f.listRulesErr != nil {
		return nil, f.listRulesErr
	}
	return f.listRules, nil
}
