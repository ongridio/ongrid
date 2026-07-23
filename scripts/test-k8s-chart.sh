#!/usr/bin/env bash
set -euo pipefail

chart_dir=${1:-deploy/kubernetes/ongrid-edge}
chart_package=${2:-bin/k8s/ongrid-edge.tgz}
expected_image=${3:-}

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

common_args=(
  --namespace ongrid-system
  --set-string manager.publicURL=https://manager.example:8443
  --set-string manager.tunnelAddr=manager.example:40012
  --set-string enrollment.clusterID=1
  --set-string enrollment.controllerBootstrapToken=controller-token
  --set-string enrollment.nodeBootstrapToken=node-token
)

extract_source() {
  local source=$1
  local input=$2
  local output=$3
  awk -v marker="# Source: ${source}" '
    $0 == marker { capture = 1; next }
    capture && /^---$/ { exit }
    capture { print }
  ' "$input" >"$output"
}

expect_template_failure() {
  local expected=$1
  shift
  if "$@" >"$tmp_dir/failure.log" 2>&1; then
    echo "expected Helm template failure containing: $expected" >&2
    exit 1
  fi
  grep -Fq "$expected" "$tmp_dir/failure.log"
}

helm lint "$chart_dir" "${common_args[@]}"

helm template ongrid-edge "$chart_package" "${common_args[@]}" >"$tmp_dir/default.yaml"
extract_source 'ongrid-edge/templates/telemetry-credentials-secret.yaml' "$tmp_dir/default.yaml" "$tmp_dir/telemetry-secret.yaml"
grep -q 'type: Recreate' "$tmp_dir/default.yaml"
! grep -q 'kubernetes.io/arch:' "$tmp_dir/default.yaml"
if [[ -n "$expected_image" ]]; then
  test "$(grep -F -c "image: \"${expected_image}\"" "$tmp_dir/default.yaml")" -eq 3
fi
grep -q 'k8s-inventory-full-sync-interval: "10m"' "$tmp_dir/default.yaml"
grep -q 'k8s-metrics-timeout: "15s"' "$tmp_dir/default.yaml"
grep -q 'k8s-metrics-push-timeout: "30s"' "$tmp_dir/default.yaml"
grep -q 'k8s-metrics-sample-limit: "250000"' "$tmp_dir/default.yaml"
grep -q 'k8s-metrics-batch-sample-limit: "10000"' "$tmp_dir/default.yaml"
grep -q 'k8s-metrics-batch-byte-limit: "4194304"' "$tmp_dir/default.yaml"
grep -q 'name: ONGRID_K8S_METRICS_TIMEOUT' "$tmp_dir/default.yaml"
grep -q 'name: ONGRID_K8S_METRICS_PUSH_TIMEOUT' "$tmp_dir/default.yaml"
grep -q 'name: ONGRID_K8S_METRICS_SAMPLE_LIMIT' "$tmp_dir/default.yaml"
grep -q 'name: ONGRID_K8S_METRICS_BATCH_SAMPLE_LIMIT' "$tmp_dir/default.yaml"
grep -q 'name: ONGRID_K8S_METRICS_BATCH_BYTE_LIMIT' "$tmp_dir/default.yaml"
grep -A1 'name: ONGRID_K8S_TELEMETRY_CONFIG_REFRESH_INTERVAL' "$tmp_dir/default.yaml" | grep -q 'value: "1m"'
grep -q 'hostNetwork: true' "$tmp_dir/default.yaml"
grep -q 'name: install-host-runtime' "$tmp_dir/default.yaml"
grep -q -- '- install-k8s-host-runtime' "$tmp_dir/default.yaml"
grep -A16 'name: install-host-runtime' "$tmp_dir/default.yaml" | grep -q 'memory: 128Mi'
grep -q -- '- enter-k8s-host' "$tmp_dir/default.yaml"
test "$(grep -F -c 'mountPath: /host/root' "$tmp_dir/default.yaml")" -eq 2
grep -q 'mountPropagation: HostToContainer' "$tmp_dir/default.yaml"
grep -A1 'name: ONGRID_EDGE_COLLECTOR_MODE' "$tmp_dir/default.yaml" | grep -q 'value: "off"'
grep -q 'add: \["CHOWN", "DAC_OVERRIDE", "FOWNER"\]' "$tmp_dir/default.yaml"
grep -q 'add: \["DAC_READ_SEARCH", "NET_ADMIN", "SETGID", "SETPCAP", "SETUID", "SYS_CHROOT"\]' "$tmp_dir/default.yaml"
! grep -q 'SYS_ADMIN\|SYS_PTRACE' "$tmp_dir/default.yaml"
! grep -q 'privileged: true' "$tmp_dir/default.yaml"
! grep -q 'supplementalGroups:' "$tmp_dir/default.yaml"
! grep -q '^data:' "$tmp_dir/telemetry-secret.yaml"

