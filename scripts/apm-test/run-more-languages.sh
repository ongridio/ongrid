#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
project="ongrid-apm-more-$$"
config_dir="$(mktemp -d /tmp/ongrid-apm-more.XXXXXX)"
export APM_TEST_COLLECTOR_CONFIG="$config_dir/collector.yaml"
export APM_MORE_LOGS="$config_dir/logs"
mkdir "$APM_MORE_LOGS"
chmod 777 "$APM_MORE_LOGS"
compose=(docker compose --profile metrics -p "$project" -f scripts/apm-test/compose.yaml)
cleanup() { "${compose[@]}" down -v --remove-orphans; mkdir -p "${APM_ACCEPTANCE_OUTPUT:-output/apm-acceptance}/more-language-logs"; cp -R "$APM_MORE_LOGS/." "${APM_ACCEPTANCE_OUTPUT:-output/apm-acceptance}/more-language-logs/"; rm -rf "$config_dir"; }
trap cleanup EXIT
GOCACHE="${GOCACHE:-/tmp/ongrid-go-build-cache}" go test -count=1 -run TestAPMMetricsCollectorConfig ./internal/edgeagent/plugins/traces
"${compose[@]}" up -d
export APM_MORE_COLLECTOR="$("${compose[@]}" ps -q metrics-collector)"
for attempt in $(seq 1 60); do
  if curl --noproxy '*' -fsS http://127.0.0.1:13200/ready >/dev/null 2>&1 && curl --noproxy '*' -fsS http://127.0.0.1:19090/prometheus/-/ready >/dev/null 2>&1; then break; fi
  if [[ "$attempt" == 60 ]]; then "${compose[@]}" logs; exit 1; fi
  sleep 2
done
python3 scripts/apm-test/more-languages.py
APM_TEST_LANGUAGES=1 APM_MORE_LANGUAGES="${APM_MORE_LANGUAGES:-dotnet php cpp rust ruby}" APM_TEST_PROMETHEUS=http://127.0.0.1:19090/prometheus GOCACHE="${GOCACHE:-/tmp/ongrid-go-build-cache}" go test -race -count=1 -run TestAPMLanguageMetricsIntegration -v ./internal/manager/biz/apm
