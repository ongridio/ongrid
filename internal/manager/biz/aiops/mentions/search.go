// Package mentions implements the @-mention search used by the SPA
// chat input. The user types `@<term>` in the textarea; the popover
// hits this biz to find matching devices / incidents / rules / log
// streams (filename label values) and inserts a structured reference
// the agent can resolve when running.
//
// This package is read-only — it never mutates platform state. It also
// never returns full bodies; only the minimum (id + label + subtitle)
// needed to render a popover row and to round-trip the reference back
// to the agent. The agent itself is responsible for any deeper hydration
// (see agent.resolveMentions).
//
// Multi-tenant note: ownership scoping is the caller's responsibility.
// In v1 the platform is single-tenant, so we don't filter by user_id /
// tenant_id at this layer — when tenancy lands, add an owner argument
// to Search and propagate it to each repo call. The wire shape stays
// stable.
package mentions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/alert"
	"github.com/ongridio/ongrid/internal/manager/biz/device"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

// Type is the closed-set of mention kinds the SPA can search.
type Type string

const (
	TypeDevice   Type = "device"
	TypeIncident Type = "incident"
	TypeRule     Type = "rule"
	TypeFile     Type = "file"
)

// Item is one search result row. Its shape mirrors the wire DTO used
// by /v1/aiops/mentions/search and the chat-send `mentions[]` payload.
type Item struct {
	Type     Type   `json:"type"`
	ID       string `json:"id"`
	Label    string `json:"label"`
	Subtitle string `json:"subtitle,omitempty"`
}

