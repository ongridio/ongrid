#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
export APM_LANGUAGE_CACHE="${APM_LANGUAGE_CACHE:-/tmp/ongrid-apm-languages}"
export APM_ACCEPTANCE_OUTPUT="${APM_ACCEPTANCE_OUTPUT:-$(pwd)/output/apm-acceptance}"
mkdir -p "$APM_LANGUAGE_CACHE" "$APM_ACCEPTANCE_OUTPUT"
python3 -m venv "$APM_LANGUAGE_CACHE/python"
"$APM_LANGUAGE_CACHE/python/bin/pip" install -r examples/apm-languages/requirements.txt
mkdir -p "$APM_LANGUAGE_CACHE/node"
cp examples/apm-languages/package*.json "$APM_LANGUAGE_CACHE/node/"
npm ci --prefix "$APM_LANGUAGE_CACHE/node" --no-audit --no-fund
NODE_PATH="$APM_LANGUAGE_CACHE/node/node_modules" node --test examples/apm-languages/grpc-metrics.test.cjs
if [[ ! -s "$APM_LANGUAGE_CACHE/opentelemetry-javaagent.jar" ]]; then
  curl --fail --location https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/v2.31.1/opentelemetry-javaagent.jar -o "$APM_LANGUAGE_CACHE/opentelemetry-javaagent.jar"
fi
printf '%s  %s\n' bbf83c151b6400709e2f225bdd07a04f839d9d13b8b93464241333fd25d3e3ba "$APM_LANGUAGE_CACHE/opentelemetry-javaagent.jar" | shasum -a 256 -c -
if [[ ! -x "$APM_LANGUAGE_CACHE/apache-maven-3.9.11/bin/mvn" ]]; then
  curl --fail --location https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.11/apache-maven-3.9.11-bin.tar.gz -o "$APM_LANGUAGE_CACHE/maven.tar.gz"
  tar -xzf "$APM_LANGUAGE_CACHE/maven.tar.gz" -C "$APM_LANGUAGE_CACHE"
fi
mkdir -p "$APM_LANGUAGE_CACHE/java"
cp -R examples/apm-languages/java/. "$APM_LANGUAGE_CACHE/java/"
"$APM_LANGUAGE_CACHE/apache-maven-3.9.11/bin/mvn" -q -f "$APM_LANGUAGE_CACHE/java/pom.xml" -Dmaven.repo.local="$APM_LANGUAGE_CACHE/m2" package dependency:build-classpath -Dmdep.outputFile=classpath.txt
mkdir -p "$APM_LANGUAGE_CACHE/generated"
"$APM_LANGUAGE_CACHE/python/bin/python" -m grpc_tools.protoc -I examples/apm-languages --python_out="$APM_LANGUAGE_CACHE/generated" --grpc_python_out="$APM_LANGUAGE_CACHE/generated" examples/apm-languages/health.proto
export PYTHONPATH="$APM_LANGUAGE_CACHE/generated"
project="ongrid-apm-languages-$$"
config_dir="$(mktemp -d /tmp/ongrid-apm-languages-config.XXXXXX)"
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
"$APM_LANGUAGE_CACHE/python/bin/python" scripts/apm-test/languages.py "$@"
if [[ "${APM_OVERHEAD_ONLY:-0}" != "1" ]]; then
  APM_TEST_LANGUAGES=1 APM_TEST_PROMETHEUS=http://127.0.0.1:19090/prometheus GOCACHE="${GOCACHE:-/tmp/ongrid-go-build-cache}" go test -race -count=1 -run TestAPMLanguageMetricsIntegration -v ./internal/manager/biz/apm
fi
APM_TEST_PROMETHEUS=http://127.0.0.1:19090/prometheus GOCACHE="${GOCACHE:-/tmp/ongrid-go-build-cache}" go test -race -count=1 -run TestAPMAlertLifecycleIntegration -v ./internal/manager/data/alert/store
if [[ "${APM_TEST_LOAD:-0}" == "1" ]]; then
  APM_TEST_PROMETHEUS=http://127.0.0.1:19090/prometheus GOCACHE="${GOCACHE:-/tmp/ongrid-go-build-cache}" go test -race -count=1 -run TestAPMCapacityIntegration -v ./internal/manager/biz/apm
fi
