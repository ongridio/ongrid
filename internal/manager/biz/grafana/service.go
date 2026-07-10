// Package grafana orchestrates the manager-side Grafana auto-config flow
// (PR-2). The biz layer bridges three things:
//
//  1. system_settings.{category=grafana} for the operator-supplied root
//     URL + service-account token
//  2. system_settings.{category=prom}.query_url so the auto-created
//     datasource points at the same TSDB the manager is writing to
//  3. embedded dashboard JSON shipped inside the manager binary
//
// Two ops are exposed:
//   - Test: build a Client from settings, hit /api/health
//   - Sync: ensure the ongrid folder, upsert the prom datasource, push
//     every embedded dashboard with overwrite=true
package grafana

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	settingbiz "github.com/ongridio/ongrid/internal/manager/biz/setting"
	monitormodel "github.com/ongridio/ongrid/internal/manager/model/monitor"
	settingmodel "github.com/ongridio/ongrid/internal/manager/model/setting"
	pkggrafana "github.com/ongridio/ongrid/internal/pkg/grafana"
)

// Identifiers we hand to the user's Grafana. Keep these stable; they're
// embedded in dashboard JSON ($datasource UID references) and re-sync uses
// them as keys for "is this already there?". Renaming them would create
// duplicates instead of replacing.
const (
	folderUID      = "ongrid"
	folderTitle    = "ongrid"
	datasourceUID  = promDatasourceUID
	datasourceName = promDatasourceName

	promDatasourceUID  = "ongrid-prometheus"
	promDatasourceName = "ongrid-prometheus"
	lokiDatasourceUID  = "ongrid-loki"
	lokiDatasourceName = "ongrid-loki"
)

//go:embed dashboards/*.json
var dashboardsFS embed.FS

// Service is the biz-layer orchestrator. svc must be non-nil; log may be.
type Service struct {
	settings           *settingbiz.Service
	log                *slog.Logger
	tlsInsecure        bool   // skip cert verify when calling Grafana
	panelDashboardUID  string // Monitor-page mirror dashboard uid (HLD-monitor-panels)
	panelDashboardName string // human title shown in Grafana
}

// New builds the service. tlsInsecure mirrors cfg.Grafana.TLSInsecure —
// turn it on when the operator points at an external Grafana with a
// self-signed cert (matches the pattern PromConfig.TLSInsecure already uses).
func New(settings *settingbiz.Service, tlsInsecure bool, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		settings:           settings,
		log:                log,
		tlsInsecure:        tlsInsecure,
		panelDashboardUID:  "ongrid-monitor",
		panelDashboardName: "ongrid Monitor (managed)",
	}
}

// SetPanelDashboardUID overrides the Monitor mirror dashboard uid. cmd/
// ongrid wires this from ONGRID_GRAFANA_PANEL_DASHBOARD_UID at startup.
// Empty input leaves the default in place.
func (s *Service) SetPanelDashboardUID(uid string) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return
	}
	s.panelDashboardUID = uid
}

// httpClient builds the *http.Client used by both bootstrap and the
// Test/Sync paths. Single source of truth for TLS handling on the
// Grafana side; pkg/grafana.New(c.baseURL, c.token, hc) accepts it.
func (s *Service) httpClient() *http.Client {
	if !s.tlsInsecure {
		return nil // pkg/grafana uses a 15s default
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: true,
			},
		},
	}
}

