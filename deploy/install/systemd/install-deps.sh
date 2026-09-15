#!/usr/bin/env bash
# ongrid systemd-mode dep installer.
#
# Two responsibilities:
#   (1) apt/dnf-install OS-package deps: mariadb-server, nginx, grafana.
#       (We use the distro's grafana-oss package when available; otherwise
#       add the Grafana apt repo.)
#   (2) Download upstream Prom / Loki / Tempo / qdrant binaries, verify
#       sha256, install to /usr/local/bin/.
#
# Idempotent: re-runs skip what's already installed and at-target-version.
# Network required for the upstream binary fetches (~250 MB total).

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)

if [[ -t 1 ]]; then
    C_GREEN=$'\033[0;32m'; C_YELLOW=$'\033[1;33m'; C_RED=$'\033[0;31m'
    C_CYAN=$'\033[0;36m'; C_BOLD=$'\033[1m'; C_RESET=$'\033[0m'
else
    C_GREEN=''; C_YELLOW=''; C_RED=''; C_CYAN=''; C_BOLD=''; C_RESET=''
fi
log()  { printf '%s[INFO]%s %s\n'  "$C_GREEN"  "$C_RESET" "$*"; }
warn() { printf '%s[WARN]%s %s\n'  "$C_YELLOW" "$C_RESET" "$*"; }
err()  { printf '%s[ERROR]%s %s\n' "$C_RED"    "$C_RESET" "$*" >&2; }

# 把文件 chown 给 root:ongrid，失败时显式 warn 而非静默继续。
# install-deps.sh 可独立运行（不经 install-systemd.sh），此时 ongrid 组
# 可能尚未创建（install-systemd.sh:85-99 才创建）。静默失败会让文件归属
# 退回 root:root，违背"组可读"设计意图。helper 让所有调用点行为一致。
chown_ongrid_or_warn() {
    local file="$1"
    if ! chown root:ongrid "$file" 2>/dev/null; then
        warn "failed to chown root:ongrid $file — ongrid group missing?"
        warn "  file owned by root:root; manager/grafana may fail to read it"
        warn "  run install-systemd.sh first to create the ongrid group, then re-run"
    fi
}

if [[ $EUID -ne 0 ]]; then
    err "must run as root (sudo)"
    exit 1
fi

# -----------------------------------------------------------------------------
# flags
# -----------------------------------------------------------------------------
SKIP_GRAFANA=0
APT_TIMEOUT=600
# GH proxy — empty means "go direct to github.com/releases/...". CN
# operators commonly swap to https://ghproxy.com/https://github.com/ or
# similar to get past the github rate-limit + bandwidth crawl. Also
# honoured via env: ONGRID_GH_PROXY=...
GH_PROXY="${ONGRID_GH_PROXY:-}"
usage() {
    cat <<EOF
Usage: sudo bash install-deps.sh [OPTIONS]

Options:
  --skip-grafana          Skip grafana repo + install. Useful when:
                          - apt.grafana.com is unreachable (CN networks).
                          - operator wants to install grafana later via a
                            distro mirror or out-of-band.
                          Manager still works without grafana —
                          dashboards just won't render.
  --apt-timeout <sec>     Hard cap (default 600) for each apt/dnf install.
                          Triggered via the timeout(1) wrapper.
  --gh-proxy <prefix>     Prefix applied in front of every github.com
                          download URL — for environments behind a slow /
                          rate-limited route to github. Examples:
                            --gh-proxy https://ghproxy.com/
                            --gh-proxy https://mirror.ghproxy.com/
                          The prefix must end with '/'. Also settable via
                          ONGRID_GH_PROXY env.
  -h, --help              Print this help.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip-grafana) SKIP_GRAFANA=1; shift ;;
        --apt-timeout) APT_TIMEOUT="${2:-}"; shift 2 ;;
        --apt-timeout=*) APT_TIMEOUT="${1#*=}"; shift ;;
        --gh-proxy) GH_PROXY="${2:-}"; shift 2 ;;
        --gh-proxy=*) GH_PROXY="${1#*=}"; shift ;;
        -h|--help) usage; exit 0 ;;
        *) err "unknown flag: $1"; usage; exit 2 ;;
    esac
done

# Helper to prefix every github.com URL.
gh_url() {
    local u="$1"
    if [[ -n "$GH_PROXY" ]]; then
        # Strip trailing / from proxy if present, then re-attach as we
        # join.
        local p="${GH_PROXY%/}"
        printf '%s/%s' "$p" "$u"
    else
        printf '%s' "$u"
    fi
}

# -----------------------------------------------------------------------------
# Pinned upstream versions. These mirror what docker-compose.yml uses today,
# so behaviour is identical across modes. Bump in lock-step with the compose
# images so a re-package picks up upgrades.
# -----------------------------------------------------------------------------
PROM_VERSION=2.54.0
LOKI_VERSION=3.4.0
TEMPO_VERSION=2.5.0
QDRANT_VERSION=1.11.3
# Exporters：主机 + 进程指标采集，Prometheus scrape
NODE_EXPORTER_VERSION=1.8.2
PROCESS_EXPORTER_VERSION=0.7.10

