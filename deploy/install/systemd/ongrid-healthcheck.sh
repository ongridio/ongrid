#!/usr/bin/env bash
# ongrid-healthcheck.sh —— manager 健康检查探针（独立脚本）。
# ---------------------------------------------------------------------------
# 用途：供 systemd ExecStartPost（已接入 ongrid.service.d/wait-for-deps.conf
# 的 ExecStartPost=-/usr/local/bin/ongrid-healthcheck.sh）、
# 外部监控系统（Prometheus blackbox_exporter）调用，独立探活 manager
# /readyz 端点。退出码：0=健康，1=不健康。
#
# 设计原则：
#   - 不读敏感变量（无 JWT token / DB 密码），纯 HTTP GET
#   - 5s 超时（manager 重启或 OOM 时快速失败）
#   - 失败时 stderr 打印诊断信息，不污染 stdout
# ---------------------------------------------------------------------------

set -uo pipefail

HEALTH_URL="${ONGRID_HEALTH_URL:-http://127.0.0.1:8080/readyz}"
TIMEOUT="${ONGRID_HEALTH_TIMEOUT:-5}"

if curl -sf --max-time "$TIMEOUT" "$HEALTH_URL" >/dev/null 2>&1; then
    exit 0
fi

# 诊断输出（仅失败时）
printf '[ongrid-healthcheck] FAIL: %s not ready within %ss\n' "$HEALTH_URL" "$TIMEOUT" >&2
# 列出可能的原因（不泄露任何敏感信息）
printf '[ongrid-healthcheck] check:\n' >&2
printf '  systemctl is-active ongrid\n' >&2
printf '  journalctl -u ongrid -n 50 --no-pager\n' >&2
exit 1
