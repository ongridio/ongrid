#!/usr/bin/env bash
# =============================================================================
# fresh-host-assert.sh — fresh host VM 实测断言脚本
# =============================================================================
#
# 封装 FRESH-HOST 验收全流程：
#   fresh host checklist（模板污染防护）
#   → install-systemd.sh --with-deps
#   → ${#UNITS[@]} unit active 断言
#   → 数据 dump
#   → reboot gate（unit active + 数据对比，假阳性防护）
#   → uninstall --purge
#   → find 全局扫描断言空（残留防护）
#   → 循环 3 次（幂等性验证）
#
# reboot gate 实现说明：
#   reboot 会导致 SSH 断开，脚本无法在同进程中继续。本脚本支持 --resume flag：
#   1. 首次执行：跑完 install + 数据 dump（reboot 前快照），保存到 /tmp/fresh-host-dump-cycleN.txt
#   2. 触发 reboot（systemctl reboot），SSH 断开
#   3. operator 等待 ~90s 后重新 SSH，执行 bash fresh-host-assert.sh --resume <cycle>
#   4. resume 模式：跳过已完成步骤，从"reboot 后 unit active 断言 + 数据对比"开始
#   5. resume 完成后继续 purge + find 扫描 + 下一次循环（如果 N < CYCLES）
#
# 设计决策：
#   - 3 次循环验证幂等性
#   - ：PASS/FAIL 文本格式，与 tests/deploy/phase-02/ + phase-06/ 风格一致
#   - 用当前版 install-systemd.sh / uninstall-systemd.sh
#   - 默认 ONGRID_FRONTIER_ADDR=127.0.0.1:40011 内网隔离
#   - 首个 FAIL 立即 exit 1，不自动 rollback（危险操作不回滚）
#
# Usage:
#   bash fresh-host-assert.sh             # 首次执行（从 cycle 1 开始）
#   bash fresh-host-assert.sh --resume 2  # reboot 后恢复（从 cycle 2 reboot 后验证开始）
#
# =============================================================================
set -euo pipefail
trap 'fail "fresh-host-assert.sh failed at line $LINENO (exit code: $?)"' ERR

# -----------------------------------------------------------------------------
# 辅助函数（PASS/FAIL 文本格式，参考 assert-render-output.sh）
# -----------------------------------------------------------------------------
log()  { echo "[INFO] $*"; }
warn() { echo "[WARN] $*"; }
fail() { echo "FAIL: FRESH-HOST — $*" >&2; exit 1; }
pass() { echo "PASS: FRESH-HOST — $*"; }

# -----------------------------------------------------------------------------
# 变量定义
# -----------------------------------------------------------------------------
SCRIPT_DIR=$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/../../../.." && pwd)
INSTALL_SH="$REPO_ROOT/deploy/install/systemd/install-systemd.sh"
UNINSTALL_SH="$REPO_ROOT/deploy/install/systemd/uninstall-systemd.sh"

CYCLES=3  # 3 次循环验证幂等性

# install-systemd.sh enable 的 8 个 systemd unit（不含 ongrid-edge.service）
# 注：install-systemd.sh 实际只 enable 8 个 unit
UNITS=(
    ongrid.service
    ongrid-frontier.service
    prometheus.service
    loki.service
    tempo.service
    qdrant.service
    node_exporter.service
    process_exporter.service
)

# fresh host checklist 检查路径（prevention）
FRESH_HOST_PATHS=(/etc/nginx /etc/docker /var/lib/docker)

# 端口占用检查（prevention — install 前关键端口应未被占用）
FRESH_HOST_PORTS=":(80|3306|6333|9090|3100|3200|40011|40012) "

# find 断言已知排除项（install-systemd.sh 不装这些，但 operator 可能预装）
# 使用数组 + grep -vE 支持多排除项的 OR 模式
FIND_EXCLUDES=(
    '/usr/local/bin/ongrid-edge'
)

# 数据 dump 临时文件目录
DUMP_DIR="/tmp"

