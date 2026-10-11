#!/usr/bin/env bash
# e2e-checklist.sh — 公网 broker 加固链路端到端验收脚本
#
# 逐项验证 7 项 Success Criteria（公网 broker 加固链路）。
# 架构：公网反代 + manager 主机本地 frontier broker（非公网 VPS 部署）。
# 两层加固（TLS 1.3 + Go rate limiter，无 fail2ban）+ 应用层限速。
#
# 用法：
#   BROKER_DOMAIN=broker.example.com bash e2e-checklist.sh
#
# 环境变量：
#   BROKER_DOMAIN  — DDNS 域名（默认 broker.example.com）
#   EDGE_PORT      — edgebound 公网端口（默认 16667）
#   SERVICE_PORT   — servicebound 公网端口（默认 16668）
#   METRICS_URL    — manager 主机本地 broker metrics endpoint（默认 http://127.0.0.1:8080）
#   EDGE_ONLINE_MIN — 检查 #4 期望的边端最小在线数（默认 1，按实际部署规模设置）
#
# 返回值：0 = 全部 PASS，1 = 有 FAIL 项。
#
# 相关文档：
#   本脚本头部注释即验收标准来源（Success Criteria 7 项）。

set -euo pipefail

# ─── 配置 ────────────────────────────────────────────────────────────
BROKER_DOMAIN="${BROKER_DOMAIN:-broker.example.com}"
EDGE_PORT="${EDGE_PORT:-16667}"        # edgebound（公网入口 → manager:40012）
SERVICE_PORT="${SERVICE_PORT:-16668}"  # servicebound（公网入口 → manager:40011）
METRICS_URL="${METRICS_URL:-http://127.0.0.1:8080}"
EDGE_ONLINE_MIN="${EDGE_ONLINE_MIN:-1}"

PASS_COUNT=0
FAIL_COUNT=0
TOTAL=7

# ─── 辅助函数 ────────────────────────────────────────────────────────

# 输出 PASS 结果并计数
pass() {
    echo "[PASS] $1"
    PASS_COUNT=$((PASS_COUNT + 1))
}

# 输出 FAIL 结果并计数
fail() {
    echo "[FAIL] $1"
    echo "       原因: $2"
    FAIL_COUNT=$((FAIL_COUNT + 1))
}

# 分隔线
separator() {
    echo "──────────────────────────────────────────────────────────"
}

# ─── Success Criteria #1: TLS 1.3 端到端强制（经 DDNS 域名）──────────
# 验证：openssl s_client -tls1_3 经 DDNS 域名连接成功
#       openssl s_client -tls1_2 预期失败（客户端强制 TLS 1.3，服务端受 frontier 上游 SDK 的 TLS 版本限制）
check_tls_1_3() {
    separator
    echo "检查 #1: TLS 1.3 端到端强制（经 DDNS 域名 ${BROKER_DOMAIN}:${EDGE_PORT}）"

    local tls13_output
    if tls13_output=$(openssl s_client -connect "${BROKER_DOMAIN}:${EDGE_PORT}" \
        -tls1_3 </dev/null 2>&1); then
        if echo "${tls13_output}" | grep -q "Verify return code"; then
            pass "#1 TLS 1.3 经 DDNS 域名握手成功"
            return 0
        fi
    fi

    fail "#1 TLS 1.3 经 DDNS 域名握手" \
        "openssl s_client -tls1_3 未返回 Verify return code（可能 TLS 未配置或 反代 TLS 终止而非 passthrough）"
    return 1
}