# Pinned sha256s. Verified against upstream release manifests at package time
# where upstream publishes them. If you bump a version above, update the
# sha256 values here and in dist/package.sh together.
HOST_ARCH="${ONGRID_SYSTEMD_ARCH:-$(uname -m)}"
case "$HOST_ARCH" in
    x86_64|amd64)
        STACK_TARGET=linux-amd64
        PROM_ASSET="prometheus-${PROM_VERSION}.linux-amd64.tar.gz"
        PROM_EXTRACT_DIR="prometheus-${PROM_VERSION}.linux-amd64"
        PROM_SHA=465e1393a0cca9705598f6ffaf96ffa78d0347808ab21386b0c6aaec2cf7aa13
        LOKI_ASSET="loki-linux-amd64.zip"
        LOKI_BIN="loki-linux-amd64"
        LOKI_SHA=fb07349f21cc86eec1162d81f90ad2706280cd731eabc5456ecd8e21a5df8404
        TEMPO_ASSET="tempo_${TEMPO_VERSION}_linux_amd64.tar.gz"
        TEMPO_SHA=a708a86230fa43478e8a30174787a1171fbfdc33ad135ce1625769dbadc16e38
        QDRANT_ASSET="qdrant-x86_64-unknown-linux-gnu.tar.gz"
        QDRANT_SHA=4000a4924c118cc88296f879aad25bebb5869bb5baac7801bec8860a96396914
        # node_exporter / process_exporter
        NODE_EXPORTER_ASSET="node_exporter-${NODE_EXPORTER_VERSION}.linux-amd64.tar.gz"
        NODE_EXPORTER_EXTRACT_DIR="node_exporter-${NODE_EXPORTER_VERSION}.linux-amd64"
        NODE_EXPORTER_SHA=6809dd0b3ec45fd6e992c19071d6b5253aed3ead7bf0686885a51d85c6643c66
        PROCESS_EXPORTER_ASSET="process-exporter-${PROCESS_EXPORTER_VERSION}.linux-amd64.tar.gz"
        PROCESS_EXPORTER_EXTRACT_DIR="process-exporter-${PROCESS_EXPORTER_VERSION}.linux-amd64"
        PROCESS_EXPORTER_SHA=52503649649c0be00e74e8347c504574582b95ad428ff13172d658e82b3da1b5
        ;;
    aarch64|arm64)
        STACK_TARGET=linux-arm64
        PROM_ASSET="prometheus-${PROM_VERSION}.linux-arm64.tar.gz"
        PROM_EXTRACT_DIR="prometheus-${PROM_VERSION}.linux-arm64"
        PROM_SHA=ed50b67cb833a225ec2a53b487c6e20372b20e56dce226423fa8611c8aa50392
        LOKI_ASSET="loki-linux-arm64.zip"
        LOKI_BIN="loki-linux-arm64"
        LOKI_SHA=0e5d9aa98ccfd7114c74e87201963fe70c0de0d051b8359dd7cafe37a9f2e492
        TEMPO_ASSET="tempo_${TEMPO_VERSION}_linux_arm64.tar.gz"
        TEMPO_SHA=4c96c11e4950541fcc190be620bf8551e8b2bc645fee0883464ac8a9b363f8d6
        QDRANT_ASSET="qdrant-aarch64-unknown-linux-musl.tar.gz"
        QDRANT_SHA=e164496afa9e4cacdd5679be550f735320e51b2e74d6ce6fbcb0b8260ed4c7d3
        NODE_EXPORTER_ASSET="node_exporter-${NODE_EXPORTER_VERSION}.linux-arm64.tar.gz"
        NODE_EXPORTER_EXTRACT_DIR="node_exporter-${NODE_EXPORTER_VERSION}.linux-arm64"
        NODE_EXPORTER_SHA=627382b9723c642411c33f48861134ebe893e70a63bcc8b3fc0619cd0bfac4be
        PROCESS_EXPORTER_ASSET="process-exporter-${PROCESS_EXPORTER_VERSION}.linux-arm64.tar.gz"
        PROCESS_EXPORTER_EXTRACT_DIR="process-exporter-${PROCESS_EXPORTER_VERSION}.linux-arm64"
        PROCESS_EXPORTER_SHA=b377e673558bd0d51f5f771c2b3b3be44b60fcac0689709f47d8c7ca8136f6f5
        ;;
    *)
        err "unsupported CPU architecture: $HOST_ARCH (supported: x86_64/amd64, aarch64/arm64)"
        exit 2
        ;;
esac
log "detected arch $STACK_TARGET"

PREFIX_BIN=/usr/local/bin
DOWNLOAD_DIR=/var/cache/ongrid-install
mkdir -p "$DOWNLOAD_DIR"

# -----------------------------------------------------------------------------
# distro detect
# -----------------------------------------------------------------------------
PKG_MGR=
PKG_INSTALL=
PKG_UPDATE=
if command -v apt-get >/dev/null 2>&1; then
    PKG_MGR=apt
    PKG_INSTALL="apt-get install -y --no-install-recommends"
    PKG_UPDATE="apt-get update"
elif command -v dnf >/dev/null 2>&1; then
    PKG_MGR=dnf
    PKG_INSTALL="dnf install -y"
    PKG_UPDATE="dnf makecache"
elif command -v yum >/dev/null 2>&1; then
    PKG_MGR=yum
    PKG_INSTALL="yum install -y"
    PKG_UPDATE="yum makecache"
else
    err "no supported package manager found (apt / dnf / yum)"
    exit 2
fi
log "detected $PKG_MGR"

# -----------------------------------------------------------------------------
# step 1 — OS-package deps
# -----------------------------------------------------------------------------
log "installing OS-package deps: mariadb-server, nginx, grafana"
# 与 grafana 安装一致，用 timeout 包裹避免 CN 网络下 distro 镜像
# 不可达时挂起数小时。
timeout "$APT_TIMEOUT" $PKG_UPDATE || { err "apt/dnf update timeout or failed"; exit 1; }

