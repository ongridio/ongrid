package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
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

// DatabaseMetricsSecretWriter writes a managed databasemetrics credential file
// on an edge. Implemented by the frontierbound client. The manager calls this
// during a UI save and then persists only the non-secret plugin spec.
type DatabaseMetricsSecretWriter interface {
	WriteDatabaseMetricsSecrets(ctx context.Context, edgeID uint64, reqs []tunnel.WriteDatabaseMetricsSecretRequest) error
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
	repo         PluginConfigRepo
	notifier     EdgeReloadNotifier
	secretWriter DatabaseMetricsSecretWriter
	resolver     EndpointResolver
	log          *slog.Logger
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

// SetDatabaseMetricsSecretWriter injects the edge-side credential writer once
// frontierbound is alive.
func (uc *PluginConfigUC) SetDatabaseMetricsSecretWriter(w DatabaseMetricsSecretWriter) {
	uc.secretWriter = w
}

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
		model.PluginNameGPUMetrics,
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
	var databaseSecretReqs []tunnel.WriteDatabaseMetricsSecretRequest
	var previous *model.PluginConfig
	switch plugin {
	case model.PluginNameCustomMetrics:
		if err := validateCustomMetricsSpec(in.Spec); err != nil {
			return nil, err
		}
	case model.PluginNameDatabaseMetrics:
		spec, secretReqs, err := uc.prepareDatabaseMetricsSpec(in.Spec)
		if err != nil {
			return nil, err
		}
		in.Spec = spec
		databaseSecretReqs = secretReqs
		previous, err = uc.repo.Get(ctx, edgeID, plugin)
		if errors.Is(err, errs.ErrNotFound) {
			previous = nil
		} else if err != nil {
			return nil, fmt.Errorf("load previous %s config: %w", plugin, err)
		}
		if previous != nil {
			databaseSecretReqs = append(databaseSecretReqs, databaseMetricsSecretDeleteRequests(decodeSpec(previous.SpecJSON), in.Spec)...)
		}
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
	if len(databaseSecretReqs) > 0 {
		if err := uc.writeDatabaseMetricsSecrets(ctx, edgeID, databaseSecretReqs); err != nil {
			if rollbackErr := uc.rollbackPluginConfig(ctx, edgeID, plugin, previous); rollbackErr != nil {
				return nil, errors.Join(err, fmt.Errorf("rollback plugin config: %w", rollbackErr))
			}
			return nil, err
		}
	}
	uc.notify(ctx, edgeID, plugin)
	// gpumetrics: sync GPU exporter URL into metrics plugin target_urls.
	// Best-effort — if it fails the subprocess still starts, just the
	// metrics pipeline won't scrape it.
	if plugin == model.PluginNameGPUMetrics {
		if in.Enabled {
			if err := uc.syncGPUTargetURL(ctx, edgeID, in.Spec); err != nil {
				uc.log.Warn("sync GPU target URL", slog.Any("err", err))
			}
		} else {
			if err := uc.removeGPUTargetURL(ctx, edgeID, in.Spec); err != nil {
				uc.log.Warn("remove GPU target URL", slog.Any("err", err))
			}
		}
	}
	return &PluginRow{PluginName: row.PluginName, Enabled: row.Enabled, Spec: decodeSpec(row.SpecJSON)}, nil
}

func (uc *PluginConfigUC) rollbackPluginConfig(ctx context.Context, edgeID uint64, plugin string, previous *model.PluginConfig) error {
	if previous != nil {
		_, err := uc.repo.Upsert(ctx, previous)
		return err
	}
	return uc.repo.Delete(ctx, edgeID, plugin)
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
		model.PluginNameGPUMetrics,
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

// gpuDefaultListenAddress matches the gpumetrics plugin's default
// (internal/edgeagent/plugins/gpumetrics DefaultListenAddress).
// Keep in sync — both sides must agree on the port.
const gpuDefaultListenAddress = ":9835"

// gpuTargetURL builds the scrape URL for the GPU exporter from spec.
func gpuTargetURL(spec map[string]interface{}) string {
	listen := gpuDefaultListenAddress
	if spec != nil {
		if v, ok := spec["listen_address"].(string); ok && v != "" {
			listen = v
		}
	}
	return "http://127.0.0.1" + listen + "/metrics"
}

// syncGPUTargetURL adds the GPU exporter URL to the metrics plugin's
// target_urls array. Idempotent — skips if the URL is already present.
func (uc *PluginConfigUC) syncGPUTargetURL(ctx context.Context, edgeID uint64, gpuSpec map[string]interface{}) error {
	url := gpuTargetURL(gpuSpec)
	metricsRow, err := uc.repo.Get(ctx, edgeID, model.PluginNameMetrics)
	if err != nil && !errors.Is(err, errs.ErrNotFound) {
		return fmt.Errorf("get metrics config: %w", err)
	}
	var spec map[string]interface{}
	if metricsRow != nil {
		spec = decodeSpec(metricsRow.SpecJSON)
	}
	if spec == nil {
		spec = map[string]interface{}{}
	}
	urls := extractStringSlice(spec["target_urls"])
	for _, u := range urls {
		if u == url {
			return nil // already present
		}
	}
	urls = append(urls, url)
	spec["target_urls"] = toInterfaceSlice(urls)
	blob, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal metrics spec: %w", err)
	}
	if metricsRow == nil {
		metricsRow = &model.PluginConfig{
			EdgeID:     edgeID,
			PluginName: model.PluginNameMetrics,
			Enabled:    true,
			SpecJSON:   string(blob),
		}
	} else {
		metricsRow.SpecJSON = string(blob)
	}
	_, err = uc.repo.Upsert(ctx, metricsRow)
	if err != nil {
		return fmt.Errorf("upsert metrics config: %w", err)
	}
	uc.notify(ctx, edgeID, model.PluginNameMetrics)
	return nil
}

// removeGPUTargetURL removes the GPU exporter URL from the metrics
// plugin's target_urls array.
func (uc *PluginConfigUC) removeGPUTargetURL(ctx context.Context, edgeID uint64, gpuSpec map[string]interface{}) error {
	url := gpuTargetURL(gpuSpec)
	metricsRow, err := uc.repo.Get(ctx, edgeID, model.PluginNameMetrics)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return nil // no metrics config to update
		}
		return fmt.Errorf("get metrics config: %w", err)
	}
	spec := decodeSpec(metricsRow.SpecJSON)
	if spec == nil {
		return nil
	}
	urls := extractStringSlice(spec["target_urls"])
	filtered := make([]string, 0, len(urls))
	for _, u := range urls {
		if u != url {
			filtered = append(filtered, u)
		}
	}
	if len(filtered) == len(urls) {
		return nil // URL not present, nothing to remove
	}
	if len(filtered) > 0 {
		spec["target_urls"] = toInterfaceSlice(filtered)
	} else {
		delete(spec, "target_urls")
	}
	blob, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal metrics spec: %w", err)
	}
	metricsRow.SpecJSON = string(blob)
	_, err = uc.repo.Upsert(ctx, metricsRow)
	if err != nil {
		return fmt.Errorf("upsert metrics config: %w", err)
	}
	uc.notify(ctx, edgeID, model.PluginNameMetrics)
	return nil
}

// extractStringSlice coerces an interface{} to []string. JSON unmarshals
// into []interface{} so we convert element-wise.
func extractStringSlice(raw interface{}) []string {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// toInterfaceSlice converts []string to []interface{} for JSON marshaling.
func toInterfaceSlice(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