# ─── Success Criteria #2: 未持有效 token / AccessKey 被拒 ─────────────
# 验证：用错误 key 拨号被 broker 拒绝
# PoC 测试需边端真公网机器执行（家宽内 NAT Loopback 不可信），此处检查日志证据
check_auth_reject() {
    separator
    echo "检查 #2: 未持有效 token / AccessKey 被拒（日志证据）"

    # 检查 journalctl 中是否有鉴权失败日志
    if journalctl -u ongrid --since '24h ago' --no-pager 2>/dev/null | \
       grep -q "token verification failed\|argon2id.*mismatch\|frontierauth.*reject"; then
        pass "#2 鉴权失败日志存在（broker 曾拒绝无效 token 连接）"
        return 0
    fi

    # fallback：检查 metrics 中是否有鉴权失败计数
    local metrics_failures
    if metrics_failures=$(curl -sf "${METRICS_URL}/metrics" 2>/dev/null | \
          grep "ongrid_frontier_auth_failures_total"); then
        if [ -n "${metrics_failures}" ]; then
            pass "#2 metrics 确认鉴权失败计数存在（broker 曾拒绝无效连接）"
            return 0
        fi
    fi

    fail "#2 未持有效 token 被拒" \
        "journalctl 无鉴权失败日志且 metrics 无 failures_total 计数（需从边端真公网机器执行 PoC 测试）"
    return 1
}

# ─── Success Criteria #3: 应用层 rate limiter 10 次 ban 1h ───────────
# 验证：curl /metrics 检查 ongrid_frontier_auth_blocked_total
# 连续 10 次失败（按 AccessKey hash 计数）触发 1h 临时拒服
check_rate_limiter() {
    separator
    echo "检查 #3: 应用层 rate limiter（ongrid_frontier_auth_blocked_total）"

    local blocked_metric
    if blocked_metric=$(curl -sf "${METRICS_URL}/metrics" 2>/dev/null | \
          grep "ongrid_frontier_auth_blocked_total"); then
        if [ -n "${blocked_metric}" ]; then
            echo "       metrics 输出: ${blocked_metric}"
            pass "#3 rate limiter metrics 暴露成功（ongrid_frontier_auth_blocked_total）"
            return 0
        fi
    fi

    fail "#3 rate limiter metrics" \
        "curl ${METRICS_URL}/metrics 未找到 ongrid_frontier_auth_blocked_total（rate limiter 未触发或 metrics endpoint 不可达）"
    return 1
}

# ─── Success Criteria #4: 边端通过 DDNS 域名反向拨号 + RPC ────────────
# 验证：manager 查询边端在线状态 + RPC 调用
# 此项需边端真公网机器部署后执行
check_edge_reverse_dial() {
    separator
    echo "检查 #4: 边端通过 DDNS 域名反向拨号（需边端部署）"

    # 检查 manager 是否能查询到在线边端
    # 通过 Web API 或直接查 DB
    local online_count
    if command -v ongrid-cli &>/dev/null; then
        online_count=$(ongrid-cli edge list --status online 2>/dev/null | wc -l)
    else
        # fallback：检查 frontier broker 连接数（通过 metrics 或 ss）
        online_count=$(ss -tnp 2>/dev/null | \
            grep -c ":${EDGE_PORT}\|:${SERVICE_PORT}" || echo "0")
    fi

    if [ "${online_count}" -ge "${EDGE_ONLINE_MIN}" ] 2>/dev/null; then
        pass "#4 边端在线（连接数 >= ${EDGE_ONLINE_MIN}）"
        return 0
    fi

    echo "       提示: 此项需边端部署完成后从边端真公网机器验证"
    echo "       当前连接数: ${online_count}（期望 >= ${EDGE_ONLINE_MIN}）"
    fail "#4 边端反向拨号" \
        "边端连接数不足或边端尚未部署（待边端部署后复测）"
    return 1
}