# mariadb + nginx come from the distro repo unconditionally.
# libstdc++6 + libgcc-s1 are the runtime deps of libonnxruntime.so (the
# local embedder, installed below). Near-universal already, but listed so
# a minimal host doesn't fail the .so dlopen with a cryptic loader error.
case "$PKG_MGR" in
    apt)
        timeout "$APT_TIMEOUT" $PKG_INSTALL mariadb-server nginx ca-certificates curl gnupg unzip tar \
            libstdc++6 libgcc-s1 || { err "apt install deps timeout or failed"; exit 1; }
        ;;
    dnf|yum)
        timeout "$APT_TIMEOUT" $PKG_INSTALL mariadb-server nginx ca-certificates curl gnupg unzip tar \
            libstdc++ libgcc || { err "dnf/yum install deps timeout or failed"; exit 1; }
        ;;
esac

# grafana — distro packages are usually stale; pull from grafana's repo.
# Failure here is non-fatal: manager + telemetry stack still work, only
# dashboards go missing. The apt.grafana.com endpoint is known to be
# flaky over some CN networks — we hard-cap the install so a hang
# doesn't wedge the whole install-deps run.
if (( SKIP_GRAFANA )); then
    warn "grafana install skipped (--skip-grafana)"
elif command -v grafana-server >/dev/null 2>&1; then
    log "grafana already present — skipping repo setup"
else
    log "adding grafana repo + installing grafana (timeout=${APT_TIMEOUT}s)"
    set +e
    # 初始化 rc=0，避免 case 未匹配分支（PKG_MGR 未来扩展时漏写）
    # 在 set -u 下报 unbound variable。当前 line 178 已 fail-closed 退出
    # 未知 PKG_MGR，case 必匹配，但防御式编程加 default 安全。
    rc=0
    case "$PKG_MGR" in
        apt)
            install -d -m 0755 /etc/apt/keyrings
            timeout "$APT_TIMEOUT" bash -c '
                curl -fsSL --max-time 30 https://apt.grafana.com/gpg.key \
                    | gpg --dearmor -o /etc/apt/keyrings/grafana.gpg &&
                echo "deb [signed-by=/etc/apt/keyrings/grafana.gpg] https://apt.grafana.com stable main" \
                    > /etc/apt/sources.list.d/grafana.list &&
                apt-get update &&
                apt-get install -y --no-install-recommends grafana
            '
            rc=$?
            ;;
        dnf|yum)
            cat > /etc/yum.repos.d/grafana.repo <<'EOF'
[grafana]
name=grafana
baseurl=https://rpm.grafana.com
repo_gpgcheck=1
enabled=1
gpgcheck=1
gpgkey=https://rpm.grafana.com/gpg.key
sslverify=1
sslcacert=/etc/pki/tls/certs/ca-bundle.crt
EOF
            timeout "$APT_TIMEOUT" $PKG_INSTALL grafana
            rc=$?
            ;;
    esac
    set -e
    if [[ $rc -ne 0 ]]; then
        warn "grafana install failed/timeout (rc=$rc) — continuing without it"
        warn "manager + telemetry stack will run; dashboards unavailable"
        warn "to install grafana later:"
        warn "  apt-get install grafana    # from grafana repo"
        warn "OR via a CN mirror:"
        warn "  https://mirrors.tuna.tsinghua.edu.cn/grafana/apt/"
        # remove the half-written sources list so apt-get update doesn't
        # break later from a broken repo entry.
        rm -f /etc/apt/sources.list.d/grafana.list 2>/dev/null || true
        # dnf/yum 分支写的 /etc/yum.repos.d/grafana.repo 同样需清理，
        # 否则残留的损坏 repo 文件（gpgkey 拉取失败留下的半成品）会让后续
        # dnf update 持续报警告/错误，影响其他包安装。
        rm -f /etc/yum.repos.d/grafana.repo 2>/dev/null || true
    fi
fi

# -----------------------------------------------------------------------------
# step 2 — upstream binary downloads
# -----------------------------------------------------------------------------
fetch_and_verify() {
    # fetch_and_verify <name> <url> <sha256> <dst-path-in-cache>
    local name="$1" url="$2" sha="$3" dst="$4"
    if [[ -f "$dst" ]]; then
        local actual
        actual=$(sha256sum "$dst" | awk '{print $1}')
        if [[ "$actual" == "$sha" ]]; then
            log "$name cached + sha256 ok"
            return 0
        fi
        warn "$name cached but sha256 mismatch — re-downloading"
        rm -f "$dst"
    fi
    log "downloading $name → $dst"
    curl -fsSL --retry 3 --retry-delay 2 --max-time 1800 -o "$dst" "$url"
    local actual
    actual=$(sha256sum "$dst" | awk '{print $1}')
    if [[ "$actual" != "$sha" ]]; then
        err "$name sha256 mismatch! expected $sha got $actual"
        rm -f "$dst"
        return 1
    fi
    log "$name sha256 ok"
}

# install_bin <name> <src-in-extracted-dir> <dst-bin-name>
install_bin() {
    local name="$1" src="$2" dst_name="$3"
    if [[ ! -f "$src" ]]; then
        err "$name extracted but $src missing"
        return 1
    fi
    install -m 0755 -o root -g root "$src" "$PREFIX_BIN/$dst_name"
    log "installed $PREFIX_BIN/$dst_name"
}