// BootstrapEmbedded is a one-shot startup hook that auto-creates an
// ongrid SA + token in the embedded Grafana so the operator doesn't
// have to log into Grafana to wire up the integration on first run.
//
// Skip conditions (any one short-circuits to nil — log only):
//   - settings.grafana.sa_token already non-empty (already bootstrapped)
//   - adminUser / adminPassword empty (operator pointing at external Grafana
//     where we don't know the admin creds)
//   - Grafana root URL empty
//
// The Grafana SA name and token name are well-known so re-runs don't
// duplicate: find-or-create by name. If a previous bootstrap minted a
// token but it got lost, we mint a new one — Grafana keeps both, and
// rotation is the operator's job (UI provides the field).
func (s *Service) BootstrapEmbedded(ctx context.Context, adminUser, adminPassword string) {
	if s.settings == nil {
		return
	}
	existing, _, _ := s.settings.Get(ctx, settingmodel.CategoryGrafana, settingmodel.KeyGrafanaSAToken)
	if strings.TrimSpace(existing) != "" {
		s.log.Debug("grafana bootstrap skipped: token already set")
		return
	}
	if adminUser == "" || adminPassword == "" {
		s.log.Info("grafana bootstrap skipped: admin creds not provided (external Grafana?)")
		return
	}
	rootURL, _, _ := s.settings.Get(ctx, settingmodel.CategoryGrafana, settingmodel.KeyGrafanaRootURL)
	rootURL = strings.TrimSpace(rootURL)
	if rootURL == "" {
		s.log.Info("grafana bootstrap skipped: root_url empty")
		return
	}

	c := pkggrafana.NewWithBasicAuth(rootURL, adminUser, adminPassword, s.httpClient())
	if err := c.Health(ctx); err != nil {
		// Grafana not up yet, or admin creds wrong, or DNS fails. Don't
		// crash startup — operator can still configure manually via UI.
		s.log.Warn("grafana bootstrap: health check failed; skipping", slog.Any("err", err))
		return
	}

	const saName = "ongrid"
	sa, err := c.FindServiceAccountByName(ctx, saName)
	if err != nil {
		s.log.Warn("grafana bootstrap: SA search failed; skipping", slog.Any("err", err))
		return
	}
	if sa == nil {
		sa, err = c.CreateServiceAccount(ctx, saName, "Admin")
		if err != nil {
			s.log.Warn("grafana bootstrap: SA create failed; skipping", slog.Any("err", err))
			return
		}
	}
	token, err := c.CreateServiceAccountToken(ctx, sa.ID, "ongrid-bootstrap")
	if err != nil {
		s.log.Warn("grafana bootstrap: token create failed; skipping", slog.Any("err", err))
		return
	}
	if err := s.settings.Set(ctx, settingmodel.CategoryGrafana, settingmodel.KeyGrafanaSAToken, token, true); err != nil {
		s.log.Error("grafana bootstrap: persist token failed", slog.Any("err", err))
		return
	}
	s.log.Info("grafana bootstrap done", slog.String("sa", saName), slog.Int64("sa_id", sa.ID))
}

// SyncResult summarises what Sync did. The wire-shape mirrors this struct
// (server/integration/http.go).
type SyncResult struct {
	Folder      string   `json:"folder"`
	Datasource  string   `json:"datasource"`
	Datasources []string `json:"datasources,omitempty"`
	Dashboards  []string `json:"dashboards"` // titles synced
}

// Test reads root_url + sa_token from system_settings, builds a client,
// and calls /api/health. Returns an error with a human-readable cause on
// any of the obvious failure modes (missing config, network, auth, db).
func (s *Service) Test(ctx context.Context) error {
	c, err := s.client(ctx)
	if err != nil {
		return err
	}
	return c.Health(ctx)
}