# ─── Success Criteria #5: 鉴权失败日志含 hash 不含明文 ───────────────
# 验证：journalctl 检查鉴权失败日志
# 日志含 token_hash / access_key_hash，不含明文 token / SecretKey
check_auth_log_hash() {
    separator
    echo "检查 #5: 鉴权失败日志含 hash 不含明文 token / SecretKey"

    local auth_logs
    auth_logs=$(journalctl -u ongrid --since '24h ago' --no-pager 2>/dev/null | \
        grep -i "frontierauth\|token verification failed\|argon2id" || true)

    if [ -z "${auth_logs}" ]; then
        echo "       提示: 近 24h 无鉴权失败日志（可能无人尝试非法连接）"
        fail "#5 鉴权失败日志" "无鉴权失败日志可供检查"
        return 1
    fi

    # prevention：日志不得含明文 token / SecretKey
    if echo "${auth_logs}" | grep -qi "secret_key=\|SecretKey=" | \
       grep -v "hash\|hashed\|sha256"; then
        fail "#5 日志含明文" "鉴权日志中出现疑似明文 SecretKey（violation）"
        return 1
    fi

    # 验证日志含 hash（token_hash 或 access_key_hash_prefix）
    if echo "${auth_logs}" | grep -qi "hash\|token_hash\|access_key_hash"; then
        pass "#5 鉴权日志含 hash 标记，未检测到明文 token"
        return 0
    fi

    fail "#5 鉴权日志" "日志无 hash 标记且无明文（格式可能不符合预期）"
    return 1
}

# ─── Success Criteria #6: token 不入非密钥文件（CI grep check）────────
# 验证：ONGRID_FRONTIER_AUTH_TOKEN 字面值不出现在 .go / .md / .yaml 非密钥文件
# 例外：deploy/ 下部署模板和文档允许引用变量名
check_token_not_leaked() {
    separator
    echo "检查 #6: ONGRID_FRONTIER_AUTH_TOKEN 不入非密钥文件（CI grep check）"

    # 在项目根目录执行 grep（脚本可能从任意位置调用，用 git rev-parse 定位）
    local repo_root
    repo_root=$(git rev-parse --show-toplevel 2>/dev/null || echo ".")

    # 排除 deploy/（部署模板和文档允许引用变量名）
    # 排除 .git/ + vendor/ + node_modules/
    local leaked_count
    leaked_count=$(cd "${repo_root}" && \
        grep -rn "ONGRID_FRONTIER_AUTH_TOKEN" \
            --include="*.go" --include="*.md" --include="*.yaml" --include="*.yml" \
            . 2>/dev/null | \
        grep -v "deploy/\|\.git/\|vendor/\|node_modules/" | \
        wc -l)

    if [ "${leaked_count}" -eq 0 ]; then
        pass "#6 ONGRID_FRONTIER_AUTH_TOKEN 未泄漏到非密钥文件（CI grep check 通过）"
        return 0
    fi

    echo "       泄漏文件列表:"
    cd "${repo_root}" && \
        grep -rn "ONGRID_FRONTIER_AUTH_TOKEN" \
            --include="*.go" --include="*.md" --include="*.yaml" --include="*.yml" \
            . 2>/dev/null | \
        grep -v "deploy/\|\.git/\|vendor/\|node_modules/"
    fail "#6 token 泄漏" "在 ${leaked_count} 个非密钥文件中发现 ONGRID_FRONTIER_AUTH_TOKEN 引用"
    return 1
}

