package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// TunnelConfigFetcher pulls plugin configs from the manager via the
// tunnel RPC `get_plugin_configs`. Auth credentials
// (basic-auth user/pass for the data plane endpoint) are NOT carried on
// the wire — the edge fills them in from its local env (the same
// access_key/secret_key it used to authenticate the tunnel).
//
// Composes with EnvConfigFetcher as a fallback: when the tunnel is
// unreachable (cold start before Dial completes, or transient outage),
// Fetch returns the env-driven snapshot so the supervisor can keep a
// reasonable default running.
type TunnelConfigFetcher struct {
	client       tunnel.Client
	knownPlugins []string
	fallback     *EnvConfigFetcher

	// Auth + edge_id materialised from env once at construction so
	// every Fetch doesn't re-read os.Getenv. ConfigFetcher contract
	// allows mutation of returned PluginConfigs each call so this is
	// safe: we copy values into each PluginConfig per Fetch call.
	authUser string
	authPass string
	edgeID   uint64

	k8sRole          string
	k8sMode          string
	k8sClusterID     uint64
	k8sNodeName      string
	k8sNamespace     string
	k8sTLSInsecure   bool
	k8sGateway       bool
	managerPublicURL string
}

// NewTunnelConfigFetcher builds a fetcher that fronts a tunnel.Client
// with an EnvConfigFetcher fallback for offline/early-boot cases.
//
// knownPlugins is the same slice passed to NewEnvConfigFetcher — used
// to filter the manager's response (defensive against the manager
// somehow returning configs for unknown plugin names) and to drive the
// fallback path.
func NewTunnelConfigFetcher(client tunnel.Client, knownPlugins []string) *TunnelConfigFetcher {
	return NewTunnelConfigFetcherWithCredentials(client, knownPlugins, "", "")
}

// NewTunnelConfigFetcherWithCredentials builds a fetcher and uses the
// provided edge credentials as the data-plane Basic auth fallback. This is
// needed for Kubernetes enrollment, where access/secret are returned at
// runtime rather than pre-populated in process env.
func NewTunnelConfigFetcherWithCredentials(client tunnel.Client, knownPlugins []string, accessKey, secretKey string) *TunnelConfigFetcher {
	return &TunnelConfigFetcher{
		client:           client,
		knownPlugins:     append([]string(nil), knownPlugins...),
		fallback:         NewEnvConfigFetcher(knownPlugins),
		authUser:         firstNonEmpty(os.Getenv("ONGRID_EDGE_PLUGIN_DATAPLANE_USER"), os.Getenv("ONGRID_EDGE_ACCESS_KEY"), accessKey),
		authPass:         firstNonEmpty(os.Getenv("ONGRID_EDGE_PLUGIN_DATAPLANE_PASS"), os.Getenv("ONGRID_EDGE_SECRET_KEY"), secretKey),
		edgeID:           envUint("ONGRID_EDGE_ID"),
		k8sRole:          os.Getenv("ONGRID_K8S_ROLE"),
		k8sMode:          os.Getenv("ONGRID_K8S_MODE"),
		k8sClusterID:     envUint("ONGRID_K8S_CLUSTER_ID"),
		k8sNodeName:      os.Getenv("ONGRID_K8S_NODE_NAME"),
		k8sNamespace:     os.Getenv("ONGRID_K8S_POD_NAMESPACE"),
		k8sTLSInsecure:   envBool("ONGRID_K8S_ENROLL_TLS_INSECURE"),
		k8sGateway:       envBool("ONGRID_K8S_TELEMETRY_GATEWAY_ENABLED"),
		managerPublicURL: os.Getenv("ONGRID_MANAGER_PUBLIC_URL"),
	}
}

