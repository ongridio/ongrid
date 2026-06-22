#!/usr/bin/env bash
# ongrid upgrade.sh - in-place upgrade, preserves .env and data volume.
# Run from inside a freshly extracted newer tarball.

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)
cd "$SCRIPT_DIR"

if [[ -t 1 ]]; then
    C_RED=$'\033[0;31m'; C_GREEN=$'\033[0;32m'; C_YELLOW=$'\033[1;33m'
    C_CYAN=$'\033[0;36m'; C_BOLD=$'\033[1m'; C_RESET=$'\033[0m'
else
    C_RED=''; C_GREEN=''; C_YELLOW=''; C_CYAN=''; C_BOLD=''; C_RESET=''
fi

log_info()  { printf '%s[INFO]%s %s\n'  "$C_GREEN"  "$C_RESET" "$*"; }
log_warn()  { printf '%s[WARN]%s %s\n'  "$C_YELLOW" "$C_RESET" "$*"; }
log_error() { printf '%s[ERROR]%s %s\n' "$C_RED"    "$C_RESET" "$*" >&2; }

generate_self_signed_tls_cert() {
    local cert_dir="$1"
    local cert_file="$cert_dir/tls.crt"
    local key_file="$cert_dir/tls.key"
    local openssl_conf openssl_output

    command -v openssl >/dev/null 2>&1 || {
        log_error "openssl not found; cannot generate self-signed cert"
        log_error "install openssl, or drop tls.crt + tls.key into $cert_dir/ and re-run"
        return 1
    }

    openssl_conf=$(mktemp "${TMPDIR:-/tmp}/ongrid-openssl.XXXXXX.cnf") || {
        log_error "failed to create temporary OpenSSL config"
        return 1
    }
    cat >"$openssl_conf" <<'EOF'
[req]
distinguished_name = dn
prompt = no
x509_extensions = v3_req

[dn]
CN = ongrid

[v3_req]
subjectAltName = DNS:ongrid,DNS:localhost,IP:127.0.0.1
EOF

    if ! openssl_output=$(openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
        -keyout "$key_file" \
        -out "$cert_file" \
        -config "$openssl_conf" \
        -extensions v3_req 2>&1); then
        log_error "failed to generate self-signed TLS cert using $(openssl version 2>/dev/null || printf 'openssl')"
        [[ -z "$openssl_output" ]] || printf '%s\n' "$openssl_output" >&2
        rm -f "$openssl_conf" "$cert_file" "$key_file"
        return 1
    fi

    rm -f "$openssl_conf"
    chmod 600 "$key_file"
    chmod 644 "$cert_file"
}

docker_supports_host_gateway() {
    local version major minor
    version=$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)
    version=${version%%-*}
    version=${version%%+*}
    IFS=. read -r major minor _ <<<"$version"

    [[ "$major" =~ ^[0-9]+$ && "$minor" =~ ^[0-9]+$ ]] || return 1
    (( major > 20 || (major == 20 && minor >= 10) ))
}

detect_docker_bridge_gateway() {
    local gateway
    gateway=$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null | head -n 1 || true)
    if [[ -z "$gateway" ]] && command -v ip >/dev/null 2>&1; then
        gateway=$(ip -4 addr show docker0 2>/dev/null | sed -n -E 's/.*inet ([0-9.]+)\/.*/\1/p' | head -n 1 || true)
    fi
    printf '%s' "$gateway"
}

set_env_value() {
    local key="$1" value="$2" esc
    esc=$(printf '%s' "$value" | sed -e 's/[\\|&]/\\&/g')
    if grep -qE "^${key}=" "$ENV_FILE"; then
        sed -i.bak -E "s|^${key}=.*|${key}=${esc}|" "$ENV_FILE"
        rm -f "${ENV_FILE}.bak"
    else
        printf '%s=%s\n' "$key" "$value" >> "$ENV_FILE"
    fi
}

