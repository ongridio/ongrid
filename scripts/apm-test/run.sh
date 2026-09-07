#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
project="ongrid-apm-test-$$"
config_dir="$(mktemp -d /tmp/ongrid-apm-metrics.XXXXXX)"
export APM_TEST_COLLECTOR_CONFIG="$config_dir/collector.yaml"
compose=(docker compose --profile metrics -p "$project" -f scripts/apm-test/compose.yaml)
cleanup() { "${compose[@]}" down -v --remove-orphans; rm -rf "$config_dir"; }
trap cleanup EXIT
GOCACHE="${GOCACHE:-/tmp/ongrid-go-build-cache}" go test -count=1 -run TestAPMMetricsCollectorConfig ./internal/edgeagent/plugins/traces
"${compose[@]}" up -d
for attempt in $(seq 1 60); do
  if curl --noproxy '*' -fsS http://127.0.0.1:13200/ready >/dev/null 2>&1 && curl --noproxy '*' -fsS http://127.0.0.1:19090/prometheus/-/ready >/dev/null 2>&1; then break; fi
  if [[ "$attempt" == 60 ]]; then "${compose[@]}" logs; exit 1; fi
  sleep 2
done
APM_TEST_PROMTOOL="$(pwd)/scripts/apm-test/promtool.sh" APM_TEST_PROMETHEUS=http://127.0.0.1:19090/prometheus APM_TEST_TEMPO=http://127.0.0.1:13200 APM_TEST_OTLP=http://127.0.0.1:14318/v1/traces GOCACHE="${GOCACHE:-/tmp/ongrid-go-build-cache}" go test -race -count=1 -run 'TestOTLPIntegration|TestAlertDwellPromtool' -v ./internal/manager/biz/apm

APM_TEST_METRICS=http://127.0.0.1:14319 APM_TEST_PROMETHEUS=http://127.0.0.1:19090/prometheus GOCACHE="${GOCACHE:-/tmp/ongrid-go-build-cache}" go test -race -count=1 -run TestExampleMetricsWithoutTraces -v ./examples/apm-go