# -----------------------------------------------------------------------------
# --resume flag 解析（reboot 后恢复执行）
# -----------------------------------------------------------------------------
RESUME_CYCLE=0
if [[ "${1:-}" == "--resume" ]]; then
    RESUME_CYCLE="${2:-0}"
    if [[ "$RESUME_CYCLE" -lt 1 || "$RESUME_CYCLE" -gt "$CYCLES" ]]; then
        fail "--resume cycle 编号应在 1..$CYCLES 之间，收到: $RESUME_CYCLE"
    fi
    log "resume mode: 从 cycle $RESUME_CYCLE 的 reboot 后验证开始"
fi

# =============================================================================
# Step 0 — fresh host checklist（模板污染防护）
# =============================================================================
# 仅在非 resume 模式时检查（resume 时 VM 已被 install 污染，checklist 不适用）
if [[ "$RESUME_CYCLE" -eq 0 ]]; then
    echo ""
    echo "=========================================="
    echo "Step 0: Fresh Host Checklist"
    echo "=========================================="

    # 检查 fresh host 路径不存在或为空
    for p in "${FRESH_HOST_PATHS[@]}"; do
        if [[ -e "$p" ]]; then
            fail ": $p 已存在 — VM 非干净状态（A1 cloud-init 全新模板要求）"
        fi
    done
    pass "Step 0: /etc/nginx /etc/docker /var/lib/docker 均不存在"

    # 检查关键端口未占用
    if ss -tlnp 2>/dev/null | grep -E "$FRESH_HOST_PORTS" >/dev/null; then
        ss -tlnp 2>/dev/null | grep -E "$FRESH_HOST_PORTS"
        fail "端口已占用 — VM 非干净状态"
    fi
    pass "Step 0: 关键端口 (80/3306/6333/9090/3100/3200/40011/40012) 未占用"
fi

