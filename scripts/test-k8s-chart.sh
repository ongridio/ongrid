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
extract_source 'ongrid-edge/templates/deployment.yaml' "$tmp_dir/default.yaml" "$tmp_dir/default-controller.yaml"
extract_source 'ongrid-edge/templates/daemonset.yaml' "$tmp_dir/default.yaml" "$tmp_dir/default-node.yaml"
extract_source 'ongrid-edge/templates/metrics-scraper-deployment.yaml' "$tmp_dir/default.yaml" "$tmp_dir/default-scraper.yaml"
extract_source 'ongrid-edge/templates/telemetry-gateway-deployment.yaml' "$tmp_dir/default.yaml" "$tmp_dir/default-gateway.yaml"
extract_source 'ongrid-edge/templates/telemetry-gateway-service.yaml' "$tmp_dir/default.yaml" "$tmp_dir/default-gateway-service.yaml"
grep -q 'type: Recreate' "$tmp_dir/default.yaml"
! grep -q 'kubernetes.io/arch:' "$tmp_dir/default.yaml"
if [[ -n "$expected_image" ]]; then
  test "$(grep -F -c "image: \"${expected_image}\"" "$tmp_dir/default.yaml")" -eq 5
fi
grep -q '# Source: ongrid-edge/templates/telemetry-gateway-deployment.yaml' "$tmp_dir/default.yaml"
grep -q '# Source: ongrid-edge/templates/metrics-scraper-deployment.yaml' "$tmp_dir/default.yaml"
grep -q 'kind: HorizontalPodAutoscaler' "$tmp_dir/default.yaml"
grep -q 'minReplicas: 2' "$tmp_dir/default.yaml"
grep -q 'maxReplicas: 10' "$tmp_dir/default.yaml"
grep -q 'averageUtilization: 60' "$tmp_dir/default.yaml"
grep -q 'averageValue: 512Mi' "$tmp_dir/default.yaml"
grep -A12 '^    scaleUp:' "$tmp_dir/default.yaml" | grep -q 'value: 100'
grep -A12 '^    scaleUp:' "$tmp_dir/default.yaml" | grep -q 'value: 4'
grep -A8 '^    scaleDown:' "$tmp_dir/default.yaml" | grep -q 'stabilizationWindowSeconds: 300'
grep -A8 '^    scaleDown:' "$tmp_dir/default.yaml" | grep -q 'value: 1'
grep -A8 '^    scaleDown:' "$tmp_dir/default.yaml" | grep -q 'periodSeconds: 60'
! grep -q '^  replicas:' "$tmp_dir/default-gateway.yaml"
! grep -q '# Source: ongrid-edge/templates/upgrade-preflight.yaml' "$tmp_dir/default.yaml"
! grep -q 'ONGRID_K8S_METRICS_ENDPOINT\|containerPort: 4317\|containerPort: 4318' "$tmp_dir/default-controller.yaml"
grep -q 'ongrid.io/telemetry-backend: "false"' "$tmp_dir/default-controller.yaml"
grep -q 'ongrid.io/telemetry-backend: "true"' "$tmp_dir/default-gateway-service.yaml"
test "$(grep -F -c 'checksum/config:' "$tmp_dir/default.yaml")" -eq 3
grep -q 'checksum/controller-bootstrap:' "$tmp_dir/default-controller.yaml"
grep -q 'checksum/node-bootstrap:' "$tmp_dir/default.yaml"
# kube-state-metrics, the telemetry gateway k8sattributes processor, and the
# controller each need the same read-only workload ownership resources.
test "$(grep -F -c 'resources: ["deployments", "statefulsets", "daemonsets", "replicasets"]' "$tmp_dir/default.yaml")" -ge 3
test "$(grep -F -c 'resources: ["jobs", "cronjobs"]' "$tmp_dir/default.yaml")" -ge 3
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
test "$(grep -E -c '^[[:space:]]+mountPath: /host/root$' "$tmp_dir/default-node.yaml")" -eq 2
grep -q 'mountPropagation: HostToContainer' "$tmp_dir/default.yaml"
grep -q 'automountServiceAccountToken: false' "$tmp_dir/default-node.yaml"
grep -q 'mountPath: /host/root/var/lib/ongrid-edge/k8s-runtime/serviceaccount' "$tmp_dir/default-node.yaml"
! grep -q 'mountPath: /host/root/var/run/secrets/kubernetes.io/serviceaccount' "$tmp_dir/default-node.yaml"
grep -q 'expirationSeconds: 3600' "$tmp_dir/default-node.yaml"
grep -q 'defaultMode: 0444' "$tmp_dir/default-node.yaml"
grep -A1 'name: ONGRID_EDGE_SECRET_DIR' "$tmp_dir/default-node.yaml" | grep -q 'value: /var/lib/ongrid-edge/k8s-state/secrets'
grep -A1 'name: ONGRID_EDGE_COLLECTOR_MODE' "$tmp_dir/default.yaml" | grep -q 'value: "off"'
grep -A1 'name: ONGRID_EDGE_METRICS_ADDR' "$tmp_dir/default-node.yaml" | grep -q 'value: ":9101"'
grep -q 'add: \["CHOWN", "DAC_OVERRIDE", "FOWNER"\]' "$tmp_dir/default.yaml"
grep -q 'add: \["DAC_READ_SEARCH", "NET_ADMIN", "SETGID", "SETPCAP", "SETUID", "SYS_CHROOT"\]' "$tmp_dir/default.yaml"
! grep -q 'SYS_ADMIN\|SYS_PTRACE' "$tmp_dir/default.yaml"
! grep -q 'privileged: true' "$tmp_dir/default.yaml"
! grep -q 'supplementalGroups:' "$tmp_dir/default.yaml"
! grep -q '^data:' "$tmp_dir/telemetry-secret.yaml"

