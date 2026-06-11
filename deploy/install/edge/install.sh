#!/usr/bin/env bash
# ongrid-edge curl-pipe installer.
#
# Usage:
#   curl -k -sSL https://<server>/install.sh | bash -s -- \
#       --access-key=KEY \
#       --secret-key=SECRET \
#       --server-edge-addr=<host>:40012 \
#       --server-http-addr=<host>:8443
#
#   --server-http-addr  is the same host:port your browser uses (the nginx
#                       front-door); the script downloads the right binary
#                       from https://<http-addr>/edge/ongrid-edge-<os>-<arch>.
#   --server-edge-addr  is the geminio tunnel endpoint (host:port).
#
# Supported targets: linux/amd64, linux/arm64.
#
# Idempotent: re-running with new keys replaces the env file and restarts the
# service. Re-running with the same keys is a no-op aside from the binary
# refresh.

set -euo pipefail

# --- defaults / constants ----------------------------------------------------

ACCESS_KEY=""
SECRET_KEY=""
SERVER_EDGE_ADDR=""
SERVER_HTTP_ADDR=""

INSTALL_DIR="/usr/local/bin"
ENV_DIR="/etc/ongrid-edge"
ENV_FILE="${ENV_DIR}/ongrid-edge.env"
SERVICE_FILE="/etc/systemd/system/ongrid-edge.service"
LOG_DIR="/var/log/ongrid-edge"
SERVICE_USER="ongrid-edge"
SERVICE_GROUP="ongrid-edge"

UNINSTALL=0

# Wait up to N seconds for systemd-managed agent to log "registered with cloud"
# before declaring success. Connect handshake is sub-second on a healthy box;
# 20s leaves headroom for slow DNS / network. Set ONGRID_INSTALL_WAIT to override.
WAIT_SECS="${ONGRID_INSTALL_WAIT:-20}"

# --- pretty-print helpers ----------------------------------------------------

if [[ -t 1 && "${NO_COLOR:-}" == "" ]]; then
    C_RED=$'\033[0;31m'
    C_GREEN=$'\033[0;32m'
    C_YELLOW=$'\033[1;33m'
    C_CYAN=$'\033[0;36m'
    C_DIM=$'\033[2m'
    C_BOLD=$'\033[1m'
    C_RESET=$'\033[0m'
else
    C_RED=''; C_GREEN=''; C_YELLOW=''; C_CYAN=''; C_DIM=''; C_BOLD=''; C_RESET=''
fi

log_info()  { printf '%s[INFO]%s  %s\n' "$C_GREEN"  "$C_RESET" "$*"; }
log_warn()  { printf '%s[WARN]%s  %s\n' "$C_YELLOW" "$C_RESET" "$*"; }
log_error() { printf '%s[ERROR]%s %s\n' "$C_RED"    "$C_RESET" "$*" >&2; }
log_ok()    { printf '%s[OK]%s    %s\n' "$C_GREEN"  "$C_RESET" "$*"; }

trap 'log_error "install failed at line $LINENO (exit $?)"' ERR

# --- arg parsing -------------------------------------------------------------

usage() {
    cat <<EOF
Usage: install.sh [OPTIONS]

Required (install):
  --access-key=KEY
  --secret-key=SECRET
  --server-edge-addr=HOST:PORT     edge geminio endpoint, e.g. ongrid.example.com:40012
  --server-http-addr=HOST[:PORT]   http endpoint, e.g. ongrid.example.com:8443

Other:
  --uninstall                      stop + remove ongrid-edge (keeps /var/log)
  -h, --help                       this help

Env:
  ONGRID_INSTALL_WAIT=20           seconds to poll journal for connect-success (default 20)
  NO_COLOR=1                       disable ANSI colors
EOF
}

for arg in "$@"; do
    case "$arg" in
        --access-key=*)        ACCESS_KEY="${arg#*=}" ;;
        --secret-key=*)        SECRET_KEY="${arg#*=}" ;;
        --server-edge-addr=*)  SERVER_EDGE_ADDR="${arg#*=}" ;;
        --server-http-addr=*)  SERVER_HTTP_ADDR="${arg#*=}" ;;
        --uninstall)           UNINSTALL=1 ;;
        -h|--help)             usage; exit 0 ;;
        *) log_error "unknown arg: $arg"; usage; exit 2 ;;
    esac
