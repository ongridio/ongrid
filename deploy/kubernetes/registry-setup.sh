#!/usr/bin/env bash

set -euo pipefail

REGISTRY=""
RUNTIME="auto"
K3D_CLUSTER=""
SUDO=()
TMP=""

log_info() { printf '[INFO] %s\n' "$*"; }
log_ok() { printf '[OK] %s\n' "$*"; }
log_error() { printf '[ERROR] %s\n' "$*" >&2; }

usage() {
    cat <<'EOF'
Usage: registry-setup.sh --registry=HOST[:PORT] [--runtime=auto|k3s|k3d|rke2|containerd|docker]
                         [--k3d-cluster=NAME]

Configures a self-signed HTTPS registry without replacing unrelated registry
settings. The default runtime mode detects K3s, K3d, RKE2, containerd, or Docker.
EOF
}

cleanup() {
    if [[ -n "$TMP" ]]; then
        rm -rf "$TMP"
    fi
}
trap cleanup EXIT

for arg in "$@"; do
    case "$arg" in
        --registry=*) REGISTRY="${arg#*=}" ;;
        --runtime=*) RUNTIME="${arg#*=}" ;;
        --k3d-cluster=*) K3D_CLUSTER="${arg#*=}" ;;
        -h|--help) usage; exit 0 ;;
        *) log_error "unknown argument: $arg"; usage; exit 2 ;;
    esac
done

if [[ -z "$REGISTRY" ]]; then
    log_error "--registry is required"
    usage
    exit 2
fi
if [[ ! "$REGISTRY" =~ ^([A-Za-z0-9._-]+|\[[0-9A-Fa-f:]+\])(:[0-9]{1,5})?$ ]]; then
    log_error "invalid registry host: $REGISTRY"
    exit 2
fi
case "$RUNTIME" in
    auto|k3s|k3d|rke2|containerd|docker) ;;
    *) log_error "unsupported runtime: $RUNTIME"; exit 2 ;;
esac

if [[ "$(id -u)" -eq 0 ]]; then
    SUDO=()
elif command -v sudo >/dev/null 2>&1; then
    SUDO=(sudo)
else
    log_error "root or sudo is required to configure the container runtime"
    exit 1
fi

run_root() {
    "${SUDO[@]}" "$@"
}

service_active() {
    command -v systemctl >/dev/null 2>&1 || return 1
    run_root systemctl is-active --quiet "$1"
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || {
        log_error "$1 is required"
        exit 1
    }
}

detect_runtime() {
    if service_active rke2-server || service_active rke2-agent; then
        printf 'rke2\n'
    elif service_active k3s || service_active k3s-agent; then
        printf 'k3s\n'
    elif command -v k3d >/dev/null 2>&1 && command -v docker >/dev/null 2>&1 &&
        [[ -n "$(k3d cluster list --no-headers 2>/dev/null)" ]]; then
        printf 'k3d\n'
    elif command -v containerd >/dev/null 2>&1 && service_active containerd; then
        printf 'containerd\n'
    elif command -v docker >/dev/null 2>&1 && service_active docker; then
        printf 'docker\n'
    else
        log_error "could not detect an active K3s, K3d, RKE2, containerd, or Docker runtime"
        exit 1
    fi
}