helm template compatibility "$chart_package" "${common_args[@]}" \
  --set telemetryGateway.mode=embedded \
  --set kubernetesMetrics.mode=controller \
  >"$tmp_dir/compatibility.yaml"
extract_source 'ongrid-edge/templates/deployment.yaml' "$tmp_dir/compatibility.yaml" "$tmp_dir/compatibility-controller.yaml"
! grep -q '# Source: ongrid-edge/templates/telemetry-gateway-deployment.yaml' "$tmp_dir/compatibility.yaml"
! grep -q '# Source: ongrid-edge/templates/metrics-scraper-deployment.yaml' "$tmp_dir/compatibility.yaml"
grep -q 'ONGRID_K8S_METRICS_ENDPOINT' "$tmp_dir/compatibility-controller.yaml"
grep -q 'containerPort: 4317' "$tmp_dir/compatibility-controller.yaml"
grep -q 'ongrid.io/telemetry-backend: "true"' "$tmp_dir/compatibility-controller.yaml"

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
grep -q 'name: ONGRID_K8S_APP_METRICS_DISCOVERY' "$tmp_dir/scraper.yaml"
! grep -q 'telemetry-access-key\|telemetry-secret-key\|telemetry-traces-endpoint\|telemetry-logs-endpoint' "$tmp_dir/scraper.yaml"

# Values persisted by Charts before kubernetesMetrics existed must continue to
# drive the standalone Scraper after --reset-then-reuse-values.
helm template legacy-external "$chart_package" "${common_args[@]}" \
  --set kubeStateMetrics.enabled=false \
  --set controller.metrics.enabled=true \
  --set-string controller.metrics.endpoint=http://metrics.monitoring.svc:9090/metrics \
  --set-string controller.metrics.interval=2m \
  --set-string controller.metrics.timeout=45s \
  --set-string controller.metrics.pushTimeout=1m \
  --set controller.metrics.sampleLimit=123456 \
  --set controller.metrics.batchSampleLimit=4321 \
  --set controller.metrics.batchByteLimit=2097152 \
  >"$tmp_dir/legacy-external.yaml"
grep -q '# Source: ongrid-edge/templates/metrics-scraper-deployment.yaml' "$tmp_dir/legacy-external.yaml"
grep -q 'k8s-metrics-endpoint: "http://metrics.monitoring.svc:9090/metrics"' "$tmp_dir/legacy-external.yaml"
grep -q 'k8s-metrics-interval: "2m"' "$tmp_dir/legacy-external.yaml"
grep -q 'k8s-metrics-timeout: "45s"' "$tmp_dir/legacy-external.yaml"
grep -q 'k8s-metrics-push-timeout: "1m"' "$tmp_dir/legacy-external.yaml"
grep -q 'k8s-metrics-sample-limit: "123456"' "$tmp_dir/legacy-external.yaml"
grep -q 'k8s-metrics-batch-sample-limit: "4321"' "$tmp_dir/legacy-external.yaml"
grep -q 'k8s-metrics-batch-byte-limit: "2097152"' "$tmp_dir/legacy-external.yaml"

helm template explicit-new-metrics "$chart_package" "${common_args[@]}" \
  --set-string controller.metrics.interval=2m \
  --set-string kubernetesMetrics.interval=45s \
  >"$tmp_dir/explicit-new-metrics.yaml"
grep -q 'k8s-metrics-interval: "45s"' "$tmp_dir/explicit-new-metrics.yaml"

helm template legacy-disabled "$chart_package" "${common_args[@]}" \
  --set kubeStateMetrics.enabled=false \
  --set controller.metrics.enabled=false \
  >"$tmp_dir/legacy-disabled.yaml"