# try_bundled <name> <dst-bin-name>
# Returns 0 + installs from the bundle when bin/stack-deps/<name> ships
# in the tarball; returns 1 otherwise. Lets release builds pre-bundle
# the four upstream binaries so an offline-network install needs zero
# github reach. Bundle layout matches what dist/package.sh writes when
# ONGRID_BUNDLE_STACK_BINS=1 was set at package time.
try_bundled() {
    local name="$1" dst_name="$2"
    local cand="$SCRIPT_DIR/../bin/stack-deps/$name"
    local marker="$SCRIPT_DIR/../bin/stack-deps/ARCH"
    if [[ -f "$cand" ]]; then
        if [[ -f "$marker" ]] && [[ "$(tr -d '[:space:]' < "$marker")" != "$STACK_TARGET" ]]; then
            warn "bundled $name is for $(tr -d '[:space:]' < "$marker"), host is $STACK_TARGET — downloading host arch"
            return 1
        fi
        install -m 0755 -o root -g root "$cand" "$PREFIX_BIN/$dst_name"
        log "installed $PREFIX_BIN/$dst_name (from bundle)"
        return 0
    fi
    return 1
}

# --- prometheus ---
if ! try_bundled prometheus prometheus; then
    PROM_TGZ="$DOWNLOAD_DIR/$PROM_ASSET"
    fetch_and_verify prometheus \
        "$(gh_url https://github.com/prometheus/prometheus/releases/download/v${PROM_VERSION}/${PROM_ASSET})" \
        "$PROM_SHA" "$PROM_TGZ"
    PROM_EXTRACT="$DOWNLOAD_DIR/$PROM_EXTRACT_DIR"
    rm -rf "$PROM_EXTRACT"
    tar -xzf "$PROM_TGZ" -C "$DOWNLOAD_DIR"
    install_bin prometheus "$PROM_EXTRACT/prometheus" prometheus
fi

# --- loki ---
if ! try_bundled loki loki; then
    LOKI_ZIP="$DOWNLOAD_DIR/$LOKI_ASSET"
    fetch_and_verify loki \
        "$(gh_url https://github.com/grafana/loki/releases/download/v${LOKI_VERSION}/${LOKI_ASSET})" \
        "$LOKI_SHA" "$LOKI_ZIP"
    rm -f "$DOWNLOAD_DIR/$LOKI_BIN"
    unzip -qo "$LOKI_ZIP" -d "$DOWNLOAD_DIR"
    install_bin loki "$DOWNLOAD_DIR/$LOKI_BIN" loki
fi

# --- tempo ---
if ! try_bundled tempo tempo; then
    TEMPO_TGZ="$DOWNLOAD_DIR/$TEMPO_ASSET"
    fetch_and_verify tempo \
        "$(gh_url https://github.com/grafana/tempo/releases/download/v${TEMPO_VERSION}/${TEMPO_ASSET})" \
        "$TEMPO_SHA" "$TEMPO_TGZ"
    TEMPO_EXTRACT="$DOWNLOAD_DIR/tempo-${TEMPO_VERSION}"
    rm -rf "$TEMPO_EXTRACT" && mkdir -p "$TEMPO_EXTRACT"
    tar -xzf "$TEMPO_TGZ" -C "$TEMPO_EXTRACT"
    install_bin tempo "$TEMPO_EXTRACT/tempo" tempo
fi

# --- qdrant ---
if ! try_bundled qdrant qdrant; then
    QDRANT_TGZ="$DOWNLOAD_DIR/$QDRANT_ASSET"
    fetch_and_verify qdrant \
        "$(gh_url https://github.com/qdrant/qdrant/releases/download/v${QDRANT_VERSION}/${QDRANT_ASSET})" \
        "$QDRANT_SHA" "$QDRANT_TGZ"
    QDRANT_EXTRACT="$DOWNLOAD_DIR/qdrant-${QDRANT_VERSION}"
    rm -rf "$QDRANT_EXTRACT" && mkdir -p "$QDRANT_EXTRACT"
    tar -xzf "$QDRANT_TGZ" -C "$QDRANT_EXTRACT"
    install_bin qdrant "$QDRANT_EXTRACT/qdrant" qdrant
fi

# --- node_exporter ---
# 二进制名 node_exporter（下划线）。监听 127.0.0.1:19100（避开 manager
# metrics 默认 :9100，见 node_exporter.service / prometheus.yml）。
if ! try_bundled node_exporter node_exporter; then
    NODE_EXPORTER_TGZ="$DOWNLOAD_DIR/$NODE_EXPORTER_ASSET"
    fetch_and_verify node_exporter \
        "$(gh_url https://github.com/prometheus/node_exporter/releases/download/v${NODE_EXPORTER_VERSION}/${NODE_EXPORTER_ASSET})" \
        "$NODE_EXPORTER_SHA" "$NODE_EXPORTER_TGZ"
    NODE_EXPORTER_EXTRACT="$DOWNLOAD_DIR/$NODE_EXPORTER_EXTRACT_DIR"
    rm -rf "$NODE_EXPORTER_EXTRACT"
    tar -xzf "$NODE_EXPORTER_TGZ" -C "$DOWNLOAD_DIR"
    install_bin node_exporter "$NODE_EXPORTER_EXTRACT/node_exporter" node_exporter
fi