// Sync runs the full bootstrap flow.
func (s *Service) Sync(ctx context.Context) (*SyncResult, error) {
	c, err := s.client(ctx)
	if err != nil {
		return nil, err
	}

	// We re-read prom.query_url here rather than caching it — operators
	// often configure prom + grafana in the same sitting, and the
	// query_url they typed seconds ago is what they expect to land in
	// the datasource.
	promURL, _, _ := s.settings.Get(ctx, settingmodel.CategoryProm, settingmodel.KeyPromQueryURL)
	promURL = strings.TrimSpace(promURL)
	if promURL == "" {
		return nil, errors.New("grafana: cannot sync — prom.query_url is empty (configure Prometheus first)")
	}

	if err := c.EnsureFolder(ctx, folderUID, folderTitle); err != nil {
		return nil, fmt.Errorf("ensure folder: %w", err)
	}

	// Forward the ongrid-side prom credentials into Grafana's encrypted
	// secureJsonData so the user's external Grafana can actually query
	// the same TSDB ongrid is writing to. Bearer wins over Basic; if
	// neither is set, datasource is anonymous.
	bearer, _, _ := s.settings.Get(ctx, settingmodel.CategoryProm, settingmodel.KeyPromBearerToken)
	basicUser, _, _ := s.settings.Get(ctx, settingmodel.CategoryProm, settingmodel.KeyPromBasicUser)
	basicPass, _, _ := s.settings.Get(ctx, settingmodel.CategoryProm, settingmodel.KeyPromBasicPassword)

	ds := pkggrafana.Datasource{
		UID:    promDatasourceUID,
		Name:   promDatasourceName,
		Type:   "prometheus",
		URL:    promURL,
		Access: "proxy",
		JSONData: map[string]any{
			"httpMethod":   "POST",
			"timeInterval": "30s",
		},
	}
	if bearer = strings.TrimSpace(bearer); bearer != "" {
		ds.JSONData["httpHeaderName1"] = "Authorization"
		ds.SecureJSONData = map[string]string{"httpHeaderValue1": "Bearer " + bearer}
	} else if basicUser = strings.TrimSpace(basicUser); basicUser != "" {
		ds.BasicAuth = true
		ds.BasicAuthUser = basicUser
		ds.SecureJSONData = map[string]string{"basicAuthPassword": basicPass}
	}
	if err := c.UpsertDatasource(ctx, ds); err != nil {
		return nil, fmt.Errorf("upsert prometheus datasource: %w", err)
	}

	datasources := []string{promDatasourceName}
	if lokiDS := s.lokiDatasource(ctx); lokiDS != nil {
		if err := c.UpsertDatasource(ctx, *lokiDS); err != nil {
			return nil, fmt.Errorf("upsert loki datasource: %w", err)
		}
		datasources = append(datasources, lokiDatasourceName)
	}

	titles, err := s.pushDashboards(ctx, c)
	if err != nil {
		return nil, err
	}

	res := &SyncResult{
		Folder:      folderTitle,
		Datasource:  promDatasourceName,
		Datasources: datasources,
		Dashboards:  titles,
	}
	s.log.Info("grafana sync done",
		slog.String("folder", res.Folder),
		slog.Any("datasources", res.Datasources),
		slog.Int("dashboards", len(res.Dashboards)),
	)
	return res, nil
}

func (s *Service) lokiDatasource(ctx context.Context) *pkggrafana.Datasource {
	lokiURL, _, _ := s.settings.Get(ctx, settingmodel.CategoryLoki, settingmodel.KeyLokiURL)
	lokiURL = strings.TrimRight(strings.TrimSpace(lokiURL), "/")
	if lokiURL == "" {
		return nil
	}
	ds := &pkggrafana.Datasource{
		UID:    lokiDatasourceUID,
		Name:   lokiDatasourceName,
		Type:   "loki",
		URL:    lokiURL,
		Access: "proxy",
		JSONData: map[string]any{
			"timeout":  60,
			"maxLines": 5000,
		},
	}
	if tlsInsecure, _, _ := s.settings.Get(ctx, settingmodel.CategoryLoki, settingmodel.KeyLokiTLSInsecure); strings.EqualFold(strings.TrimSpace(tlsInsecure), "true") {
		ds.JSONData["tlsSkipVerify"] = true
	}
	basicUser, _, _ := s.settings.Get(ctx, settingmodel.CategoryLoki, settingmodel.KeyLokiBasicUser)
	basicPass, _, _ := s.settings.Get(ctx, settingmodel.CategoryLoki, settingmodel.KeyLokiBasicPassword)
	if basicUser = strings.TrimSpace(basicUser); basicUser != "" {
		ds.BasicAuth = true
		ds.BasicAuthUser = basicUser
		ds.SecureJSONData = map[string]string{"basicAuthPassword": basicPass}
	}
	return ds
}

