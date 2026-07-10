#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SETUP_SCRIPT="$ROOT_DIR/deploy/kubernetes/registry-setup.sh"
FIXTURE_REGISTRY="${REGISTRY_TEST_HOST:-manager.example:8443}"
PULL_IMAGE="${REGISTRY_TEST_PULL_IMAGE:-}"
CONTAINERD_IMAGE="${REGISTRY_TEST_CONTAINERD_IMAGE:-kindest/node:v1.34.0}"
K3D_IMAGE="${REGISTRY_TEST_K3D_IMAGE:-rancher/k3s:v1.35.5-k3s1}"
DEBIAN_IMAGE="${REGISTRY_TEST_DEBIAN_IMAGE:-debian:12-slim}"
PYTHON_IMAGE="${REGISTRY_TEST_PYTHON_IMAGE:-python:3.12-slim}"
DIND_IMAGE="${REGISTRY_TEST_DIND_IMAGE:-docker:27-dind}"

CONTAINERS=()
CLUSTERS=()
TEMP_DIRS=()

log() { printf '[registry-test] %s\n' "$*"; }
fail() { printf '[registry-test] ERROR: %s\n' "$*" >&2; exit 1; }
without_proxy() {
    env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY \
        -u http_proxy -u https_proxy -u all_proxy "$@"
}
cleanup() {
    local item
    if [[ "${#CONTAINERS[@]}" -gt 0 ]]; then
        for item in "${CONTAINERS[@]}"; do docker rm -f "$item" >/dev/null 2>&1 || true; done
    fi
    if [[ "${#CLUSTERS[@]}" -gt 0 ]]; then
        for item in "${CLUSTERS[@]}"; do k3d cluster delete "$item" >/dev/null 2>&1 || true; done
    fi
    if [[ "${#TEMP_DIRS[@]}" -gt 0 ]]; then
        for item in "${TEMP_DIRS[@]}"; do rm -rf "$item"; done
    fi
}
trap cleanup EXIT

command -v docker >/dev/null 2>&1 || fail "docker is required"
command -v k3d >/dev/null 2>&1 || fail "k3d is required"
[[ -f "$SETUP_SCRIPT" ]] || fail "missing $SETUP_SCRIPT"
bash -n "$SETUP_SCRIPT"

test_rancher_runtime() {
    local runtime="$1"
    local active_service="$2"
    local target="$3"
    log "testing $runtime with $active_service"
    docker run --rm -i \
        -e RUNTIME="$runtime" \
        -e ACTIVE_SERVICE="$active_service" \
        -e TARGET="$target" \
        -e REGISTRY="$FIXTURE_REGISTRY" \
        -v "$SETUP_SCRIPT:/registry-setup.sh:ro" \
        "$DEBIAN_IMAGE" bash -s <<'CONTAINER'
set -euo pipefail
mkdir -p /test-bin "$(dirname "$TARGET")"
cat > /test-bin/systemctl <<'SH'
#!/bin/sh
if [ "$1" = "is-active" ] && [ "$2" = "--quiet" ] && [ "$3" = "$ACTIVE_SERVICE" ]; then exit 0; fi
if [ "$1" = "restart" ] && [ "$2" = "$ACTIVE_SERVICE" ]; then
  echo "$ACTIVE_SERVICE" >> /tmp/restarts
  [ ! -f /tmp/fail-restart ]
  exit $?
fi
exit 1
SH
chmod +x /test-bin/systemctl
cat > "$TARGET" <<'YAML'
mirrors:
  "docker.io":
    endpoint:
      - "https://registry-1.docker.io"
configs:
  "existing.example:5000":
    tls:
      insecure_skip_verify: false
YAML
PATH="/test-bin:$PATH" /registry-setup.sh --runtime="$RUNTIME" --registry="$REGISTRY"
PATH="/test-bin:$PATH" /registry-setup.sh --runtime="$RUNTIME" --registry="$REGISTRY"
grep -q 'existing.example:5000' "$TARGET"
grep -q "$REGISTRY" "$TARGET"
test "$(wc -l < /tmp/restarts)" -eq 1
cp "$TARGET" /tmp/before-failure.yaml
touch /tmp/fail-restart
if PATH="/test-bin:$PATH" /registry-setup.sh --runtime="$RUNTIME" --registry=broken.example:8443; then exit 1; fi
cmp -s /tmp/before-failure.yaml "$TARGET"
CONTAINER
}

