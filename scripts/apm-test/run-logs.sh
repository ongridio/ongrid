#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
project="ongrid-apm-logs-$$"
compose=(docker compose -p "$project" -f scripts/apm-test/compose.yaml --profile logs)
cleanup() { "${compose[@]}" down -v --remove-orphans; }
trap cleanup EXIT
"${compose[@]}" up -d loki elasticsearch
for attempt in $(seq 1 60); do
  if curl --noproxy '*' -fsS http://127.0.0.1:13100/ready >/dev/null 2>&1 && curl --noproxy '*' -fsS http://127.0.0.1:19200/_cluster/health >/dev/null 2>&1; then break; fi
  if [[ "$attempt" == 60 ]]; then "${compose[@]}" logs; exit 1; fi
  sleep 2
done
APM_TEST_DOCKER_NETWORK="${project}_default" GOCACHE="${GOCACHE:-/tmp/ongrid-go-build-cache}" go test -race -count=1 -run TestAPMLogCorrelationIntegration -v ./internal/edgeagent/plugins/logs