// client builds a pkg/grafana.Client from settings; returns a friendly
// error if config is incomplete.
//
// Auth precedence: sa_token wins over api_key. The bootstrap path mints
// sa_token for the embedded Grafana so it's the "happy path"; api_key
// is the operator-pasted fallback for external Grafanas where the user
// doesn't have admin to mint a fresh SA. Either credential lands in the
// same Authorization: Bearer header — Grafana doesn't distinguish.
func (s *Service) client(ctx context.Context) (*pkggrafana.Client, error) {
	if s.settings == nil {
		return nil, errors.New("grafana: settings service not wired")
	}
	root, _, _ := s.settings.Get(ctx, settingmodel.CategoryGrafana, settingmodel.KeyGrafanaRootURL)
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("grafana: root_url is empty (configure under 设置 → 集成)")
	}
	saToken, _, _ := s.settings.Get(ctx, settingmodel.CategoryGrafana, settingmodel.KeyGrafanaSAToken)
	apiKey, _, _ := s.settings.Get(ctx, settingmodel.CategoryGrafana, settingmodel.KeyGrafanaAPIKey)
	token := strings.TrimSpace(saToken)
	if token == "" {
		token = strings.TrimSpace(apiKey)
	}
	if token == "" {
		return nil, errors.New("grafana: sa_token / api_key empty (create a Grafana service account and paste its token, or paste an api_key for external Grafana)")
	}
	return pkggrafana.New(root, token, s.httpClient()), nil
}

// FetchDashboardJSON returns the raw `{ dashboard, meta }` envelope for
// `uid`. Used by the SPA's PromQLPanel renderer — the manager is the
// only party with access to the Grafana credential, so the browser
// hands its dashboard request through here.
//
// Errors:
//   - errs.ErrInvalid when uid is empty
//   - errs.ErrNotFound when Grafana 404s (wrap pkggrafana.ErrDashboardNotFound)
//   - errs.ErrNotWiredYet when settings service or grafana config absent
//   - any other error is a transport / auth failure surfaced verbatim
func (s *Service) FetchDashboardJSON(ctx context.Context, uid string) ([]byte, error) {
	if strings.TrimSpace(uid) == "" {
		return nil, errors.New("grafana: uid is required")
	}
	c, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	body, err := c.FetchDashboard(ctx, uid)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// pushDashboards reads every JSON under embed dashboards/*.json and pushes
// it to the configured Grafana folder. Returns the list of titles synced
// for the response.
func (s *Service) pushDashboards(ctx context.Context, c *pkggrafana.Client) ([]string, error) {
	entries, err := dashboardsFS.ReadDir("dashboards")
	if err != nil {
		return nil, fmt.Errorf("read embedded dashboards: %w", err)
	}
	// Sort for deterministic order — makes test fixtures stable and gives
	// the operator a predictable progress sequence in logs.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	titles := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, rerr := dashboardsFS.ReadFile("dashboards/" + e.Name())
		if rerr != nil {
			return titles, fmt.Errorf("read %s: %w", e.Name(), rerr)
		}
		title := dashboardTitle(raw, e.Name())
		if perr := c.UpsertDashboard(ctx, raw, folderUID, true); perr != nil {
			return titles, fmt.Errorf("push %s: %w", e.Name(), perr)
		}
		titles = append(titles, title)
		s.log.Info("grafana dashboard pushed", slog.String("title", title))
	}
	return titles, nil
}

// dashboardTitle pulls the human title out of a dashboard JSON for the
// response payload. Falls back to the filename without extension.
func dashboardTitle(raw []byte, fallback string) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		if t, ok := m["title"].(string); ok && t != "" {
			return t
		}
	}
	return strings.TrimSuffix(fallback, ".json")
}