# --- process_exporter ---
# 二进制名 process-exporter（连字符），安装为 process_exporter（下划线，
# 与 prometheus job_name 约定一致）。监听 127.0.0.1:9256。
if ! try_bundled process_exporter process_exporter; then
    PROCESS_EXPORTER_TGZ="$DOWNLOAD_DIR/$PROCESS_EXPORTER_ASSET"
    fetch_and_verify process_exporter \
        "$(gh_url https://github.com/ncabatoff/process-exporter/releases/download/v${PROCESS_EXPORTER_VERSION}/${PROCESS_EXPORTER_ASSET})" \
        "$PROCESS_EXPORTER_SHA" "$PROCESS_EXPORTER_TGZ"
    PROCESS_EXPORTER_EXTRACT="$DOWNLOAD_DIR/$PROCESS_EXPORTER_EXTRACT_DIR"
    rm -rf "$PROCESS_EXPORTER_EXTRACT"
    tar -xzf "$PROCESS_EXPORTER_TGZ" -C "$DOWNLOAD_DIR"
    # tarball 内是 process-exporter（连字符），安装为 process_exporter（下划线）
    install_bin process_exporter "$PROCESS_EXPORTER_EXTRACT/process-exporter" process_exporter
fi

# --- libonnxruntime.so (local ONNX embedder) ---
# The ongrid manager binary is the CGO build (fastembed-go → onnxruntime_go)
# and dlopens this .so at runtime via ONNX_PATH=/usr/lib/libonnxruntime.so
# (set in ongrid.service). package.sh extracted it from the docker image
# into bin/; install it to /usr/lib + the SONAME symlinks + ldconfig so the
# loader resolves it. Without this, ONGRID_EMBEDDING_PROVIDER=local fails to
# load the model. Compose mode bundles the .so inside the image instead.
ORT_SO=$(ls "$SCRIPT_DIR/../bin/"libonnxruntime.so.* 2>/dev/null | head -1 || true)
if [[ -n "$ORT_SO" && -f "$ORT_SO" ]]; then
    ort_base=$(basename "$ORT_SO")                 # libonnxruntime.so.1.20.1
    ort_ver="${ort_base#libonnxruntime.so.}"       # 1.20.1
    ort_major="libonnxruntime.so.${ort_ver%%.*}"   # libonnxruntime.so.1
    install -m 0755 "$ORT_SO" "/usr/lib/$ort_base"
    ln -sf "$ort_base"  "/usr/lib/$ort_major"       # libonnxruntime.so.1
    ln -sf "$ort_base"  "/usr/lib/libonnxruntime.so"
    ldconfig 2>/dev/null || true
    log "installed $ort_base → /usr/lib (+ symlinks); local embedder enabled"
else
    warn "libonnxruntime.so not bundled — ONGRID_EMBEDDING_PROVIDER=local will"
    warn "  fail to load; use an API-key embedder or rebuild the package with"
    warn "  ONNXRUNTIME bundled (see dist/package.sh)."
fi

# -----------------------------------------------------------------------------
# step 3a — observability stack data directories (loki / tempo)
# -----------------------------------------------------------------------------
# loki-config.yaml common.path_prefix=/loki，tempo StorageConfig.Trace 在
# /var/tempo；二者必须存在且归 ongrid-{loki,tempo} 用户，否则 ExecStart
# 启动时报 "mkdir /loki: permission denied"（已发生）。
# 独立运行容错：ongrid-loki/ongrid-tempo 用户由 install-systemd.sh 创建；
# 本脚本独立运行时（--with-deps 之外的路径）用户可能尚未创建，降级 root:root
# + warn（operator 跑 install-systemd.sh 后属主会被 service unit 的运行用户约束）。
DATA_OWNER_LOKI=ongrid-loki; DATA_GRP_LOKI=ongrid-loki
DATA_OWNER_TEMPO=ongrid-tempo; DATA_GRP_TEMPO=ongrid-tempo
id ongrid-loki  &>/dev/null || { DATA_OWNER_LOKI=root;  DATA_GRP_LOKI=root;  warn "user ongrid-loki missing — /loki falls back to root:root"; }
id ongrid-tempo &>/dev/null || { DATA_OWNER_TEMPO=root; DATA_GRP_TEMPO=root; warn "user ongrid-tempo missing — /var/tempo falls back to root:root"; }
install -d -o "$DATA_OWNER_LOKI"  -g "$DATA_GRP_LOKI"  -m 0750 /loki
install -d -o "$DATA_OWNER_LOKI"  -g "$DATA_GRP_LOKI"  -m 0750 /loki/chunks /loki/rules /loki/compactor /loki/rules-tmp
install -d -o "$DATA_OWNER_TEMPO" -g "$DATA_GRP_TEMPO" -m 0750 /var/tempo
log "created data dirs: /loki ($DATA_OWNER_LOKI), /var/tempo ($DATA_OWNER_TEMPO)"

# -----------------------------------------------------------------------------
# step 4 — mariadb schema bootstrap
# -----------------------------------------------------------------------------
# 强制 MariaDB 只监听 127.0.0.1。覆盖 distro 默认的
# 50-server.cnf（部分发行版 bind-address=*）。放在 systemctl enable --now
# 之前，确保首次启动就应用配置。
MARIADB_CONF=/etc/mysql/mariadb.conf.d/60-ongrid.cnf
mkdir -p /etc/mysql/mariadb.conf.d
cat > "$MARIADB_CONF" <<'EOF'
# ongrid: 强制 bind-address=127.0.0.1（systemd 模式）。
# 60- 前缀确保覆盖 distro 默认的 50-server.cnf。
[mysqld]
bind-address = 127.0.0.1
EOF
log "wrote $MARIADB_CONF (bind-address=127.0.0.1)"

log "starting mariadb to bootstrap schema"
systemctl enable --now mariadb >/dev/null 2>&1 || \
    systemctl enable --now mariadb.service

