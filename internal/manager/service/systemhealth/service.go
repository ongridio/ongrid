// Package systemhealth aggregates platform self-checks for the manager UI.
package systemhealth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	alertsvc "github.com/ongridio/ongrid/internal/manager/service/alert"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusFailed   Status = "failed"
	StatusUnknown  Status = "unknown"
)

type Check struct {
	ID         string         `json:"id"`
	Group      string         `json:"group"`
	Label      string         `json:"label"`
	Status     Status         `json:"status"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	DurationMS int64          `json:"duration_ms"`
}

type Summary struct {
	OK       int `json:"ok"`
	Degraded int `json:"degraded"`
	Failed   int `json:"failed"`
	Unknown  int `json:"unknown"`
}

type Report struct {
	Status    Status    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
	Summary   Summary   `json:"summary"`
	Checks    []Check   `json:"checks"`
}

type DBPinger interface {
	PingContext(ctx context.Context) error
}

type PromQuerier interface {
	Query(ctx context.Context, expr string, ts time.Time) (any, error)
}

type URLProbe interface {
	Probe(ctx context.Context) error
}

type GrafanaTester interface {
	Test(ctx context.Context) error
}

type RuleLister interface {
	ListRules(ctx context.Context, caller alertsvc.Caller, scopeType string) ([]*alertsvc.Rule, error)
}

type IncidentCounter interface {
	CountIncidents(ctx context.Context, caller alertsvc.Caller, in alertsvc.IncidentFilter) (int64, error)
}

type EdgeLister interface {
	List(ctx context.Context, f edgebiz.ListFilter) ([]*edgemodel.Edge, error)
}

type LLMProviderResolver interface {
	ResolveProviders(ctx context.Context) ([]llm.ProviderConfig, string, error)
}

type Dependencies struct {
	DB        DBPinger
	Prom      PromQuerier
	Grafana   GrafanaTester
	Loki      URLProbe
	Tempo     URLProbe
	Rules     RuleLister
	Incidents IncidentCounter
	Edges     EdgeLister
	LLM       LLMProviderResolver
	HTTP      *http.Client
}

type Config struct {
	Version             string
	ProbeTimeout        time.Duration
	PromEnabled         bool
	LogsEnabled         bool
	TracesEnabled       bool
	AlertEnabled        bool
	EvaluatorInterval   time.Duration
	NotifyCooldown      time.Duration
	FrontierAddr        string
	FrontierDisabled    bool
	LLMConfigured       bool
	EmbeddingConfigured bool
	QdrantURL           string
	QdrantCollection    string
}

type Service struct {
	cfg  Config
	deps Dependencies
}

func New(cfg Config, deps Dependencies) *Service {
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 3 * time.Second
	}
	if cfg.QdrantCollection == "" {
		cfg.QdrantCollection = "ongrid_knowledge"
	}
	if deps.HTTP == nil {
		deps.HTTP = &http.Client{Timeout: cfg.ProbeTimeout}
	}
	return &Service{cfg: cfg, deps: deps}
}

func (s *Service) Check(ctx context.Context, caller alertsvc.Caller) (*Report, error) {
	checks := []Check{
		s.checkManager(ctx),
		s.checkDatabase(ctx),
		s.checkPrometheus(ctx),
		s.checkGrafana(ctx),
		s.checkLoki(ctx),
		s.checkTempo(ctx),
		s.checkQdrant(ctx),
		s.checkFrontier(ctx),
		s.checkAlerts(ctx, caller),
		s.checkEdges(ctx),
		s.checkLLM(ctx),
		s.checkEmbedding(ctx),
	}
	summary := summarize(checks)
	return &Report{
		Status:    overall(summary),
		CheckedAt: time.Now().UTC(),
		Summary:   summary,
		Checks:    checks,
	}, nil
}

func (s *Service) checkManager(ctx context.Context) Check {
	return s.probe(ctx, "manager_api", "core", "Manager API", func(context.Context) (Status, string, map[string]any) {
		details := map[string]any{}
		if s.cfg.Version != "" {
			details["version"] = s.cfg.Version
		}
		return StatusOK, "health endpoint reached the manager API", details
	})
}

func (s *Service) checkDatabase(ctx context.Context) Check {
	return s.probe(ctx, "database", "core", "Database", func(ctx context.Context) (Status, string, map[string]any) {
		if s.deps.DB == nil {
			return StatusFailed, "database pool is not wired", nil
		}
		if err := s.deps.DB.PingContext(ctx); err != nil {
			return StatusFailed, "database ping failed: " + err.Error(), nil
		}
		return StatusOK, "database ping succeeded", nil
	})
}

func (s *Service) checkPrometheus(ctx context.Context) Check {
	return s.probe(ctx, "prometheus", "observability", "Prometheus", func(ctx context.Context) (Status, string, map[string]any) {
		if !s.cfg.PromEnabled || s.deps.Prom == nil {
			return StatusDegraded, "Prometheus is disabled or not wired", map[string]any{"enabled": s.cfg.PromEnabled}
		}
		if _, err := s.deps.Prom.Query(ctx, "up", time.Now()); err != nil {
			return StatusFailed, "Prometheus query failed: " + err.Error(), nil
		}
		return StatusOK, "Prometheus query succeeded", nil
	})
}

func (s *Service) checkGrafana(ctx context.Context) Check {
	return s.probe(ctx, "grafana", "observability", "Grafana", func(ctx context.Context) (Status, string, map[string]any) {
		if s.deps.Grafana == nil {
			return StatusDegraded, "Grafana service is not wired", nil
		}
		if err := s.deps.Grafana.Test(ctx); err != nil {
			if isGrafanaConfigMissing(err) {
				return StatusDegraded, "Grafana integration is not configured: " + err.Error(), nil
			}
			return StatusFailed, "Grafana test failed: " + err.Error(), nil
		}
		return StatusOK, "Grafana test succeeded", nil
	})
}

func isGrafanaConfigMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "root_url is empty") ||
		strings.Contains(msg, "sa_token / api_key empty")
}

func (s *Service) checkLoki(ctx context.Context) Check {
	return s.probe(ctx, "loki", "observability", "Loki", func(ctx context.Context) (Status, string, map[string]any) {
		if !s.cfg.LogsEnabled || s.deps.Loki == nil {
			return StatusDegraded, "Loki is disabled or not wired", map[string]any{"enabled": s.cfg.LogsEnabled}
		}
		if err := s.deps.Loki.Probe(ctx); err != nil {
			return StatusFailed, "Loki readiness probe failed: " + err.Error(), nil
		}
		return StatusOK, "Loki readiness probe succeeded", nil
	})
}

func (s *Service) checkTempo(ctx context.Context) Check {
	return s.probe(ctx, "tempo", "observability", "Tempo", func(ctx context.Context) (Status, string, map[string]any) {
		if !s.cfg.TracesEnabled || s.deps.Tempo == nil {
			return StatusDegraded, "Tempo is disabled or not wired", map[string]any{"enabled": s.cfg.TracesEnabled}
		}
		if err := s.deps.Tempo.Probe(ctx); err != nil {
			return StatusFailed, "Tempo readiness probe failed: " + err.Error(), nil
		}
		return StatusOK, "Tempo readiness probe succeeded", nil
	})
}

func (s *Service) checkQdrant(ctx context.Context) Check {
	return s.probe(ctx, "qdrant", "data", "Qdrant", func(ctx context.Context) (Status, string, map[string]any) {
		if strings.TrimSpace(s.cfg.QdrantURL) == "" {
			return StatusDegraded, "Qdrant URL is not configured", nil
		}
		u := strings.TrimRight(s.cfg.QdrantURL, "/") + "/collections/" + url.PathEscape(s.cfg.QdrantCollection)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return StatusFailed, "Qdrant request build failed: " + err.Error(), nil
		}
		resp, err := s.deps.HTTP.Do(req)
		if err != nil {
			return StatusFailed, "Qdrant collection probe failed: " + err.Error(), nil
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			return StatusOK, "Qdrant collection is reachable", map[string]any{"collection": s.cfg.QdrantCollection}
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		msg := fmt.Sprintf("Qdrant collection probe returned HTTP %d", resp.StatusCode)
		if body := strings.TrimSpace(string(raw)); body != "" {
			msg += ": " + body
		}
		return StatusFailed, msg, map[string]any{"collection": s.cfg.QdrantCollection}
	})
}

func (s *Service) checkFrontier(ctx context.Context) Check {
	return s.probe(ctx, "frontier", "core", "Frontier", func(context.Context) (Status, string, map[string]any) {
		details := map[string]any{"addr": s.cfg.FrontierAddr}
		if s.cfg.FrontierDisabled {
			return StatusDegraded, "frontier client is disabled", details
		}
		return StatusOK, "frontier client is enabled; edge online state is checked separately", details
	})
}

func (s *Service) checkAlerts(ctx context.Context, caller alertsvc.Caller) Check {
	return s.probe(ctx, "alert_engine", "automation", "Alert engine", func(ctx context.Context) (Status, string, map[string]any) {
		if !s.cfg.AlertEnabled {
			return StatusDegraded, "alert evaluator is disabled", map[string]any{"enabled": false}
		}
		if s.deps.Rules == nil || s.deps.Incidents == nil {
			return StatusDegraded, "alert service is not fully wired", nil
		}
		rules, err := s.deps.Rules.ListRules(ctx, caller, "")
		if err != nil {
			return StatusFailed, "alert rules check failed: " + err.Error(), nil
		}
		enabled := 0
		for _, r := range rules {
			if r != nil && r.Enabled {
				enabled++
			}
		}
		open, err := s.deps.Incidents.CountIncidents(ctx, caller, alertsvc.IncidentFilter{Status: "open"})
		if err != nil {
			return StatusFailed, "open incident count failed: " + err.Error(), nil
		}
		details := map[string]any{
			"rules":                      len(rules),
			"enabled_rules":              enabled,
			"open_incidents":             open,
			"evaluator_interval_seconds": int(s.cfg.EvaluatorInterval.Seconds()),
			"notify_cooldown_seconds":    int(s.cfg.NotifyCooldown.Seconds()),
		}
		switch {
		case len(rules) == 0:
			return StatusDegraded, "alert engine is enabled but has no rules", details
		case open > 0:
			return StatusDegraded, fmt.Sprintf("%d open incident(s) need attention", open), details
		default:
			return StatusOK, "alert rules loaded and no open incident exists", details
		}
	})
}

func (s *Service) checkEdges(ctx context.Context) Check {
	return s.probe(ctx, "edges", "edge", "Edge agents", func(ctx context.Context) (Status, string, map[string]any) {
		if s.deps.Edges == nil {
			return StatusDegraded, "edge service is not wired", nil
		}
		edges, err := s.deps.Edges.List(ctx, edgebiz.ListFilter{Limit: 1000})
		if err != nil {
			return StatusFailed, "edge list check failed: " + err.Error(), nil
		}
		online := 0
		offline := 0
		for _, e := range edges {
			if e == nil {
				continue
			}
			switch e.Status {
			case edgemodel.StatusOnline:
				online++
			case edgemodel.StatusOffline:
				offline++
			}
		}
		details := map[string]any{
			"sampled": len(edges),
			"online":  online,
			"offline": offline,
			"limit":   1000,
		}
		switch {
		case len(edges) == 0:
			return StatusOK, "no edge agent is registered; edge access is ready", details
		case online == 0:
			return StatusFailed, "all sampled edge agents are offline", details
		case offline > 0:
			return StatusDegraded, fmt.Sprintf("%d sampled edge agent(s) are offline", offline), details
		default:
			return StatusOK, "sampled edge agents are online", details
		}
	})
}

func (s *Service) checkLLM(ctx context.Context) Check {
	return s.probe(ctx, "llm", "ai", "LLM provider", func(ctx context.Context) (Status, string, map[string]any) {
		if s.deps.LLM != nil {
			providers, defaultProvider, err := s.deps.LLM.ResolveProviders(ctx)
			if err != nil {
				return StatusFailed, "LLM provider resolver failed: " + err.Error(), nil
			}
			details := map[string]any{
				"providers":        len(providers),
				"default_provider": defaultProvider,
			}
			if len(providers) == 0 {
				return StatusDegraded, "no LLM provider is configured", details
			}
			return StatusOK, "LLM provider catalog is configured", details
		}
		if !s.cfg.LLMConfigured {
			return StatusDegraded, "LLM provider is not configured", nil
		}
		return StatusOK, "LLM provider key is configured", nil
	})
}

func (s *Service) checkEmbedding(ctx context.Context) Check {
	return s.probe(ctx, "embedding", "ai", "Embedding provider", func(context.Context) (Status, string, map[string]any) {
		if !s.cfg.EmbeddingConfigured {
			return StatusDegraded, "embedding provider is not configured", nil
		}
		return StatusOK, "embedding provider is configured", nil
	})
}

func (s *Service) probe(
	ctx context.Context,
	id string,
	group string,
	label string,
	fn func(context.Context) (Status, string, map[string]any),
) Check {
	start := time.Now()
	pctx, cancel := context.WithTimeout(ctx, s.cfg.ProbeTimeout)
	defer cancel()
	status, message, details := fn(pctx)
	if pctx.Err() == context.DeadlineExceeded && status == StatusOK {
		status = StatusFailed
		message = "probe timed out"
	}
	return Check{
		ID:         id,
		Group:      group,
		Label:      label,
		Status:     status,
		Message:    message,
		Details:    details,
		DurationMS: time.Since(start).Milliseconds(),
	}
}

func summarize(checks []Check) Summary {
	var out Summary
	for _, c := range checks {
		switch c.Status {
		case StatusOK:
			out.OK++
		case StatusDegraded:
			out.Degraded++
		case StatusFailed:
			out.Failed++
		default:
			out.Unknown++
		}
	}
	return out
}

func overall(s Summary) Status {
	switch {
	case s.Failed > 0:
		return StatusFailed
	case s.Degraded > 0:
		return StatusDegraded
	case s.Unknown > 0:
		return StatusUnknown
	default:
		return StatusOK
	}
}
