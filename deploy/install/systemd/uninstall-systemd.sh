#!/usr/bin/env bash
# ongrid pure-systemd uninstaller. Mirror of install-systemd.sh.
#
# Default: stop + disable + remove unit files; preserve data dirs + env.
# --purge: also nuke /var/lib/ongrid* and the service users.

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)

if [[ -t 1 ]]; then
    C_RED=$'\033[0;31m'; C_GREEN=$'\033[0;32m'; C_YELLOW=$'\033[1;33m'
    C_BOLD=$'\033[1m'; C_RESET=$'\033[0m'
else
    C_RED=''; C_GREEN=''; C_YELLOW=''; C_BOLD=''; C_RESET=''
fi

log()  { printf '%s[INFO]%s %s\n'  "$C_GREEN"  "$C_RESET" "$*"; }
warn() { printf '%s[WARN]%s %s\n'  "$C_YELLOW" "$C_RESET" "$*"; }
err()  { printf '%s[ERROR]%s %s\n' "$C_RED"    "$C_RESET" "$*" >&2; }

PURGE=0
ASSUME_YES=0
usage() {
    cat <<EOF
Usage: sudo uninstall-systemd.sh [OPTIONS]

Options:
  --purge   Also delete /var/lib/ongrid* + /var/log/ongrid + service users.
            Manager + dep data (DB, vectors, metrics, logs) is lost.
  --yes     Skip the confirmation prompt (only with --purge).
  -h        Print this help.

Without --purge, units are stopped + removed but data + users remain so a
later install-systemd.sh resumes where you left off.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --purge) PURGE=1; shift ;;
        --yes|-y) ASSUME_YES=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) err "unknown flag: $1"; usage; exit 2 ;;
    esac
done

if [[ $EUID -ne 0 ]]; then
    err "must run as root (sudo)"
    exit 1
fi

# UNITS 补完 node_exporter / process_exporter /
# ongrid-edge.service。ongrid-edge 由独立 install-edge.sh 部署，在 fresh
# host 可能不存在；`systemctl is-active --quiet` 对不存在 unit 返回非 0
# （非 active），stop+disable 循环自然跳过，无需特殊处理。下方 unit 文件
# 删除循环对不存在 unit 加 2>/dev/null || true 容忍。
UNITS=(ongrid.service ongrid-frontier.service ongrid-edge.service \
       prometheus.service loki.service tempo.service qdrant.service \
       node_exporter.service process_exporter.service)
SYSTEMD_DIR=/etc/systemd/system

# -----------------------------------------------------------------------------
# stop + disable
# -----------------------------------------------------------------------------
for u in "${UNITS[@]}"; do
    if systemctl is-active --quiet "$u" 2>/dev/null; then
        systemctl stop "$u" || warn "stop $u failed"
        log "stopped $u"
    fi
    if systemctl is-enabled --quiet "$u" 2>/dev/null; then
        systemctl disable "$u" >/dev/null 2>&1 || warn "disable $u failed"
        log "disabled $u"
    fi
done

# -----------------------------------------------------------------------------
# stragglers — manager + frontier might be wedged outside systemd's view
# -----------------------------------------------------------------------------
for proc in /usr/local/bin/ongrid /usr/local/bin/ongrid-frontier; do
    pids=$(pgrep -f "^$proc" 2>/dev/null || true)
    if [[ -n "$pids" ]]; then
        warn "killing straggler $proc (pids: $pids)"
        kill -TERM $pids 2>/dev/null || true
        sleep 2
        pids=$(pgrep -f "^$proc" 2>/dev/null || true)
        if [[ -n "$pids" ]]; then
            warn "force-killing $proc (pids: $pids)"
            kill -KILL $pids 2>/dev/null || true
        fi
    fi
done

# -----------------------------------------------------------------------------
# unit files + binaries
# -----------------------------------------------------------------------------
# 对不存在 unit 文件（如 ongrid-edge.service 在 fresh host 未部署）加
# 2>/dev/null || true 容忍，避免 set -e 退出。
for u in "${UNITS[@]}"; do
    if [[ -f "$SYSTEMD_DIR/$u" ]]; then
        rm -f "$SYSTEMD_DIR/$u"
        log "removed $SYSTEMD_DIR/$u"
    fi
done
for bin in ongrid ongrid-frontier ongrid-healthcheck.sh wait-for-deps.sh; do
    if [[ -f "/usr/local/bin/$bin" ]]; then
        rm -f "/usr/local/bin/$bin"
        log "removed /usr/local/bin/$bin"
    fi
done
systemctl daemon-reload

# -----------------------------------------------------------------------------
# stop-only short-circuit
# -----------------------------------------------------------------------------
if [[ $PURGE -eq 0 ]]; then
    echo ""
    echo "${C_BOLD}${C_GREEN}stop-only uninstall complete${C_RESET}"
    echo "  - units stopped + removed"
    echo "  - data dirs preserved (/var/lib/ongrid*, /var/log/ongrid)"
    echo "  - service users preserved (ongrid, ongrid-prometheus, ...)"
    echo "  - configs preserved (/etc/ongrid/)"
    echo ""
    echo "Re-install with: sudo bash install-systemd.sh"
    echo "Wipe data with:  sudo bash uninstall-systemd.sh --purge"
    exit 0