# Wait for socket.
for _ in $(seq 1 30); do
    mysqladmin -uroot ping >/dev/null 2>&1 && break
    sleep 1
done
# 显式校验 MariaDB 就绪状态——循环用 break 跳出，若 30s 后仍未就绪
# 需明确报错退出，避免后续 SQL 在 socket 不可用时半途失败（set -e 下
# 错误信息不清晰，且可能留下不一致的权限状态）。
if ! mysqladmin -uroot ping >/dev/null 2>&1; then
    err "mariadb did not become ready within 30s — aborting schema bootstrap"
    exit 1
fi

DB_PASS_FILE=/etc/ongrid/db-password
if [[ -f "$DB_PASS_FILE" ]]; then
    DB_PASS=$(cat "$DB_PASS_FILE")
    # 复用分支从外部文件读取，必须校验只含 alnum，否则下游
    # heredoc SQL 和 sed 替换会被 ' / \ / & / |
    # 等特殊字符破坏（SQL 注入模式 + sed 边界符冲突）。首次生成分支用
    # tr -dc 'A-Za-z0-9' 保证安全，复用分支必须同样校验。
    if ! [[ "$DB_PASS" =~ ^[A-Za-z0-9]+$ ]]; then
        err "DB password in $DB_PASS_FILE contains non-alphanumeric chars — refusing to use"
        err "for SQL interpolation. To use a custom password, regenerate with:"
        err "  sudo truncate -s 0 $DB_PASS_FILE && sudo bash $0"
        err "or manually edit $DB_PASS_FILE to contain only [A-Za-z0-9]."
        exit 1
    fi
    log "reusing existing DB password from $DB_PASS_FILE (validated alnum)"
else
    # head -c closes the pipe before tr finishes → SIGPIPE → pipefail kills
    # the whole script. Read a fixed-size buffer then map alpha-num so no
    # short-read is needed.
    DB_PASS=$(LC_ALL=C tr -dc 'A-Za-z0-9' < <(head -c 256 /dev/urandom) | cut -c1-24)
    mkdir -p /etc/ongrid
    printf '%s' "$DB_PASS" > "$DB_PASS_FILE"
    chmod 0600 "$DB_PASS_FILE"
    chown_ongrid_or_warn "$DB_PASS_FILE"
    log "generated DB password → $DB_PASS_FILE (0600)"
fi

# 注意：ALTER USER ... IDENTIFIED BY 'plain' 在不同 MariaDB / MySQL
# 版本行为不同（MariaDB 10.4+ 默认 unix_socket plugin、MySQL 8.0.11+ 标记
# 废弃）。不修改 SQL 语法（改 IDENTIFIED VIA ... OR unix_socket 反而会在
# 10.1-10.3 报错），改为加错误处理让 bootstrap 失败时输出可诊断信息。
set +e
mysql -uroot <<SQL
CREATE DATABASE IF NOT EXISTS ongrid CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'ongrid'@'localhost' IDENTIFIED BY '${DB_PASS}';
ALTER USER 'ongrid'@'localhost' IDENTIFIED BY '${DB_PASS}';
GRANT ALL PRIVILEGES ON ongrid.* TO 'ongrid'@'localhost';
FLUSH PRIVILEGES;
SQL
mysql_rc=$?
set -e
if [ "$mysql_rc" -ne 0 ]; then
    err "MariaDB schema bootstrap failed (exit $mysql_rc) — check server version + plugin compat"
    err "  (MariaDB 10.4+ may need: ALTER USER ... IDENTIFIED VIA mysql_native_password)"
    err "  raw error above. Aborting."
    exit 1
fi
log "mariadb schema bootstrapped (db=ongrid user=ongrid)"

# Auto-write DSN into ongrid.env if it still has the placeholder
ENV_FILE=/etc/ongrid/ongrid.env
if [[ -f "$ENV_FILE" ]] && grep -q 'CHANGE_ME' "$ENV_FILE"; then
    # DB_PASS 此时已通过 ^[A-Za-z0-9]+$ 校验（复用分支）
    # 或 tr -dc 'A-Za-z0-9' 生成（首次分支），不含 sed 元字符 & / | 等，
    # sed 替换安全。若未来放宽密码字符集，需改用 awk 或先转义。
    sed -i "s|ongrid:CHANGE_ME@|ongrid:${DB_PASS}@|" "$ENV_FILE"
    log "updated ONGRID_DB_DSN in $ENV_FILE"
fi

# -----------------------------------------------------------------------------
# step 3b — frontier broker auth token（声明降级）
# -----------------------------------------------------------------------------
# frontier-auth.env 声明降级：ONGRID_FRONTIER_AUTH_TOKEN
# 在 Go 代码层零消费（internal/ + cmd/ 下 0 引用，main.go
# managersvcfb.Config{Addr, ServiceName} 无 Token 字段）。真实鉴权走
# EDGE_ACCESS_KEY/SECRET_KEY（DB 行 + manager CreateEdge API），127.0.0.1
# 绑定 + 单机部署兜底。保留 token 生成逻辑作为扩展位，未来 frontier 启用
# Meta 校验时可直接消费此变量。
FRONTIER_AUTH_FILE=/etc/ongrid/frontier-auth.env
if [[ ! -f "$FRONTIER_AUTH_FILE" ]]; then
    mkdir -p /etc/ongrid
    AUTH_TOKEN=$(openssl rand -hex 32)
    printf 'ONGRID_FRONTIER_AUTH_TOKEN=%s\n' "$AUTH_TOKEN" > "$FRONTIER_AUTH_FILE"
    chmod 0600 "$FRONTIER_AUTH_FILE"
    chown_ongrid_or_warn "$FRONTIER_AUTH_FILE"
    log "generated frontier auth token → $FRONTIER_AUTH_FILE (0600 root:ongrid)"
