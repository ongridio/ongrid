package logs

import (
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"gopkg.in/yaml.v3"
)

func TestRenderHappyPath(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   42,
		Endpoint: "https://manager.example.com/loki/api/v1/push",
		AuthUser: "ak-edge42",
		AuthPass: "sk-secret",
		Spec: map[string]interface{}{
			"file_paths":      []interface{}{"/var/log/syslog", "/var/log/auth.log"},
			"journald_units":  []interface{}{"sshd", "ongrid-edge"},
			"extra_labels":    map[string]interface{}{"service": "edge", "env": "test"},
			"enable_journald": true,
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	var parsed map[string]interface{}
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("rendered kubernetes config must be valid YAML: %v\n--- full body ---\n%s", err, body)
	}

	for _, want := range []string{
		"https://manager.example.com/loki/api/v1/push",
		"basic_auth:",
		"username: ak-edge42",
		"password: sk-secret",
		`device_id: "42"`,
		`service: "edge"`,
		`env: "test"`,
		"job_name: journald",
		"target_label:  'identifier'", // journald syslog_identifier → label for non-unit entries
		"job_name: 'file-var-log-syslog'",
		"job_name: 'file-var-log-auth-log'",
		"__path__:      '/var/log/syslog'",
		"__path__:      '/var/log/auth.log'",
		"ongrid-edge|sshd", // sorted lex; '-' isn't a regex meta so not escaped
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered config missing %q\n--- full body ---\n%s", want, body)
		}
	}
}

func TestRenderRejectsMissingEndpoint(t *testing.T) {
	cfg := plugins.PluginConfig{Enabled: true, EdgeID: 1}
	if _, err := render(cfg); err == nil {
		t.Errorf("render must reject missing endpoint")
	}
}

func TestRenderRejectsMissingEdgeID(t *testing.T) {
	cfg := plugins.PluginConfig{Enabled: true, Endpoint: "https://x/loki/api/v1/push"}
	if _, err := render(cfg); err == nil {
		t.Errorf("render must reject missing edge_id")
	}
}

func TestRenderEnableJournaldFalse(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://x/loki/api/v1/push",
		Spec: map[string]interface{}{
			"enable_journald": false,
			"file_paths":      []interface{}{"/var/log/x.log"},
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	if strings.Contains(body, "job_name: journald") {
		t.Errorf("journald block should be omitted when enable_journald=false:\n%s", body)
	}
	if !strings.Contains(body, "/var/log/x.log") {
		t.Errorf("file path missing from rendered config")
	}
}

// TestRenderSingleClient: with default spec, the rendered config has
// exactly one client[] entry pointing at cfg.Endpoint.
func TestRenderSingleClient(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://manager.example.com/loki/api/v1/push",
		AuthUser: "ak",
		AuthPass: "sk",
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)
	if strings.Count(body, "- url: ") != 1 {
		t.Errorf("expected exactly one clients[] entry, got body:\n%s", body)
	}
	if !strings.Contains(body, "https://manager.example.com/loki/api/v1/push") {
		t.Errorf("internal endpoint missing from rendered config")
	}
}

// TestRenderJournaldDefaultOn: journald is the default source. Default
// render (no enable_journald in spec) MUST emit the journald scrape
// block; explicit enable_journald=false removes it (the operator falls
// back to file/syslog tail).
func TestRenderJournaldDefaultOn(t *testing.T) {
	base := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   1,
		Endpoint: "https://x/loki/api/v1/push",
		Spec: map[string]interface{}{
			"file_paths": []interface{}{"/var/log/syslog"},
		},
	}
	out, err := render(base)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(out), "job_name: journald") {
		t.Errorf("journald should be on by default (unset spec):\n%s", string(out))
	}

	base.Spec["enable_journald"] = false
	out, err = render(base)
	if err != nil {
		t.Fatalf("render with journald off: %v", err)
	}
	if strings.Contains(string(out), "job_name: journald") {
		t.Errorf("enable_journald=false must remove the journald scrape block:\n%s", string(out))
	}
}

func TestRenderKubernetesMode(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   42,
		Endpoint: "https://manager.example.com/loki/api/v1/push",
		AuthUser: "ak-edge42",
		AuthPass: "sk-secret",
		Spec: map[string]interface{}{
			"mode":       "kubernetes",
			"cluster_id": float64(7),
			"node_name":  "kind-worker",
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)

	for _, want := range []string{
		`device_id: "42"`,
		`cluster_id: "7"`,
		`node: "kind-worker"`,
		"job_name: kubernetes-pods",
		"- cri: {}",
		`ongrid_source: "kubernetes:pod"`,
		`job: "kubernetes-pods"`,
		`__path__: "/var/log/pods/*/*/*.log"`,
		"source: filename",
		"namespace:",
		"pod:",
		"container:",
		`expression: '^/var/log/pods/(?P<namespace>[^_]+)_(?P<pod>[^_]+)_(?P<pod_uid>[^/]+)/(?P<container>[^/]+)/[^/]+\.log$'`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered config missing %q\n--- full body ---\n%s", want, body)
		}
	}
	for _, notWant := range []string{
		"job_name: journald",
		"file-var-log-syslog",
		"file-var-log-messages",
		"target_label: 'pod_uid'",
	} {
		if strings.Contains(body, notWant) {
			t.Errorf("rendered kubernetes config should not contain %q\n--- full body ---\n%s", notWant, body)
		}
	}
}

func TestRenderKubernetesModeRejectsMissingClusterID(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   42,
		Endpoint: "https://manager.example.com/loki/api/v1/push",
		Spec: map[string]interface{}{
			"mode": "kubernetes",
		},
	}
	if _, err := render(cfg); err == nil {
		t.Fatalf("render must reject mode=kubernetes without cluster_id")
	}
}

func TestJobNameSafe(t *testing.T) {
	cases := map[string]string{
		"/var/log/syslog":      "var-log-syslog",
		"/opt/app/log/app.log": "opt-app-log-app-log",
		"alpha_beta":           "alpha-beta",
	}
	for in, want := range cases {
		if got := jobNameSafe(in); got != want {
			t.Errorf("jobNameSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
