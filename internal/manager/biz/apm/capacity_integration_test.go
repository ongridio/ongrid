package apm

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/promquery"
	"github.com/ongridio/ongrid/internal/pkg/promwrite"
)

// Bounded synthetic scale test through real Prometheus and the production APM
// query adapter. Run only on the isolated acceptance stack, never production.
func TestAPMCapacityIntegration(t *testing.T) {
	endpoint := os.Getenv("APM_TEST_PROMETHEUS")
	if os.Getenv("APM_TEST_LOAD") != "1" || endpoint == "" {
		t.Skip("set APM_TEST_LOAD=1 on the isolated acceptance stack")
	}
	ctx := t.Context()
	writer := promwrite.New(endpoint, nil)
	svc := New(promquery.New(endpoint, nil), nil, nil)
	end := time.Now().Add(-time.Minute).Truncate(time.Minute)
	ns := fmt.Sprintf("capacity-%d", time.Now().UnixNano())
	env := "acceptance"
	previous := 0
	for _, total := range []int{100, 500, 1000} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			batch := make([]promwrite.Sample, 0, 4000)
			flush := func() {
				t.Helper()
				if err := writer.Write(ctx, batch); err != nil {
					t.Fatal(err)
				}
				batch = batch[:0]
			}
			for service := previous; service < total; service++ {
				for _, protocol := range []string{"http", "rpc"} {
					name := "http_server_request_duration_seconds"
					if protocol == "rpc" {
						name = "rpc_server_call_duration_seconds"
					}
					for _, failed := range []bool{false, true} {
						status, weight := "200", 3.0
						if failed {
							status, weight = "500", 1
						}
						for bucket, fraction := range map[string]float64{"0.01": 0.75, "0.05": 0.9, "0.1": 0.95, "0.5": 0.99, "1": 1, "+Inf": 1, "count": 1} {
							labels := []promwrite.Label{{Name: "service_name", Value: fmt.Sprintf("service-%04d", service)}, {Name: "service_namespace", Value: ns}, {Name: "deployment_environment_name", Value: env}, {Name: "service_instance_id", Value: "instance-1"}}
							metric := name + "_bucket"
							if bucket == "count" {
								metric = name + "_count"
							} else {
								labels = append(labels, promwrite.Label{Name: "le", Value: bucket})
							}
							labels = append(labels, promwrite.Label{Name: "__name__", Value: metric})
							if protocol == "http" {
								labels = append(labels, promwrite.Label{Name: "http_request_method", Value: "GET"}, promwrite.Label{Name: "http_route", Value: "/orders/{id}"}, promwrite.Label{Name: "http_response_status_code", Value: status})
							} else {
								rpcStatus := "OK"
								if failed {
									rpcStatus = "NOT_FOUND"
								}
								labels = append(labels, promwrite.Label{Name: "rpc_system_name", Value: "grpc"}, promwrite.Label{Name: "rpc_method", Value: "Orders/Get"}, promwrite.Label{Name: "rpc_response_status_code", Value: rpcStatus})
							}
							sort.Slice(labels, func(i, j int) bool { return labels[i].Name < labels[j].Name })
							for point := 0; point <= 10; point++ {
								batch = append(batch, promwrite.Sample{Labels: labels, Value: float64(point+1) * 100 * weight * fraction, TsMs: end.Add(time.Duration(point-10) * time.Minute).UnixMilli()})
								if len(batch) == cap(batch) {
									flush()
								}
							}
						}
					}
				}
			}
			flush()
			q := Query{Start: end.Add(-10 * time.Minute), End: end, Environment: &env, ServiceNamespace: &ns, Protocol: "all", PageSize: 20}
			rows, err := svc.List(ctx, q, false)
			if err != nil || rows.Total != total {
				t.Fatalf("service completeness: %+v %v", rows, err)
			}
			if len(rows.Items) != 20 || rows.Items[0].ErrorRate == nil || math.Abs(*rows.Items[0].ErrorRate-25) > 0.01 {
				t.Fatalf("pagination or error semantics: %+v", rows)
			}
			for _, concurrency := range []int{1, 4, 8} {
				var wg sync.WaitGroup
				latencies := make(chan time.Duration, concurrency*3)
				errors := make(chan error, concurrency*3)
				start := time.Now()
				for worker := 0; worker < concurrency; worker++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for n := 0; n < 3; n++ {
							before := time.Now()
							result, err := svc.List(ctx, q, false)
							latencies <- time.Since(before)
							if err != nil {
								errors <- err
							} else if result.Total != total {
								errors <- fmt.Errorf("total=%d want %d", result.Total, total)
							}
						}
					}()
				}
				wg.Wait()
				close(latencies)
				close(errors)
				var values []float64
				for elapsed := range latencies {
					values = append(values, float64(elapsed.Microseconds())/1000)
				}
				sort.Float64s(values)
				failures := 0
				for err := range errors {
					t.Error(err)
					failures++
				}
				record := map[string]any{"services": total, "input_series": total * 28, "concurrency": concurrency, "queries": len(values), "errors": failures, "elapsed_ms": time.Since(start).Milliseconds(), "p50_ms": values[len(values)/2], "p95_ms": values[int(math.Ceil(float64(len(values))*0.95))-1]}
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				t.Log(string(raw))
			}
			previous = total
		})
	}
}