ensure_host_gateway_env() {
    local configured gateway
    configured=$(grep -E '^ONGRID_HOST_GATEWAY=' "$ENV_FILE" 2>/dev/null | tail -n 1 | cut -d= -f2- || true)
    if [[ -n "$configured" && "$configured" != "host-gateway" ]]; then
        log_info "ONGRID_HOST_GATEWAY=${configured} (from .env)"
        return
    fi

    if docker_supports_host_gateway; then
        return
    fi

    gateway=$(detect_docker_bridge_gateway)
    if [[ -z "$gateway" ]]; then
        log_error "docker daemon does not support host-gateway and docker bridge gateway could not be detected"
        log_error "set ONGRID_HOST_GATEWAY=<docker0 gateway IP> in ${ENV_FILE}, then re-run sudo ./upgrade.sh"
        return 1
    fi

    set_env_value ONGRID_HOST_GATEWAY "$gateway"
    log_warn "docker daemon does not support host-gateway; using ONGRID_HOST_GATEWAY=${gateway}"
}

trap 'log_error "upgrade failed at line $LINENO"' ERR

if [[ $EUID -ne 0 ]]; then
    log_warn "not running as root; re-executing via sudo"
    exec sudo -E bash "$0" "$@"
fi

command -v docker >/dev/null 2>&1 || { log_error "docker CLI not found"; exit 1; }
docker info >/dev/null 2>&1 || { log_error "docker daemon not reachable"; exit 1; }
docker compose version >/dev/null 2>&1 || { log_error "docker compose v2 required"; exit 1; }

INSTALL_DIR="${ONGRID_INSTALL_DIR:-/opt/ongrid}"
ENV_FILE="$INSTALL_DIR/.env"

if [[ ! -f "$ENV_FILE" ]]; then
    log_error "no existing install found at $INSTALL_DIR/.env"
    log_error "run install.sh for a fresh install, not upgrade.sh"
    exit 1
fi

log_info "upgrading ongrid at $INSTALL_DIR"

# Determine new version from tarball.
NEW_VERSION=""
if [[ -f "$SCRIPT_DIR/VERSION" ]]; then
    NEW_VERSION=$(tr -d '[:space:]' < "$SCRIPT_DIR/VERSION" || true)
fi
if [[ -z "$NEW_VERSION" ]]; then
    NEW_VERSION=$(grep -E '^ONGRID_VERSION=' "$SCRIPT_DIR/.env.example" | cut -d= -f2- | tr -d '[:space:]' || true)
fi
[[ -n "$NEW_VERSION" ]] || { log_error "cannot determine new version"; exit 1; }
log_info "new version: ${NEW_VERSION}"

OLD_VERSION=$(grep -E '^ONGRID_VERSION=' "$ENV_FILE" | cut -d= -f2- | tr -d '[:space:]' || true)
log_info "old version: ${OLD_VERSION:-unknown}"

# Stop stack first so legacy named volumes (if any) aren't being written
# to during migration, and so bind-mount paths can be safely chowned.
log_info "stopping stack"
(
    cd "$INSTALL_DIR"
    # No explicit -f so docker-compose.override.yml (if present) auto-loads.
    # Naming -f docker-compose.yml disables override discovery, which silently
    # dropped operator env overrides (the 2026-05-19 ONGRID_INVESTIGATOR_ENABLED
    # regression where the structured RCA investigator was unwired after every
    # upgrade and only the legacy investigator ran).
    docker compose --env-file .env down || true
)

# ---------- host data dirs (bind-mount targets) ----------
# Same shape as install.sh — every upgrade re-asserts dir ownership in
# case the operator deleted/renamed a dir or the image uid changed.
ONGRID_DATA_DIR="${ONGRID_DATA_DIR:-/var/lib/ongrid}"
ONGRID_LOG_DIR="${ONGRID_LOG_DIR:-/var/log/ongrid}"
log_info "data dir: $ONGRID_DATA_DIR  (override via ONGRID_DATA_DIR)"
log_info "log dir:  $ONGRID_LOG_DIR  (override via ONGRID_LOG_DIR)"

mkdir -p \
    "$ONGRID_DATA_DIR/mysql" \
    "$ONGRID_DATA_DIR/prometheus" \
    "$ONGRID_DATA_DIR/loki" \
    "$ONGRID_DATA_DIR/tempo" \
    "$ONGRID_DATA_DIR/qdrant" \
    "$ONGRID_DATA_DIR/grafana" \
    "$ONGRID_DATA_DIR/embeddings" \
    "$ONGRID_LOG_DIR"