else
    log "frontier auth token already present at $FRONTIER_AUTH_FILE — reusing"
fi

# -----------------------------------------------------------------------------
# step 4 — grafana datasource provisioning
# -----------------------------------------------------------------------------
# Only when grafana is actually installed — otherwise the group doesn't
# exist and the install(1) call fails. install-deps.sh skips grafana
# when --skip-grafana is passed or the apt fetch times out, so checking
# the group is the right signal.
if getent group grafana >/dev/null && [[ -f "$SCRIPT_DIR/grafana-provisioning/datasources.yaml" ]]; then
    install -d -m 0755 /etc/grafana/provisioning/datasources
    install -m 0640 -o root -g grafana "$SCRIPT_DIR/grafana-provisioning/datasources.yaml" \
        /etc/grafana/provisioning/datasources/ongrid.yaml
    log "wrote grafana datasource provisioning"
elif [[ -f "$SCRIPT_DIR/grafana-provisioning/datasources.yaml" ]]; then
    warn "grafana not installed — datasource provisioning will be applied"
    warn "when you install grafana later; copy this file then:"
    warn "  $SCRIPT_DIR/grafana-provisioning/datasources.yaml"
    warn "  → /etc/grafana/provisioning/datasources/ongrid.yaml"
fi

# -----------------------------------------------------------------------------
# step 4a — grafana dashboards provisioning
# -----------------------------------------------------------------------------
# datasources 已就位，补齐 dashboards provisioning（dashboard provider yaml +
# 3 个 dashboard JSON：cluster-overview / manager-internals / server-detail）。
# 模板在 $SCRIPT_DIR/../grafana/provisioning/dashboards/（deploy/install/grafana/）。
# 复用 datasource 段的 getent group grafana 判断，只在 grafana 已安装时执行。
if getent group grafana >/dev/null; then
    install -d -m 0755 /etc/grafana/provisioning/dashboards
    install -d -m 0755 /etc/grafana/provisioning/dashboards/json
    # dashboard provider 配置（default.yml → ongrid.yml，避免与 distro 默认冲突）
    if [[ -f "$SCRIPT_DIR/../grafana/provisioning/dashboards/default.yml" ]]; then
        install -m 0640 -o root -g grafana \
            "$SCRIPT_DIR/../grafana/provisioning/dashboards/default.yml" \
            /etc/grafana/provisioning/dashboards/ongrid.yml
    fi
    # 复制 dashboard JSON 文件
    dashboard_count=0
    if [[ -d "$SCRIPT_DIR/../grafana/provisioning/dashboards/json/" ]]; then
        for json in "$SCRIPT_DIR/../grafana/provisioning/dashboards/json/"*.json; do
            [[ -f "$json" ]] || continue
            install -m 0640 -o root -g grafana "$json" \
                /etc/grafana/provisioning/dashboards/json/
            dashboard_count=$((dashboard_count + 1))
        done
    fi
    if (( dashboard_count > 0 )); then
        log "wrote grafana dashboard provisioning ($dashboard_count dashboards)"
    else
        warn "grafana dashboards dir empty — no JSON copied"
    fi
fi

# -----------------------------------------------------------------------------
# step 4c — grafana admin password + server drop-in
# -----------------------------------------------------------------------------
# 初始 admin 密码由 install.sh 自动生成（24 字符 alnum），写入两处：
#   (1) /etc/ongrid/grafana-admin-password          裸密码（operator 查看）
#   (2) /etc/ongrid/grafana-admin.env               systemd EnvironmentFile
# 二者均 0600 root:ongrid，密钥禁止进 0644 world-readable 的 systemd drop-in
# grafana-server drop-in 通过 EnvironmentFile= 引用 .env 文件，
# drop-in 本身只保留非敏感配置，保持 0640 root:ongrid。幂等：已存在则跳过。
#
# drop-in 与密码 EnvironmentFile 在本 step 同源写入（避免 Environment
# 重复声明冲突）。distro grafana 默认 anonymous auth OFF + sub-path OFF + org role=Viewer。
# 本套件 nginx 反代面向受信内网（无 auth_request 门禁，compose 版才有），
# Grafana 侧依赖（公开 PR 场景下 operator 自行决定是否叠加 nginx 层门禁）：
#   (1) serve_from_sub_path + root_url 让 /grafana/ 反代生效
#   (2) anonymous org role=Viewer（加固：Editor
# 会允许本机任意进程通过 loopback:3000 改 dashboard / Explore 全部
#       Prom/Loki/Tempo 数据，构成 EoP。降为 Viewer 后只读，Editor 级
# 改动需通过 admin 登录或后续引入服务账号）
#   (3) GF_SECURITY_ADMIN_PASSWORD —— 通过 EnvironmentFile 注入，
# 不再写入 drop-in 本身（安全加固）
if getent group grafana >/dev/null; then
    GRAFANA_PASS_FILE=/etc/ongrid/grafana-admin-password
    GRAFANA_ENV_FILE=/etc/ongrid/grafana-admin.env
    mkdir -p /etc/ongrid
    if [[ ! -f "$GRAFANA_PASS_FILE" ]]; then
        GF_PASS=$(LC_ALL=C tr -dc 'A-Za-z0-9' < <(head -c 256 /dev/urandom) | cut -c1-24)
        printf '%s' "$GF_PASS" > "$GRAFANA_PASS_FILE"
        chmod 0600 "$GRAFANA_PASS_FILE"
        chown_ongrid_or_warn "$GRAFANA_PASS_FILE"
        log "generated grafana admin password → $GRAFANA_PASS_FILE (0600)"
    else
        GF_PASS=$(cat "$GRAFANA_PASS_FILE")
        log "grafana admin password already present at $GRAFANA_PASS_FILE — reusing"
    fi
    # 写 EnvironmentFile（KEY=VALUE 格式，0600 root:ongrid）。systemd 加载
    # 后等价于 Environment=GF_SECURITY_ADMIN_PASSWORD=<pass>，但密钥不进
    # drop-in（drop-in 必须 0644/0640 才能被 systemd 解析，无法保密）。
    # 幂等：每次重写以同步密码变更（密码本身以 GRAFANA_PASS_FILE 为权威）。
    printf 'GF_SECURITY_ADMIN_PASSWORD=%s\n' "$GF_PASS" > "$GRAFANA_ENV_FILE"
    chmod 0600 "$GRAFANA_ENV_FILE"
    chown_ongrid_or_warn "$GRAFANA_ENV_FILE"

    # 注入到 grafana-server drop-in（与 step 4b 的 10-ongrid.conf 合并写入，
    # 保证 drop-in 只有一个文件，避免 Environment 重复声明冲突）。drop-in
    # 本身不写密码，仅通过 EnvironmentFile= 引用 0600 的 .env 文件。
    install -d -m 0750 -o root -g ongrid /etc/systemd/system/grafana-server.service.d
    # 注意：%%(protocol)s 是错误的双百分号，Grafana 的 ini %-syntax
    # 只识别单 %。bash heredoc（无引号 EOF）不展开 %；systemd Environment=
    # 对值的 % 也不展开（systemd specifier 仅对 ExecStart= / WorkingDirectory=
    # 等特定指令生效）。故正确写法是单 %。
    cat > /etc/systemd/system/grafana-server.service.d/10-ongrid.conf <<EOF