# =============================================================================
# 主循环 — 3 次 install/purge（幂等性验证）
# =============================================================================
for cycle in $(seq 1 "$CYCLES"); do
    echo ""
    echo "=========================================="
    echo "=== Cycle $cycle / $CYCLES ==="
    echo "=========================================="

    # -------------------------------------------------------------------------
    # resume 模式跳过已完成步骤
    # -------------------------------------------------------------------------
    if [[ "$RESUME_CYCLE" -ne 0 && "$cycle" -lt "$RESUME_CYCLE" ]]; then
        log "Cycle $cycle: 跳过（已完成，resume 模式）"
        continue
    fi

    # =========================================================================
    # Phase A: install + unit active 断言（仅非 resume 或 resume 当前 cycle 时跳过）
    # =========================================================================
    if [[ "$RESUME_CYCLE" -eq 0 || "$cycle" -gt "$RESUME_CYCLE" ]]; then

        # ---------------------------------------------------------------------
        # Step 1a: install-systemd.sh --with-deps（当前版）
        # ---------------------------------------------------------------------
        log "Cycle $cycle: 执行 install-systemd.sh --with-deps"
        bash "$INSTALL_SH" --with-deps
        pass "Cycle $cycle: install-systemd.sh --with-deps 完成"

        # ---------------------------------------------------------------------
        # Step 1b: systemctl start（install 只 enable 不 auto-start）
        # ---------------------------------------------------------------------
        for u in "${UNITS[@]}"; do
            systemctl start "$u" || fail "Cycle $cycle: systemctl start $u 失败"
        done
        pass "Cycle $cycle: ${#UNITS[@]} 个 unit 已 start"

        # ---------------------------------------------------------------------
        # Step 1c: unit active 断言（第一层）
        # ---------------------------------------------------------------------
        for u in "${UNITS[@]}"; do
            systemctl is-active --quiet "$u" || fail "Cycle $cycle: $u 未 active"
        done
        pass "Cycle $cycle: ${#UNITS[@]} 个 unit 全部 active"

        # ---------------------------------------------------------------------
        # Step 1d: unit enabled 断言（reboot 后自启动的前提）
        # ---------------------------------------------------------------------
        for u in "${UNITS[@]}"; do
            systemctl is-enabled --quiet "$u" || fail "Cycle $cycle: $u 未 enabled"
        done
        pass "Cycle $cycle: ${#UNITS[@]} 个 unit 全部 enabled"

        # ---------------------------------------------------------------------
        # Step 1e: 数据 dump（reboot 前快照）—  prevention
        # ---------------------------------------------------------------------
        log "Cycle $cycle: 数据 dump（reboot 前快照）"
        DB_PASS=""
        if [[ -f /etc/ongrid/db-password ]]; then
            DB_PASS=$(tr -d '[:space:]' < /etc/ongrid/db-password)
        elif [[ -f /etc/ongrid/ongrid.env ]]; then
            # fallback：从 ONGRID_DB_DSN 中提取密码（ongrid:<password>@tcp...）
            DB_PASS=$(grep '^ONGRID_DB_DSN=' /etc/ongrid/ongrid.env \
                | head -1 \
                | sed -n 's|^ONGRID_DB_DSN=ongrid:\([^@]*\)@tcp.*|\1|p')
        fi
        [[ -n "$DB_PASS" ]] || fail "Cycle $cycle: 无法提取 DB 密码（etc/ongrid/db-password 和 ONGRID_DB_DSN 均无效）"
        DUMP_FILE="$DUMP_DIR/fresh-host-dump-cycle${cycle}.txt"

        DATA_BEFORE=$(mysql -h 127.0.0.1 -u ongrid -p"$DB_PASS" ongrid -e "
            SELECT 'users', COUNT(*) FROM users;
            SELECT 'alert_events', COUNT(*) FROM alert_events;
            SELECT * FROM system_settings;
        " 2>/dev/null) || fail "Cycle $cycle: 数据 dump 失败（MariaDB 连接失败）"

        echo "$DATA_BEFORE" > "$DUMP_FILE"
        log "Cycle $cycle: 数据快照已保存到 $DUMP_FILE（$(echo "$DATA_BEFORE" | wc -l) 行）"

        # ---------------------------------------------------------------------
        # Step 1f: 触发 reboot（核心验证）
        # ---------------------------------------------------------------------
        echo ""
        log "Cycle $cycle: 准备触发 reboot..."
        log "  reboot 后 SSH 会断开，请等待 ~90s 后重新 SSH"
        log "  然后执行: bash $0 --resume $cycle"
        echo ""

        # reboot 会导致脚本进程终止，operator 需 --resume 恢复
        systemctl reboot
        # 此行正常不会执行到（reboot 已终止进程）
        sleep 999  # 占位，防止 reboot 未立即终止时的意外继续
    fi

    # =========================================================================
    # Phase B: reboot 后验证（resume 模式从此处开始）
    # =========================================================================

    # ---------------------------------------------------------------------
    # Step 1f-resume-a: 等待服务恢复（resume 模式下 SSH 刚重连）
    # ---------------------------------------------------------------------
    if [[ "$RESUME_CYCLE" -ne 0 && "$cycle" -eq "$RESUME_CYCLE" ]]; then
        log "Cycle $cycle: resume 模式 — 等待 MariaDB 可用..."
        for i in $(seq 1 30); do
            if mysqladmin ping -h 127.0.0.1 --silent 2>/dev/null; then
                break
            fi
            sleep 2
        done
        mysqladmin ping -h 127.0.0.1 --silent 2>/dev/null || fail "Cycle $cycle: MariaDB 在 60s 内未恢复"
        log "Cycle $cycle: MariaDB 已恢复"
    fi

    # ---------------------------------------------------------------------
    # Step 1f-resume-b: reboot 后 unit active 断言（prevention 第一层）
    # ---------------------------------------------------------------------
    for u in "${UNITS[@]}"; do
        systemctl is-active --quiet "$u" || fail ": Cycle $cycle — reboot 后 $u 未自启动 active"
    done
    pass "Cycle $cycle: reboot 后 ${#UNITS[@]} 个 unit 全部 active（自启动恢复）"

    # ---------------------------------------------------------------------
    # Step 1f-resume-c: 数据对比（prevention 第二层 — 假阳性防护）
    # ---------------------------------------------------------------------
    DB_PASS=""
    if [[ -f /etc/ongrid/db-password ]]; then
        DB_PASS=$(tr -d '[:space:]' < /etc/ongrid/db-password)
    elif [[ -f /etc/ongrid/ongrid.env ]]; then
        DB_PASS=$(grep '^ONGRID_DB_DSN=' /etc/ongrid/ongrid.env \
            | head -1 \
            | sed -n 's|^ONGRID_DB_DSN=ongrid:\([^@]*\)@tcp.*|\1|p')
    fi
    [[ -n "$DB_PASS" ]] || fail "Cycle $cycle: 无法提取 DB 密码（etc/ongrid/db-password 和 ONGRID_DB_DSN 均无效）"
    DUMP_FILE="$DUMP_DIR/fresh-host-dump-cycle${cycle}.txt"

    DATA_AFTER=$(mysql -h 127.0.0.1 -u ongrid -p"$DB_PASS" ongrid -e "
        SELECT 'users', COUNT(*) FROM users;
        SELECT 'alert_events', COUNT(*) FROM alert_events;
        SELECT * FROM system_settings;
    " 2>/dev/null) || fail "Cycle $cycle: reboot 后数据 dump 失败（MariaDB 连接失败）"

    DATA_BEFORE=$(cat "$DUMP_FILE")
    if [[ "$DATA_BEFORE" != "$DATA_AFTER" ]]; then
        echo "--- reboot 前数据 ---" >&2
        echo "$DATA_BEFORE" >&2
        echo "--- reboot 后数据 ---" >&2
        echo "$DATA_AFTER" >&2
        fail ": Cycle $cycle — reboot 后数据不一致（users/alert_events/system_settings 对比失败）"
    fi
    pass "Cycle $cycle: reboot gate 通过 — unit active + 数据一致（防护）"

    # =========================================================================
    # Phase C: uninstall --purge（prevention）
    # =========================================================================

    # ---------------------------------------------------------------------
    # Step 1g: uninstall-systemd.sh --purge --yes
    # ---------------------------------------------------------------------
    log "Cycle $cycle: 执行 uninstall-systemd.sh --purge --yes"
    bash "$UNINSTALL_SH" --purge --yes
    pass "Cycle $cycle: uninstall-systemd.sh --purge --yes 完成"

    # ---------------------------------------------------------------------
    # Step 1h: find 全局扫描断言空（prevention）
    # ---------------------------------------------------------------------
    # fix: grep -vE 支持多排除项 OR 模式 + prune 虚拟 FS 避免 /proc /sys 干扰
    EXCLUDE_PATTERN=$(IFS='|'; echo "${FIND_EXCLUDES[*]}")
    RESIDUAL=$(find / \( -path /proc -o -path /sys -o -path /dev -o -path /run \) -prune \
        -o -name '*ongrid*' -print 2>/dev/null \
        | grep -vE "$EXCLUDE_PATTERN" || true)
    if [[ -n "$RESIDUAL" ]]; then
        echo "--- purge 后残留文件 ---" >&2
        echo "$RESIDUAL" >&2
        fail ": Cycle $cycle — purge 后残留 ongrid 文件（见上方列表）"
    fi
    pass "Cycle $cycle: find 全局扫描断言空"

    # 清理 dump 文件（本 cycle 完成）
    rm -f "$DUMP_FILE"

    # 清除 resume 标记（第一个 cycle resume 完成后，后续 cycle 走正常流程）
    RESUME_CYCLE=0

    echo ""
    log "Cycle $cycle: 全部步骤通过（install + reboot gate + purge + find 扫描）"

done  # for cycle in 1..CYCLES

# =============================================================================
# 总结
# =============================================================================
echo ""
echo "=========================================="
echo "FRESH-HOST ASSERT: ALL CYCLES PASSED"
echo "=========================================="
pass "fresh-host-assert.sh 全部 $CYCLES 次循环通过"
echo "  - install:        $CYCLES 次 PASS"
echo "  - reboot gate:    $CYCLES 次 PASS（unit active + 数据一致）"
echo "  - purge:          $CYCLES 次 PASS（find 断言空）"
echo "  - 幂等性:          $CYCLES 次循环无差异"
echo ""
echo "PASS: FRESH-HOST —  fresh host fresh VM 实测验收完成"

exit 0
