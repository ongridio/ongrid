package apm

import (
	"strings"
	"testing"
)

func TestServiceMapScopeAndIdentity(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		p := &fakeProm{result: `[]`}
		q := testQuery()
		q.ServiceName = ""
		if !scoped {
			q.Environment, q.ServiceNamespace = nil, nil
		}
		if _, err := New(p, nil, nil).Dependencies(t.Context(), q); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.expr, `client=""`) || strings.Contains(p.expr, `server=""`) {
			t.Fatal(p.expr)
		}
		for _, side := range []string{"client", "server"} {
			if strings.Contains(p.expr, side+`_deployment_environment_name="production"`) != scoped {
				t.Fatal(p.expr)
			}
			if strings.Contains(p.expr, side+`_service_namespace="trade"`) != scoped {
				t.Fatal(p.expr)
			}
		}
		q.ServiceName, q.Environment = "orders", nil
		if _, err := New(p, nil, nil).Dependencies(t.Context(), q); err == nil {
			t.Fatal("partial service identity accepted")
		}
	}
	q := testQuery()
	q.ServiceName, q.DeviceID = "", "42"
	if _, err := New(&fakeProm{result: `[]`}, nil, nil).Dependencies(t.Context(), q); err == nil {
		t.Fatal("unsupported device filter accepted")
	}
}
