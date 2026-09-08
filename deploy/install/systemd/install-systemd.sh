#!/usr/bin/env bash
# ongrid pure-systemd installer.
#
# Runs from the extracted release tarball after the top-level
# Bare-metal systemd installer for the manager host: installs manager +
# frontier + dep stack (prometheus / loki / tempo / qdrant) as systemd
# units. OS-package deps (mariadb-server / nginx / grafana) are handled by
# install-deps.sh (run it first, or via --with-deps below).
#
# The install bails with a friendly message if any required binary is
# missing, rather than silently producing a half-working system.
#
# Requires: manager binaries pre-staged at deploy/install/bin/
# (see require_bundled_bin below for build instructions).

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)
BUNDLE_DIR=$(cd -- "$SCRIPT_DIR/.." && pwd)
cd "$BUNDLE_DIR"

if [[ -t 1 ]]; then
    C_RED=$'\033[0;31m'; C_GREEN=$'\033[0;32m'; C_YELLOW=$'\033[1;33m'
    C_CYAN=$'\033[0;36m'; C_BOLD=$'\033[1m'; C_RESET=$'\033[0m'
else
    C_RED=''; C_GREEN=''; C_YELLOW=''; C_CYAN=''; C_BOLD=''; C_RESET=''
fi

log()  { printf '%s[INFO]%s %s\n'  "$C_GREEN"  "$C_RESET" "$*"; }
warn() { printf '%s[WARN]%s %s\n'  "$C_YELLOW" "$C_RESET" "$*"; }
err()  { printf '%s[ERROR]%s %s\n' "$C_RED"    "$C_RESET" "$*" >&2; }

# 把文件 chown 给 root:ongrid，失败时显式 warn 而非静默继续。
# 与 install-deps.sh 同名 helper 行为一致（详见该脚本注释）。
chown_ongrid_or_warn() {
    local file="$1"
    if ! chown root:ongrid "$file" 2>/dev/null; then
        warn "failed to chown root:ongrid $file — ongrid group missing?"
        warn "  file owned by root:root; manager may fail to read it"
    fi
}

trap 'err "install-systemd failed at line $LINENO (exit $?)"' ERR

if [[ $EUID -ne 0 ]]; then
    err "must run as root (sudo)"
    exit 1
fi

# -----------------------------------------------------------------------------
# flags
# -----------------------------------------------------------------------------
WITH_DEPS=0
usage() {
    cat <<EOF
Usage: sudo bash install-systemd.sh [OPTIONS]

Options:
  --with-deps   Also run install-deps.sh — apt/dnf installs mariadb +
                nginx + grafana, downloads pinned prom/loki/tempo/qdrant
                binaries from upstream with sha256 verify, bootstraps the
                mariadb schema, writes grafana datasource provisioning +
                nginx site config. Internet required (~250 MB downloads).
  -h, --help    Print this help.

Without --with-deps the script only installs the manager + frontier
binaries + the six systemd units. Operator handles deps separately.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --with-deps) WITH_DEPS=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) err "unknown flag: $1"; usage; exit 2 ;;
    esac
done

# -----------------------------------------------------------------------------
# paths
# -----------------------------------------------------------------------------
PREFIX_BIN=/usr/local/bin
ETC_DIR=/etc/ongrid
STATE_DIR=/var/lib/ongrid
LOG_DIR=/var/log/ongrid
SYSTEMD_DIR=/etc/systemd/system
SERVICE_USER=ongrid

# -----------------------------------------------------------------------------
# system user
# -----------------------------------------------------------------------------
# useradd 失败时 set -e 会让脚本退出，但错误信息是 raw useradd
# 输出，不易诊断（PAM 配置异常 / /etc/passwd 锁竞争等）。显式 || { err; exit 1; }
# 给出清晰错误信息。主用户 ongrid 创建失败时若继续，下面 --gid ongrid 的
# dep_user 创建会连锁失败，所以主用户必须 fail-loud。
if id "$SERVICE_USER" &>/dev/null; then
    log "user $SERVICE_USER already exists"
else
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER" \
        || { err "failed to create system user $SERVICE_USER (check PAM / /etc/passwd lock)"; exit 1; }
    log "created system user $SERVICE_USER"
fi

