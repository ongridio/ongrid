package plugins

import (
	"context"
	"encoding/json"
	"os"
	"strings"
)

// EnvConfigFetcher reads plugin configs from environment variables. This
// is the PR-C1 bootstrap fetcher — it lets us validate the plugin runtime
// end-to-end without first having to ship the manager-side `edge_plugin_configs`
// table + tunnel RPC. PR-C2 will introduce TunnelConfigFetcher and demote
// EnvConfigFetcher to a fallback / dev-mode path.
//
// Env layout (per plugin):
//
//	ONGRID_EDGE_PLUGIN_<NAME>_ENABLED    = true|false
//	ONGRID_EDGE_PLUGIN_<NAME>_ENDPOINT   = https://manager.example.com/loki/api/v1/push
//	ONGRID_EDGE_PLUGIN_<NAME>_AUTH_USER  = <basic-auth user, typically = edge access key>
//	ONGRID_EDGE_PLUGIN_<NAME>_AUTH_PASS  = <basic-auth pass, typically = edge secret key; bearer token when AUTH_USER empty>
//	ONGRID_EDGE_PLUGIN_<NAME>_SPEC_JSON  = {"journald_units": ["sshd"], ...}
//
// Sensible default for AUTH_USER / AUTH_PASS: when unset, fall back to the
// edge's tunnel ACCESS_KEY / SECRET_KEY env vars (already set by install
// scripts). This means operators only have to set ENABLED + ENDPOINT for
// the common case of "data plane reuses tunnel credentials".
//
// Plus a global host-device identifier (baked into labels). The legacy
// ONGRID_EDGE_ID fallback is retained for standalone/dev deployments only;
// tunnel-managed deployments receive the canonical device_id from Manager.
//
//	ONGRID_EDGE_DEVICE_ID = "42"
type EnvConfigFetcher struct {
	knownPlugins []string
}

// NewEnvConfigFetcher builds a fetcher that knows which plugin names to
// look for in env. Pass the set of plugin names registered with the
// Supervisor — env vars for unknown plugins are silently ignored.
func NewEnvConfigFetcher(knownPlugins []string) *EnvConfigFetcher {
	return &EnvConfigFetcher{knownPlugins: append([]string(nil), knownPlugins...)}
}

// Fetch returns the env-driven snapshot. Plugins absent from env are
// returned with Enabled=false (so the supervisor stops them if they were
// previously enabled).
func (e *EnvConfigFetcher) Fetch(_ context.Context) (map[string]PluginConfig, error) {
	out := make(map[string]PluginConfig, len(e.knownPlugins))
	deviceID := envUint("ONGRID_EDGE_DEVICE_ID")
	if deviceID == 0 {
		deviceID = envUint("ONGRID_EDGE_ID")
	}

	for _, name := range e.knownPlugins {
		prefix := "ONGRID_EDGE_PLUGIN_" + strings.ToUpper(name) + "_"
		cfg := PluginConfig{
			Enabled:  envBool(prefix + "ENABLED"),
			EdgeID:   deviceID,
			Endpoint: os.Getenv(prefix + "ENDPOINT"),
			AuthUser: firstNonEmpty(os.Getenv(prefix+"AUTH_USER"), os.Getenv("ONGRID_EDGE_ACCESS_KEY")),
			AuthPass: firstNonEmpty(os.Getenv(prefix+"AUTH_PASS"), os.Getenv("ONGRID_EDGE_SECRET_KEY")),
		}
		if rawSpec := os.Getenv(prefix + "SPEC_JSON"); rawSpec != "" {
			spec := map[string]interface{}{}
			if err := json.Unmarshal([]byte(rawSpec), &spec); err == nil {
				cfg.Spec = spec
			}
			// Bad JSON: silently ignore — operator will see plugin
			// "disabled" / lacking spec, which is the correct failure
			// mode (don't crash supervisor on env typo).
		}
		out[name] = cfg
	}
	return out, nil
}

func envBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func envUint(key string) uint64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0
	}
	var n uint64
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + uint64(r-'0')
	}
	return n
}
