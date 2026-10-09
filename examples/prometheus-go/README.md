# Prometheus metrics and Auto APM test

This Go service uses `prometheus/client_golang`. It has no OpenTelemetry SDK.
It exposes HTTP histograms, a custom `demo_jobs_total` counter, Go runtime
metrics, and process metrics at `/metrics`. `/work` returns 200 and `/fail`
returns 500. Health checks and scrapes do not enter the application histogram.

Build from the repository root (set `GOARCH` for the cluster nodes):

```sh
mkdir -p output/prometheus-go
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
  -o output/prometheus-go/prometheus-go ./examples/prometheus-go
docker build -t ongrid-prometheus-go:local \
  -f examples/prometheus-go/Dockerfile output/prometheus-go
```

Load the image into the test cluster or use an accessible registry, then apply
`examples/prometheus-go/kubernetes.yaml`. Its Pod template declares the usual
`prometheus.io/scrape`, `prometheus.io/port`, and `prometheus.io/path` annotations.
These annotations need a configured scraper; Kubernetes does not scrape them.
The default Ongrid Scraper uses the bundled OpenTelemetry Collector Prometheus
Receiver. A second declared container port tests that the annotated 8080 endpoint
is still scraped once; the application does not listen on 8081.

Select namespace `metrics-demo`, Deployment `prometheus-go` in Ongrid Auto APM.
Both Auto APM and annotated Pod metrics follow this selection. Selecting the
whole namespace includes its annotated Pods, including future Pods. Selecting
a workload includes only its Pods. An empty selection collects no application
metrics; kube-state-metrics remains independent. Scope changes propagate through
the controller's configuration sync and Kubernetes Secret projection.
Keep other capture rules. Generate requests with:

```sh
kubectl -n metrics-demo port-forward service/prometheus-go 18080:8080
# In another terminal:
curl http://127.0.0.1:18080/work
curl http://127.0.0.1:18080/fail
curl http://127.0.0.1:18080/metrics
```

Check raw application metrics with
`{namespace="metrics-demo",ongrid_source="k8s:app-metrics"}`. Check Auto APM
metrics with `{service_name="prometheus-go",ongrid_instrumentation_source="obi"}`.
The HTTP histogram deliberately shares OBI's metric name and service labels. These are independent
measurements; metric names alone do not establish equivalent scope or semantics.
Compare each source separately, and check traces as well as request statistics.

Run `go test -race ./examples/prometheus-go` for the local endpoint check.
Remove the demo with `kubectl delete -f examples/prometheus-go/kubernetes.yaml`
and remove only its Auto APM capture rule.