// Fetch calls MethodGetPluginConfigs and converts the wire response into
// the supervisor's PluginConfig shape. On any RPC error it falls back to
// EnvConfigFetcher so a partition between edge and manager doesn't kill
// already-configured plugins.
func (t *TunnelConfigFetcher) Fetch(ctx context.Context) (map[string]PluginConfig, error) {
	if t.client == nil {
		envSnap, err := t.fallback.Fetch(ctx)
		if err != nil {
			return nil, err
		}
		return t.applyKubernetesDefaults(envSnap), nil
	}
	var resp tunnel.GetPluginConfigsResponse
	if err := t.client.Call(ctx, tunnel.MethodGetPluginConfigs, struct{}{}, &resp); err != nil {
		// Don't surface the error — supervisor would log "config fetch
		// failed; keeping previous state" and never recover until the
		// next reload. Falling back to env keeps things alive at the
		// cost of a stale snapshot during outages.
		envSnap, fallbackErr := t.fallback.Fetch(ctx)
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		return t.applyKubernetesDefaults(envSnap), nil
	}

	out := make(map[string]PluginConfig, len(t.knownPlugins))
	known := map[string]bool{}
	for _, n := range t.knownPlugins {
		known[n] = true
	}
	// Edge ID source of truth: env > tunnel response. Env wins because
	// the operator may run a single edge against multiple managers in
	// dev — the env's ID is what's baked into Loki labels regardless.
	edgeID := t.edgeID
	if edgeID == 0 {
		edgeID = resp.EdgeID
	}
	for name, entry := range resp.Configs {
		if !known[name] {
			continue
		}
		out[name] = t.withKubernetesDefaults(name, PluginConfig{
			Enabled:  entry.Enabled,
			EdgeID:   edgeID,
			Endpoint: entry.Endpoint,
			AuthUser: t.authUser,
			AuthPass: t.authPass,
			Spec:     entry.Spec,
		})
	}
	// Plugins manager didn't mention default to disabled; supervisor
	// stops them if they were running.
	for _, name := range t.knownPlugins {
		if _, ok := out[name]; !ok {
			out[name] = t.withKubernetesDefaults(name, PluginConfig{Enabled: false, EdgeID: edgeID})
		}
	}
	return out, nil
}

func (t *TunnelConfigFetcher) applyKubernetesDefaults(in map[string]PluginConfig) map[string]PluginConfig {
	out := make(map[string]PluginConfig, len(in))
	for name, cfg := range in {
		out[name] = t.withKubernetesDefaults(name, cfg)
	}
	return out
}

func (t *TunnelConfigFetcher) withKubernetesDefaults(name string, cfg PluginConfig) PluginConfig {
	if t.isKubernetesTelemetryGatewayAgent() {
		if name == "traces" {
			return t.withKubernetesGatewayTracesDefaults(cfg)
		}
		return cfg
	}
	if !t.isFullNodeKubernetesAgent() {
		return cfg
	}
	switch name {
	case "logs":
		return t.withKubernetesLogsDefaults(cfg)
	case "traces":
		return t.withKubernetesTracesDefaults(cfg)
	case "hostmetrics":
		return withKubernetesHostMetricsDefaults(cfg)
	case "procmetrics":
		return withKubernetesProcMetricsDefaults(cfg)
	default:
		return cfg
	}
}

func (t *TunnelConfigFetcher) withKubernetesLogsDefaults(cfg PluginConfig) PluginConfig {
	if t.managerPublicURL != "" && (cfg.Endpoint == "" || isLoopbackEndpoint(cfg.Endpoint)) {
		cfg.Endpoint = strings.TrimRight(t.managerPublicURL, "/") + "/loki/api/v1/push"
	}

	spec := copySpec(cfg.Spec)
	modeRaw, modeSet := spec["mode"]
	mode := strings.TrimSpace(fmt.Sprint(modeRaw))
	if modeSet && mode != "" && !strings.EqualFold(mode, "kubernetes") {
		cfg.Spec = spec
		return cfg
	}

	spec["mode"] = "kubernetes"
	if _, ok := spec["cluster_id"]; !ok && t.k8sClusterID != 0 {
		spec["cluster_id"] = fmt.Sprintf("%d", t.k8sClusterID)
	}
	if _, ok := spec["node_name"]; !ok && t.k8sNodeName != "" {
		spec["node_name"] = t.k8sNodeName
	}
	if _, ok := spec["pod_log_path"]; !ok {
		spec["pod_log_path"] = "/var/log/pods/*/*/*.log"
	}
	if _, ok := spec["enable_journald"]; !ok {
		spec["enable_journald"] = false
	}
	cfg.Spec = spec
	return cfg
}

