package apm

import (
	"context"
	"fmt"
	"strconv"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// ServiceDeployments reads a complete current inventory independently of UI
// filters/pagination. Native resource metrics include idle services; spanmetrics
// provide the existing trace-only fallback. No historical dashboard window is
// used, so expired telemetry cannot indefinitely retain a deployment relation.
func (s *Service) ServiceDeployments(ctx context.Context, now time.Time) ([]model.ServiceDeployment, error) {
	// timestamp drops __name__; retain it until aggregation so count/sum and
	// runtime metrics sharing resource labels do not form duplicate labelsets.
	// Tempo keeps flushing idle counters: require recent changes or a newly
	// observed series, rather than interpreting each flush as a fresh span.
	const expression = `topk(5001, max by (service,service_namespace,deployment_environment_name,device_id) (
label_replace(timestamp(label_replace({service_name!="",__name__!~"traces_.*"}, "ongrid_metric", "$1", "__name__", "(.+)")), "service", "$1", "service_name", "(.+)")
or timestamp((traces_spanmetrics_calls_total{service!=""} > 0) and (
  (changes(traces_spanmetrics_calls_total{service!=""}[5m]) > 0)
  or (traces_spanmetrics_calls_total{service!=""} unless traces_spanmetrics_calls_total{service!=""} offset 5m)
))
))`
	series, err := s.instant(ctx, expression, now)
	if err != nil {
		return nil, err
	}
	if len(series) > 5000 {
		return nil, fmt.Errorf("%w: service topology exceeds 5000 deployments", errs.ErrBudgetExceeded)
	}
	out := make([]model.ServiceDeployment, 0, len(series))
	for _, item := range series {
		seen, err := sampleValue(item.Value)
		if err != nil || seen == nil {
			return nil, fmt.Errorf("%w: invalid service deployment timestamp", errs.ErrInvalid)
		}
		if *seen < float64(now.Add(-5*time.Minute).Unix()) {
			continue
		}
		var deviceID uint64
		if raw := item.Metric["device_id"]; raw != "" {
			deviceID, err = strconv.ParseUint(raw, 10, 64)
			if err != nil || deviceID == 0 {
				return nil, fmt.Errorf("%w: invalid service deployment device_id", errs.ErrInvalid)
			}
		}
		id := identityFromLabels(item.Metric)
		out = append(out, model.ServiceDeployment{ServiceName: id.ServiceName, ServiceNamespace: id.ServiceNamespace, Environment: id.Environment, DeviceID: deviceID})
	}
	return out, nil
}
