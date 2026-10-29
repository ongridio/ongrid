package externalmcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	monitorbiz "github.com/ongridio/ongrid/internal/manager/biz/monitor"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	monitormodel "github.com/ongridio/ongrid/internal/manager/model/monitor"
	alertsvc "github.com/ongridio/ongrid/internal/manager/service/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
)

type Resources struct {
	Devices *devicebiz.Usecase
	Alerts  *alertsvc.Service
	Monitor *monitorbiz.Service
}

func textSchema(max int) map[string]any { return map[string]any{"type": "string", "maxLength": max} }
func idSchema() map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "maximum": 9007199254740991}
}
func enumSchema(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
func objectSchema(properties map[string]any, required []string) json.RawMessage {
	if required == nil {
		required = []string{}
	}
	data, err := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required})
	if err != nil {
		panic(err)
	} // Only static schema values are passed during startup.
	return data
}
func writeSchema(properties map[string]any, required ...string) json.RawMessage {
	properties["idempotency_key"] = map[string]any{"type": "string", "minLength": 8, "maxLength": 128, "description": "Keep the same key for retries of this logical change. Never replay an unknown operation with a new key."}
	properties["expected_revision"] = textSchema(64)
	properties["reason"] = textSchema(512)
	properties["dry_run"] = map[string]any{"type": "boolean"}
	return objectSchema(properties, append(required, "idempotency_key"))
}

