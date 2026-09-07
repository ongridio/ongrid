#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
project="ongrid-apm-test-$$"
compose=(docker compose -p "$project" -f scripts/apm-test/compose.yaml)
cleanup() { "${compose[@]}" down -v --remove-orphans; }
trap cleanup EXIT
"${compose[@]}" up -d
for attempt in $(seq 1 60); do
  if curl --noproxy '*' -fsS http://127.0.0.1:13200/ready >/dev/null 2>&1 && curl --noproxy '*' -fsS http://127.0.0.1:19090/prometheus/-/ready >/dev/null 2>&1; then break; fi
  if [[ "$attempt" == 60 ]]; then "${compose[@]}" logs; exit 1; fi
  sleep 2
done
APM_TEST_PROMTOOL="$(pwd)/scripts/apm-test/promtool.sh" APM_TEST_PROMETHEUS=http://127.0.0.1:19090/prometheus APM_TEST_TEMPO=http://127.0.0.1:13200 APM_TEST_OTLP=http://127.0.0.1:14318/v1/traces GOCACHE="${GOCACHE:-/tmp/ongrid-go-build-cache}" go test -race -count=1 -run 'TestOTLPIntegration|TestAlertDwellPromtool' -v ./internal/manager/biz/apm