! grep -q '# Source: ongrid-edge/templates/metrics-scraper-deployment.yaml' "$tmp_dir/legacy-disabled.yaml"

helm template legacy-app-discovery "$chart_package" "${common_args[@]}" \
  --set kubeStateMetrics.enabled=false \
  --set controller.metrics.appDiscovery.enabled=true \
  >"$tmp_dir/legacy-app-discovery.yaml"
extract_source 'ongrid-edge/templates/metrics-scraper-deployment.yaml' "$tmp_dir/legacy-app-discovery.yaml" "$tmp_dir/legacy-app-scraper.yaml"
grep -q 'k8s-metrics-endpoint: ""' "$tmp_dir/legacy-app-discovery.yaml"
grep -q 'k8s-app-metrics-discovery: "true"' "$tmp_dir/legacy-app-discovery.yaml"
grep -q 'automountServiceAccountToken: true' "$tmp_dir/legacy-app-scraper.yaml"
grep -q 'ongrid-edge-metrics-scraper-discovery' "$tmp_dir/legacy-app-discovery.yaml"

# A Scraper-only tuning change must restart the Scraper without needlessly
# recycling the cluster-wide Controller or every node Agent.
helm template metrics-config-change "$chart_package" "${common_args[@]}" \
  --set-string kubernetesMetrics.endpoint=http://metrics.monitoring.svc:9090/metrics \
  >"$tmp_dir/metrics-config-change.yaml"
extract_source 'ongrid-edge/templates/deployment.yaml' "$tmp_dir/metrics-config-change.yaml" "$tmp_dir/metrics-config-controller.yaml"
extract_source 'ongrid-edge/templates/daemonset.yaml' "$tmp_dir/metrics-config-change.yaml" "$tmp_dir/metrics-config-node.yaml"
extract_source 'ongrid-edge/templates/metrics-scraper-deployment.yaml' "$tmp_dir/metrics-config-change.yaml" "$tmp_dir/metrics-config-scraper.yaml"
test "$(awk '/checksum\/config:/ {print $2}' "$tmp_dir/default-controller.yaml")" = "$(awk '/checksum\/config:/ {print $2}' "$tmp_dir/metrics-config-controller.yaml")"
test "$(awk '/checksum\/config:/ {print $2}' "$tmp_dir/default-node.yaml")" = "$(awk '/checksum\/config:/ {print $2}' "$tmp_dir/metrics-config-node.yaml")"
test "$(awk '/checksum\/config:/ {print $2}' "$tmp_dir/default-scraper.yaml")" != "$(awk '/checksum\/config:/ {print $2}' "$tmp_dir/metrics-config-scraper.yaml")"

# The node agent shares the host network, so operators must be able to move
# its diagnostics listener away from an existing host-level edge agent.
helm template node-metrics-address "$chart_package" "${common_args[@]}" \
  --set-string node.metricsAddr=:19101 \
  >"$tmp_dir/node-metrics-address.yaml"
extract_source 'ongrid-edge/templates/daemonset.yaml' "$tmp_dir/node-metrics-address.yaml" "$tmp_dir/node-metrics-address-node.yaml"
grep -A1 'name: ONGRID_EDGE_METRICS_ADDR' "$tmp_dir/node-metrics-address-node.yaml" | grep -q 'value: ":19101"'
test "$(awk '/checksum\/config:/ {print $2}' "$tmp_dir/default-node.yaml")" != "$(awk '/checksum\/config:/ {print $2}' "$tmp_dir/node-metrics-address-node.yaml")"

helm template upgrade "$chart_package" "${common_args[@]}" --is-upgrade >"$tmp_dir/upgrade.yaml"
grep -q '# Source: ongrid-edge/templates/upgrade-preflight.yaml' "$tmp_dir/upgrade.yaml"
test "$(grep -F -c 'helm.sh/hook: pre-upgrade' "$tmp_dir/upgrade.yaml")" -eq 4
grep -q 'activeDeadlineSeconds: 480' "$tmp_dir/upgrade.yaml"
grep -q -- '- prepare-k8s-upgrade' "$tmp_dir/upgrade.yaml"
grep -q -- '- --gateway-mode=deployment' "$tmp_dir/upgrade.yaml"
grep -q -- '- --metrics-mode=scraper' "$tmp_dir/upgrade.yaml"

helm template upgrade-hook-disabled "$chart_package" "${common_args[@]}" --is-upgrade \
  --set upgrade.migrationHook.enabled=false \
  >"$tmp_dir/upgrade-hook-disabled.yaml"
! grep -q '# Source: ongrid-edge/templates/upgrade-preflight.yaml' "$tmp_dir/upgrade-hook-disabled.yaml"

