package autoapm

import "testing"

func TestSelectiveContract(t *testing.T) {
	if s, err := Parse(nil); err != nil || len(s.Targets) != 0 {
		t.Fatalf("empty selection must remain empty: %v %v", s, err)
	}
	valid := func() map[string]interface{} {
		return map[string]interface{}{"targets": []interface{}{map[string]interface{}{"executable": "/opt/orders", "port": 8080, "service_name": "orders"}}}
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]interface{})
	}{
		{"unknown mode", func(m map[string]interface{}) { m["mode"] = "auto" }},
		{"unknown exporter", func(m map[string]interface{}) { m["endpoint"] = "https://other" }},
		{"environment substitution", func(m map[string]interface{}) { m["environment"] = "${SECRET}" }},
		{"ratio", func(m map[string]interface{}) { m["sample_ratio"] = 1.1 }},
		{"missing selector", func(m map[string]interface{}) {
			m["targets"] = []interface{}{map[string]interface{}{"service_name": "orders"}}
		}},
		{"port overflow", func(m map[string]interface{}) {
			m["targets"].([]interface{})[0].(map[string]interface{})["port"] = 65536
		}},
		{"self instrumentation", func(m map[string]interface{}) {
			m["targets"].([]interface{})[0].(map[string]interface{})["executable"] = "/usr/bin/ongrid-edge"
		}},
		{"duplicate", func(m map[string]interface{}) { ts := m["targets"].([]interface{}); m["targets"] = append(ts, ts[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := valid()
			tc.edit(m)
			if _, err := Parse(m); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	if s, err := Parse(valid()); err != nil || s.Ratio() != 0.1 {
		t.Fatalf("valid: %v %v", s, err)
	}
}

func TestEnvironmentOverridesAndSystemProcessExclusion(t *testing.T) {
	for _, exe := range []string{"/usr/lib/systemd/systemd", "/usr/lib/systemd/systemd-resolved"} {
		if !Excluded(exe) {
			t.Fatalf("system process allowed: %s", exe)
		}
		if _, err := Parse(map[string]interface{}{"targets": []Target{{Executable: exe, Port: 22, ServiceName: "system"}}}); err == nil {
			t.Fatal("manual system target accepted")
		}
	}
	for _, exe := range []string{"/usr/bin/nginx", "/opt/orders", "/usr/bin/java", "/usr/bin/python3"} {
		if Excluded(exe) {
			t.Fatalf("business process excluded: %s", exe)
		}
	}
	targets := []Target{{Executable: "/opt/orders", Port: 8080, ServiceName: "orders", Environment: "test"}, {Executable: "/opt/orders", Port: 8081, ServiceName: "orders", ServiceNamespace: "prod", Environment: "production"}}
	if _, err := Parse(map[string]interface{}{"targets": targets}); err != nil {
		t.Fatal(err)
	}
	targets[1].ServiceNamespace = ""
	if _, err := Parse(map[string]interface{}{"targets": targets}); err == nil {
		t.Fatal("ambiguous identity environments accepted")
	}
	targets[0].Environment = "${SECRET}"
	if _, err := Parse(map[string]interface{}{"targets": targets[:1]}); err == nil {
		t.Fatal("environment substitution accepted")
	}
}

func TestKubernetesSelectorsAreBoundedAndUnambiguous(t *testing.T) {
	valid := Spec{Kubernetes: &Kubernetes{Rules: []KubernetesRule{{Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "api.v1", Container: "app"}}}}
	if parsed, err := Parse(valid.Map()); err != nil || parsed.Kubernetes.Rules[0].Container != "" {
		t.Fatalf("legacy container restriction retained: %+v %v", parsed, err)
	}
	for _, rules := range [][]KubernetesRule{
		{{Namespace: ".*"}},
		{{Namespace: "shop", WorkloadKind: "Pod", WorkloadName: "api"}},
		{{Namespace: "shop", WorkloadName: "api"}},
		{{Namespace: "shop"}, {Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "api"}},
		{{Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "api"}, {Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "api", Container: "app"}},
	} {
		spec := Spec{Kubernetes: &Kubernetes{Rules: rules}}
		if _, err := Parse(spec.Map()); err == nil {
			t.Fatalf("accepted unsafe selectors: %+v", rules)
		}
	}
	valid.Targets = []Target{{Executable: "/opt/app", Port: 8080, ServiceName: "app"}}
	if _, err := Parse(valid.Map()); err == nil {
		t.Fatal("mixed host and Kubernetes targets")
	}
	if _, err := Parse(map[string]interface{}{"kubernetes": map[string]interface{}{"rules": []interface{}{map[string]interface{}{"namespace": "shop", "service_namespace": "fake"}}}}); err == nil {
		t.Fatal("namespace override accepted")
	}
}

func TestServiceLogPaths(t *testing.T) {
	target := Target{Executable: "/opt/orders", Port: 8080, ServiceName: "orders"}
	for _, pattern := range []string{"/var/log/orders/*.log", "/opt/orders/logs/app.log", ""} {
		target.LogPath = pattern
		if _, err := Parse(Spec{Targets: []Target{target}}.Map()); err != nil {
			t.Fatal(err)
		}
	}
	for _, pattern := range []string{"relative.log", "/var/log/../secret", "/var/log/${SECRET}", "/var/log/a\n", " /var/log/a"} {
		target.LogPath = pattern
		if _, err := Parse(Spec{Targets: []Target{target}}.Map()); err == nil {
			t.Fatalf("accepted %q", pattern)
		}
	}
	target.LogPath = "/var/log/app.log"
	other := target
	other.Port, other.ServiceName = 8081, "inventory"
	if _, err := Parse(Spec{Targets: []Target{target, other}}.Map()); err == nil {
		t.Fatal("ambiguous log identity accepted")
	}
}
