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