test_docker() {
    local tmp name
    tmp="$(mktemp -d /tmp/ongrid-registry-docker.XXXXXX)"
    TEMP_DIRS+=("$tmp")
    name="ongrid-registry-dind-$RANDOM"
    CONTAINERS+=("$name")
    log "testing Docker config, rollback, and daemon reload"
    cat > "$tmp/systemctl" <<'SH'
#!/bin/sh
if [ "$1" = "is-active" ] && [ "$2" = "--quiet" ] && [ "$3" = "docker" ]; then exit 0; fi
if [ "$1" = "restart" ] && [ "$2" = "docker" ]; then
  echo restart >> /etc/docker/restarts
  [ ! -f /etc/docker/fail-restart ]
  exit $?
fi
exit 1
SH
    chmod +x "$tmp/systemctl"
    printf '{"log-driver":"json-file","insecure-registries":["existing.example:5000"]}\n' > "$tmp/daemon.json"
    docker run --rm \
        -v "$SETUP_SCRIPT:/registry-setup.sh:ro" \
        -v "$tmp:/etc/docker" \
        -v "$tmp/systemctl:/usr/local/bin/systemctl:ro" \
        "$PYTHON_IMAGE" bash /registry-setup.sh --runtime=docker --registry="$FIXTURE_REGISTRY"
    docker run --rm \
        -v "$SETUP_SCRIPT:/registry-setup.sh:ro" \
        -v "$tmp:/etc/docker" \
        -v "$tmp/systemctl:/usr/local/bin/systemctl:ro" \
        "$PYTHON_IMAGE" bash /registry-setup.sh --runtime=docker --registry="$FIXTURE_REGISTRY"
    [[ "$(wc -l < "$tmp/restarts")" -eq 1 ]]
    cp "$tmp/daemon.json" "$tmp/before-failure.json"
    touch "$tmp/fail-restart"
    if docker run --rm \
        -v "$SETUP_SCRIPT:/registry-setup.sh:ro" \
        -v "$tmp:/etc/docker" \
        -v "$tmp/systemctl:/usr/local/bin/systemctl:ro" \
        "$PYTHON_IMAGE" bash /registry-setup.sh --runtime=docker --registry=broken.example:8443; then
        fail "Docker restart failure did not fail the script"
    fi
    cmp -s "$tmp/before-failure.json" "$tmp/daemon.json"
    rm -f "$tmp/fail-restart"

    if [[ -n "$PULL_IMAGE" ]]; then
        local registry="${PULL_IMAGE%%/*}"
        docker run --rm \
            -v "$SETUP_SCRIPT:/registry-setup.sh:ro" \
            -v "$tmp:/etc/docker" \
            -v "$tmp/systemctl:/usr/local/bin/systemctl:ro" \
            "$PYTHON_IMAGE" bash /registry-setup.sh --runtime=docker --registry="$registry"
    fi
    docker run --rm -d --privileged --name "$name" \
        -v "$tmp/daemon.json:/etc/docker/daemon.json:ro" \
        --entrypoint dockerd "$DIND_IMAGE" \
        --host=unix:///var/run/docker.sock --config-file=/etc/docker/daemon.json >/dev/null
    local i
    for i in $(seq 1 60); do
        docker exec "$name" docker info >/dev/null 2>&1 && break
        sleep 1
    done
    docker exec "$name" docker info >/dev/null
    if [[ -n "$PULL_IMAGE" ]]; then docker exec "$name" docker pull "$PULL_IMAGE" >/dev/null; fi
    docker rm -f "$name" >/dev/null
}