// Revision hashes editable resource state. The write path compares it while
// holding a database row lock, including against concurrent REST changes.
func revision(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func checkRevision(args biz.Arguments, value any) error {
	if biz.String(args, "expected_revision") != revision(value) {
		return biz.ErrVersionConflict
	}
	return nil
}
func target(kind string, id uint64) biz.Target {
	return biz.Target{Type: kind, ID: id, Key: fmt.Sprintf("%s:%d", kind, id)}
}
func caller(ctx context.Context) alertsvc.Caller {
	i, _ := integrationauth.From(ctx)
	return alertsvc.Caller{UserID: i.Owner.UserID, Role: i.Owner.Role}
}
func only(args biz.Arguments, allowed ...string) error {
	allowed = append(allowed, "idempotency_key", "expected_revision", "dry_run", "reason")
	for key := range args {
		if !slices.Contains(allowed, key) {
			return errs.ErrInvalid
		}
	}
	return nil
}
func requireRevision(args biz.Arguments) error {
	if len(biz.String(args, "expected_revision")) != 64 {
		return errs.ErrInvalid
	}
	return nil
}
func requireReason(args biz.Arguments) error {
	if strings.TrimSpace(biz.String(args, "reason")) == "" {
		return errs.ErrInvalid
	}
	return nil
}

type deviceView struct {
	ID          uint64   `json:"device_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Roles       []string `json:"roles"`
	Environment *string  `json:"environment"`
}

func deviceFields(d *devicemodel.Device) deviceView {
	return deviceView{ID: d.ID, Name: d.Name, Description: d.Description, Roles: devicemodel.DecodeRoles(d.Roles), Environment: d.Environment}
}
func deviceResult(d *devicemodel.Device) any {
	return map[string]any{"resource": deviceFields(d), "revision": revision(deviceFields(d)), "hostname": d.Hostname, "ip_address": d.IPAddress, "online": d.Online, "last_seen_at": d.LastSeenAt}
}
func panelFields(p *monitormodel.Panel) any {
	return map[string]any{"id": p.ID, "title": p.Title, "type": p.Type, "promql": p.PromQL, "legend": p.Legend, "unit": p.Unit, "ordinal": p.Ordinal}
}
func panelResult(p *monitormodel.Panel) any {
	v := panelFields(p)
	return map[string]any{"resource": v, "revision": revision(v), "grafana_sync": "asynchronous"}
}
func ruleResult(r *alertsvc.Rule) any { return map[string]any{"resource": r, "revision": revision(r)} }

func (r Resources) Tools() []biz.Tool {
	tools := []biz.Tool{
		{Name: "get_device", Description: "Read one device and its revision before an update or deletion.", Scopes: []string{"devices:read"}, Schema: objectSchema(map[string]any{"device_id": idSchema()}, []string{"device_id"}), Run: func(ctx context.Context, a biz.Arguments) (any, error) {
			d, err := r.Devices.Get(ctx, uint64(biz.Number(a, "device_id")))
			if err != nil {
				return nil, err
			}
			return deviceResult(d), nil
		}},
		{Name: "update_device", Description: "Update only supplied display metadata, roles or environment. Read get_device for expected_revision first.", Scopes: []string{"devices:read", "devices:write"}, Mutation: true, Schema: writeSchema(map[string]any{"device_id": idSchema(), "name": textSchema(128), "description": textSchema(255), "roles": map[string]any{"type": "array", "maxItems": 5, "items": enumSchema("server", "storage", "network", "database", "gpu", "unknown")}, "environment": textSchema(256)}, "device_id", "expected_revision"), Validate: func(a biz.Arguments) error {
			if err := requireRevision(a); err != nil {
				return err
			}
			if _, name := a["name"]; !name {
				if _, desc := a["description"]; !desc {
					if _, roles := a["roles"]; !roles {
						if _, env := a["environment"]; !env {
							return errs.ErrInvalid
						}
					}
				}
			}
			if _, ok := a["environment"]; ok {
				return requireReason(a)
			}
			return nil
		}, Policy: func(a biz.Arguments) (biz.Policy, error) {
			return biz.Policy{Target: target("device", uint64(biz.Number(a, "device_id"))), LowRisk: a["roles"] == nil && a["environment"] == nil}, nil
		}, Run: r.updateDevice},
		{Name: "delete_device", Description: "Delete one offline device and its linked Edge identities after the user confirms once at approval_url. This does not uninstall the remote Agent. Use dry_run to inspect impact.", Scopes: []string{"devices:read", "devices:delete"}, Mutation: true, Schema: writeSchema(map[string]any{"device_id": idSchema()}, "device_id", "expected_revision", "reason"), Validate: func(a biz.Arguments) error {
			if err := requireRevision(a); err != nil {
				return err
			}
			return requireReason(a)
		}, Policy: func(a biz.Arguments) (biz.Policy, error) {
			return biz.Policy{Target: target("device", uint64(biz.Number(a, "device_id")))}, nil
		}, Run: r.deleteDevice},
		{Name: "get_incident_detail", Description: "Read up to ten incidents, revisions and bounded timelines. Missing entries are reported separately.", Scopes: []string{"alerts:read"}, Schema: objectSchema(map[string]any{"incident_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": 10, "uniqueItems": true, "items": idSchema()}}, []string{"incident_ids"}), Run: r.incidentDetails},
		{Name: "update_incident", Description: "Acknowledge, resolve or silence one incident. Silence requires an explicit future expires_at and reason.", Scopes: []string{"alerts:read", "alerts:write"}, Mutation: true, Schema: writeSchema(map[string]any{"incident_id": idSchema(), "action": enumSchema("ack", "resolve", "silence"), "until": textSchema(64), "note": textSchema(512)}, "incident_id", "action", "expected_revision"), Validate: func(a biz.Arguments) error {
			if err := requireRevision(a); err != nil {
				return err
			}
			if biz.String(a, "action") == "silence" {
				if err := only(a, "incident_id", "action", "until"); err != nil {
					return err
				}
				if _, err := time.Parse(time.RFC3339, biz.String(a, "until")); err != nil {
					return errs.ErrInvalid
				}
				return requireReason(a)
			}
			return only(a, "incident_id", "action", "note")
		}, ValidateNew: func(a biz.Arguments) error {
			if biz.String(a, "action") == "silence" {
				until, err := time.Parse(time.RFC3339, biz.String(a, "until"))
				now := time.Now()
				if err != nil || !until.After(now) || until.After(now.Add(30*24*time.Hour)) {
					return errs.ErrInvalid
				}
			}
			return nil
		}, Policy: func(a biz.Arguments) (biz.Policy, error) {
			return biz.Policy{Target: target("incident", uint64(biz.Number(a, "incident_id")))}, nil
		}, Run: r.updateIncident},
		{Name: "get_alert_rule", Description: "Read one alert rule and its revision.", Scopes: []string{"alert-rules:read"}, Schema: objectSchema(map[string]any{"rule_id": idSchema()}, []string{"rule_id"}), Run: func(ctx context.Context, a biz.Arguments) (any, error) {
			v, err := r.Alerts.GetRule(ctx, caller(ctx), uint64(biz.Number(a, "rule_id")))
			if err != nil {
				return nil, err
			}
			return ruleResult(v), nil
		}},
		{Name: "query_alert_rules", Description: "List a bounded page of alert rules.", Scopes: []string{"alert-rules:read"}, Schema: pageSchema(), Run: r.listRules},
		{Name: "get_monitor_panel", Description: "Read one monitor panel and its revision.", Scopes: []string{"monitor:read"}, Schema: objectSchema(map[string]any{"panel_id": idSchema()}, []string{"panel_id"}), Run: func(ctx context.Context, a biz.Arguments) (any, error) {
			p, err := r.Monitor.Get(ctx, uint64(biz.Number(a, "panel_id")))
			if err != nil {
				return nil, err
			}
			return panelResult(p), nil
		}},
		{Name: "list_monitor_panels", Description: "List a bounded page of monitor panels.", Scopes: []string{"monitor:read"}, Schema: pageSchema(), Run: r.listPanels},
		{Name: "manage_monitor_panel", Description: "Create, update or delete one panel; Grafana mirrors asynchronously. Update/delete require a current revision. Delete requires monitor:delete.", Scopes: []string{"monitor:read"}, ActionScopes: map[string][]string{"create": {"monitor:write"}, "update": {"monitor:write"}, "delete": {"monitor:delete"}}, Mutation: true, Schema: writeSchema(map[string]any{"action": enumSchema("create", "update", "delete"), "panel_id": idSchema(), "title": textSchema(128), "type": enumSchema("timeseries", "stat", "gauge"), "promql": textSchema(8192), "legend": textSchema(255), "unit": textSchema(32), "ordinal": map[string]any{"type": "integer", "minimum": 0, "maximum": 10000}}, "action"), Validate: validatePanel, Policy: func(a biz.Arguments) (biz.Policy, error) {
			return biz.Policy{Target: target("monitor_panel", uint64(biz.Number(a, "panel_id"))), LowRisk: biz.String(a, "action") == "create" || (biz.String(a, "action") == "update" && only(a, "action", "panel_id", "title") == nil)}, nil
		}, Run: r.managePanel},
	}
	tools = append(tools, r.ruleTool())
	return tools
}

func pageSchema() json.RawMessage {
	return objectSchema(map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": 100000}}, nil)
}
func bounds(a biz.Arguments, n int) (int, int) {
	limit := int(biz.Number(a, "limit"))
	if limit == 0 {
		limit = 20
	}
	start := min(int(biz.Number(a, "offset")), n)
	return start, min(start+limit, n)
}
func (r Resources) listRules(ctx context.Context, a biz.Arguments) (any, error) {
	rows, err := r.Alerts.ListRules(ctx, caller(ctx), "")
	if err != nil {
		return nil, err
	}
	start, end := bounds(a, len(rows))
	items := make([]any, 0, end-start)
	for _, row := range rows[start:end] {
		items = append(items, ruleResult(row))
	}
	return map[string]any{"items": items, "total_count": len(rows), "truncated": end < len(rows)}, nil
}
func (r Resources) listPanels(ctx context.Context, a biz.Arguments) (any, error) {
	rows, err := r.Monitor.List(ctx)
	if err != nil {
		return nil, err
	}
	start, end := bounds(a, len(rows))
	items := make([]any, 0, end-start)
	for _, row := range rows[start:end] {
		items = append(items, panelResult(row))
	}
	return map[string]any{"items": items, "total_count": len(rows), "truncated": end < len(rows)}, nil
}

func (r Resources) updateDevice(ctx context.Context, a biz.Arguments) (any, error) {
	id := uint64(biz.Number(a, "device_id"))
	d, err := r.Devices.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := checkRevision(a, deviceFields(d)); err != nil {
		return nil, err
	}
	if biz.Bool(a, "dry_run") {
		return map[string]any{"before": deviceFields(d), "changes": a, "applied": false}, nil
	}
	name, description := d.Name, d.Description
	if v, ok := a["name"].(string); ok {
		name = v
	}
	if v, ok := a["description"].(string); ok {
		description = v
	}
	if name != d.Name || description != d.Description {
		if err := r.Devices.UpdateNameDescription(ctx, id, name, description); err != nil {
			return nil, err
		}
	}
	if raw, ok := a["roles"]; ok {
		roles := []string{}
		for _, v := range raw.([]any) {
			roles = append(roles, v.(string))
		}
		if err := r.Devices.UpdateRoles(ctx, id, roles); err != nil {
			return nil, err
		}
	}
	if v, ok := a["environment"].(string); ok {
		if _, err := r.Devices.SetEnvironment(ctx, id, v); err != nil {
			return nil, err
		}
	}
	d, err = r.Devices.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return deviceResult(d), nil
}
func (r Resources) deleteDevice(ctx context.Context, a biz.Arguments) (any, error) {
	id := uint64(biz.Number(a, "device_id"))
	d, err := r.Devices.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := checkRevision(a, deviceFields(d)); err != nil {
		return nil, err
	}
	if d.Online {
		return nil, errs.ErrConflict
	}
	if biz.Bool(a, "dry_run") {
		links, err := r.Devices.Links().ListEdgesForDevice(ctx, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"device": deviceFields(d), "linked_edges": len(links), "approval_required": true, "applied": false}, nil
	}
	if err := r.Devices.Delete(ctx, id); err != nil {
		return nil, err
	}
	return map[string]any{"device_id": id, "deleted": true, "agent_uninstalled": false}, nil
}

func (r Resources) incidentDetails(ctx context.Context, a biz.Arguments) (any, error) {
	results := []any{}
	for _, raw := range a["incident_ids"].([]any) {
		id := uint64(raw.(float64))
		row, err := r.Alerts.GetIncident(ctx, caller(ctx), id)
		if err != nil {
			results = append(results, map[string]any{"incident_id": id, "error_code": biz.ErrorCode(err)})
			continue
		}
		events, err := r.Alerts.ListIncidentEvents(ctx, caller(ctx), id, 100)
		if err != nil {
			return nil, err
		}
		results = append(results, map[string]any{"incident_id": id, "incident": row, "revision": revision(row), "events": events, "timeline_complete": len(events) < 100})
	}
	return map[string]any{"results": results}, nil
}
func (r Resources) updateIncident(ctx context.Context, a biz.Arguments) (any, error) {
	id := uint64(biz.Number(a, "incident_id"))
	c := caller(ctx)
	row, err := r.Alerts.GetIncident(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if err := checkRevision(a, row); err != nil {
		return nil, err
	}
	if biz.Bool(a, "dry_run") {
		return map[string]any{"before": row, "action": biz.String(a, "action"), "applied": false}, nil
	}
	switch biz.String(a, "action") {
	case "ack":
		row, err = r.Alerts.AcknowledgeIncident(ctx, c, id, alertsvc.IncidentMutationInput{Note: biz.String(a, "note")})
	case "resolve":
		row, err = r.Alerts.ResolveIncident(ctx, c, id, alertsvc.IncidentMutationInput{Note: biz.String(a, "note")})
	case "silence":
		row, err = r.Alerts.SilenceIncident(ctx, c, id, alertsvc.IncidentSilenceInput{Until: biz.String(a, "until"), Reason: biz.String(a, "reason")})
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"incident": row, "revision": revision(row)}, nil
}

func validatePanel(a biz.Arguments) error {
	action := biz.String(a, "action")
	if action == "create" {
		if _, ok := a["panel_id"]; ok {
			return errs.ErrInvalid
		}
		if strings.TrimSpace(biz.String(a, "title")) == "" || strings.TrimSpace(biz.String(a, "promql")) == "" {
			return errs.ErrInvalid
		}
		return nil
	}
	if biz.Number(a, "panel_id") < 1 {
		return errs.ErrInvalid
	}
	if err := requireRevision(a); err != nil {
		return err
	}
	if action == "delete" {
		if err := requireReason(a); err != nil {
			return err
		}
		return only(a, "action", "panel_id")
	}
	return nil
}
func (r Resources) managePanel(ctx context.Context, a biz.Arguments) (any, error) {
	id := uint64(biz.Number(a, "panel_id"))
	action := biz.String(a, "action")
	var before *monitormodel.Panel
	if action != "create" {
		var err error
		before, err = r.Monitor.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := checkRevision(a, panelFields(before)); err != nil {
			return nil, err
		}
	}
	if biz.Bool(a, "dry_run") {
		return map[string]any{"action": action, "changes": a, "applied": false}, nil
	}
	if action == "delete" {
		if err := r.Monitor.Delete(ctx, id); err != nil {
			return nil, err
		}
		return map[string]any{"panel_id": id, "deleted": true}, nil
	}
	var result *monitormodel.Panel
	var err error
	if action == "create" {
		in := monitorbiz.CreateInput{Title: biz.String(a, "title"), Type: biz.String(a, "type"), PromQL: biz.String(a, "promql"), Legend: biz.String(a, "legend"), Unit: biz.String(a, "unit")}
		if v, ok := a["ordinal"].(float64); ok {
			n := int(v)
			in.Ordinal = &n
		}
		result, err = r.Monitor.Create(ctx, in)
	} else {
		in := monitorbiz.UpdateInput{}
		for key, dst := range map[string]**string{"title": &in.Title, "type": &in.Type, "promql": &in.PromQL, "legend": &in.Legend, "unit": &in.Unit} {
			if v, ok := a[key].(string); ok {
				*dst = &v
			}
		}
		if v, ok := a["ordinal"].(float64); ok {
			n := int(v)
			in.Ordinal = &n
		}
		result, err = r.Monitor.Update(ctx, id, in)
	}
	if err != nil {
		return nil, err
	}
	return panelResult(result), nil
}