done

# --- root check --------------------------------------------------------------

if [[ $EUID -ne 0 ]]; then
    log_info "re-executing with sudo"
    exec sudo -E bash "$0" "$@"
fi

# --- uninstall path ----------------------------------------------------------

if [[ $UNINSTALL -eq 1 ]]; then
    log_info "stopping ongrid-edge"
    systemctl disable --now ongrid-edge 2>/dev/null || true
    rm -f "$SERVICE_FILE" "$INSTALL_DIR/ongrid-edge"
    rm -rf "$ENV_DIR"
    systemctl daemon-reload || true
    log_ok "uninstalled (logs under $LOG_DIR preserved)"
    exit 0
fi

# --- arg validation ----------------------------------------------------------

[[ -n "$ACCESS_KEY"       ]] || { log_error "missing --access-key";       usage; exit 2; }
[[ -n "$SECRET_KEY"       ]] || { log_error "missing --secret-key";       usage; exit 2; }
[[ -n "$SERVER_EDGE_ADDR" ]] || { log_error "missing --server-edge-addr"; usage; exit 2; }
[[ -n "$SERVER_HTTP_ADDR" ]] || { log_error "missing --server-http-addr"; usage; exit 2; }

# --- detect OS / arch --------------------------------------------------------

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) log_error "unsupported arch: $(uname -m)"; exit 1 ;;
esac
if [[ "$OS" != "linux" ]]; then
    log_error "only linux is supported by this installer; got: $OS"
    exit 1
fi

BINARY="ongrid-edge-${OS}-${ARCH}"
URL="https://${SERVER_HTTP_ADDR}/edge/${BINARY}"

# --- download ----------------------------------------------------------------

# Stop the running agent before overwriting the binary. ETXTBSY is rare on
# linux for ELF replacement but `systemctl stop` is cheap and cleaner.
if systemctl is-active --quiet ongrid-edge 2>/dev/null; then
    log_info "stopping running ongrid-edge to refresh binary"
    systemctl stop ongrid-edge || true
fi

log_info "downloading ${URL}"
TMP_BIN=$(mktemp /tmp/ongrid-edge.XXXXXX)
trap 'rm -f "$TMP_BIN"; log_error "install failed at line $LINENO (exit $?)"' ERR
if ! curl -fLk --retry 3 --retry-delay 2 -o "$TMP_BIN" "$URL"; then
    log_error "download failed: ${URL}"
    log_error "  - check that the http endpoint is correct and reachable"
    log_error "  - try: curl -kI https://${SERVER_HTTP_ADDR}/install.sh"
    rm -f "$TMP_BIN"; exit 1
fi
if [[ ! -s "$TMP_BIN" ]]; then
    log_error "downloaded binary is empty: $TMP_BIN"
    rm -f "$TMP_BIN"; exit 1
fi
install -m 0755 -o root -g root "$TMP_BIN" "${INSTALL_DIR}/ongrid-edge"
rm -f "$TMP_BIN"
trap 'log_error "install failed at line $LINENO (exit $?)"' ERR

# --- ADR-024 ExecStartPre hook ----------------------------------------------
#
# apply-pending-upgrade.sh runs before ongrid-edge each boot. It looks for a
# staged bundle dropped by the edge agent (during MethodFetchPackage), swaps
# every file in MANIFEST.txt atomically, then on the NEXT boot rolls back if
# no healthy_marker landed. Without this script installed remote whole-bundle
# upgrades are silently no-ops. Anonymous /edge/ static path serves it.
APPLY_HOOK_DIR=/usr/local/lib/ongrid-edge
APPLY_HOOK="${APPLY_HOOK_DIR}/apply-pending-upgrade.sh"
APPLY_URL="https://${SERVER_HTTP_ADDR}/edge/apply-pending-upgrade.sh"
log_info "installing ${APPLY_HOOK}"
mkdir -p "$APPLY_HOOK_DIR"
TMP_HOOK=$(mktemp /tmp/apply-pending-upgrade.XXXXXX)
if curl -fLk --retry 3 --retry-delay 2 -o "$TMP_HOOK" "$APPLY_URL"; then
    install -m 0755 -o root -g root "$TMP_HOOK" "$APPLY_HOOK"