[Service]
Environment=GF_SERVER_SERVE_FROM_SUB_PATH=true
Environment=GF_SERVER_ROOT_URL=%(protocol)s://%(domain)s/grafana/
Environment=GF_SERVER_HTTP_ADDR=127.0.0.1
Environment=GF_AUTH_ANONYMOUS_ENABLED=true
Environment=GF_AUTH_ANONYMOUS_ORG_ROLE=Viewer
Environment=GF_ANALYTICS_REPORTING_ENABLED=false
Environment=GF_ANALYTICS_CHECK_FOR_UPDATES=false
EnvironmentFile=$GRAFANA_ENV_FILE
EOF
    chmod 0640 /etc/systemd/system/grafana-server.service.d/10-ongrid.conf
    chown_ongrid_or_warn /etc/systemd/system/grafana-server.service.d/10-ongrid.conf
    log "wrote grafana-server drop-in (0640 root:ongrid) + EnvironmentFile $GRAFANA_ENV_FILE (0600)"
    systemctl daemon-reload 2>/dev/null || true
    if systemctl is-active --quiet grafana-server 2>/dev/null; then
        systemctl restart grafana-server 2>/dev/null || true
        log "restarted grafana-server to apply drop-in"
    fi
fi

# -----------------------------------------------------------------------------
# step 5 — nginx site config（详见 step 5 段）
# -----------------------------------------------------------------------------
# -----------------------------------------------------------------------------
# 本脚本不写任何 nginx 配置——nginx 配置统一由 install-systemd.sh 负责
# （snippets + sites-available + conf.d/ongrid-upgrade-map.conf，见
# install-systemd.sh nginx 段）。两条路径同时启用会导致 `listen 80
# default_server` 重复定义 + map 块重复展开（双写冲突），故此处仅清理
# 目标机上可能残留的旧版 conf.d/ongrid.conf（防升级场景双写冲突）。
# nginx-ongrid.conf 仍保留在仓库作为非 Debian 布局的参考版本（operator
# 可手动 cp 到 conf.d/，但不应与本仓库 install 脚本混用）。
if [[ -f "/etc/nginx/conf.d/ongrid.conf" ]]; then
    rm -f /etc/nginx/conf.d/ongrid.conf
    log "removed stale /etc/nginx/conf.d/ongrid.conf (superseded by install-systemd.sh sites-available path)"
fi

# -----------------------------------------------------------------------------
# finish
# -----------------------------------------------------------------------------
cat <<EOF

${C_BOLD}${C_GREEN}deps install complete${C_RESET}

Stack-dep binaries:
$(for b in prometheus loki tempo qdrant; do
      # 显式跳过不存在的二进制，避免 ls 报错被 2>/dev/null 吞掉后
      # awk 无输入静默空输出（summary 该行整个消失，operator 误以为
      # 全部就位）。同时对 $PREFIX_BIN 加引号，防御路径含空格（理论场景）。
      [[ -f "$PREFIX_BIN/$b" ]] && ls -l "$PREFIX_BIN/$b"
  done | awk '{print "    ", $NF, $5"B"}')

OS packages:
  mariadb-server  $(systemctl is-active mariadb 2>/dev/null || echo not-running)
  nginx           $(systemctl is-active nginx 2>/dev/null    || echo not-running)
  grafana         $(systemctl is-active grafana-server 2>/dev/null || echo not-running)

Next:
  sudo systemctl start prometheus loki tempo qdrant
  sudo systemctl start ongrid-frontier ongrid
  sudo systemctl enable --now nginx grafana-server   # serve UI on :80
EOF