func (t *TunnelConfigFetcher) withKubernetesTracesDefaults(cfg PluginConfig) PluginConfig {
	if t.managerPublicURL != "" && (cfg.Endpoint == "" || isLoopbackEndpoint(cfg.Endpoint)) {
		cfg.Endpoint = strings.TrimRight(t.managerPublicURL, "/") + "/v1/traces"
	}

	spec := copySpec(cfg.Spec)
	extraAttrs := copyStringMapSpec(spec["extra_attrs"])
	if extraAttrs == nil {
		extraAttrs = map[string]interface{}{}
	}
	if _, ok := extraAttrs["cluster_id"]; !ok && t.k8sClusterID != 0 {
		extraAttrs["cluster_id"] = fmt.Sprintf("%d", t.k8sClusterID)
	}
	if _, ok := extraAttrs["node_name"]; !ok && t.k8sNodeName != "" {
		extraAttrs["node_name"] = t.k8sNodeName
	}
	if len(extraAttrs) > 0 {
		spec["extra_attrs"] = extraAttrs
	}
	if _, ok := spec["tls_insecure_skip_verify"]; !ok && t.k8sTLSInsecure {
		spec["tls_insecure_skip_verify"] = true
	}
	cfg.Spec = spec
	return cfg
}

func (t *TunnelConfigFetcher) withKubernetesGatewayTracesDefaults(cfg PluginConfig) PluginConfig {
	if t.managerPublicURL != "" && (cfg.Endpoint == "" || isLoopbackEndpoint(cfg.Endpoint)) {
		cfg.Endpoint = strings.TrimRight(t.managerPublicURL, "/") + "/v1/traces"
	}

	spec := copySpec(cfg.Spec)
	if t.managerPublicURL != "" {
		raw, _ := spec["logs_endpoint"].(string)
		if strings.TrimSpace(raw) == "" || isLoopbackEndpoint(raw) {
			spec["logs_endpoint"] = strings.TrimRight(t.managerPublicURL, "/") + "/loki/api/v1/push"
		}
	}
	if _, ok := spec["grpc_endpoint"]; !ok {
		spec["grpc_endpoint"] = "0.0.0.0:4317"
	}
	if _, ok := spec["http_endpoint"]; !ok {
		spec["http_endpoint"] = "0.0.0.0:4318"
	}
	if _, ok := spec["omit_device_id"]; !ok {
		spec["omit_device_id"] = true
	}
	if _, ok := spec["enable_k8sattributes"]; !ok {
		spec["enable_k8sattributes"] = true
	}
	if _, ok := spec["enable_logs"]; !ok {
		spec["enable_logs"] = true
	}
	if _, ok := spec["enable_metrics"]; !ok {
		spec["enable_metrics"] = true
	}
	if _, ok := spec["metrics_export_endpoint"]; !ok {
		spec["metrics_export_endpoint"] = "127.0.0.1:9464"
	}
	extraAttrs := copyStringMapSpec(spec["extra_attrs"])
	if extraAttrs == nil {
		extraAttrs = map[string]interface{}{}
	}
	if _, ok := extraAttrs["cluster_id"]; !ok && t.k8sClusterID != 0 {
		extraAttrs["cluster_id"] = fmt.Sprintf("%d", t.k8sClusterID)
	}
	if _, ok := extraAttrs["telemetry_gateway"]; !ok {
		extraAttrs["telemetry_gateway"] = "kubernetes"
	}
	if _, ok := extraAttrs["gateway_namespace"]; !ok && t.k8sNamespace != "" {
		extraAttrs["gateway_namespace"] = t.k8sNamespace
	}
	if len(extraAttrs) > 0 {
		spec["extra_attrs"] = extraAttrs
	}
	if _, ok := spec["tls_insecure_skip_verify"]; !ok && t.k8sTLSInsecure {
		spec["tls_insecure_skip_verify"] = true
	}
	cfg.Spec = spec
	return cfg
}

