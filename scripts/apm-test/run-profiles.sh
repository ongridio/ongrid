#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
project="ongrid-apm-profiles-$$"
compose=(docker compose -p "$project" -f scripts/apm-test/compose.yaml --profile profiles)
cleanup() { "${compose[@]}" down -v --remove-orphans; }
trap cleanup EXIT
"${compose[@]}" up -d pyroscope profiles-gateway
for attempt in $(seq 1 60); do
  if curl --noproxy '*' -fsS http://127.0.0.1:14040/ready >/dev/null 2>&1; then break; fi
  if [[ "$attempt" == 60 ]]; then "${compose[@]}" logs; exit 1; fi
  sleep 2
done
APM_TEST_DOCKER_NETWORK="${project}_default" GOCACHE="${GOCACHE:-/tmp/ongrid-go-build-cache}" go test -race -count=1 -run TestAPMProfileIdentityIntegration -v ./internal/edgeagent/plugins/profiles