helm template split "$chart_package" "${common_args[@]}" \
  --set telemetryGateway.mode=deployment \
  --set kubernetesMetrics.mode=scraper \
  >"$tmp_dir/split.yaml"
extract_source 'ongrid-edge/templates/deployment.yaml' "$tmp_dir/split.yaml" "$tmp_dir/split-controller.yaml"
extract_source 'ongrid-edge/templates/metrics-scraper-deployment.yaml' "$tmp_dir/split.yaml" "$tmp_dir/scraper.yaml"
grep -q '# Source: ongrid-edge/templates/telemetry-gateway-deployment.yaml' "$tmp_dir/split.yaml"
grep -q '# Source: ongrid-edge/templates/metrics-scraper-deployment.yaml' "$tmp_dir/split.yaml"
! grep -q 'ONGRID_K8S_TELEMETRY_GATEWAY_ENABLED\|ONGRID_K8S_METRICS_ENDPOINT\|containerPort: 4317\|containerPort: 4318' "$tmp_dir/split-controller.yaml"
grep -A1 'name: ONGRID_K8S_TELEMETRY_REQUIRED' "$tmp_dir/split-controller.yaml" | grep -q 'value: "true"'
grep -q 'replicas: 1' "$tmp_dir/scraper.yaml"
grep -q 'automountServiceAccountToken: false' "$tmp_dir/scraper.yaml"
! grep -q 'telemetry-access-key\|telemetry-secret-key\|telemetry-traces-endpoint\|telemetry-logs-endpoint' "$tmp_dir/scraper.yaml"

helm template paused "$chart_package" "${common_args[@]}" \
  --set kubernetesMetrics.mode=controller \
  --set kubernetesMetrics.enabled=false \
  >"$tmp_dir/paused.yaml"
extract_source 'ongrid-edge/templates/deployment.yaml' "$tmp_dir/paused.yaml" "$tmp_dir/paused-controller.yaml"
! grep -q '# Source: ongrid-edge/templates/metrics-scraper-deployment.yaml' "$tmp_dir/paused.yaml"
! grep -q 'ONGRID_K8S_METRICS_ENDPOINT' "$tmp_dir/paused-controller.yaml"
grep -A1 'name: ONGRID_K8S_TELEMETRY_REQUIRED' "$tmp_dir/paused-controller.yaml" | grep -q 'value: "false"'

helm template hpa "$chart_package" "${common_args[@]}" \
  --set telemetryGateway.mode=deployment \
  --set telemetryGateway.autoscaling.enabled=true \
  >"$tmp_dir/hpa.yaml"
grep -q '# Source: ongrid-edge/templates/telemetry-gateway-policy.yaml' "$tmp_dir/hpa.yaml"
grep -q 'kind: HorizontalPodAutoscaler' "$tmp_dir/hpa.yaml"
grep -q 'averageValue: 600Mi' "$tmp_dir/hpa.yaml"

expect_template_failure 'kubernetesMetrics.mode must be controller or scraper' \
  helm template invalid-mode "$chart_package" "${common_args[@]}" --set kubernetesMetrics.mode=invalid
expect_template_failure 'kubernetesMetrics.replicas must be 1' \
  helm template invalid-scraper "$chart_package" "${common_args[@]}" --set kubernetesMetrics.mode=scraper --set kubernetesMetrics.replicas=2
expect_template_failure 'telemetryGateway.replicas must be at least 2' \
  helm template invalid-gateway "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.replicas=1
expect_template_failure 'telemetryGateway.memoryLimiter.limitMiB must be at most 80%' \
  helm template invalid-limiter "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.memoryLimiter.limitMiB=900
expect_template_failure 'telemetryGateway.batch requires 0 < sendSize <= maxSize <= 4096' \
  helm template invalid-batch "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.batch.maxSize=5000
expect_template_failure 'targetMemoryAverageValue must be below the memory_limiter soft limit' \
  helm template invalid-hpa "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.autoscaling.enabled=true --set telemetryGateway.autoscaling.targetMemoryAverageValue=650Mi

echo "Kubernetes Helm chart validation passed"
