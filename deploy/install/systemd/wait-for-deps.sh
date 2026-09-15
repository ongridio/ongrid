#!/usr/bin/env bash
# wait-for-deps.sh —— ongrid manager 启动前置依赖就绪检查。
# ---------------------------------------------------------------------------
# 用途：作为 ongrid.service 的 ExecStartPre 调用，确保 MariaDB / Qdrant /
# frontier 三大依赖都 LISTEN 后才启动 manager，避免启动期 panic 循环
# （reboot 自启风险前置缓解）。
#
# 检查项：
#   1. MariaDB：mariadb-admin ping（用 /etc/ongrid/ongrid.env 解析出的密码）
#   2. Qdrant：curl http://127.0.0.1:6333/healthz
#   3. frontier：ss -tln | grep 127.0.0.1:40011
#
# 三者全 ready 才 exit 0；超时（默认 120s）后 exit 1，systemd 会按
# StartLimitInterval=60s StartLimitBurst=3（ongrid.service 既有）规则重启。
# ---------------------------------------------------------------------------
set -euo pipefail

ENV_FILE="${ONGRID_ENV_FILE:-/etc/ongrid/ongrid.env}"
TIMEOUT="${ONGRID_WAIT_TIMEOUT:-120}"
POLL_INTERVAL="${ONGRID_WAIT_POLL_INTERVAL:-2}"

log()  { printf '[wait-for-deps] %s\n' "$*" >&2; }
die()  { printf '[wait-for-deps] error: %s\n' "$*" >&2; exit 1; }

# 从 env 文件提取 DB 密码（DSN 形如 ongrid:PWD@tcp(...)）
extract_db_password() {
    if [ ! -f "$ENV_FILE" ]; then
        log "WARN: $ENV_FILE missing; skipping MariaDB ping"
        return 0
    fi
    # shellcheck disable=SC1090
    # 仅提取 DSN 字段，不 source 整个文件（防止 & 等特殊字符触发 shell 解析）
    local dsn
    dsn="$(grep -E '^ONGRID_DB_DSN=' "$ENV_FILE" | head -1 | sed -E 's/^ONGRID_DB_DSN=//; s/^"(.*)"$/\1/')"
    if [ -z "$dsn" ]; then
        log "WARN: ONGRID_DB_DSN missing in $ENV_FILE; skipping MariaDB ping"
        return 0
    fi
    # 形如 ongrid:PASSWORD@tcp(...) — 取首个 : 和 @ 之间的部分
    printf '%s' "$dsn" | sed -nE 's/^[^:]+:([^@]+)@.*/\1/p'
}

check_mariadb() {
    local pwd
    pwd="$(extract_db_password)"
    [ -z "$pwd" ] && return 0
    MYSQL_PWD="$pwd" mariadb-admin -h 127.0.0.1 -u ongrid --connect-timeout=3 ping 2>/dev/null \
        | grep -q "^mysqld is alive"
}

check_qdrant() {
    curl -sf --max-time 3 http://127.0.0.1:6333/healthz >/dev/null
}

check_frontier() {
    ss -tln 2>/dev/null | awk '{print $4}' | grep -qE '127\.0\.0\.1:40011$|\]:40011$'
}

check_all() {
    check_mariadb && check_qdrant && check_frontier
}

# --- 主循环 ----------------------------------------------------------------
log "waiting for mariadb + qdrant + frontier (timeout ${TIMEOUT}s)"
ELAPSED=0
while [ "$ELAPSED" -lt "$TIMEOUT" ]; do
    if check_all; then
        log "all deps ready (after ${ELAPSED}s)"
        exit 0
    fi
    # 单项诊断（仅打 stderr，不影响循环）
    check_mariadb || log "  · mariadb: not ready"
    check_qdrant  || log "  · qdrant:  not ready"
    check_frontier|| log "  · frontier:not ready"
    sleep "$POLL_INTERVAL"
    ELAPSED=$((ELAPSED + POLL_INTERVAL))
done

log "FAIL: deps not ready after ${TIMEOUT}s"
exit 1