// SyncMonitorPanels mirrors the user-managed Monitor page panel list
// into a single ongrid-managed Grafana dashboard. The dashboard's uid
// is fixed (panelDashboardUID, default "ongrid-monitor") so re-pushes
// always overwrite the same row instead of cluttering Grafana with
// dashboards-per-edit.
//
// One-way: any edits made directly in Grafana to this dashboard get
// overwritten on the next push. The biz/monitor layer is the source of
// truth.
//
// Failures (Grafana down, auth missing, network) bubble up so the
// caller (biz/monitor.Service.kickSync) can record them on
// last_sync_error. We do NOT retry here; the next user edit re-triggers
// a sync, and a manual "重新同步" button can be added later if needed.
func (s *Service) SyncMonitorPanels(ctx context.Context, panels []*monitormodel.Panel) error {
	c, err := s.client(ctx)
	if err != nil {
		return err
	}
	// Folder is best-effort — if it already exists, EnsureFolder is a
	// no-op. If creation fails (e.g. Grafana read-only), surface it so
	// the operator sees a real error.
	if err := c.EnsureFolder(ctx, folderUID, folderTitle); err != nil {
		return fmt.Errorf("ensure folder: %w", err)
	}
	// Prepend the hardcoded core panels the SPA's Monitor page always
	// renders so "open in Grafana" shows the SAME panels as the platform —
	// the user-managed DB panels alone left the core fleet panels (CPU /
	// mem / Top-8 procs / load / disk IO / conntrack / TCP) missing from
	// Grafana. core first, then user panels; the index-based layout in
	// buildMonitorDashboardJSON positions them in order.
	all := append(coreMonitorPanels(), panels...)
	dash := buildMonitorDashboardJSON(s.panelDashboardUID, s.panelDashboardName, all)
	raw, err := json.Marshal(dash)
	if err != nil {
		return fmt.Errorf("marshal monitor dashboard: %w", err)
	}
	if err := c.UpsertDashboard(ctx, raw, folderUID, true); err != nil {
		return fmt.Errorf("push monitor dashboard: %w", err)
	}
	return nil
}

// coreMonitorPanels re-declares the hardcoded default panels the SPA's
// Monitor page always renders (web/src/pages/Monitor.tsx buildMonitorPanels).
// They live in the frontend for instant first-paint without a Grafana round
// trip, but the operator expects "在 Grafana 中打开" to show the SAME panels —
// so we mirror them here and prepend to the user-managed list when building
// the ongrid-monitor dashboard. $__rate_interval resolves natively in
// Grafana. IDs are offset to 9000+ so they never collide with the
// auto-increment monitor_panels row ids. KEEP IN LOCKSTEP WITH Monitor.tsx.
func coreMonitorPanels() []*monitormodel.Panel {
	const ts = monitormodel.PanelTypeTimeseries
	return []*monitormodel.Panel{
		{ID: 9001, Type: ts, Title: "CPU 使用率", Unit: "percent",
			PromQL: `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[$__rate_interval])))`},
		{ID: 9002, Type: ts, Title: "内存使用率", Unit: "percent",
			PromQL: `100 * (1 - (sum by (device_id) (node_memory_MemAvailable_bytes{}) / sum by (device_id) (node_memory_MemTotal_bytes{})))`},
		{ID: 9003, Type: ts, Title: "磁盘使用率（按物理设备）", Unit: "percent",
			PromQL: `100 * max by (device_id, device) ((node_filesystem_size_bytes{fstype=~"ext4|xfs|btrfs|zfs|ext3|ext2|f2fs",device=~"(/dev/)?(vd|sd|xvd)[a-z]+[0-9]*|(/dev/)?nvme[0-9]+n[0-9]+(p[0-9]+)?"} - node_filesystem_avail_bytes{fstype=~"ext4|xfs|btrfs|zfs|ext3|ext2|f2fs",device=~"(/dev/)?(vd|sd|xvd)[a-z]+[0-9]*|(/dev/)?nvme[0-9]+n[0-9]+(p[0-9]+)?"}) / node_filesystem_size_bytes{fstype=~"ext4|xfs|btrfs|zfs|ext3|ext2|f2fs",device=~"(/dev/)?(vd|sd|xvd)[a-z]+[0-9]*|(/dev/)?nvme[0-9]+n[0-9]+(p[0-9]+)?"})`},
		{ID: 9004, Type: ts, Title: "网络吞吐（接收 + 发送）", Unit: "Bps",
			PromQL: `sum by (device_id) (rate(node_network_receive_bytes_total{device!~"lo|veth.*|docker.*"}[$__rate_interval])) + sum by (device_id) (rate(node_network_transmit_bytes_total{device!~"lo|veth.*|docker.*"}[$__rate_interval]))`},
		{ID: 9005, Type: ts, Title: "Top 8 进程 · CPU", Unit: "short",
			PromQL: `topk(8, avg by (groupname) (rate(namedprocess_namegroup_cpu_seconds_total{}[$__rate_interval])))`},
		{ID: 9006, Type: ts, Title: "Top 8 进程 · 内存", Unit: "bytes",
			PromQL: `topk(8, avg by (groupname) (namedprocess_namegroup_memory_bytes{memtype="resident"}))`},
		{ID: 9007, Type: ts, Title: "负载饱和度（load1 ÷ 核数）", Unit: "short",
			PromQL: `avg by (device_id) (node_load1{}) / on (device_id) group_left count by (device_id) (node_cpu_seconds_total{mode="idle"})`},
		{ID: 9008, Type: ts, Title: "磁盘 I/O 吞吐（读 + 写）", Unit: "Bps",
			PromQL: `sum by (device_id) (rate(node_disk_read_bytes_total{}[$__rate_interval])) + sum by (device_id) (rate(node_disk_written_bytes_total{}[$__rate_interval]))`},
		{ID: 9009, Type: ts, Title: "conntrack 利用率", Unit: "percent",
			PromQL: `100 * node_nf_conntrack_entries{} / node_nf_conntrack_entries_limit{}`},
		{ID: 9010, Type: ts, Title: "TCP 连接数", Unit: "short",
			PromQL: `sum by (device_id) (node_netstat_Tcp_CurrEstab{})`},
	}
}