fi

# -----------------------------------------------------------------------------
# purge — confirm + delete
# -----------------------------------------------------------------------------
if [[ $ASSUME_YES -eq 0 ]]; then
    # 非 TTY 环境（CI / cron / 父脚本无 stdin）下 read 会永久
    # 挂起。fail-fast：拒绝在非交互场景无 --yes 运行 purge。同时给 30s
    # 超时避免交互场景下操作员离开导致挂起。
    if [[ ! -t 0 ]]; then
        err "refusing --purge in non-interactive mode without --yes (stdin is not a TTY)"
        exit 1
    fi
    printf "%sThis deletes ALL ongrid data:\n" "$C_YELLOW"
    printf "  - /var/lib/ongrid* (manager state, prom TSDB, loki/tempo store, qdrant vectors)\n"
    printf "  - /var/log/ongrid (all logs)\n"
    printf "  - /etc/ongrid (configs, secrets)\n"
    printf "  - service users (ongrid, ongrid-prometheus, ongrid-loki, ongrid-tempo, ongrid-qdrant)\n"
    printf "Continue? [y/N] %s" "$C_RESET"
    read -r -t 30 answer || { err "no input within 30s — aborted"; exit 1; }
    case "$answer" in
        y|Y|yes|YES) ;;
        *) log "aborted"; exit 0 ;;
    esac
fi

for d in /var/lib/ongrid /var/lib/ongrid-prometheus /var/lib/ongrid-loki \
         /var/lib/ongrid-tempo /var/lib/ongrid-qdrant /var/log/ongrid \
         /etc/ongrid; do
    if [[ -d "$d" ]]; then
        rm -rf "$d"
        log "removed $d"
    fi
done

# 清理 fresh host install-systemd.sh 新增的目录与配置。
# 注意：不 rm /etc/nginx/nginx.conf（nginx 主配置，非 Ongrid 资产）。
# 不 rm /usr/local/bin/{node_exporter,process_exporter,ongrid-edge}
# （这些可能是 standalone 部署或由独立 install-edge.sh 管理，operator 自行决定）。
# 同时清理 conf.d/ongrid*.conf（历史残留的 conf.d/ongrid.conf
# +  新增的 conf.d/ongrid-upgrade-map.conf），保证 uninstall + reinstall
# 等价 fresh install（幂等）。
for d in /var/lib/ongrid/web \
         /etc/nginx/sites-enabled/ongrid \
         /etc/nginx/sites-available/ongrid \
         /etc/nginx/snippets/ongrid-locations.conf \
         /etc/nginx/snippets/ongrid-upgrade-map.conf \
         /etc/nginx/conf.d/ongrid.conf \
         /etc/nginx/conf.d/ongrid-upgrade-map.conf \
         /etc/mysql/mariadb.conf.d/60-ongrid.cnf \
         /etc/systemd/system/grafana-server.service.d \
         /etc/grafana/provisioning/datasources/ongrid.yaml \
         /etc/grafana/provisioning/dashboards/ongrid.yml \
         /loki \
         /var/tempo; do
    if [[ -e "$d" ]]; then
        rm -rf "$d"
        log "removed $d"
    fi
done

# ongrid.service.d/ drop-in 目录：rm 整个目录而非单个文件，
# 因为 install-systemd.sh 创建该目录并 cp wait-for-deps.conf 进去。
# rm 后立即 daemon-reload，避免 systemd 缓存仍认为 drop-in
# 生效（operator 立即 reinstall + 启动可能命中缓存不一致状态）。
if [[ -d "$SYSTEMD_DIR/ongrid.service.d" ]]; then
    rm -rf "$SYSTEMD_DIR/ongrid.service.d"
    log "removed $SYSTEMD_DIR/ongrid.service.d/ (drop-in dir)"
    systemctl daemon-reload 2>/dev/null || true
fi

for u in ongrid ongrid-prometheus ongrid-loki ongrid-tempo ongrid-qdrant; do
    if id "$u" &>/dev/null; then
        userdel "$u" 2>/dev/null || warn "userdel $u failed"
        log "removed user $u"
    fi
done

echo ""
if [[ -d /etc/grafana/provisioning/dashboards/json ]]; then
    warn "note: /etc/grafana/provisioning/dashboards/json may contain copied ongrid dashboards — review manually"
fi
echo "${C_BOLD}${C_GREEN}purge complete${C_RESET}"
echo "  - units removed"
echo "  - data dirs removed"
echo "  - service users removed"
echo ""
echo "Note: OS-package deps (mariadb-server, nginx, grafana, the prom/loki/"
echo "tempo/qdrant binaries you may have placed in /usr/local/bin) were NOT"
echo "touched. Remove with your package manager if no longer needed."