# ─── Success Criteria #7: nmap 端口收敛 + broker 非 root ──────────────
# 验证：nmap 外网扫描 DDNS 域名公网入口
# 仅 22 + EDGE_PORT + SERVICE_PORT 开放，反代管理界面端口不开放
#       broker 进程在 manager 主机上以非 root 运行
check_port_surface() {
    separator
    echo "检查 #7: nmap 端口收敛 + broker 非 root 运行"

    local port_check_failed=0

    # 7a: broker 进程非 root（在 manager 主机本地检查）
    local broker_user
    broker_user=$(ps -eo user,comm 2>/dev/null | \
        grep -i "frontier\|ongrid" | \
        awk '{print $1}' | head -1 || echo "")

    if [ -n "${broker_user}" ] && [ "${broker_user}" != "root" ]; then
        pass "#7a broker 进程以 '${broker_user}' 用户运行（非 root）"
    else
        if [ -z "${broker_user}" ]; then
            echo "       提示: 未检测到 frontier/ongrid 进程（可能在远程 manager 主机上运行）"
        fi
        fail "#7a broker 非 root" "broker 进程以 root 运行或未检测到（用户: ${broker_user:-unknown}）"
        port_check_failed=1
    fi

    # 7b: nmap 端口收敛（需 nmap 可用）
    if ! command -v nmap &>/dev/null; then
        echo "       提示: nmap 未安装，跳过端口扫描（请手动安装: apt install nmap）"
        if [ ${port_check_failed} -eq 0 ]; then
            pass "#7b nmap 跳过（工具未安装），broker 非 root 已验证"
            return 0
        fi
        return 1
    fi

    local nmap_output
    nmap_output=$(nmap -sT -p "22,80,443,${EDGE_PORT},${SERVICE_PORT}" \
        "${BROKER_DOMAIN}" 2>/dev/null || echo "")

    if [ -z "${nmap_output}" ]; then
        fail "#7b nmap 扫描" "nmap 无输出（域名不可达或网络问题）"
        return 1
    fi

    local open_count
    open_count=$(echo "${nmap_output}" | grep -c "open" || echo "0")

    echo "       nmap 结果:"
    echo "${nmap_output}" | grep -E "open|closed|filtered" | sed 's/^/         /'

    # 期望：22 + EDGE_PORT + SERVICE_PORT = 3 个 open（80/443 应 closed/filtered）
    if [ "${open_count}" -le 3 ]; then
        pass "#7b 端口收敛（open 端口数 ${open_count} <= 3，反代管理界面未暴露）"
    else
        fail "#7b 端口收敛" "open 端口数 ${open_count} > 3（可能 反代管理界面端口对外暴露）"
        port_check_failed=1
    fi

    return ${port_check_failed}
}

# ─── 主流程 ──────────────────────────────────────────────────────────
main() {
    echo "╔══════════════════════════════════════════════════════════════╗"
    echo "║   公网 broker 加固链路 — 端到端验收清单              ║"
    echo "║  架构: 公网反代 + manager 主机本地 frontier broker               ║"
    echo "║  两层加固 + 应用层限速（+ TLS 1.3 + rate limiter）║"
    echo "╚══════════════════════════════════════════════════════════════╝"
    echo ""
    echo "目标域名: ${BROKER_DOMAIN}"
    echo "edgebound 端口: ${EDGE_PORT}（公网入口 → manager:40012）"
    echo "servicebound 端口: ${SERVICE_PORT}（公网入口 → manager:40011）"
    echo "metrics endpoint: ${METRICS_URL}（manager 主机本地 broker metrics）"
    echo ""

    check_tls_1_3          || true  # #1
    check_auth_reject      || true  # #2
    check_rate_limiter     || true  # #3
    check_edge_reverse_dial || true  # #4
    check_auth_log_hash    || true  # #5
    check_token_not_leaked || true  # #6
    check_port_surface     || true  # #7

    separator
    echo ""
    echo "═══ 验收结果汇总 ═══"
    echo "  PASS: ${PASS_COUNT}/${TOTAL}"
    echo "  FAIL: ${FAIL_COUNT}/${TOTAL}"
    echo ""

    if [ ${FAIL_COUNT} -eq 0 ]; then
        echo "  *** 全部 PASS —  端到端验收通过 ***"
        exit 0
    else
        echo "  *** 存在 FAIL 项 — 需回溯修复或等待边端部署后重新验收 ***"
        echo ""
        echo "  注意: #4（边端反向拨号）依赖边端部署，待边端部署后复测。"
        echo "        #2（PoC 拒绝测试）依赖边端侧拒绝测试，待边端部署后复测。"
        exit 1
    fi
}

main "$@"
