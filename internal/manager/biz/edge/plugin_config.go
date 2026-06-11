package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// PluginConfigRepo is the narrow persistence contract this biz layer
// needs. *sqlite.PluginConfigRepo satisfies it.
type PluginConfigRepo interface {
	ListByEdge(ctx context.Context, edgeID uint64) ([]*model.PluginConfig, error)
	Get(ctx context.Context, edgeID uint64, plugin string) (*model.PluginConfig, error)
	Upsert(ctx context.Context, in *model.PluginConfig) (*model.PluginConfig, error)
	Delete(ctx context.Context, edgeID uint64, plugin string) error
	CountByPlugin(ctx context.Context) (map[string]int64, error)
}

// EdgeReloadNotifier abstracts "tell this edge to re-fetch its plugin
// configs". Implemented by frontierbound.Client.PluginConfigsChanged.
type EdgeReloadNotifier interface {
	NotifyPluginConfigsChanged(ctx context.Context, edgeID uint64) error
}

// EndpointResolver returns the data plane endpoint a given plugin
// should push to. Implementation lives at the wiring site (cmd/ongrid)
// because it composes ONGRID_PUBLIC_URL + the per-plugin path AND
// consults system_settings (loki.url / tempo.url) so an admin edit in
// the Integrations UI re-targets edges automatically. Stubbed out as
// an interface so PluginConfigUC stays testable without env.
//
// ctx is threaded through so the resolver can hit the cached settings
// service without inventing a background context that ignores deadlines.
type EndpointResolver interface {
	Endpoint(ctx context.Context, plugin string) string
}

// PluginConfigUC is the use-case for managing per-edge plugin configs.
//
// Two consumers:
//   - HTTP API (UI): list / set / delete via internal/manager/server/edge.
//   - Tunnel RPC (edge): FetchForEdge serves the wire snapshot when an
//     edge calls MethodGetPluginConfigs.
//
// On any mutating call, UC fires-and-forgets a reload notification to
// the affected edge so changes propagate within seconds, not within the
// edge's 60s safety-net poll window.
type PluginConfigUC struct {
	repo     PluginConfigRepo
	notifier EdgeReloadNotifier
	resolver EndpointResolver
	log      *slog.Logger
}

// NewPluginConfigUC builds the use-case. notifier may be nil during
// startup (before frontierbound is wired); calls become no-ops then.
// resolver MUST be non-nil — without it FetchForEdge can't tell the edge
// where to push.
func NewPluginConfigUC(repo PluginConfigRepo, notifier EdgeReloadNotifier, resolver EndpointResolver, log *slog.Logger) *PluginConfigUC {
	if log == nil {
		log = slog.Default()
	}
	return &PluginConfigUC{repo: repo, notifier: notifier, resolver: resolver, log: log}
}

// SetNotifier injects the notifier post-construction. cmd/ongrid wires
// the use-case before frontierbound is ready, then back-fills the
// notifier once the tunnel is alive.
func (uc *PluginConfigUC) SetNotifier(n EdgeReloadNotifier) { uc.notifier = n }

// PluginRow is the UI/HTTP-friendly view of one plugin row.
type PluginRow struct {
	PluginName string                 `json:"plugin_name"`
	Enabled    bool                   `json:"enabled"`
	Spec       map[string]interface{} `json:"spec,omitempty"`
}