else
    log_warn "could not fetch ${APPLY_URL}; ADR-024 whole-bundle upgrade won't apply"
fi
rm -f "$TMP_HOOK"

# --- bundled plugin binaries (ADR-015) --------------------------------------
#
# The agent's plugin supervisor runs promtail (logs), node_exporter
# (hostmetrics), process_exporter (procmetrics), otelcol-contrib (traces),
# and database exporters (databasemetrics)
# as subprocesses, expecting them under ${APPLY_HOOK_DIR}. The old curl-pipe
# installer fetched ONLY the agent binary, so every edge enrolled via the UI
# one-liner came up with an empty plugin dir → all plugins "crashed: binary
# missing" → silent empty Logs / Monitor / Traces. (install-edge.sh, run from
# an extracted tarball, did install them — but nobody uses that for
# enrollment.) Fetch them here from the same /edge/ static path the agent
# binary came from. Best-effort per binary: a missing one only disables its
# plugin, surfaced loudly in the self-check below.
fetch_plugin_bin() {
    local name="$1" dest="${APPLY_HOOK_DIR}/$1"
    local url="https://${SERVER_HTTP_ADDR}/edge/${name}-${OS}-${ARCH}"
    local tmp
    tmp=$(mktemp "/tmp/${name}.XXXXXX")
    if curl -fLk --retry 3 --retry-delay 2 -o "$tmp" "$url" && [[ -s "$tmp" ]]; then
        install -m 0755 -o root -g root "$tmp" "$dest"
        log_info "installed plugin binary: ${name}"
    else
        log_warn "could not fetch ${url}; the ${name} plugin will not run until present"
    fi
    rm -f "$tmp"
}
for pbin in promtail node_exporter process_exporter otelcol-contrib mysqld_exporter postgres_exporter redis_exporter mongodb_exporter; do
    fetch_plugin_bin "$pbin"
done

# --- service user ------------------------------------------------------------

if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
    log_info "creating system user ${SERVICE_USER}"
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
fi

# Grant log-read group membership so the logs plugin (promtail) can read
# /var/log/* (root:adm 640) and the journal (systemd-journal). Idempotent.
# Re-asserted on every root start by apply-pending-upgrade.sh, so bundle
# upgrades that skip this installer don't silently lose it.
for grp in adm systemd-journal; do
    if getent group "$grp" >/dev/null 2>&1; then
        usermod -aG "$grp" "$SERVICE_USER" 2>/dev/null || true
    fi
done

# --- log dir -----------------------------------------------------------------

mkdir -p "$LOG_DIR"
chown "$SERVICE_USER":"$SERVICE_GROUP" "$LOG_DIR"
chmod 750 "$LOG_DIR"

# --- env dir + file ----------------------------------------------------------
#
# Group ownership matters here: the service runs as ${SERVICE_USER} and must
# be able to traverse ${ENV_DIR} (mode 750 needs the group bit). Without the
# explicit chown, the dir stays root:root → service can't read scrape.yaml,
# you see the misleading "permission denied" in the journal, and the agent
# silently runs without any scrape config.

mkdir -p "$ENV_DIR"
chown "root:${SERVICE_GROUP}" "$ENV_DIR"
chmod 750 "$ENV_DIR"

cat > "$ENV_FILE" <<EOF
ONGRID_EDGE_CLOUD_ADDR=${SERVER_EDGE_ADDR}
ONGRID_EDGE_ACCESS_KEY=${ACCESS_KEY}
ONGRID_EDGE_SECRET_KEY=${SECRET_KEY}
EOF
chmod 640 "$ENV_FILE"
chown "root:${SERVICE_GROUP}" "$ENV_FILE"

# --- systemd unit ------------------------------------------------------------

