package apm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type clusterScopes struct{}

func (clusterScopes) ResolveClusterTelemetryScope(_ context.Context, id uint64) (string, []string, error) {
	switch id {
	case 101:
		return "", []string{"42", "43"}, nil
	case 102:
		return "7", nil, nil
	case 103:
		return "", []string{}, nil
	default:
		return "", nil, errs.ErrNotFound
	}
}

func TestUnifiedClusterScope(t *testing.T) {
	for _, tc := range []struct {
		id            uint64
		metric, trace string
	}{{101, `device_id=~"42|43"`, `resource.device_id =~ "^(42|43)$"`}, {102, `cluster_id="7"`, `resource.cluster_id = "7"`}, {103, `device_id=~"a^"`, `resource.device_id =~ "^(a^)$"`}} {
		prom := &fakeProm{result: `[]`}
		svc := New(prom, nil, nil).WithClusterScopes(clusterScopes{})
		q := testQuery()
		q.ClusterNodeID, q.DeviceID = tc.id, "42"
		result, err := svc.List(context.Background(), q, false)
		if err != nil {
			t.Fatal(err)
		}
		if result.Metadata.ResourceScope == nil || !strings.Contains(prom.expr, tc.metric) || !strings.Contains(prom.expr, `device_id="42"`) {
			t.Fatal(prom.expr)
		}
		if err := svc.validateQuery(context.Background(), &q, true); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(TraceQL(q), tc.trace) {
			t.Fatal(TraceQL(q))
		}
		q.MetricSource = "application_metrics"
		alert, err := svc.AlertTemplate(context.Background(), q, "error_rate", 5, 1, 0)
		if err != nil || !strings.Contains(alert.Expr, tc.metric) {
			t.Fatalf("alert: %v %v", alert, err)
		}
		if _, err := svc.Dependencies(context.Background(), q); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("scoped dependencies: %v", err)
		}
	}
	q := testQuery()
	q.ClusterNodeID = 404
	if _, err := New(&fakeProm{result: `[]`}, nil, nil).WithClusterScopes(clusterScopes{}).List(context.Background(), q, false); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("unknown cluster widened query: %v", err)
	}
}