// buildMonitorDashboardJSON renders the user-managed panel list into a
// minimal Grafana dashboard JSON. We model only the fields Grafana
// requires plus the few that affect rendering — Grafana fills the rest
// with sensible defaults on import.
//
// Layout: 2-column 12-col-wide grid (matches Monitor.tsx's PanelGrid),
// each panel 12 wide × 8 high, ordinal driving the row order.
func buildMonitorDashboardJSON(uid, title string, panels []*monitormodel.Panel) map[string]any {
	gPanels := make([]map[string]any, 0, len(panels))
	for i, p := range panels {
		col := (i % 2) * 12
		row := (i / 2) * 8
		gPanels = append(gPanels, map[string]any{
			"id":    p.ID,
			"type":  mapPanelType(p.Type),
			"title": p.Title,
			"datasource": map[string]any{
				"type": "prometheus",
				"uid":  datasourceUID,
			},
			"gridPos": map[string]any{
				"x": col, "y": row, "w": 12, "h": 8,
			},
			"fieldConfig": map[string]any{
				"defaults": map[string]any{
					"unit": p.Unit,
				},
				"overrides": []any{},
			},
			"targets": []map[string]any{
				{
					"refId":        "A",
					"expr":         p.PromQL,
					"legendFormat": p.Legend,
					"datasource": map[string]any{
						"type": "prometheus",
						"uid":  datasourceUID,
					},
				},
			},
		})
	}
	return map[string]any{
		"uid":           uid,
		"title":         title,
		"description":   "Auto-managed by ongrid. Edits in Grafana will be overwritten — change panels in the ongrid Monitor page.",
		"tags":          []string{"ongrid", "managed"},
		"editable":      true,
		"timezone":      "",
		"schemaVersion": 38,
		"panels":        gPanels,
		"time": map[string]any{
			"from": "now-1h",
			"to":   "now",
		},
	}
}

// mapPanelType normalises ongrid's panel type into Grafana's. Today
// they're 1:1, but we keep the indirection so future ongrid types
// (e.g. "table") can map onto a Grafana variant or fall back to
// timeseries.
func mapPanelType(t string) string {
	switch t {
	case monitormodel.PanelTypeStat:
		return "stat"
	case monitormodel.PanelTypeGauge:
		return "gauge"
	case monitormodel.PanelTypeTimeseries:
		return "timeseries"
	}
	return "timeseries"
}