cat > "$SERVICE_FILE" <<'EOF'
[Unit]
Description=ongrid edge agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/ongrid-edge/ongrid-edge.env
# ADR-024 ExecStartPre — applies a staged whole-bundle upgrade then
# rolls back on next boot if no healthy_marker landed. `+` runs as
# root regardless of User=; `-` lets a missing/failing script exit 0
# without blocking the unit so the pre-upgrade binary still starts.
ExecStartPre=-+/usr/local/lib/ongrid-edge/apply-pending-upgrade.sh
ExecStart=/usr/local/bin/ongrid-edge
Restart=always
RestartSec=5
User=ongrid-edge
Group=ongrid-edge
AmbientCapabilities=CAP_NET_ADMIN
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
# StateDirectory auto-creates /var/lib/ongrid-edge (mode 0755 owned by
# User=) at start and implicitly adds it to ReadWritePaths. Without
# this, ProtectSystem=strict makes /var/lib read-only and the agent's
# runtime mkdir of /var/lib/ongrid-edge/.upgrade fails EROFS.
StateDirectory=ongrid-edge
StateDirectoryMode=0755
ReadWritePaths=/var/log/ongrid-edge
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
log_info "starting ongrid-edge"
systemctl enable ongrid-edge >/dev/null 2>&1 || true
systemctl restart ongrid-edge

# --- post-start verification -------------------------------------------------

# Resolve version line. The agent logs "ongrid-edge vX.Y.Z starting" to the
# journal as the very first line on boot — extract it from there rather than
# re-invoking the binary (which would race the live systemd process and is
# how the old install.sh leaked a misleading "unauthorized" into its summary).
# Falls back to the binary path if we can't find the line in time.
VERSION_LINE=""

# Poll for either a "registered with cloud" success line, a fatal "unauthorized"
# line, or a service crash. Bail at WAIT_SECS.
START_TS=$(date +%s)
STATUS="pending"
EDGE_ID=""
FAIL_REASON=""
printf '%s[INFO]%s  ' "$C_GREEN" "$C_RESET"
printf 'waiting for tunnel handshake (up to %ss)' "$WAIT_SECS"
while :; do
    NOW=$(date +%s)
    if (( NOW - START_TS >= WAIT_SECS )); then break; fi

    JOURNAL=$(journalctl -u ongrid-edge --since "-${WAIT_SECS}s" --no-pager 2>/dev/null || true)

    # Capture version line as soon as the agent prints it on boot.
    if [[ -z "$VERSION_LINE" ]]; then
        VERSION_LINE=$(printf '%s\n' "$JOURNAL" | grep -oE 'ongrid-edge v[0-9][^ ]* starting' | tail -1 | sed 's/ starting$//' || true)
    fi

    # Failure: service flapped or fatal error logged.
    if ! systemctl is-active --quiet ongrid-edge 2>/dev/null; then
        STATUS="failed"
        FAIL_REASON="service not active"
        break
    fi

    # Success: agent printed registered-with-cloud (covers both fresh and
    # warm reconnect). Capture edge_id from the JSON.
    REG_LINE=$(printf '%s\n' "$JOURNAL" | grep -F 'agent: registered with cloud' | tail -1 || true)
    if [[ -n "$REG_LINE" ]]; then
        STATUS="active"
        EDGE_ID=$(printf '%s' "$REG_LINE" | grep -oE '"edge_id":[0-9]+' | head -1 | cut -d: -f2 || true)
        break
    fi

    # Fast-fail on unauthorized — no point in waiting out the timeout.
    if printf '%s\n' "$JOURNAL" | grep -q 'unauthorized'; then
        STATUS="failed"
        FAIL_REASON="cloud rejected access_key/secret_key (unauthorized)"
        break
    fi

    printf '.'
    sleep 1
done
printf '\n'

[[ -z "$VERSION_LINE" ]] && VERSION_LINE="ongrid-edge ($(stat -c '%y' ${INSTALL_DIR}/ongrid-edge 2>/dev/null | cut -d. -f1))"