configure_containerd() {
    require_command containerd
    require_command mktemp
    if ! service_active containerd; then
        log_error "containerd service is not active"
        exit 1
    fi

    run_root mkdir -p /etc/containerd
    if ! run_root test -f /etc/containerd/config.toml; then
        TMP="$(mktemp /tmp/ongrid-containerd-default.XXXXXX)"
        if ! containerd config default > "$TMP"; then
            log_error "failed to generate the default containerd config"
            exit 1
        fi
        local root_default="/etc/containerd/config.toml.ongrid.$$"
        run_root cp "$TMP" "$root_default"
        rm -f "$TMP"
        TMP=""
        run_root mv "$root_default" /etc/containerd/config.toml
    fi

    local containerd_major plugin_key section config_path config_changed certs_dir
    containerd_major="$(containerd --version | awk '{v=$3; sub(/^v/, "", v); split(v, p, "."); print p[1]}')"
    if run_root grep -Eq '^[[:space:]]*\[plugins\."io.containerd.cri.v1.images"' /etc/containerd/config.toml; then
        plugin_key="io.containerd.cri.v1.images"
        section='[plugins."io.containerd.cri.v1.images".registry]'
    elif run_root grep -Eq '^[[:space:]]*\[plugins\."io.containerd.grpc.v1.cri"' /etc/containerd/config.toml; then
        plugin_key="io.containerd.grpc.v1.cri"
        section='[plugins."io.containerd.grpc.v1.cri".registry]'
    elif [[ "$containerd_major" -ge 2 ]]; then
        plugin_key="io.containerd.cri.v1.images"
        section='[plugins."io.containerd.cri.v1.images".registry]'
    else
        plugin_key="io.containerd.grpc.v1.cri"
        section='[plugins."io.containerd.grpc.v1.cri".registry]'
    fi

    config_path="$(run_root awk -v plugin="$plugin_key" '
function value(line, raw, quote, end) {
  raw=line
  sub(/^[^=]*=[[:space:]]*/, "", raw)
  quote=substr(raw, 1, 1)
  if (quote != sprintf("%c", 34) && quote != sprintf("%c", 39)) return ""
  raw=substr(raw, 2)
  end=index(raw, quote)
  return end ? substr(raw, 1, end - 1) : ""
}
/^\[/ { in_target=(index($0, plugin) > 0 && $0 ~ /\.registry\]$/) }
in_target && /^[[:space:]]*config_path[[:space:]]*=/ { print value($0); exit }
' /etc/containerd/config.toml)"

    config_changed=0
    local backup=""
    if [[ -z "$config_path" ]]; then
        backup="/etc/containerd/config.toml.bak.$(date +%s)"
        TMP="$(mktemp /tmp/ongrid-containerd-config.XXXXXX)"
        run_root cp /etc/containerd/config.toml "$backup"
        if ! run_root awk -v plugin="$plugin_key" -v section="$section" '
function emit_path() { if (in_target && !path_written) { print "  config_path = \"/etc/containerd/certs.d\""; path_written=1 } }
/^\[/ {
  if (in_target) emit_path()
  in_target=(index($0, plugin) > 0 && $0 ~ /\.registry\]$/)
  if (in_target) { found=1; path_written=0 }
  print; next
}
in_target && /^[[:space:]]*config_path[[:space:]]*=/ { if (!path_written) print "  config_path = \"/etc/containerd/certs.d\""; path_written=1; next }
{ print }
END { if (in_target) emit_path(); if (!found) { print ""; print section; print "  config_path = \"/etc/containerd/certs.d\"" } }
' /etc/containerd/config.toml > "$TMP"; then
            log_error "failed to update containerd config; original config kept at $backup"
            exit 1
        fi
        if ! run_root containerd --config "$TMP" config dump >/dev/null; then
            log_error "generated containerd config is invalid; original config kept at $backup"
            exit 1
        fi
        local root_tmp="/etc/containerd/config.toml.ongrid.$$"
        run_root cp "$TMP" "$root_tmp"
        rm -f "$TMP"
        TMP=""
        run_root mv "$root_tmp" /etc/containerd/config.toml
        config_path="/etc/containerd/certs.d"
        config_changed=1
    fi

    certs_dir="${config_path%%:*}"
    if [[ "$certs_dir" != /* ]]; then
        log_error "containerd registry config_path must be absolute: $config_path"
        exit 1
    fi
    run_root mkdir -p "$certs_dir/$REGISTRY"
    run_root tee "$certs_dir/$REGISTRY/hosts.toml" >/dev/null <<EOF
server = "https://${REGISTRY}"

[host."https://${REGISTRY}"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
EOF

    if [[ "$config_changed" -eq 1 ]] && ! run_root systemctl restart containerd; then
        run_root cp "$backup" /etc/containerd/config.toml
        if ! run_root systemctl restart containerd; then
            log_error "containerd also failed to restart after rollback"
        fi
        log_error "containerd restart failed; restored $backup"
        exit 1
    fi
}

configure_rancher_runtime() {
    local runtime="$1"
    local flavor target server_service agent_service
    case "$runtime" in
        k3s)
            flavor="K3s"
            target="/etc/rancher/k3s/registries.yaml"
            server_service="k3s"
            agent_service="k3s-agent"
            ;;
        rke2)
            flavor="RKE2"
            target="/etc/rancher/rke2/registries.yaml"
            server_service="rke2-server"
            agent_service="rke2-agent"
            ;;
        *)
            log_error "unsupported Rancher runtime: $runtime"
            exit 2
            ;;
    esac
    require_command mktemp

    local backup="${target}.bak.$(date +%s)"
    local service
    if service_active "$server_service"; then
        service="$server_service"
    elif service_active "$agent_service"; then
        service="$agent_service"
    else
        log_error "neither $server_service nor $agent_service is active"
        exit 1
    fi

    run_root mkdir -p "$(dirname "$target")"
    TMP="$(mktemp "/tmp/ongrid-${runtime}-registries.XXXXXX")"
    local had_target=0 configs_count
    if run_root test -e "$target"; then
        had_target=1
        if run_root grep -q "$(printf '\t')" "$target" || run_root grep -Eq '^(---|\.\.\.)[[:space:]]*(#.*)?$' "$target"; then
            log_error "unsupported tabs or multi-document YAML in $target; original file was not changed"
            exit 1
        fi
        configs_count="$(run_root awk '/^configs:[[:space:]]*(#.*)?$/ { count++ } END { print count + 0 }' "$target")"
        if [[ "$configs_count" -gt 1 ]] || { run_root grep -Eq '^configs[[:space:]]*:' "$target" && [[ "$configs_count" -eq 0 ]]; }; then
            log_error "unsupported configs YAML shape in $target; original file was not changed"
            exit 1
        fi
        if ! run_root awk -v registry="$REGISTRY" '
function mapping_key(line, raw, quote, end, tail) {
  raw=line
  sub(/^[[:space:]]+/, "", raw)
  quote=substr(raw, 1, 1)
  if (quote == sprintf("%c", 34) || quote == sprintf("%c", 39)) {
    raw=substr(raw, 2)
    end=index(raw, quote)
    if (!end) return ""
    tail=substr(raw, end + 1)
    return tail ~ /^[[:space:]]*:/ ? substr(raw, 1, end - 1) : ""
  }
  if (raw !~ /:[[:space:]]*(#.*)?$/) return ""
  sub(/:[[:space:]]*(#.*)?$/, "", raw)
  return raw
}
function emit() {
  if (exists) return
  print indent sprintf("%c", 34) registry sprintf("%c", 34) ":"
  print indent "  tls:"
  print indent "    insecure_skip_verify: true"
  exists=1
}
BEGIN { in_configs=0; seen=0; exists=0; indent="  "; child_indent_set=0 }
/^configs:[[:space:]]*(#.*)?$/ { seen=1; in_configs=1; print; next }
in_configs && /^[^[:space:]#]/ { emit(); in_configs=0 }
in_configs && !child_indent_set && /^[[:space:]]+[^[:space:]#]/ {
  match($0, /[^[:space:]]/)
  indent=substr($0, 1, RSTART - 1)
  child_indent_set=1
}
in_configs && child_indent_set && index($0, indent) == 1 {
  rest=substr($0, length(indent) + 1)
  if (rest ~ /^[^[:space:]#]/ && mapping_key($0) == registry) exists=1
}
{ print }
END {
  if (in_configs) emit()
  if (!seen) { if (NR) print ""; print "configs:"; indent="  "; exists=0; emit() }
}
' "$target" > "$TMP"; then
            log_error "failed to merge $target; original file was not changed"
            exit 1
        fi
    else
        tee "$TMP" >/dev/null <<EOF
configs:
  "${REGISTRY}":
    tls:
      insecure_skip_verify: true
EOF
    fi

    if [[ "$had_target" -eq 1 ]] && run_root cmp -s "$target" "$TMP"; then
        log_info "registry $REGISTRY already exists; existing $flavor settings were preserved"
        return
    fi
    if [[ "$had_target" -eq 1 ]]; then
        run_root cp "$target" "$backup"
    fi
    local root_tmp="${target}.ongrid.$$"
    run_root cp "$TMP" "$root_tmp"
    rm -f "$TMP"
    TMP=""
    run_root mv "$root_tmp" "$target"

    if ! run_root systemctl restart "$service"; then
        if [[ "$had_target" -eq 1 ]]; then
            run_root cp "$backup" "$target"
        else
            run_root rm -f "$target"
        fi
        if ! run_root systemctl restart "$service"; then
            log_error "$service also failed to restart after rollback"
        fi
        log_error "$service restart failed; previous registry configuration was restored"
        exit 1
    fi
}

merge_k3s_registry_file() {
    local source="$1"
    local output="$2"

    if [[ ! -f "$source" ]]; then
        cat > "$output" <<EOF
configs:
  "${REGISTRY}":
    tls:
      insecure_skip_verify: true
EOF
        return
    fi

    if grep -q "$(printf '\t')" "$source" || grep -Eq '^(---|\.\.\.)[[:space:]]*(#.*)?$' "$source"; then
        log_error "unsupported tabs or multi-document YAML in K3s registry config"
        exit 1
    fi
    local configs_count
    configs_count="$(awk '/^configs:[[:space:]]*(#.*)?$/ { count++ } END { print count + 0 }' "$source")"
    if [[ "$configs_count" -gt 1 ]] || { grep -Eq '^configs[[:space:]]*:' "$source" && [[ "$configs_count" -eq 0 ]]; }; then
        log_error "unsupported configs YAML shape in K3s registry config"
        exit 1
    fi

    awk -v registry="$REGISTRY" '
function mapping_key(line, raw, quote, end, tail) {
  raw=line
  sub(/^[[:space:]]+/, "", raw)
  quote=substr(raw, 1, 1)
  if (quote == sprintf("%c", 34) || quote == sprintf("%c", 39)) {
    raw=substr(raw, 2)
    end=index(raw, quote)
    if (!end) return ""
    tail=substr(raw, end + 1)
    return tail ~ /^[[:space:]]*:/ ? substr(raw, 1, end - 1) : ""
  }
  if (raw !~ /:[[:space:]]*(#.*)?$/) return ""
  sub(/:[[:space:]]*(#.*)?$/, "", raw)
  return raw
}
function emit() {
  if (exists) return
  print indent sprintf("%c", 34) registry sprintf("%c", 34) ":"
  print indent "  tls:"
  print indent "    insecure_skip_verify: true"
  exists=1
}
BEGIN { in_configs=0; seen=0; exists=0; indent="  "; child_indent_set=0 }
/^configs:[[:space:]]*(#.*)?$/ { seen=1; in_configs=1; print; next }
in_configs && /^[^[:space:]#]/ { emit(); in_configs=0 }
in_configs && !child_indent_set && /^[[:space:]]+[^[:space:]#]/ {
  match($0, /[^[:space:]]/)
  indent=substr($0, 1, RSTART - 1)
  child_indent_set=1
}
in_configs && child_indent_set && index($0, indent) == 1 {
  rest=substr($0, length(indent) + 1)
  if (rest ~ /^[^[:space:]#]/ && mapping_key($0) == registry) exists=1
}
{ print }
END {
  if (in_configs) emit()
  if (!seen) { if (NR) print ""; print "configs:"; indent="  "; exists=0; emit() }
}
' "$source" > "$output"
}

configure_k3d() {
    require_command k3d
    require_command docker
    require_command mktemp

    local cluster="$K3D_CLUSTER"
    if [[ -z "$cluster" ]]; then
        local clusters cluster_count
        clusters="$(k3d cluster list --no-headers | awk '{ print $1 }')"
        cluster_count="$(printf '%s\n' "$clusters" | awk 'NF { count++ } END { print count + 0 }')"
        if [[ "$cluster_count" -ne 1 ]]; then
            log_error "found $cluster_count K3d clusters; use --k3d-cluster=NAME"
            exit 1
        fi
        cluster="$clusters"
    fi
    if ! k3d cluster list "$cluster" --no-headers | awk -v name="$cluster" '$1 == name { found=1 } END { exit !found }'; then
        log_error "K3d cluster not found: $cluster"
        exit 1
    fi

    local nodes
    nodes="$(docker ps --filter "label=k3d.cluster=$cluster" \
        --format '{{.Names}} {{.Label "k3d.role"}}' | \
        awk '$2 == "server" || $2 == "agent" { print $1 }')"
    if [[ -z "$nodes" ]]; then
        log_error "no running K3d server or agent nodes found for cluster $cluster"
        exit 1
    fi

    TMP="$(mktemp -d /tmp/ongrid-k3d-registry.XXXXXX)"
    local changed=0 timestamp node source merged had_target remote_tmp
    timestamp="$(date +%s)"
    for node in $nodes; do
        source="$TMP/${node}.source.yaml"
        merged="$TMP/${node}.merged.yaml"
        had_target=0
        if docker exec "$node" test -f /etc/rancher/k3s/registries.yaml; then
            had_target=1
            docker cp "$node:/etc/rancher/k3s/registries.yaml" "$source" >/dev/null
        fi
        merge_k3s_registry_file "$source" "$merged"
        if [[ "$had_target" -eq 1 ]] && cmp -s "$source" "$merged"; then
            continue
        fi

        docker exec "$node" mkdir -p /etc/rancher/k3s
        if [[ "$had_target" -eq 1 ]]; then
            docker exec "$node" cp /etc/rancher/k3s/registries.yaml \
                "/etc/rancher/k3s/registries.yaml.bak.$timestamp"
        fi
        remote_tmp="/tmp/ongrid-registries-${timestamp}.yaml"
        docker cp "$merged" "$node:$remote_tmp" >/dev/null
        docker exec "$node" mv "$remote_tmp" /etc/rancher/k3s/registries.yaml
        changed=$((changed + 1))
    done

    if [[ "$changed" -eq 0 ]]; then
        log_info "registry $REGISTRY already exists on all K3d nodes"
        return
    fi

    log_info "configured $changed K3d nodes; restarting cluster $cluster"
    k3d cluster stop "$cluster"
    if ! k3d cluster start "$cluster"; then
        log_error "K3d cluster failed to restart; node backups use suffix .bak.$timestamp"
        exit 1
    fi
}

configure_docker() {
    require_command python3
    if ! service_active docker; then
        log_error "docker service is not active"
        exit 1
    fi

    run_root mkdir -p /etc/docker
    local result changed backup
    result="$(run_root env REGISTRY="$REGISTRY" python3 - <<'PY'
import json
import os
import shutil
import time
from pathlib import Path

path = Path('/etc/docker/daemon.json')
registry = os.environ['REGISTRY']
data = {}
if path.exists() and path.read_text().strip():
    data = json.loads(path.read_text())
registries = data.setdefault('insecure-registries', [])
if registry in registries:
    print('0|')
    raise SystemExit
registries.append(registry)
backup = ''
if path.exists():
    backup_path = path.with_name(f"{path.name}.bak.{int(time.time())}")
    shutil.copy2(path, backup_path)
    backup = str(backup_path)
tmp = path.with_name(f"{path.name}.ongrid.tmp")
tmp.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")
os.replace(tmp, path)
print(f'1|{backup}')
PY
)"
    changed="${result%%|*}"
    backup="${result#*|}"
    if [[ "$changed" -eq 1 ]]; then
        if ! run_root systemctl restart docker; then
            if [[ -n "$backup" ]]; then
                run_root cp "$backup" /etc/docker/daemon.json
            else
                run_root rm -f /etc/docker/daemon.json
            fi
            if ! run_root systemctl restart docker; then
                log_error "docker also failed to restart after rollback"
            fi
            log_error "docker restart failed; previous configuration was restored"
            exit 1
        fi
    fi
}

if [[ "$RUNTIME" == "auto" ]]; then
    RUNTIME="$(detect_runtime)"
fi
log_info "detected runtime: $RUNTIME"

case "$RUNTIME" in
    k3s|rke2) configure_rancher_runtime "$RUNTIME" ;;
    k3d) configure_k3d ;;
    containerd) configure_containerd ;;
    docker) configure_docker ;;
esac

log_ok "registry $REGISTRY configured for $RUNTIME"
