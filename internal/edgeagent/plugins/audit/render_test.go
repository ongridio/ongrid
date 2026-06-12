package audit

import (
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

func TestRender_Defaults(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled: true,
		EdgeID:  42,
		Spec:    map[string]interface{}{},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)

	for _, want := range []string{
		"module: file_integrity",
		"/etc",
		"/bin",
		"output.file:",
		"audit.jsonl",
		`device_id: "42"`,
		"source: auditbeat",
		"output.elasticsearch.enabled: false",
		"output.logstash.enabled: false",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered config missing %q\n--- full body ---\n%s", want, body)
		}
	}
}

func TestRender_CustomModules(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled: true,
		EdgeID:  1,
		Spec: map[string]interface{}{
			"modules":     []interface{}{"fim"},
			"fim_paths":   []interface{}{"/opt/myapp"},
			"output_file": "/var/log/audit.jsonl",
		},
	}
	out, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(out)

	if !strings.Contains(body, "/opt/myapp") {
		t.Errorf("missing custom fim path\n%s", body)
	}
	if !strings.Contains(body, "/var/log/audit.jsonl") {
		t.Errorf("missing custom output file\n%s", body)
	}
	// Only fim module, no system module
	if strings.Contains(body, "module: system") {
		t.Errorf("system module should not appear when only fim is requested\n%s", body)
	}
}