# --- self-check --------------------------------------------------------------
#
# Turn the three silent failure modes (missing plugin binary, unreadable
# journal, unreachable data plane) into loud, actionable output. All checks
# are guarded inside `if` so they never trip the ERR trap.
echo
echo "${C_BOLD}${C_CYAN}--- self-check ---${C_RESET}"
SELFCHECK_FAIL=0
for tool in promtail otelcol-contrib node_exporter process_exporter mysqld_exporter postgres_exporter redis_exporter mongodb_exporter; do
    if [[ -x "${APPLY_HOOK_DIR}/${tool}" ]]; then
        log_ok "plugin binary present: ${tool}"
    else
        log_error "plugin binary MISSING: ${APPLY_HOOK_DIR}/${tool} — that plugin will not run"
        SELFCHECK_FAIL=1
    fi
done
if command -v runuser >/dev/null 2>&1; then
    JREAD=(runuser -u "$SERVICE_USER" -- journalctl -n 1 --no-pager)
else
    JREAD=(sudo -u "$SERVICE_USER" journalctl -n 1 --no-pager)
fi
if "${JREAD[@]}" >/dev/null 2>&1; then
    log_ok "journald readable by ${SERVICE_USER}"
else
    log_error "${SERVICE_USER} cannot read the journal — journald log shipping will be empty"
    log_error "  fix: usermod -aG systemd-journal ${SERVICE_USER}; ensure persistent journal (/var/log/journal)"
    SELFCHECK_FAIL=1
fi
DP_HOST="${SERVER_HTTP_ADDR%%:*}"
if [[ -n "$DP_HOST" ]] && timeout 5 bash -c "exec 3<>/dev/tcp/${DP_HOST}/443" 2>/dev/null; then
    log_ok "data-plane host ${DP_HOST}:443 reachable (TCP)"
else
    log_warn "data-plane host ${DP_HOST}:443 not reachable from here — logs/traces push may fail"
fi
if [[ $SELFCHECK_FAIL -eq 0 ]]; then
    log_ok "self-check passed"
else
    log_warn "self-check found problems above — agent is up but some telemetry will be missing until fixed"
fi

# --- final report ------------------------------------------------------------

echo
case "$STATUS" in
    active)
        log_ok "installed:    ${VERSION_LINE}"
        if [[ -n "$EDGE_ID" ]]; then
            log_ok "connected:    edge_id=${EDGE_ID} via ${SERVER_EDGE_ADDR}"
        else
            log_ok "connected:    via ${SERVER_EDGE_ADDR}"
        fi
        log_ok "tail logs:    journalctl -u ongrid-edge -f"
        log_ok "env file:     ${ENV_FILE}"
        log_ok "uninstall:    curl -k -sSL https://${SERVER_HTTP_ADDR}/install.sh | bash -s -- --uninstall"
        ;;
    failed)
        log_ok "installed:    ${VERSION_LINE}"
        log_warn "service did not reach connected state: ${FAIL_REASON}"
        echo
        echo "${C_DIM}---- last 20 journal lines ----${C_RESET}"
        journalctl -u ongrid-edge -n 20 --no-pager 2>/dev/null | sed 's/^/    /' || true
        echo
        if [[ "$FAIL_REASON" == *unauthorized* ]]; then
            log_warn "next step: confirm the access_key/secret_key match what the UI shows."
            log_warn "  the secret_key was only displayed once — if lost, rotate the edge in the UI."
        else
            log_warn "next step: tail the journal to diagnose:"
            log_warn "  journalctl -u ongrid-edge -f"
        fi
        log_ok "env file:     ${ENV_FILE}"
        log_ok "uninstall:    curl -k -sSL https://${SERVER_HTTP_ADDR}/install.sh | bash -s -- --uninstall"
        exit 1
        ;;
    pending)
        log_ok "installed:    ${VERSION_LINE}"
        log_warn "service is running but did not log a connect within ${WAIT_SECS}s"
        log_warn "this can happen on slow networks; tail the journal to confirm:"
        log_warn "  journalctl -u ongrid-edge -f"
        log_ok "env file:     ${ENV_FILE}"
        log_ok "uninstall:    curl -k -sSL https://${SERVER_HTTP_ADDR}/install.sh | bash -s -- --uninstall"
        ;;
esac