helm template paused "$chart_package" "${common_args[@]}" \
  --set telemetryGateway.mode=embedded \
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
  --set telemetryGateway.autoscaling.minReplicas=3 \
  --set telemetryGateway.autoscaling.maxReplicas=12 \
  --set telemetryGateway.autoscaling.scaleDownStabilizationWindowSeconds=0 \
  --set telemetryGateway.autoscaling.scaleDownMaxPods=2 \
  --set telemetryGateway.autoscaling.scaleDownPeriodSeconds=120 \
  >"$tmp_dir/hpa.yaml"
grep -q '# Source: ongrid-edge/templates/telemetry-gateway-policy.yaml' "$tmp_dir/hpa.yaml"
grep -q 'kind: HorizontalPodAutoscaler' "$tmp_dir/hpa.yaml"
grep -q 'minReplicas: 3' "$tmp_dir/hpa.yaml"
grep -q 'maxReplicas: 12' "$tmp_dir/hpa.yaml"
grep -q 'averageValue: 512Mi' "$tmp_dir/hpa.yaml"
grep -A8 '^    scaleDown:' "$tmp_dir/hpa.yaml" | grep -q 'stabilizationWindowSeconds: 0'
grep -A8 '^    scaleDown:' "$tmp_dir/hpa.yaml" | grep -q 'value: 2'
grep -A8 '^    scaleDown:' "$tmp_dir/hpa.yaml" | grep -q 'periodSeconds: 120'

helm template fixed-gateway "$chart_package" "${common_args[@]}" \
  --set telemetryGateway.autoscaling.enabled=false \
  >"$tmp_dir/fixed-gateway.yaml"
extract_source 'ongrid-edge/templates/telemetry-gateway-deployment.yaml' "$tmp_dir/fixed-gateway.yaml" "$tmp_dir/fixed-gateway-deployment.yaml"
! grep -q 'kind: HorizontalPodAutoscaler' "$tmp_dir/fixed-gateway.yaml"
grep -q '^  replicas: 2' "$tmp_dir/fixed-gateway-deployment.yaml"

expect_template_failure 'kubernetesMetrics.mode must be controller or scraper' \
  helm template invalid-mode "$chart_package" "${common_args[@]}" --set kubernetesMetrics.mode=invalid
expect_template_failure 'kubernetesMetrics.replicas must be 1' \
  helm template invalid-scraper "$chart_package" "${common_args[@]}" --set kubernetesMetrics.mode=scraper --set kubernetesMetrics.replicas=2
expect_template_failure 'telemetryGateway.replicas must be at least 2' \
  helm template invalid-gateway "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.autoscaling.enabled=false --set telemetryGateway.replicas=1
expect_template_failure 'telemetryGateway.memoryLimiter.limitMiB must be at most 80%' \
  helm template invalid-limiter "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.memoryLimiter.limitMiB=900
expect_template_failure 'telemetryGateway.batch requires 0 < sendSize <= maxSize <= 4096' \
  helm template invalid-batch "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.batch.maxSize=5000
expect_template_failure 'targetMemoryAverageValue must be positive and at most 80% of the memory_limiter soft limit' \
  helm template invalid-hpa-memory "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.autoscaling.enabled=true --set telemetryGateway.autoscaling.targetMemoryAverageValue=600Mi
expect_template_failure 'targetCPUUtilizationPercentage must be between 1 and 100' \
  helm template invalid-hpa-cpu "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.autoscaling.enabled=true --set telemetryGateway.autoscaling.targetCPUUtilizationPercentage=0
expect_template_failure 'scaleDownMaxPods must be at least 1' \
  helm template invalid-hpa-scale-down-pods "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.autoscaling.enabled=true --set telemetryGateway.autoscaling.scaleDownMaxPods=-1
expect_template_failure 'scaleDownStabilizationWindowSeconds must be between 0 and 3600' \
  helm template invalid-hpa-scale-down-window "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.autoscaling.enabled=true --set telemetryGateway.autoscaling.scaleDownStabilizationWindowSeconds=3601
expect_template_failure 'scaleDownPeriodSeconds must be between 1 and 1800' \
  helm template invalid-hpa-scale-down-period "$chart_package" "${common_args[@]}" --set telemetryGateway.mode=deployment --set telemetryGateway.autoscaling.enabled=true --set telemetryGateway.autoscaling.scaleDownPeriodSeconds=1801
expect_template_failure 'upgrade.migrationHook.timeout must be a whole second, minute, or hour duration' \
  helm template invalid-upgrade-timeout "$chart_package" "${common_args[@]}" --is-upgrade --set upgrade.migrationHook.timeout=1m30s

echo "Kubernetes Helm chart validation passed"