test_containerd() {
    local name="ongrid-registry-containerd-$RANDOM"
    CONTAINERS+=("$name")
    log "testing containerd config parsing, restart, and hosts preservation"
    docker run --rm -d --name "$name" --privileged --tmpfs /tmp --tmpfs /run "$CONTAINERD_IMAGE" >/dev/null
    local i
    for i in $(seq 1 60); do
        docker exec "$name" systemctl is-active --quiet containerd >/dev/null 2>&1 && break
        sleep 1
    done
    docker exec "$name" systemctl is-active --quiet containerd >/dev/null
    docker cp "$SETUP_SCRIPT" "$name:/root/registry-setup.sh"
    docker exec "$name" sed -i '/^[[:space:]]*config_path[[:space:]]*=/d' /etc/containerd/config.toml
    docker exec "$name" bash /root/registry-setup.sh --runtime=containerd --registry="$FIXTURE_REGISTRY"
    docker exec "$name" bash -lc \
        "containerd --config /etc/containerd/config.toml config dump >/dev/null && systemctl is-active --quiet containerd"
    docker exec "$name" mkdir -p /etc/containerd/certs.d/existing.example
    docker exec "$name" sh -c 'echo preserved > /etc/containerd/certs.d/existing.example/hosts.toml'
    docker exec "$name" sh -c 'sha256sum /etc/containerd/config.toml > /root/config.sum'
    docker exec "$name" bash /root/registry-setup.sh --runtime=containerd --registry="$FIXTURE_REGISTRY"
    docker exec "$name" sh -c \
        "sha256sum -c /root/config.sum >/dev/null && test -f /etc/containerd/certs.d/existing.example/hosts.toml"
    if [[ -n "$PULL_IMAGE" ]]; then
        local registry="${PULL_IMAGE%%/*}"
        docker exec "$name" bash /root/registry-setup.sh --runtime=containerd --registry="$registry"
        docker exec "$name" crictl pull "$PULL_IMAGE" >/dev/null
    fi
    docker rm -f "$name" >/dev/null
}

test_k3d() {
    local cluster="ongrid-registry-test-$RANDOM"
    local registry="$FIXTURE_REGISTRY"
    [[ -n "$PULL_IMAGE" ]] && registry="${PULL_IMAGE%%/*}"
    CLUSTERS+=("$cluster")
    log "testing K3d first-run configuration and idempotency"
    without_proxy k3d cluster create "$cluster" --servers 1 --agents 1 --image "$K3D_IMAGE" --wait --timeout 180s >/dev/null
    without_proxy bash "$SETUP_SCRIPT" --runtime=k3d --k3d-cluster="$cluster" --registry="$registry"
    local nodes node before after
    nodes="$(docker ps --filter "label=k3d.cluster=$cluster" --format '{{.Names}} {{.Label "k3d.role"}}' | \
        awk '$2 == "server" || $2 == "agent" { print $1 }')"
    [[ "$(printf '%s\n' "$nodes" | awk 'NF { count++ } END { print count + 0 }')" -eq 2 ]]
    for node in $nodes; do docker exec "$node" grep -q "$registry" /etc/rancher/k3s/registries.yaml; done
    before="$(for node in $nodes; do docker inspect "$node" --format '{{.Name}} {{.State.StartedAt}}'; done | sort)"
    without_proxy bash "$SETUP_SCRIPT" --runtime=k3d --k3d-cluster="$cluster" --registry="$registry"
    after="$(for node in $nodes; do docker inspect "$node" --format '{{.Name}} {{.State.StartedAt}}'; done | sort)"
    [[ "$before" == "$after" ]]
    if [[ -n "$PULL_IMAGE" ]]; then
        node="$(printf '%s\n' "$nodes" | head -1)"
        docker exec "$node" crictl pull "$PULL_IMAGE" >/dev/null
    fi
    k3d cluster delete "$cluster" >/dev/null
}

test_rancher_runtime k3s k3s /etc/rancher/k3s/registries.yaml
test_rancher_runtime k3s k3s-agent /etc/rancher/k3s/registries.yaml
test_rancher_runtime rke2 rke2-server /etc/rancher/rke2/registries.yaml
test_rancher_runtime rke2 rke2-agent /etc/rancher/rke2/registries.yaml
test_containerd
test_docker
test_k3d

log "all registry runtime tests passed"
