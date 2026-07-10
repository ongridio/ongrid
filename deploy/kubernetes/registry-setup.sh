#!/usr/bin/env bash

set -euo pipefail

REGISTRY=""
RUNTIME="auto"
SUDO=()
TMP=""

log_info() { printf '[INFO] %s\n' "$*"; }
log_ok() { printf '[OK] %s\n' "$*"; }
log_error() { printf '[ERROR] %s\n' "$*" >&2; }

usage() {
    cat <<'EOF'
Usage: registry-setup.sh --registry=HOST[:PORT] [--runtime=auto|k3s|containerd|docker]

Configures a self-signed HTTPS registry without replacing unrelated registry
settings. The default runtime mode detects K3s, containerd, or Docker.
EOF
}

cleanup() {
    if [[ -n "$TMP" ]]; then
        rm -f "$TMP"
    fi
}
trap cleanup EXIT

for arg in "$@"; do
    case "$arg" in
        --registry=*) REGISTRY="${arg#*=}" ;;
        --runtime=*) RUNTIME="${arg#*=}" ;;
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
    auto|k3s|containerd|docker) ;;
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
    run_root systemctl is-active --quiet "$1"
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || {
        log_error "$1 is required"
        exit 1
    }
}

detect_runtime() {
    if service_active k3s || service_active k3s-agent; then
        printf 'k3s\n'
    elif command -v containerd >/dev/null 2>&1 && service_active containerd; then
        printf 'containerd\n'
    elif command -v docker >/dev/null 2>&1 && service_active docker; then
        printf 'docker\n'
    else
        log_error "could not detect an active K3s, containerd, or Docker runtime"
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

configure_k3s() {
    require_command mktemp

    local target="/etc/rancher/k3s/registries.yaml"
    local backup="${target}.bak.$(date +%s)"
    local service
    if service_active k3s; then
        service="k3s"
    elif service_active k3s-agent; then
        service="k3s-agent"
    else
        log_error "neither k3s nor k3s-agent is active"
        exit 1
    fi

    run_root mkdir -p /etc/rancher/k3s
    TMP="$(mktemp /tmp/ongrid-k3s-registries.XXXXXX)"
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
        log_info "registry $REGISTRY already exists; existing K3s settings were preserved"
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

configure_docker() {
    require_command python3
    if ! service_active docker; then
        log_error "docker service is not active"
        exit 1
    fi

    run_root mkdir -p /etc/docker
    local changed
    changed="$(run_root env REGISTRY="$REGISTRY" python3 - <<'PY'
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
    print(0)
    raise SystemExit
registries.append(registry)
if path.exists():
    shutil.copy2(path, path.with_name(f"{path.name}.bak.{int(time.time())}"))
tmp = path.with_name(f"{path.name}.ongrid.tmp")
tmp.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")
os.replace(tmp, path)
print(1)
PY
)"
    if [[ "$changed" -eq 1 ]]; then
        run_root systemctl restart docker
    fi
}

require_command systemctl
if [[ "$RUNTIME" == "auto" ]]; then
    RUNTIME="$(detect_runtime)"
fi
log_info "detected runtime: $RUNTIME"

case "$RUNTIME" in
    k3s) configure_k3s ;;
    containerd) configure_containerd ;;
    docker) configure_docker ;;
esac

log_ok "registry $REGISTRY configured for $RUNTIME"