# Embedding model cache (ADR-027 Phase-2). Same staging logic as
# install.sh so the bundled BGE model lands on the host the first
# time an upgrade includes it. Idempotent — skip if already there.
chown -R 65532:65532 "$ONGRID_DATA_DIR/embeddings" 2>/dev/null || true
chmod -R 0755 "$ONGRID_DATA_DIR/embeddings" 2>/dev/null || true
if [[ -d "$SCRIPT_DIR/embeddings/fast-bge-small-zh-v1.5" ]]; then
    target="$ONGRID_DATA_DIR/embeddings/fast-bge-small-zh-v1.5"
    if [[ ! -f "$target/model_optimized.onnx" ]]; then
        log_info "staging bundled embedding model → $target"
        mkdir -p "$target"
        cp -rf "$SCRIPT_DIR/embeddings/fast-bge-small-zh-v1.5/." "$target/"
        chown -R 65532:65532 "$target"
    fi
fi

# Detect legacy docker named volumes from pre-bind-mount installs. If
# any are still around, the new compose would start with empty bind
# mounts — operator would see "fresh install" symptoms (no devices, no
# alert history, no Grafana dashboards) and the live data would sit
# orphaned in /var/lib/docker/volumes/. Refuse to bring the stack up
# unless --migrate-volumes was passed (auto-copies) OR --no-migrate-volumes
# (operator promises to migrate manually per README "数据卷迁移").
# IMPORTANT: docker-compose prefixes named volumes with the project name
# (default = install-dir basename, i.e. "ongrid" → real volume names look
# like ongrid_qdrant_data). The pre-v0.7.45 compose declared bare names
# (qdrant_data) which compose then prefixed; older installs that started
# with `docker compose` at /opt/ongrid/ end up with ongrid_<name>_data.
# We list both forms — first hit wins per dst. The 2026-05-19 test-env
# migration lost 521MB of mysql + 121MB of qdrant (knowledge base) +
# 547MB of prometheus TSDB because v0.7.45's upgrade.sh only looked at
# the bare names. Don't repeat that.
declare -A LEGACY_VOL_TO_DST=(
    [ongrid_ongrid_mysql_data]="$ONGRID_DATA_DIR/mysql"
    [ongrid_mysql_data]="$ONGRID_DATA_DIR/mysql"
    [mysql_data]="$ONGRID_DATA_DIR/mysql"
    [ongrid_prometheus_data]="$ONGRID_DATA_DIR/prometheus"
    [prometheus_data]="$ONGRID_DATA_DIR/prometheus"
    [ongrid_loki_data]="$ONGRID_DATA_DIR/loki"
    [loki_data]="$ONGRID_DATA_DIR/loki"
    [ongrid_tempo_data]="$ONGRID_DATA_DIR/tempo"
    [tempo_data]="$ONGRID_DATA_DIR/tempo"
    [ongrid_qdrant_data]="$ONGRID_DATA_DIR/qdrant"
    [qdrant_data]="$ONGRID_DATA_DIR/qdrant"
    [ongrid_grafana_data]="$ONGRID_DATA_DIR/grafana"
    [grafana_data]="$ONGRID_DATA_DIR/grafana"
    [ongrid_ongrid_logs]="$ONGRID_LOG_DIR"
    [ongrid_logs]="$ONGRID_LOG_DIR"
)
LEGACY_FOUND=()
for v in "${!LEGACY_VOL_TO_DST[@]}"; do
    if docker volume inspect "$v" >/dev/null 2>&1; then
        LEGACY_FOUND+=("$v")
    fi
done

MIGRATE_VOLUMES="${MIGRATE_VOLUMES:-}"
NO_MIGRATE_VOLUMES="${NO_MIGRATE_VOLUMES:-}"
for arg in "$@"; do
    case "$arg" in
        --migrate-volumes) MIGRATE_VOLUMES=1 ;;
        --no-migrate-volumes) NO_MIGRATE_VOLUMES=1 ;;
    esac
done