// pluginDefaultEnabled declares the on-by-default policy for fresh
// edges that don't yet have a row in edge_plugin_configs. Every
// subprocess + push path ships in the edge tarball (install-edge.sh
// drops the binaries into /usr/local/lib/ongrid-edge), so they're
// safe to auto-start on first connect. Without this every freshly
// installed edge shows up with empty Monitor panels and silent log /
// trace ingestion until an operator hand-clicks every toggle on
// /edges/{id}.
//
// Data path:
//   - hostmetrics — node_exporter subprocess exposing :9102/metrics
//   - procmetrics — process_exporter subprocess exposing :9256/metrics
//   - metrics — parent metrics pipeline whose sub-plugins push via
//     the tunnel's push_prom_samples RPC into cloud Prom's
//     remote_write. This is the universal path that works for any
//     edge (local or across the internet). It replaces the legacy
//     prometheus.yml host.docker.internal scrape, which only ever
//     worked for an edge co-resident with the manager.
//   - custommetrics / databasemetrics — operator configured metric
//     sub-plugins. They stay disabled until targets/sources are set.
//   - logs / traces — promtail / otelcol subprocesses pushing direct
//     to manager nginx via publicURL.
//
// Stay off:
//   - profiles — pyroscope agent isn't in the default install bundle.
//
// Explicit operator opt-out is preserved: Set writes a row with
// Enabled=false, which beats this default (the table lookup wins
// over the map fallback below).
var pluginDefaultEnabled = map[string]bool{
	model.PluginNameMetrics:     true,
	model.PluginNameHostMetrics: true,
	model.PluginNameProcMetrics: true,
	model.PluginNameLogs:        true,
	model.PluginNameTraces:      true,
}

// ListForUI returns every plugin config row for an edge, decoding the
// spec JSON for the UI. Plugins the system knows about but that have no
// row yet are filled in as Enabled=false / empty spec so the UI shows a
// stable list of toggles.
func (uc *PluginConfigUC) ListForUI(ctx context.Context, edgeID uint64) ([]PluginRow, error) {
	rows, err := uc.repo.ListByEdge(ctx, edgeID)
	if err != nil {
		return nil, err
	}
	have := map[string]*model.PluginConfig{}
	for _, r := range rows {
		have[r.PluginName] = r
	}
	knownPlugins := []string{
		model.PluginNameMetrics,
		model.PluginNameLogs,
		model.PluginNameTraces,
		model.PluginNameProfiles,
		model.PluginNameHostMetrics,
		model.PluginNameProcMetrics,
		model.PluginNameCustomMetrics,
		model.PluginNameDatabaseMetrics,
	}
	out := make([]PluginRow, 0, len(knownPlugins))
	for _, name := range knownPlugins {
		row := PluginRow{PluginName: name, Enabled: pluginDefaultEnabled[name]}
		if r, ok := have[name]; ok {
			// Explicit DB row always wins — preserves operator opt-out.
			row.Enabled = r.Enabled
			row.Spec = decodeSpec(r.SpecJSON)
		}
		out = append(out, row)
	}
	return out, nil
}

// SetInput is the mutation payload from the UI / API.
type SetInput struct {
	Enabled bool                   `json:"enabled"`
	Spec    map[string]interface{} `json:"spec,omitempty"`
}

// Set upserts one plugin config and (best-effort) notifies the edge to
// reload. Validates plugin name + spec marshallability.
func (uc *PluginConfigUC) Set(ctx context.Context, edgeID uint64, plugin string, in SetInput) (*PluginRow, error) {
	if edgeID == 0 {
		return nil, fmt.Errorf("%w: edge_id required", errs.ErrInvalid)
	}
	if !model.IsKnownPluginName(plugin) {
		return nil, fmt.Errorf("%w: unknown plugin %q", errs.ErrInvalid, plugin)
	}
	specJSON := "{}"
	if in.Spec != nil {
		blob, err := json.Marshal(in.Spec)
		if err != nil {
			return nil, fmt.Errorf("%w: marshal spec: %v", errs.ErrInvalid, err)
		}
		specJSON = string(blob)
	}
	row, err := uc.repo.Upsert(ctx, &model.PluginConfig{
		EdgeID:     edgeID,
		PluginName: plugin,
		Enabled:    in.Enabled,
		SpecJSON:   specJSON,
	})
	if err != nil {
		return nil, err
	}
	uc.notify(ctx, edgeID, plugin)
	return &PluginRow{PluginName: row.PluginName, Enabled: row.Enabled, Spec: decodeSpec(row.SpecJSON)}, nil
}

