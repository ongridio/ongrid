package edge

import "time"

// PluginHealth is one plugin's last-reported runtime health, shipped by the
// edge on its heartbeat. It is intentionally ephemeral — kept in memory only,
// cleared on manager restart and re-populated within one heartbeat interval
// (~30s). The point of the type is operator visibility: State + LastError turn
// "the logs plugin silently ships nothing" into "logs: crashed — subprocess
// binary missing".
type PluginHealth struct {
	Name         string               `json:"name"`
	State        string               `json:"state"` // stopped|starting|running|crashed
	LastError    string               `json:"last_error,omitempty"`
	RestartCount int                  `json:"restart_count,omitempty"`
	PID          int                  `json:"pid,omitempty"`
	StartedAt    time.Time            `json:"started_at,omitempty"`
	UpdatedAt    time.Time            `json:"updated_at,omitempty"`  // edge-side update time
	ReportedAt   time.Time            `json:"reported_at,omitempty"` // manager receive time
	Targets      []PluginTargetHealth `json:"targets,omitempty"`
}

// PluginTargetHealth is a per-source health row for metric sub-plugins
// that multiplex several scrape targets under one plugin config.
type PluginTargetHealth struct {
	ID            string    `json:"id"`
	Name          string    `json:"name,omitempty"`
	Kind          string    `json:"kind,omitempty"`
	State         string    `json:"state"`
	LastError     string    `json:"last_error,omitempty"`
	Samples       int       `json:"samples,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

// RecordPluginHealth stores the latest per-plugin health for one edge,
// overwriting any prior snapshot. Stamps ReportedAt with the manager clock so
// the UI can show staleness ("reported 4m ago") independent of edge clock
// skew. No-op for edgeID 0 or a nil/empty slice (a heartbeat without plugin
// data must not wipe a previously-good snapshot).
func (u *Usecase) RecordPluginHealth(edgeID uint64, items []PluginHealth) {
	if edgeID == 0 || len(items) == 0 {
		return
	}
	now := time.Now().UTC()
	for i := range items {
		items[i].ReportedAt = now
	}
	u.phMu.Lock()
	defer u.phMu.Unlock()
	if u.pluginHealth == nil {
		u.pluginHealth = make(map[uint64][]PluginHealth)
	}
	u.pluginHealth[edgeID] = items
}

// PluginHealth returns the last-reported plugin health for one edge, or nil
// if none has arrived yet (edge offline / pre-introduction agent / just
// restarted manager).
func (u *Usecase) PluginHealth(edgeID uint64) []PluginHealth {
	u.phMu.RLock()
	defer u.phMu.RUnlock()
	src := u.pluginHealth[edgeID]
	if len(src) == 0 {
		return nil
	}
	out := make([]PluginHealth, len(src))
	copy(out, src)
	return out
}