# dep users — 独立 uid + 独立组（不加 ongrid 组）：manager 的 env 秘密
# (/etc/ongrid/ongrid.env, 0640 root:ongrid) 只允许 manager 本体读取；
# dep 服务（prometheus/loki/tempo/qdrant）被攻陷时既读不到 manager 秘密、
# 也互相读不到对方数据目录。
for dep_user in ongrid-prometheus ongrid-loki ongrid-tempo ongrid-qdrant; do
    if id "$dep_user" &>/dev/null; then
        log "user $dep_user already exists"
    else
        useradd --system --user-group --no-create-home --shell /usr/sbin/nologin "$dep_user" \
            || { err "failed to create dep user $dep_user"; exit 1; }
        log "created system user $dep_user (own group)"
    fi
done

# 回收数据目录属主：install-deps.sh 独立运行时用户未创建会降级 root:root，
# 此处（用户已创建）统一纠正，保证 loki.service / tempo.service 可启动。
[[ -d /loki ]]      && chown -R ongrid-loki:ongrid-loki /loki
[[ -d /var/tempo ]] && chown -R ongrid-tempo:ongrid-tempo /var/tempo

# -----------------------------------------------------------------------------
# directories
# -----------------------------------------------------------------------------
mkdir -p "$ETC_DIR" "$ETC_DIR/prometheus" "$STATE_DIR" "$LOG_DIR"
# 0755（非 0750）：dep 服务用户（独立组）需穿越目录读取各自配置；
# 敏感文件本身由 0640 + 属主组控制（ongrid.env 仅 ongrid 组可读）。
chown root:ongrid "$ETC_DIR" "$ETC_DIR/prometheus"
chmod 0755 "$ETC_DIR" "$ETC_DIR/prometheus"
chown ongrid:ongrid "$STATE_DIR" "$LOG_DIR"
chmod 0755 "$STATE_DIR" "$LOG_DIR"

# -----------------------------------------------------------------------------
# manager + frontier binaries
# -----------------------------------------------------------------------------
require_bundled_bin() {
    local name="$1"
    local src="$BUNDLE_DIR/bin/$name"
    if [[ ! -f "$src" ]]; then
        err "missing $src — pre-stage manager binaries before running this script:"
        err "  make build-ongrid  # produces bin/ongrid at repo root"
        err "  cp bin/ongrid deploy/install/bin/ongrid"
        err "  # ongrid-frontier: build from singchia/frontier or fetch its release"
        err "  cp <frontier-binary> deploy/install/bin/ongrid-frontier"
        exit 2
    fi
    install -m 0755 -o root -g root "$src" "$PREFIX_BIN/$name"
    log "installed $PREFIX_BIN/$name"
}
require_bundled_bin ongrid
require_bundled_bin ongrid-frontier

# -----------------------------------------------------------------------------
# stack-dep binaries (prom / loki / tempo / qdrant)
# expectation: operator pre-stages these at /usr/local/bin/* OR runs
# install-deps.sh first. We don't silently produce a half-stack.
# -----------------------------------------------------------------------------
MISSING_DEPS=()
for dep_bin in prometheus loki tempo qdrant; do
    if [[ ! -x "$PREFIX_BIN/$dep_bin" ]]; then
        MISSING_DEPS+=("$dep_bin")
    fi