// FetchForEdge is the tunnel-RPC view: returns the wire snapshot the
// edge supervisor consumes. Includes every known plugin (disabled
// ones surface so supervisor can stop them if they were running).
// Endpoint is filled in from EndpointResolver — single source of
// truth.
//
// Default-enable policy is owned by pluginDefaultEnabled (see above):
// freshly installed edges auto-start hostmetrics / procmetrics / logs
// / traces on first connect so Monitor panels and log/trace ingestion
// just work. Any explicit DB row (operator opt-out via UI) beats the
// default — table lookup wins.
func (uc *PluginConfigUC) FetchForEdge(ctx context.Context, edgeID uint64) (*WireSnapshot, error) {
	rows, err := uc.repo.ListByEdge(ctx, edgeID)
	if err != nil {
		return nil, err
	}
	have := map[string]*model.PluginConfig{}
	for _, r := range rows {
		have[r.PluginName] = r
	}

	knownPlugins := []string{
		model.PluginNameMetrics,
		model.PluginNameLogs,
		model.PluginNameTraces,
		model.PluginNameProfiles,
		model.PluginNameHostMetrics,
		model.PluginNameProcMetrics,
		model.PluginNameCustomMetrics,
		model.PluginNameDatabaseMetrics,
	}
	out := &WireSnapshot{EdgeID: edgeID, Configs: make(map[string]WireConfig, len(knownPlugins))}
	enabledNames := make([]string, 0, len(knownPlugins))
	for _, name := range knownPlugins {
		cfg := WireConfig{
			Endpoint: uc.resolver.Endpoint(ctx, name),
			Enabled:  pluginDefaultEnabled[name],
		}
		if r, ok := have[name]; ok {
			// Explicit row wins. This preserves opt-out: an operator
			// who turns hostmetrics off via the UI lands a row with
			// Enabled=false and the default does not override it.
			cfg.Enabled = r.Enabled
			cfg.Spec = decodeSpec(r.SpecJSON)
		}
		if cfg.Enabled {
			enabledNames = append(enabledNames, name)
		}
		out.Configs[name] = cfg
	}
	uc.log.Info("FetchForEdge",
		slog.Uint64("edge_id", edgeID),
		slog.Int("rows", len(rows)),
		slog.Int("configs_out", len(out.Configs)),
		slog.Any("enabled", enabledNames))
	return out, nil
}

// CountByPlugin proxies to the repo (UI Integrations cards).
func (uc *PluginConfigUC) CountByPlugin(ctx context.Context) (map[string]int64, error) {
	return uc.repo.CountByPlugin(ctx)
}

// notify fires the reload signal to the edge without blocking the
// caller. Errors are logged only — the edge's 60s safety net catches
// missed pushes anyway.
func (uc *PluginConfigUC) notify(ctx context.Context, edgeID uint64, plugin string) {
	if uc.notifier == nil {
		uc.log.Debug("notifier not wired; skipping push", slog.Uint64("edge_id", edgeID))
		return
	}
	if err := uc.notifier.NotifyPluginConfigsChanged(ctx, edgeID); err != nil {
		uc.log.Warn("plugin config reload push failed",
			slog.Uint64("edge_id", edgeID),
			slog.String("plugin", plugin),
			slog.Any("err", err))
	}
}

// WireSnapshot is what the edge sees on a get_plugin_configs RPC.
// Endpoint is server-derived; auth_user/auth_pass are filled in by the
// edge from its own access_key/secret_key (already in env), so secrets
// never traverse the wire on this RPC.
type WireSnapshot struct {
	EdgeID  uint64                `json:"edge_id"`
	Configs map[string]WireConfig `json:"configs"`
}

// WireConfig is one plugin's config as the edge sees it.
type WireConfig struct {
	Enabled  bool                   `json:"enabled"`
	Endpoint string                 `json:"endpoint,omitempty"`
	Spec     map[string]interface{} `json:"spec,omitempty"`
}

func decodeSpec(raw string) map[string]interface{} {
	if raw == "" {
		return nil
	}
	out := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