func withKubernetesHostMetricsDefaults(cfg PluginConfig) PluginConfig {
	spec := copySpec(cfg.Spec)
	spec["extra_args"] = appendMissingCLIArgs(spec["extra_args"], []string{
		"--path.procfs=/proc",
		"--path.sysfs=/sys",
		"--path.rootfs=/",
		"--collector.filesystem.mount-points-exclude=^/(dev|proc|sys|run|var/lib/containerd/.+)($|/)",
	})
	cfg.Spec = spec
	return cfg
}

func withKubernetesProcMetricsDefaults(cfg PluginConfig) PluginConfig {
	spec := copySpec(cfg.Spec)
	if _, ok := spec["procfs"]; !ok {
		spec["procfs"] = "/proc"
	}
	cfg.Spec = spec
	return cfg
}

func (t *TunnelConfigFetcher) isFullNodeKubernetesAgent() bool {
	return strings.EqualFold(t.k8sRole, "node") && strings.EqualFold(t.k8sMode, "full-node")
}

func (t *TunnelConfigFetcher) isKubernetesTelemetryGatewayAgent() bool {
	if !t.k8sGateway {
		return false
	}
	return strings.EqualFold(t.k8sRole, "controller")
}

func copySpec(in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyStringMapSpec(raw interface{}) map[string]interface{} {
	switch v := raw.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			out[k] = val
		}
		return out
	case map[string]string:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			out[k] = val
		}
		return out
	default:
		return nil
	}
}

func isLoopbackEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.")
}

func appendMissingCLIArgs(raw interface{}, defaults []string) []interface{} {
	existing := stringValues(raw)
	out := make([]interface{}, 0, len(existing)+len(defaults))
	for _, arg := range existing {
		out = append(out, arg)
	}
	for _, def := range defaults {
		if hasCLIArg(existing, def) {
			continue
		}
		out = append(out, def)
		existing = append(existing, def)
	}
	return out
}

func stringValues(raw interface{}) []string {
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func hasCLIArg(args []string, candidate string) bool {
	prefix := candidate
	if idx := strings.IndexByte(candidate, '='); idx >= 0 {
		prefix = candidate[:idx]
	}
	for _, arg := range args {
		if arg == candidate || strings.HasPrefix(arg, prefix+"=") {
			return true
		}
	}
	return false
}

// MarshalJSON kept on the type for diagnostic dumping (currently unused
// but useful for `--dump-config` style flags).
func (t *TunnelConfigFetcher) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		KnownPlugins []string `json:"known_plugins"`
		EdgeID       uint64   `json:"edge_id"`
		HasClient    bool     `json:"has_client"`
	}{t.knownPlugins, t.edgeID, t.client != nil})
}

// AssertKnown is a tiny sanity helper for tests / debug tooling. Returns
// an error listing the unknown names, otherwise nil.
func (t *TunnelConfigFetcher) AssertKnown(names []string) error {
	known := map[string]bool{}
	for _, n := range t.knownPlugins {
		known[n] = true
	}
	var bad []string
	for _, n := range names {
		if !known[n] {
			bad = append(bad, n)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("unknown plugin names: %v", bad)
	}
	return nil
}