if (( ${#LEGACY_FOUND[@]} > 0 )); then
    if [[ -n "$MIGRATE_VOLUMES" ]]; then
        log_warn "migrating legacy named volumes to $ONGRID_DATA_DIR (this can take minutes for large TSDBs)"
        # Prefer the larger legacy volume when multiple candidates map to
        # the same dst (e.g. an old `ongrid_mysql_data` orphan AND the
        # active `ongrid_ongrid_mysql_data` both claim /var/lib/ongrid/mysql).
        # Picking by size is a heuristic but matches real-world usage —
        # the active volume is always the biggest.
        declare -A SIZE_BY_DST=()
        declare -A SRC_BY_DST=()
        for v in "${LEGACY_FOUND[@]}"; do
            d="${LEGACY_VOL_TO_DST[$v]}"
            sz=$(docker run --rm -v "$v":/d:ro alpine du -sb /d 2>/dev/null | cut -f1)
            sz=${sz:-0}
            if [[ -z "${SIZE_BY_DST[$d]:-}" ]] || (( sz > ${SIZE_BY_DST[$d]} )); then
                SIZE_BY_DST[$d]=$sz
                SRC_BY_DST[$d]=$v
            fi
        done
        for dst in "${!SRC_BY_DST[@]}"; do
            v="${SRC_BY_DST[$dst]}"
            log_info "  $v (${SIZE_BY_DST[$dst]} bytes) → $dst"
            # alpine + cp -a preserves perms. Skip if dst non-empty —
            # operator probably ran migration before; don't clobber.
            if [[ -n "$(ls -A "$dst" 2>/dev/null)" ]]; then
                log_warn "  $dst already populated; skipping ($v left intact for operator review)"
                continue
            fi
            docker run --rm \
                -v "$v":/src:ro \
                -v "$dst":/dst \
                alpine sh -c 'cp -a /src/. /dst/'
        done
        log_info "migration complete — legacy volumes preserved; remove with: docker volume rm ${LEGACY_FOUND[*]}"
    elif [[ -n "$NO_MIGRATE_VOLUMES" ]]; then
        log_warn "legacy volumes left as-is (--no-migrate-volumes): ${LEGACY_FOUND[*]}"
        log_warn "new stack will start with empty data; you MUST migrate manually before users see it"
    else
        log_error "legacy docker named volumes detected: ${LEGACY_FOUND[*]}"
        log_error "v0.7.45+ uses host bind mounts. Pick one:"
        log_error "  - re-run with --migrate-volumes to auto-copy data into $ONGRID_DATA_DIR"
        log_error "  - re-run with --no-migrate-volumes if you'll migrate by hand (see README '数据卷迁移')"
        exit 1
    fi
fi

# uids must match what the upstream images run as — re-chown every
# upgrade so a tampered/renamed dir still works.
chown -R 999:999       "$ONGRID_DATA_DIR/mysql"      2>/dev/null || true
chown -R 65534:65534   "$ONGRID_DATA_DIR/prometheus" 2>/dev/null || true
chown -R 10001:10001   "$ONGRID_DATA_DIR/loki"       2>/dev/null || true
chown -R 10001:10001   "$ONGRID_DATA_DIR/tempo"      2>/dev/null || true
chown -R 472:472       "$ONGRID_DATA_DIR/grafana"    2>/dev/null || true
chmod 755 "$ONGRID_DATA_DIR" "$ONGRID_LOG_DIR"

export ONGRID_DATA_DIR ONGRID_LOG_DIR

# Overwrite shipped assets. Do NOT touch .env or certs/.
log_info "copying new docker-compose.yml / frontier.yaml / nginx.conf / prometheus / edge / VERSION"
cp -f "$SCRIPT_DIR/docker-compose.yml" "$INSTALL_DIR/docker-compose.yml"
if [[ -f "$SCRIPT_DIR/frontier.yaml" ]]; then
    cp -f "$SCRIPT_DIR/frontier.yaml" "$INSTALL_DIR/frontier.yaml"
fi
# nginx.conf is refreshed; certs/ is intentionally NOT touched so operator's
# real cert (if any) survives the upgrade (ADR-008). If certs/ is empty
# (first upgrade onto a pre-nginx install), generate a self-signed cert.
if [[ -f "$SCRIPT_DIR/nginx.conf" ]]; then
    cp -f "$SCRIPT_DIR/nginx.conf" "$INSTALL_DIR/nginx.conf"
fi
mkdir -p "$INSTALL_DIR/certs"
chmod 700 "$INSTALL_DIR/certs"
if [[ ! -f "$INSTALL_DIR/certs/tls.crt" || ! -f "$INSTALL_DIR/certs/tls.key" ]]; then
    log_info "no TLS cert under $INSTALL_DIR/certs; generating self-signed (365d, CN=ongrid)"
    generate_self_signed_tls_cert "$INSTALL_DIR/certs"
    log_warn "self-signed cert: replace with real one in $INSTALL_DIR/certs/ later"
fi
if [[ -f "$SCRIPT_DIR/VERSION" ]]; then
    cp -f "$SCRIPT_DIR/VERSION" "$INSTALL_DIR/VERSION"
fi
# ADR-012: Loki single-node config. Bind-mounted by the loki container.
if [[ -f "$SCRIPT_DIR/loki-config.yaml" ]]; then
    cp -f "$SCRIPT_DIR/loki-config.yaml" "$INSTALL_DIR/loki-config.yaml"
fi
# ADR-013: Tempo single-node config. Bind-mounted by the tempo container.
if [[ -f "$SCRIPT_DIR/tempo-config.yaml" ]]; then
    cp -f "$SCRIPT_DIR/tempo-config.yaml" "$INSTALL_DIR/tempo-config.yaml"
fi
# searxng/settings.yml — bind-mounted into searxng container. Refresh on
# every upgrade so any config tweak (rate limiter / engines) ships with
# the new version.
if [[ -d "$SCRIPT_DIR/searxng" ]]; then
    mkdir -p "$INSTALL_DIR/searxng"
    cp -rf "$SCRIPT_DIR/searxng/." "$INSTALL_DIR/searxng/"
fi
# ADR-009: refresh the flat prometheus.yml that the post-ADR-009 compose
# bind-mounts. The legacy prometheus/ subdir is still mirrored for older
# installs that did not migrate yet.
if [[ -f "$SCRIPT_DIR/prometheus.yml" ]]; then
    cp -f "$SCRIPT_DIR/prometheus.yml" "$INSTALL_DIR/prometheus.yml"
fi
# ADR-026 self-obs alert rules — refreshed every upgrade alongside prometheus.yml.
if [[ -f "$SCRIPT_DIR/prometheus-rules.yml" ]]; then
    cp -f "$SCRIPT_DIR/prometheus-rules.yml" "$INSTALL_DIR/prometheus-rules.yml"
fi
if [[ -d "$SCRIPT_DIR/prometheus" ]]; then
    rm -rf "$INSTALL_DIR/prometheus"
    mkdir -p "$INSTALL_DIR/prometheus"
    cp -rf "$SCRIPT_DIR/prometheus/." "$INSTALL_DIR/prometheus/"
fi
if [[ -d "$SCRIPT_DIR/grafana" ]]; then
    rm -rf "$INSTALL_DIR/grafana"
    mkdir -p "$INSTALL_DIR/grafana"
    cp -rf "$SCRIPT_DIR/grafana/." "$INSTALL_DIR/grafana/"
fi
if [[ -d "$SCRIPT_DIR/edge" ]]; then
    rm -rf "$INSTALL_DIR/edge"
    mkdir -p "$INSTALL_DIR/edge"
    cp -rf "$SCRIPT_DIR/edge/." "$INSTALL_DIR/edge/"
    find "$INSTALL_DIR/edge" -maxdepth 1 -name '*.sh' -exec chmod 755 {} \;
    # Reassemble the ADR-024 one-button upgrade bundle from the loose edge
    # binaries (no longer double-packed in the tarball — see install.sh /
    # deploy/install/edge/build-edge-bundle.sh). Best-effort; warn on failure.
    if [[ -x "$INSTALL_DIR/edge/build-edge-bundle.sh" && -n "$NEW_VERSION" ]]; then
        for _edge_arch in linux-amd64 linux-arm64; do
            "$INSTALL_DIR/edge/build-edge-bundle.sh" "$INSTALL_DIR/edge" "$NEW_VERSION" "$_edge_arch" \
                || log_warn "edge upgrade bundle rebuild failed for $_edge_arch; one-button edge upgrade unavailable for that arch until next upgrade"
        done
    fi
fi

# Load new ongrid (manager) and frontier (broker) images. Both ship in the
# tarball (ADR-007); upstream Docker Hub pull is not relied upon.
if [[ -f "$SCRIPT_DIR/images/ongrid.tar" ]]; then
    log_info "loading ongrid:${NEW_VERSION} image"
    docker load -i "$SCRIPT_DIR/images/ongrid.tar"
else
    log_warn "images/ongrid.tar not found; assuming ongrid:${NEW_VERSION} already present"
fi
if [[ -f "$SCRIPT_DIR/images/frontier.tar" ]]; then
    log_info "loading frontier broker image"
    docker load -i "$SCRIPT_DIR/images/frontier.tar"
else
    log_warn "images/frontier.tar not found; assuming frontier image already present"
fi
if [[ -f "$SCRIPT_DIR/images/ongrid-web.tar" ]]; then
    log_info "loading ongrid-web (frontend + nginx) image"
    docker load -i "$SCRIPT_DIR/images/ongrid-web.tar"
else
    log_warn "images/ongrid-web.tar not found; assuming ongrid-web image already present"
fi
docker image inspect "ongrid:${NEW_VERSION}" >/dev/null 2>&1 || {
    log_error "ongrid:${NEW_VERSION} not present after docker load"
    exit 1
}

# Bump ONGRID_VERSION in .env only.
sed -i.bak -E "s|^ONGRID_VERSION=.*|ONGRID_VERSION=${NEW_VERSION}|" "$ENV_FILE"
rm -f "${ENV_FILE}.bak"

# Backfill new required keys that older .env files predate. New compose
# stanzas may use `${VAR:?...}` to force a value; without this, `compose
# up` after upgrade hard-fails. Each block: detect missing, gen+append.
backfill_secret() {
    local key="$1" len="${2:-24}"
    if ! grep -qE "^${key}=" "$ENV_FILE"; then
        local v
        v=$(openssl rand -base64 48 | tr -d '=+/\n' | cut -c1-"$len" || true)
        printf '%s=%s\n' "$key" "$v" >> "$ENV_FILE"
        log_info "backfilled ${key}"
    fi
}
backfill_plain() {
    local key="$1" val="$2"
    if ! grep -qE "^${key}=" "$ENV_FILE"; then
        printf '%s=%s\n' "$key" "$val" >> "$ENV_FILE"
        log_info "backfilled ${key}=${val}"
    fi
}
# v0.7.20+: Grafana admin pin needed for SA token bootstrap.
backfill_plain  GRAFANA_ADMIN_USER     admin
backfill_secret GRAFANA_ADMIN_PASSWORD 20
ensure_host_gateway_env

chmod 600 "$ENV_FILE"

# Bring stack back up; gorm AutoMigrate handles schema diff.
log_info "starting stack with new version"
(
    cd "$INSTALL_DIR"
    docker compose --env-file .env up -d
)

# v0.7.20+: existing Grafana volumes from older installs predate
# GF_SECURITY_ADMIN_PASSWORD being set in compose. Grafana only honors
# that env on the very first start; on subsequent boots the password is
# whatever's in its sqlite. Force-reset it so manager's bootstrap
# goroutine can basic-auth using the .env value. Idempotent: resetting
# to the same value Grafana already holds is a no-op for behavior.
GRAFANA_PWD=$(grep -E '^GRAFANA_ADMIN_PASSWORD=' "$ENV_FILE" | cut -d= -f2- || true)
if [[ -n "$GRAFANA_PWD" ]]; then
    log_info "syncing Grafana admin password from .env"
    # Wait for Grafana sqlite to be ready (cli refuses while migrations run).
    for i in $(seq 1 20); do
        if docker exec ongrid-grafana grafana cli admin reset-admin-password "$GRAFANA_PWD" >/dev/null 2>&1; then
            log_info "Grafana admin password synced (took ~$((i*2))s)"
            break
        fi
        sleep 2
        if [[ $i -eq 20 ]]; then
            log_warn "could not sync Grafana admin password after 40s — bootstrap may fail; reset manually with:"
            log_warn "  docker exec ongrid-grafana grafana cli admin reset-admin-password \"\$(grep ^GRAFANA_ADMIN_PASSWORD= $ENV_FILE | cut -d= -f2-)\""
        fi
    done
    # Manager's bootstrap goroutine runs ~10s after its own startup. Restart
    # ongrid so it re-fires the bootstrap with the now-aligned admin pwd.
    docker restart ongrid >/dev/null 2>&1 || true
fi

ONGRID_HTTP_PORT=$(grep -E '^ONGRID_HTTP_PORT=' "$ENV_FILE" | cut -d= -f2- || echo 443)
: "${ONGRID_HTTP_PORT:=443}"

# nginx terminates TLS on host port ${ONGRID_HTTP_PORT}; -k tolerates
# self-signed cert so existing installs keep working post-upgrade.
log_info "waiting for /healthz on https://localhost:${ONGRID_HTTP_PORT} (up to 90s)"
HEALTH_OK=0
for i in $(seq 1 45); do
    if curl -fsSk "https://localhost:${ONGRID_HTTP_PORT}/healthz" >/dev/null 2>&1; then
        HEALTH_OK=1
        log_info "ongrid healthy (took ~$((i*2))s)"
        break
    fi
    printf '.'
    sleep 2
done
printf '\n'
if [[ $HEALTH_OK -eq 0 ]]; then
    log_warn "ongrid did not become healthy within 90s"
    log_warn "check: docker compose -f $INSTALL_DIR/docker-compose.yml logs ongrid"
fi

# ---------- post-success cleanup (only when healthy) ----------
# Each upgrade leaves three things on disk that build up over many
# version bumps and have bitten today (2 disk-full incidents):
#   1. /tmp/ongrid-vN-linux-<arch>/ — the extracted release tree
#      (1+ GB per version, never reused after install)
#   2. Loaded docker images from old versions (ongrid:vN, ongrid-web:vN)
#      — Docker keeps them forever; one set is ~500 MB
#   3. The release tarball itself in $INSTALL_DIR (430 MB per version)
#
# Skip cleanup on failed upgrades — operator may want the artefacts
# to debug.
if [[ $HEALTH_OK -eq 1 ]]; then
    log_info "post-upgrade cleanup (older artefacts)"

    # (1) Drop extracted /tmp/ongrid-v*/ dirs except the current upgrade.
    # NB: SCRIPT_DIR for this upgrade is typically /tmp/ongrid-<NEW>/,
    # so the basename = the dir we keep.
    CURRENT_TMP=$(basename "$SCRIPT_DIR")
    for d in /tmp/ongrid-v*-linux-*; do
        [[ -d "$d" ]] || continue
        [[ "$(basename "$d")" == "$CURRENT_TMP" ]] && continue
        rm -rf "$d"
        log_info "  pruned /tmp/$(basename "$d")"
    done

    # (2) Old docker images: keep only the version compose just brought
    # up. `docker image prune -af` would also remove cached build layers
    # for unrelated workloads; filter to ongrid* repos.
    for repo in ongrid ongrid-web; do
        # List image refs matching <repo>:* and drop those that don't
        # match $NEW_VERSION. compose holds the running tag, so docker
        # won't actually delete an in-use image (it'll print "image is
        # being used by stopped container" warn and skip — harmless).
        docker images --format '{{.Repository}}:{{.Tag}}' \
            | awk -v r="$repo" -v keep="${NEW_VERSION}" '$0 ~ "^"r":" && $0 != r":"keep' \
            | xargs -r docker rmi 2>&1 | grep -v "image is being used" || true
    done

    # (3) Cap release tarballs in $INSTALL_DIR — keep the two newest
    # (so operators can roll back one version) and drop the rest.
    # Match both the legacy .tar.gz and the current .tar.xz (release packages
    # switched to xz) so upgrades from a pre-xz install still prune old gz
    # tarballs. The two explicit globs avoid .tar.* also matching .sha256.
    # `|| true` inside the group: when only one extension is present the other
    # glob stays literal and `ls` exits non-zero — harmless here, but under
    # `set -o pipefail` it would abort this best-effort cleanup step.
    { ls -1t "$INSTALL_DIR"/ongrid-v*-linux-*.tar.gz \
             "$INSTALL_DIR"/ongrid-v*-linux-*.tar.xz 2>/dev/null || true; } \
        | tail -n +3 \
        | while read -r f; do
            rm -f "$f" "${f}.sha256"
            log_info "  pruned $(basename "$f")"
        done

    log_info "disk now: $(df -h "$INSTALL_DIR" | awk 'NR==2 {print $4 " free of " $2}')"
fi

echo ""
echo "${C_BOLD}${C_CYAN}===============================================================${C_RESET}"
echo "${C_BOLD}${C_GREEN}  upgrade complete${C_RESET}"
echo "${C_BOLD}${C_CYAN}===============================================================${C_RESET}"
echo "${C_BOLD}From:${C_RESET}  ${OLD_VERSION:-unknown}"
echo "${C_BOLD}To:${C_RESET}    ${NEW_VERSION}"
echo ""
echo "Changelog: see CHANGELOG.md in the release tarball (or GitHub releases)."
echo ""