// Mention is the structured reference the agent resolves into context.
// The wire shape (in chat send + here) stays a flat triple so the LLM
// can be reasoned about without a JSON-Schema dance.
type Mention struct {
	Type  Type   `json:"type"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Searcher is the biz facade.
type Searcher struct {
	devices   *device.Usecase
	alerts    *alert.Usecase
	logClient logQuerier
}

// logQuerier is the narrow surface we need from logquery — only label
// values for the `filename` label, which we use as the file picker's
// data source. Defined here (instead of importing logquery's Client
// type directly) so tests can stub it. The real *logquery.Client
// satisfies the interface by structural typing.
type logQuerier interface {
	LabelValues(ctx context.Context, name string, start, end time.Time) ([]string, error)
}

// New builds a Searcher. Any of the three deps may be nil; the
// corresponding type just returns no results in that case (graceful
// degradation — a deployment without Loki still gets device / incident
// / rule mentions).
func New(devices *device.Usecase, alerts *alert.Usecase, logClient logQuerier) *Searcher {
	return &Searcher{devices: devices, alerts: alerts, logClient: logClient}
}

// Query is the input shape for Search.
type Query struct {
	// Term is the user's search text (post-`@`). Trimmed by the caller.
	Term string
	// Filter narrows to one type; empty → all types.
	Filter Type
	// Limit caps results per type (and total across types when Filter
	// is empty). 0 → 10. Caps at 50 to keep popover responsive.
	Limit int
}

// Search dispatches the term to each enabled sub-search. Results are
// returned in a stable type order (device → incident → rule → file)
// so the popover renders predictably.
func (s *Searcher) Search(ctx context.Context, q Query) ([]Item, error) {
	if s == nil {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	term := strings.TrimSpace(q.Term)
	out := make([]Item, 0, limit*4)

	// Helper that runs one sub-search and silently skips failures
	// (single-bad-source must not blank the whole popover). The agent
	// log for diagnostics happens at the HTTP layer.
	run := func(t Type, fn func(context.Context, string, int) ([]Item, error)) {
		if q.Filter != "" && q.Filter != t {
			return
		}
		items, err := fn(ctx, term, limit)
		if err != nil || len(items) == 0 {
			return
		}
		out = append(out, items...)
	}

	run(TypeDevice, s.searchDevices)
	run(TypeIncident, s.searchIncidents)
	run(TypeRule, s.searchRules)
	run(TypeFile, s.searchFiles)

	// When the filter is set we already capped at limit. When it isn't,
	// we return up to 4*limit (one bucket per type); that's the
	// documented popover behaviour ("top N from each grouped").
	return out, nil
}

func (s *Searcher) searchDevices(ctx context.Context, term string, limit int) ([]Item, error) {
	if s.devices == nil {
		return nil, nil
	}
	// Try numeric id match first when the term is all digits — operators
	// often paste a device id directly.
	var idMatch *Item
	if isAllDigits(term) {
		var id uint64
		fmt.Sscanf(term, "%d", &id)
		if id > 0 {
			d, err := s.devices.Get(ctx, id)
			if err == nil && d != nil {
				it := deviceToItem(d)
				idMatch = &it
			}
		}
	}
	// Substring search by name + hostname. The Repo Hostname/Name
	// filter both LIKE-match — we run two queries and union, capped at
	// limit. Empty term → most-recent N.
	out := make([]Item, 0, limit)
	if idMatch != nil {
		out = append(out, *idMatch)
	}
	if term != "" {
		byName, _ := s.devices.List(ctx, device.ListFilter{Name: term, Limit: limit})
		for _, d := range byName {
			out = append(out, deviceToItem(d))
			if len(out) >= limit {
				return out, nil
			}
		}
		byHost, _ := s.devices.List(ctx, device.ListFilter{Hostname: term, Limit: limit})
		for _, d := range byHost {
			it := deviceToItem(d)
			if !containsItem(out, it) {
				out = append(out, it)
			}
			if len(out) >= limit {
				return out, nil
			}
		}
	} else {
		recent, _ := s.devices.List(ctx, device.ListFilter{Limit: limit})
		for _, d := range recent {
			out = append(out, deviceToItem(d))
		}
	}
	return out, nil
}

func (s *Searcher) searchIncidents(ctx context.Context, term string, limit int) ([]Item, error) {
	if s.alerts == nil {
		return nil, nil
	}
	out := make([]Item, 0, limit)
	// id match first.
	if isAllDigits(term) {
		var id uint64
		fmt.Sscanf(term, "%d", &id)
		if id > 0 {
			inc, err := s.alerts.GetIncident(ctx, id)
			if err == nil && inc != nil {
				out = append(out, incidentToItem(inc))
			}
		}
	}
	// Then rule_key contains. The biz IncidentFilter only takes an
	// exact RuleKey match; we use it as a substring proxy by passing
	// term verbatim. The repo SQL is `rule = ?` (exact) so the only
	// matches are when the user typed the rule key in full — for
	// MVP that's fine; popover users typically copy/paste keys.
	rows, _ := s.alerts.ListIncidents(ctx, alert.IncidentFilter{RuleKey: term, Limit: limit})
	for _, inc := range rows {
		it := incidentToItem(inc)
		if !containsItem(out, it) {
			out = append(out, it)
		}
		if len(out) >= limit {
			break
		}
	}
	// Empty term → recent incidents.
	if term == "" && len(out) < limit {
		rows, _ := s.alerts.ListIncidents(ctx, alert.IncidentFilter{Limit: limit})
		for _, inc := range rows {
			it := incidentToItem(inc)
			if !containsItem(out, it) {
				out = append(out, it)
			}
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *Searcher) searchRules(ctx context.Context, term string, limit int) ([]Item, error) {
	if s.alerts == nil {
		return nil, nil
	}
	rows, err := s.alerts.ListRules(ctx, "")
	if err != nil {
		return nil, err
	}
	lc := strings.ToLower(term)
	out := make([]Item, 0, limit)
	for _, r := range rows {
		if term != "" {
			if !strings.Contains(strings.ToLower(r.RuleKey), lc) &&
				!strings.Contains(strings.ToLower(r.Name), lc) {
				continue
			}
		}
		out = append(out, ruleToItem(r))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *Searcher) searchFiles(ctx context.Context, term string, limit int) ([]Item, error) {
	if s.logClient == nil {
		return nil, nil
	}
	// Loki LabelValues over the last 24h is enough for the picker —
	// most operators want the file they're actively writing to. Any
	// quieter file beyond that is reachable via free-text.
	to := time.Now().UTC()
	from := to.Add(-24 * time.Hour)
	values, err := s.logClient.LabelValues(ctx, "filename", from, to)
	if err != nil {
		return nil, err
	}
	lc := strings.ToLower(term)
	out := make([]Item, 0, limit)
	for _, v := range values {
		if term != "" && !strings.Contains(strings.ToLower(v), lc) {
			continue
		}
		out = append(out, Item{
			Type:     TypeFile,
			ID:       v,
			Label:    v,
			Subtitle: "log file",
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Resolve hydrates a list of Mention references into one human-readable
// bullet per mention. The agent inlines the result into the system
// prompt so the LLM has structured context without spending a tool
// round-trip. Failures degrade silently — a deleted device shouldn't
// kill the agent run.
func (s *Searcher) Resolve(ctx context.Context, mentions []Mention) []string {
	if len(mentions) == 0 || s == nil {
		return nil
	}
	out := make([]string, 0, len(mentions))
	for _, m := range mentions {
		bullet := s.resolveOne(ctx, m)
		if bullet != "" {
			out = append(out, bullet)
		}
	}
	return out
}

func (s *Searcher) resolveOne(ctx context.Context, m Mention) string {
	switch m.Type {
	case TypeDevice:
		if s.devices == nil {
			break
		}
		var id uint64
		fmt.Sscanf(m.ID, "%d", &id)
		if id == 0 {
			break
		}
		d, err := s.devices.Get(ctx, id)
		if err != nil || d == nil {
			break
		}
		online := "offline"
		if d.Online {
			online = "online"
		}
		ipPart := ""
		if d.IPAddress != "" {
			ipPart = ", ip=" + d.IPAddress
		}
		return fmt.Sprintf("- device #%d %s (%s, hostname=%s%s)", d.ID, displayDeviceName(d), online, d.Hostname, ipPart)
	case TypeIncident:
		if s.alerts == nil {
			break
		}
		var id uint64
		fmt.Sscanf(m.ID, "%d", &id)
		if id == 0 {
			break
		}
		inc, err := s.alerts.GetIncident(ctx, id)
		if err != nil || inc == nil {
			break
		}
		return fmt.Sprintf("- incident #%d rule=%s severity=%s status=%s title=%q",
			inc.ID, inc.Rule, inc.Severity, inc.Status, inc.Title)
	case TypeRule:
		if s.alerts == nil {
			break
		}
		// Fall back to listing rules and finding the row by id or rule_key.
		rows, _ := s.alerts.ListRules(ctx, "")
		for _, r := range rows {
			if r.RuleKey == m.ID || fmt.Sprintf("%d", r.ID) == m.ID {
				return fmt.Sprintf("- rule %s (%s) severity=%s enabled=%t",
					r.RuleKey, r.Name, r.Severity, r.Enabled)
			}
		}
	case TypeFile:
		// No hydration — the filename is itself the descriptor.
		return fmt.Sprintf("- log file %s", m.ID)
	}
	// Generic fallback so the agent at least sees the user's intent.
	if m.Label != "" {
		return fmt.Sprintf("- %s %s (%s)", m.Type, m.Label, m.ID)
	}
	return ""
}

// ----------------- helpers -----------------

func deviceToItem(d *devicemodel.Device) Item {
	online := "offline"
	if d.Online {
		online = "online"
	}
	roleNames := devicemodel.DecodeRoles(d.Roles)
	rolePart := ""
	if len(roleNames) > 0 {
		rolePart = " · " + strings.Join(roleNames, ",")
	}
	ipPart := ""
	if d.IPAddress != "" {
		ipPart = " · " + d.IPAddress
	}
	return Item{
		Type:     TypeDevice,
		ID:       fmt.Sprintf("%d", d.ID),
		Label:    displayDeviceName(d),
		Subtitle: online + rolePart + ipPart,
	}
}

func displayDeviceName(d *devicemodel.Device) string {
	if d.Name != "" {
		return d.Name
	}
	if d.Hostname != "" {
		return d.Hostname
	}
	return fmt.Sprintf("device-%d", d.ID)
}

func incidentToItem(inc *alertmodel.Incident) Item {
	sub := inc.Severity + " · " + inc.Status
	return Item{
		Type:     TypeIncident,
		ID:       fmt.Sprintf("%d", inc.ID),
		Label:    fmt.Sprintf("#%d %s", inc.ID, inc.Rule),
		Subtitle: sub,
	}
}

func ruleToItem(r *alertmodel.Rule) Item {
	return Item{
		Type:     TypeRule,
		ID:       r.RuleKey,
		Label:    fmt.Sprintf("%s — %s", r.RuleKey, r.Name),
		Subtitle: r.Severity + " · " + r.ScopeType,
	}
}

func containsItem(items []Item, it Item) bool {
	for _, x := range items {
		if x.Type == it.Type && x.ID == it.ID {
			return true
		}
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// LogQuerierAdapter wraps *logquery.Client so its method set matches
// the local logQuerier interface (signatures already align — this is a
// type alias for clarity at wiring sites).
type LogQuerierAdapter = logquery.Client