done
if (( ${#MISSING_DEPS[@]} > 0 )); then
    warn "stack-dep binaries not found in $PREFIX_BIN: ${MISSING_DEPS[*]}"
    warn "manager unit will not start until they are installed."
    warn ""
    warn "install-deps.sh will download these from upstream"
    warn "releases with sha256-verify. For now, install them manually, e.g.:"
    warn "  https://github.com/prometheus/prometheus/releases"
    warn "  https://github.com/grafana/loki/releases"
    warn "  https://github.com/grafana/tempo/releases"
    warn "  https://github.com/qdrant/qdrant/releases"
fi

# -----------------------------------------------------------------------------
# OS-package deps
# -----------------------------------------------------------------------------
detect_pkg_mgr() {
    if command -v apt-get >/dev/null 2>&1; then echo apt
    elif command -v dnf >/dev/null 2>&1; then echo dnf
    elif command -v yum >/dev/null 2>&1; then echo yum
    else echo unknown
    fi
}
PKG_MGR=$(detect_pkg_mgr)
case "$PKG_MGR" in
    apt|dnf|yum)
        log "detected package manager: $PKG_MGR"
        ;;
    *)
        warn "unknown package manager — install mariadb-server / nginx / grafana manually"
        ;;
esac
# Actual apt/dnf install is in  install-deps.sh; this skeleton
# only sets the systemd units in place so the operator can drive deps
# install separately and run `systemctl start` on `ongrid` when ready.

# -----------------------------------------------------------------------------
# configs
# -----------------------------------------------------------------------------
# copy_conf 缺失文件时 append 到 MISSING_CONFS，所有调用后统一检查
# 并在 summary 中显式列出，让 operator 可见哪些配置缺失（仅 warn 不
# 统计时，install 完成的 summary 无法区分"全部就位"vs"部分缺失"）。
# 保持 warn 语义（不 fail，operator 可能有意不打包某些配置）。
declare -a MISSING_CONFS=()
copy_conf() {
    local src="$1" dst="$2"
    if [[ -f "$src" ]]; then
        install -m 0640 -o root -g ongrid "$src" "$dst"
        log "wrote $dst"
    else
        warn "$src missing — skipping"
        MISSING_CONFS+=("$dst")
    fi
}
# 配置属主按消费服务的独立组（ongrid-prometheus/loki/tempo），
# 非 ongrid 组——dep 服务不在 ongrid 组（见上面 useradd 注释）。
copy_conf_grp() {
    local src="$1" dst="$2" grp="$3"
    if [[ -f "$src" ]]; then
        install -m 0640 -o root -g "$grp" "$src" "$dst"
        log "wrote $dst"
    else
        warn "$src missing — skipping"
        MISSING_CONFS+=("$dst")
    fi
}
# 裸机拓扑 scrape 配置由本套件自带（upstream 的 prometheus/prometheus.yml
# 是 compose 拓扑：容器 DNS 主机名 + 无 exporter job，裸机不可用）。
copy_conf_grp "$SCRIPT_DIR/prometheus-scrape.yml"    "$ETC_DIR/prometheus/prometheus.yml" ongrid-prometheus
copy_conf_grp "$BUNDLE_DIR/prometheus-rules.yml"     "$ETC_DIR/prometheus/rules.yml"      ongrid-prometheus
copy_conf_grp "$BUNDLE_DIR/loki-config.yaml"         "$ETC_DIR/loki-config.yaml"          ongrid-loki
copy_conf_grp "$BUNDLE_DIR/tempo-config.yaml"        "$ETC_DIR/tempo-config.yaml"         ongrid-tempo

# The compose-mode configs hard-code container-volume paths (/loki, /var/tempo)
# which install-deps.sh step 3a also creates as host directories owned by
# ongrid-{loki,tempo}. The matching loki.service / tempo.service set
# ReadWritePaths=/loki / /var/tempo so ProtectSystem does not block writes.
# Do NOT rewrite these paths here — keeping them consistent across both
# install scripts avoids the  path-mismatch bug (yaml pointing at a
# directory that install-deps.sh never created). systemd StateDirectory
# is intentionally NOT used for loki/tempo because the data lives at the
# top-level paths the compose mode also uses, so the same volume can be
# shared across install topologies.
copy_conf "$BUNDLE_DIR/frontier.yaml"             "$ETC_DIR/frontier.yaml"

# 所有 copy_conf 调用后统一检查缺失项，显式列出让 operator 可见。
# 不 fail（保持 warn 语义），但 summary 可见避免"部分配置缺失但 install
# 报成功"的歧义。对应服务启动时会因缺配置 crash，此处提前预警。
if (( ${#MISSING_CONFS[@]} > 0 )); then
    warn "missing config files (services may fail to start):"
    for c in "${MISSING_CONFS[@]}"; do warn "  - $c"; done
fi

# process_exporter 默认配置——监控所有进程，按 comm 名称分组。
# process_exporter 启动需要 --config.path 指向的 yaml 存在，否则 fail。
PROCESS_EXPORTER_CONF="$ETC_DIR/process-exporter.yml"
if [[ ! -f "$PROCESS_EXPORTER_CONF" ]]; then
    cat > "$PROCESS_EXPORTER_CONF" <<'EOF'
# 默认监控所有进程（按 comm 名称分组）。systemd 模式由 install-systemd.sh
# 首次写入 /etc/ongrid/process-exporter.yml；后续运行保留 operator 改动。
process_names:
  - name: "{{.Comm}}"
    cmdline:
      - '.+'
EOF
    chmod 0640 "$PROCESS_EXPORTER_CONF"
    chown root:ongrid-prometheus "$PROCESS_EXPORTER_CONF"
    log "wrote $PROCESS_EXPORTER_CONF (default: all processes by comm)"
else
    log "preserved existing $PROCESS_EXPORTER_CONF"
fi

# frontier broker auth token —— install-systemd.sh
# 也作为入口生成，确保 operator 直接跑 install-systemd.sh（不经过
# install-deps.sh）时也能初始化 token。逻辑与 install-deps.sh step 3b
# 完全一致，幂等。
#
# frontier-auth.env 声明降级：ONGRID_FRONTIER_AUTH_TOKEN
# 在 Go 代码层零消费（internal/ + cmd/ 下 0 引用，main.go
# managersvcfb.Config{Addr, ServiceName} 无 Token 字段）。真实鉴权走
# EDGE_ACCESS_KEY/SECRET_KEY（DB 行 + manager CreateEdge API），127.0.0.1
# 绑定 + 单机部署兜底。保留 token 生成逻辑作为扩展位，未来 frontier 启用
# Meta 校验时可直接消费此变量。
FRONTIER_AUTH_FILE="$ETC_DIR/frontier-auth.env"
if [[ ! -f "$FRONTIER_AUTH_FILE" ]]; then
    AUTH_TOKEN=$(openssl rand -hex 32)
    printf 'ONGRID_FRONTIER_AUTH_TOKEN=%s\n' "$AUTH_TOKEN" > "$FRONTIER_AUTH_FILE"
    chmod 0600 "$FRONTIER_AUTH_FILE"
    chown_ongrid_or_warn "$FRONTIER_AUTH_FILE"
    log "generated frontier auth token → $FRONTIER_AUTH_FILE (0600 root:ongrid)"
else
    log "frontier auth token already present at $FRONTIER_AUTH_FILE — reusing"
fi

# manager env file — first install only; subsequent runs preserve.
ENV_FILE="$ETC_DIR/ongrid.env"
if [[ ! -f "$ENV_FILE" ]]; then
    cat > "$ENV_FILE" <<EOF
# ongrid manager environment — systemd mode.
# Edit this file then \`systemctl restart ongrid\`.

# Listening address (the manager HTTP listener)
ONGRID_HTTP_ADDR=:8080

# Datastore (defaults assume mariadb on localhost — install-deps.sh
# auto-fills the password when it bootstraps the schema)
ONGRID_DB_DIALECT=mysql
ONGRID_DB_DSN=ongrid:CHANGE_ME@tcp(127.0.0.1:3306)/ongrid?parseTime=true&charset=utf8mb4&loc=Local

# Frontier broker — co-located on the same host in systemd mode.
# Default frontier.yaml listens service-bound on :40011, edge-bound on
# :40012. If you point at an external frontier, change to its host:port.
ONGRID_FRONTIER_ADDR=127.0.0.1:40011
ONGRID_FRONTIER_SERVICE_NAME=ongrid-manager

# Telemetry deps — local systemd units installed by install-deps.sh.
# Set *_ENABLED=true once each dep is healthy (systemctl is-active …).
ONGRID_PROM_ENABLED=true
ONGRID_PROM_URL=http://127.0.0.1:9090
ONGRID_LOKI_URL=http://127.0.0.1:3100
ONGRID_TEMPO_URL=http://127.0.0.1:3200
ONGRID_QDRANT_URL=http://127.0.0.1:6333

# LLM — fill in to enable AIOps agents
ONGRID_OPENAI_API_KEY=
ONGRID_OPENAI_MODEL=glm-4-plus
ONGRID_OPENAI_BASE_URL=

# Knowledge base embedder — defaults to the bundled offline ONNX model so
# 知识库 works with no API key. install-deps.sh installs libonnxruntime.so
# and this script stages the model into ONGRID_EMBEDDING_CACHE_DIR below.
# To use a hosted embedder instead, set PROVIDER=openai + API_KEY/BASE_URL
# + a matching MODEL/DIM (e.g. GLM embedding-3 / dim 2048).
ONGRID_EMBEDDING_PROVIDER=local
ONGRID_EMBEDDING_MODEL=bge-small-zh-v1.5
ONGRID_EMBEDDING_DIM=512
ONGRID_EMBEDDING_CACHE_DIR=/var/lib/ongrid/embeddings
# Grafana API 内部地址（Go 默认 http://grafana:3000/grafana 是 compose 主机名，
# 裸机必须显式覆盖为 loopback）。
ONGRID_GRAFANA_INTERNAL_URL=http://127.0.0.1:3000/grafana
EOF
    chmod 0640 "$ENV_FILE"
    chown root:ongrid "$ENV_FILE"
    log "wrote $ENV_FILE (REVIEW + edit secrets before starting)"
else
    log "preserved existing $ENV_FILE"
fi

# -----------------------------------------------------------------------------
# SPA 静态部署
# -----------------------------------------------------------------------------
# 从 tarball 的 web/dist/ 部署到 /var/lib/ongrid/web/（manager 二进制从
# ONGRID_HTTP_ADDR 同进程提供静态资源，但仍需 nginx 提供前端
# 静态资源到 /var/lib/ongrid/web/，或 manager 通过 staticfs 提供）。tarball
# 可能未含 web/dist（operator 单独构建前端），此时不 fail，仅 warn 提示。
WEB_SRC="$BUNDLE_DIR/web/dist"
WEB_DST="$STATE_DIR/web"
if [[ -d "$WEB_SRC" ]] && [[ -f "$WEB_SRC/index.html" ]]; then
    install -d -m 0755 -o ongrid -g ongrid "$WEB_DST"
    # cp -r dist/. 而非 rm -rf web/ && cp —— 保幂等，不破坏 operator 自定义文件
    cp -r "$WEB_SRC/." "$WEB_DST/"
    # 注意 chown/chmod 顺序：必须先 chown 切换 owner，再设权限。
    # 若 chmod -R a+rX 先于 chown 执行，会对整个目录树无差别加 ugo+r/X，
    # operator 之前在目录内放的 .env / secrets / 调试文件会被暴露
    # 为 world-readable（chown 切换 owner 不会重置权限位）。
    #
    # chmod -R a+rX 保证 nginx www-data 可读
    # （a+rX = 文件 0644 + 目录 0755），避免
    # assets/*.js 落到 0640 导致 403 Forbidden。a+rX 是累加权限（只加
    # 不减），故先 chown 后 chmod 时 ongrid owner 的既有文件不会被
    # 降权，仅世界可读位被加。如 operator 在 web/ 放了 secrets，
    # 应在 cp 前 rm 或单独 chmod 0600——本 install 脚本不替 operator
    # 管理自定义文件的权限收紧。
    chown -R ongrid:ongrid "$WEB_DST"
    chmod -R a+rX /var/lib/ongrid/web/
    log "deployed SPA static assets → $WEB_DST (chmod a+rX)"
else
    warn "no web/dist in bundle — SPA not deployed"
    warn "  build with 'cd web && npm ci && npm run build' before packaging"
    warn "  then re-run install-systemd.sh to deploy SPA"
fi

# -----------------------------------------------------------------------------
# local embedding model — stage the bundled BGE-small-zh
# cache into ONGRID_EMBEDDING_CACHE_DIR so the offline embedder loads with
# no HuggingFace reach. Mirrors what compose's install.sh does. The .so
# itself is installed by install-deps.sh; this is just the model weights.
# -----------------------------------------------------------------------------
EMB_SRC="$BUNDLE_DIR/embeddings"
EMB_DST="$STATE_DIR/embeddings"
if [[ -d "$EMB_SRC" ]] && compgen -G "$EMB_SRC/*" >/dev/null; then
    if [[ ! -d "$EMB_DST" ]] || [[ -z "$(ls -A "$EMB_DST" 2>/dev/null)" ]]; then
        install -d -m 0755 "$EMB_DST"
        cp -rf "$EMB_SRC/." "$EMB_DST/"
        chown -R ongrid:ongrid "$EMB_DST"
        log "staged embedding model → $EMB_DST"
    else
        log "embedding model already present at $EMB_DST — preserved"
    fi
else
    warn "no bundled embedding model — local embedder needs the model at"
    warn "  $EMB_DST (or switch to an API-key embedder in $ENV_FILE)"
fi

# -----------------------------------------------------------------------------
# agents + skills
# -----------------------------------------------------------------------------
# chatruntime 在 manager 运行时通过 WorkingDirectory=/var/lib/ongrid 下的
# agents/ + skills/ 子目录加载 Agent persona + 内置技能。tarball 含 agents/
# + skills/（package.sh 打包），此处幂等部署到 STATE_DIR（/var/lib/ongrid）。
# 缺失时不 fail（warn），与 web/dist 同策略——operator 可单独构建/上传。
AGENTS_SRC="$BUNDLE_DIR/agents"
AGENTS_DST="$STATE_DIR/agents"
if [[ -d "$AGENTS_SRC" ]] && compgen -G "$AGENTS_SRC/*.md" >/dev/null; then
    install -d -m 0755 -o ongrid -g ongrid "$AGENTS_DST"
    # cp -r agents/. 而非 rm -rf agents/ && cp —— 保幂等，不破坏 operator 自定义 agent
    cp -r "$AGENTS_SRC/." "$AGENTS_DST/"
    chown -R ongrid:ongrid "$AGENTS_DST"
    log "deployed chatruntime agents → $AGENTS_DST ($(ls -1 "$AGENTS_DST"/*.md 2>/dev/null | wc -l) personas)"
else
    warn "no agents/*.md in bundle — chatruntime will boot with 0 agents"
    warn "  structured RCA worker spawn will fail (\"agent incident-investigator not found\")"
    warn "  re-run install-systemd.sh after packaging agents/ into the tarball"
fi

SKILLS_SRC="$BUNDLE_DIR/skills"
SKILLS_DST="$STATE_DIR/skills"
if [[ -d "$SKILLS_SRC" ]] && [[ -n "$(ls -A "$SKILLS_SRC" 2>/dev/null)" ]]; then
    install -d -m 0755 -o ongrid -g ongrid "$SKILLS_DST"
    cp -r "$SKILLS_SRC/." "$SKILLS_DST/"
    chown -R ongrid:ongrid "$SKILLS_DST"
    log "deployed chatruntime skills → $SKILLS_DST"
else
    warn "no skills/ in bundle — chatruntime built-in skills (bash/host-files/restart-service) unavailable"
fi

# -----------------------------------------------------------------------------
# systemd units
# -----------------------------------------------------------------------------
for unit in ongrid.service ongrid-frontier.service \
            prometheus.service loki.service tempo.service qdrant.service \
            node_exporter.service process_exporter.service; do
    install -m 0644 -o root -g root \
        "$SCRIPT_DIR/$unit" "$SYSTEMD_DIR/$unit"
    log "installed $SYSTEMD_DIR/$unit"
done

# -----------------------------------------------------------------------------
# wait-for-deps.sh + ongrid.service.d drop-in
# -----------------------------------------------------------------------------
# cp wait-for-deps.sh 到 /usr/local/bin/ + ongrid.service.d/wait-for-deps.conf
# 到 SYSTEMD_DIR/ongrid.service.d/，激活 ExecStartPre gate。drop-in 让
# ongrid.service 在 ExecStart 前等 MariaDB/Qdrant/frontier 三大依赖 LISTEN，
# 避免 reboot 自启 panic 循环。严格模式 ExecStartPre 无 `-` 前缀，失败按
# StartLimitBurst 规则重启。
if [[ -f "$SCRIPT_DIR/wait-for-deps.sh" ]]; then
    install -m 0755 -o root -g root "$SCRIPT_DIR/wait-for-deps.sh" "$PREFIX_BIN/wait-for-deps.sh"
    log "installed $PREFIX_BIN/wait-for-deps.sh"
else
    warn "wait-for-deps.sh missing in bundle — reboot self-start gate disabled"
fi

install -d -m 0755 "$SYSTEMD_DIR/ongrid.service.d"
if [[ -f "$SCRIPT_DIR/ongrid.service.d/wait-for-deps.conf" ]]; then
    install -m 0644 -o root -g root \
        "$SCRIPT_DIR/ongrid.service.d/wait-for-deps.conf" \
        "$SYSTEMD_DIR/ongrid.service.d/wait-for-deps.conf"
    log "installed $SYSTEMD_DIR/ongrid.service.d/wait-for-deps.conf (drop-in)"
fi

# ongrid-healthcheck.sh：独立 /readyz 探针，供 nginx healthcheck
# 或外部监控系统调用。不读敏感变量。
if [[ -f "$SCRIPT_DIR/ongrid-healthcheck.sh" ]]; then
    install -m 0755 -o root -g root "$SCRIPT_DIR/ongrid-healthcheck.sh" "$PREFIX_BIN/ongrid-healthcheck.sh"
    log "installed $PREFIX_BIN/ongrid-healthcheck.sh"
fi

# -----------------------------------------------------------------------------
# nginx 子路径反代配置
# -----------------------------------------------------------------------------
# cp snippets/ongrid-locations.conf + sites-available/ongrid + 建 symlink
# sites-enabled/ongrid + rm sites-enabled/default（避免 80 端口 default_server
# 冲突）。grafana.ini.fragment 合并到 grafana.ini
# [server] section（幂等：先 grep 检查）。nginx -t 验证语法；不 reload
# （保持 enable-only 原则，operator review ongrid.env 后手动 systemctl start）。
if [[ -d "/etc/nginx" ]]; then
    # 防御：清理 install-deps.sh 历史版本写入的 conf.d/ongrid.conf
    # 残留，避免与 sites-available/ongrid 的 `listen 80 default_server`
    # 重复定义冲突。install-deps.sh 现版本已不再写此文件。
    if [[ -f "/etc/nginx/conf.d/ongrid.conf" ]]; then
        rm -f /etc/nginx/conf.d/ongrid.conf
        log "removed stale /etc/nginx/conf.d/ongrid.conf (superseded by sites-available)"
    fi
    # snippets
    install -d -m 0755 /etc/nginx/snippets
    if [[ -f "$SCRIPT_DIR/nginx/snippets/ongrid-locations.conf" ]]; then
        install -m 0644 "$SCRIPT_DIR/nginx/snippets/ongrid-locations.conf" \
            /etc/nginx/snippets/ongrid-locations.conf
        log "installed /etc/nginx/snippets/ongrid-locations.conf"
    fi
    # map $http_upgrade $connection_upgrade 定义在 http context
    # 才能被 snippets/ongrid-locations.conf 的 location 块引用。抽到独立
    # conf.d/ongrid-upgrade-map.conf，避免 nginx-ongrid.conf 单体版本与
    # sites-available 版本重复定义 map（双写路径下的 duplicate map 错误）。
    install -d -m 0755 /etc/nginx/conf.d
    if [[ -f "$SCRIPT_DIR/nginx/snippets/ongrid-upgrade-map.conf" ]]; then
        install -m 0644 "$SCRIPT_DIR/nginx/snippets/ongrid-upgrade-map.conf" \
            /etc/nginx/conf.d/ongrid-upgrade-map.conf
        log "installed /etc/nginx/conf.d/ongrid-upgrade-map.conf (http context for connection_upgrade)"
    fi
    # sites-available
    install -d -m 0755 /etc/nginx/sites-available
    if [[ -f "$SCRIPT_DIR/nginx/sites-available/ongrid" ]]; then
        install -m 0644 "$SCRIPT_DIR/nginx/sites-available/ongrid" \
            /etc/nginx/sites-available/ongrid
        log "installed /etc/nginx/sites-available/ongrid"
    fi
    # sites-enabled symlink
    install -d -m 0755 /etc/nginx/sites-enabled
    ln -sf /etc/nginx/sites-available/ongrid /etc/nginx/sites-enabled/ongrid
    log "symlinked /etc/nginx/sites-enabled/ongrid → sites-available/ongrid"
    # rm default site（避免 80 default_server 冲突）
    if [[ -L "/etc/nginx/sites-enabled/default" ]] || [[ -f "/etc/nginx/sites-enabled/default" ]]; then
        rm -f /etc/nginx/sites-enabled/default
        log "removed /etc/nginx/sites-enabled/default (avoid 80 default_server conflict)"
    fi

    # nginx -t 语法验证（nginx 已装时）
    if command -v nginx >/dev/null 2>&1; then
        if nginx -t 2>&1; then
            log "nginx -t syntax OK"
        else
            warn "nginx -t failed — review /etc/nginx/snippets/ongrid-locations.conf"
            warn "  + /etc/nginx/sites-available/ongrid before starting nginx"
        fi
    fi
else
    warn "/etc/nginx not present — nginx site config not deployed"
    warn "  install nginx first (apt-get install nginx) then re-run install-systemd.sh"
fi

# -----------------------------------------------------------------------------
# grafana.ini fragment 合并
# -----------------------------------------------------------------------------
# 把 grafana-provisioning/grafana.ini.fragment 的 [server] section 合并到
# /etc/grafana/grafana.ini。幂等：先 grep serve_from_sub_path 检查是否已存在。
GRAFANA_INI=/etc/grafana/grafana.ini
GRAFANA_FRAGMENT="$SCRIPT_DIR/grafana-provisioning/grafana.ini.fragment"
if [[ -f "$GRAFANA_INI" ]] && [[ -f "$GRAFANA_FRAGMENT" ]]; then
    if grep -q 'serve_from_sub_path' "$GRAFANA_INI" 2>/dev/null; then
        log "grafana.ini already has serve_from_sub_path — fragment skipped (idempotent)"
    else
        # append fragment（如 grafana.ini 已有 [server] section，sed 追加；
        # 否则 append 整个 fragment，含 [server] 头）
        if grep -q '^\[server\]' "$GRAFANA_INI" 2>/dev/null; then
            # 在既有 [server] section 末尾追加两行（简单策略：直接 append
            # fragment，grafana 允许多个 [server] section 合并）
            cat "$GRAFANA_FRAGMENT" >> "$GRAFANA_INI"
            log "appended grafana.ini.fragment to $GRAFANA_INI (existing [server] section)"
        else
            cat "$GRAFANA_FRAGMENT" >> "$GRAFANA_INI"
            log "appended grafana.ini.fragment to $GRAFANA_INI (new [server] section)"
        fi
        # 提示 operator 重启 grafana 让配置生效
        if systemctl is-active --quiet grafana-server 2>/dev/null; then
            warn "grafana-server is running — restart to apply grafana.ini changes:"
            warn "  sudo systemctl restart grafana-server"
        fi
    fi
fi

systemctl daemon-reload
log "systemd daemon-reload"

# -----------------------------------------------------------------------------
# optional: dep auto-install
# -----------------------------------------------------------------------------
if [[ $WITH_DEPS -eq 1 ]]; then
    log "running install-deps.sh"
    bash "$SCRIPT_DIR/install-deps.sh"
fi

# -----------------------------------------------------------------------------
# enable but don't auto-start — operator should review env file first
# -----------------------------------------------------------------------------
systemctl enable ongrid.service ongrid-frontier.service \
                 prometheus.service loki.service tempo.service qdrant.service \
                 node_exporter.service process_exporter.service \
    >/dev/null 2>&1
log "enabled units (will start at boot)"

cat <<EOF

${C_BOLD}${C_GREEN}systemd install complete${C_RESET}

Next steps:
  1. Install OS-package deps if not already present:
EOF
case "$PKG_MGR" in
    apt) printf "       sudo apt-get install -y mariadb-server nginx\n" ;;
    dnf) printf "       sudo dnf install -y mariadb-server nginx\n" ;;
    yum) printf "       sudo yum install -y mariadb-server nginx\n" ;;
esac
cat <<EOF
  2. Install stack-dep binaries (install-deps.sh helper coming):
       prometheus, loki, tempo, qdrant → ${PREFIX_BIN}/
  3. Initialise mariadb schema (CREATE DATABASE ongrid; user grants).
  4. Review and edit ${ETC_DIR}/ongrid.env (DB password, LLM key, etc.)
  5. Start the stack (after reviewing ${ETC_DIR}/ongrid.env):
       sudo systemctl start prometheus loki tempo qdrant
       sudo systemctl start ongrid-frontier
       sudo systemctl start ongrid       # manager unit (Type=simple)
  6. Watch:
       sudo journalctl -u ongrid -f

Roll back this install:
  sudo $SCRIPT_DIR/uninstall-systemd.sh           # stop + remove units
  sudo $SCRIPT_DIR/uninstall-systemd.sh --purge   # also delete data dirs + user
EOF
