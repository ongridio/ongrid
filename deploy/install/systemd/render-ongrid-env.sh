#!/usr/bin/env bash
# render-ongrid-env.sh —— ongrid manager env 文件幂等渲染脚本（systemd 模式）。
# ---------------------------------------------------------------------------
# 用途：install-systemd.sh 写出的 /etc/ongrid/ongrid.env 是骨架，缺失
# JWT_SECRET / ADMIN / LLM key / 真实 DB DSN / embedding 显式覆盖。本脚本：
#   - 保留既有 env（包括 install-systemd.sh 写入的 FRONTIER_ADDR / PROM_URL 等）
#   - 若 JWT_SECRET 缺失或为默认值 → 生成 64-char random（永不重新生成）
#   - 若 ADMIN_PASSWORD 缺失 → 生成 20-char random 并一次性打印到 stderr
#   - ADMIN_EMAIL 固定 admin@ongrid.local
#   - EMBEDDING_PROVIDER 显式覆盖为 openai；空 key 时 manager 仅 warn 不阻塞
#   - DB DSN：从 /etc/ongrid/db-password 读密码，组装完整 DSN
#   - LLM provider key：从环境变量或交互式 prompt 读取（任一非空即可）
#
# Usage:
#   render-ongrid-env.sh [--provider openai|deepseek|glm] [--llm-key <key>]
#                        [--db-password <pwd>] [--embedding-key <key>]
#                        [--broker-public-addr <域名:端口>]
#                        [--tls-ca-file <CA 路径>]
#                        [--edge-only --broker-addr <域名:40012>
#                           --access-key <key> --secret-key <secret>
#                           [--tls-ca-file <CA 路径>]]
#
# 或通过环境变量注入（推荐，避免命令行历史记录）：
#   ONGRID_OPENAI_API_KEY=...     ONGRID_DB_PASSWORD=...     render-ongrid-env.sh
#   ONGRID_DEEPSEEK_API_KEY=...   ONGRID_DB_PASSWORD=...     render-ongrid-env.sh
#   ONGRID_ZHIPU_API_KEY=...      ONGRID_DB_PASSWORD=...     render-ongrid-env.sh
#
# 任一 provider key 都没传 → 交互式 prompt（read -s 隐藏输入）。
# 全部为空且非交互模式（CI）→ exit 1（fail-fast）。
#
# 退出码：
#   0  渲染成功
#   1  DB 密码缺失 / 所有 LLM provider key 都为空（非交互）
#   2  参数错误
#
# 幂等：重跑时 JWT_SECRET 与 ADMIN_PASSWORD 保留旧值。
# ---------------------------------------------------------------------------
set -euo pipefail

ENV_FILE="${ONGRID_ENV_FILE:-/etc/ongrid/ongrid.env}"
DB_PASSWORD_FILE="${ONGRID_DB_PASSWORD_FILE:-/etc/ongrid/db-password}"
DEFAULT_JWT_SECRET="dev-insecure-secret-change-me"
ADMIN_EMAIL="admin@ongrid.local"
EMBEDDING_PROVIDER="openai"
EMBEDDING_MODEL="text-embedding-3-small"
EMBEDDING_DIM="1536"

log()  { printf '[render-env] %s\n' "$*" >&2; }
die()  { printf '[render-env] error: %s\n' "$*" >&2; exit "${2:-1}"; }

# --- 参数解析 --------------------------------------------------------------
PROVIDER=""
LLM_KEY=""
DB_PASSWORD=""
EMBEDDING_KEY=""
BROKER_PUBLIC_ADDR=""
TLS_CA_FILE=""
EDGE_ONLY=0
EDGE_BROKER_ADDR=""
EDGE_ACCESS_KEY=""
EDGE_SECRET_KEY=""
INTERACTIVE=1

# 每个带值参数前先校验 $2 存在，避免 set -u 下 unbound
# variable 错误晦涩，shift 2 在 $#=1 时也出错。
while [ "$#" -gt 0 ]; do
    case "$1" in
        --provider)
            [ $# -ge 2 ] || die "--provider requires a value" 2
            PROVIDER="$2"; shift 2 ;;
        --llm-key)
            [ $# -ge 2 ] || die "--llm-key requires a value" 2
            LLM_KEY="$2"; shift 2 ;;
        --db-password)
            [ $# -ge 2 ] || die "--db-password requires a value" 2
            DB_PASSWORD="$2"; shift 2 ;;
        --embedding-key)
            [ $# -ge 2 ] || die "--embedding-key requires a value" 2
            EMBEDDING_KEY="$2"; shift 2 ;;
        --broker-public-addr)
            [ $# -ge 2 ] || die "--broker-public-addr requires a value" 2
            BROKER_PUBLIC_ADDR="$2"; shift 2 ;;
        --tls-ca-file)
            [ $# -ge 2 ] || die "--tls-ca-file requires a value" 2
            TLS_CA_FILE="$2"; shift 2 ;;
        --edge-only) EDGE_ONLY=1; shift ;;
        --broker-addr)
            [ $# -ge 2 ] || die "--broker-addr requires a value" 2
            EDGE_BROKER_ADDR="$2"; shift 2 ;;
        --access-key)
            [ $# -ge 2 ] || die "--access-key requires a value" 2
            EDGE_ACCESS_KEY="$2"; shift 2 ;;
        --secret-key)
            [ $# -ge 2 ] || die "--secret-key requires a value" 2
            EDGE_SECRET_KEY="$2"; shift 2 ;;
        --non-interactive) INTERACTIVE=0; shift ;;
        -h|--help)
            sed -n '1,/^$/p' "$0" | sed 's/^# \?//'
            exit 0 ;;
        *) die "unknown arg: $1" 2 ;;
    esac
done

# --- edge-only 模式------------------------------------------------
# 渲染边端需要的 env 子集（边端配置不漂移）。输出到
# /etc/ongrid-edge/ongrid-edge.env（Linux）或 stdout（供 Windows 边端用）。
if [ "$EDGE_ONLY" -eq 1 ]; then
    [ -n "$EDGE_BROKER_ADDR" ] || die "--edge-only requires --broker-addr <域名:40012>" 2
    [ -n "$EDGE_ACCESS_KEY" ] || die "--edge-only requires --access-key <key>" 2
    [ -n "$EDGE_SECRET_KEY" ] || die "--edge-only requires --secret-key <secret>" 2

    EDGE_ENV_FILE="${ONGRID_EDGE_ENV_FILE:-/etc/ongrid-edge/ongrid-edge.env}"
    EDGE_TLS_CA="${TLS_CA_FILE:-/etc/ongrid-edge/ca.pem}"

    # 边端 env 子集（+  TLS CA）
    EDGE_OUT=(
        "ONGRID_EDGE_CLOUD_ADDR=$EDGE_BROKER_ADDR"
        "ONGRID_EDGE_ACCESS_KEY=$EDGE_ACCESS_KEY"
        "ONGRID_EDGE_SECRET_KEY=$EDGE_SECRET_KEY"
        "ONGRID_EDGE_TLS_CA_FILE=$EDGE_TLS_CA"
        "ONGRID_EDGE_COLLECTOR_MODE=off"
    )

    # 原子写
    mkdir -p "$(dirname "$EDGE_ENV_FILE")" 2>/dev/null || true
    EDGE_TMP="${EDGE_ENV_FILE}.tmp.$$"
    {
        echo "# ongrid edge environment — rendered by render-ongrid-env.sh --edge-only"
        echo "# Deploy: SCP ca.pem + this env to edge host, then start ongrid-edge service."
        echo "# Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
        echo
        for line in "${EDGE_OUT[@]}"; do
            # key="value" 格式（引号包值防 shell 特殊字符）
            k="${line%%=*}"
            v="${line#*=}"
            printf '%s="%s"\n' "$k" "$v"
        done
    } > "$EDGE_TMP"
    chmod 0640 "$EDGE_TMP" 2>/dev/null || true
    mv -f "$EDGE_TMP" "$EDGE_ENV_FILE" 2>/dev/null || {
        # 无写权限（如 Windows/非 root）→ 输出到 stdout
        cat "$EDGE_TMP" && rm -f "$EDGE_TMP"
    }

    log "edge env summary:"
    log "  EDGE_CLOUD_ADDR  = $EDGE_BROKER_ADDR"
    log "  ACCESS_KEY       = ${EDGE_ACCESS_KEY:0:4}... (set)"
    log "  TLS_CA_FILE      = $EDGE_TLS_CA"
    exit 0
fi

# --- 解析 provider 与对应 env var -----------------------------------------
# provider 为空时按已设环境变量自动探测；显式 --provider 强制对齐。
if [ -z "$PROVIDER" ]; then
    if [ -n "${ONGRID_OPENAI_API_KEY:-}${LLM_KEY}" ]; then PROVIDER="openai"
    elif [ -n "${ONGRID_DEEPSEEK_API_KEY:-}" ]; then PROVIDER="deepseek"
    elif [ -n "${ONGRID_ZHIPU_API_KEY:-}" ]; then PROVIDER="glm"
    fi
fi

case "$PROVIDER" in
    ""|openai)
        PROVIDER_ENV_VAR="ONGRID_OPENAI_API_KEY"
        PROVIDER_MODEL_VAR="ONGRID_OPENAI_MODEL"
        PROVIDER_BASE_VAR="ONGRID_OPENAI_BASE_URL"
        PROVIDER_DEFAULT_MODEL="gpt-5.4" ;;
    deepseek)
        PROVIDER_ENV_VAR="ONGRID_DEEPSEEK_API_KEY"
        PROVIDER_MODEL_VAR="ONGRID_DEEPSEEK_MODEL"
        PROVIDER_BASE_VAR="ONGRID_DEEPSEEK_BASE_URL"
        PROVIDER_DEFAULT_MODEL="deepseek-chat" ;;
    glm)
        # 对齐：provider 命令行参数名仍为 glm（向下兼容），
        # 但 env var 名改为 ONGRID_ZHIPU_*（对齐 config.go + main.go 消费路径）。
        PROVIDER_ENV_VAR="ONGRID_ZHIPU_API_KEY"
        PROVIDER_MODEL_VAR="ONGRID_ZHIPU_MODEL"
        PROVIDER_BASE_VAR="ONGRID_ZHIPU_BASE_URL"
        PROVIDER_DEFAULT_MODEL="glm-4.7" ;;
    *) die "unknown provider: $PROVIDER (expected openai|deepseek|glm)" 2 ;;
esac

# --- DB 密码 ---------------------------------------------------------------
if [ -z "$DB_PASSWORD" ]; then
    DB_PASSWORD="${ONGRID_DB_PASSWORD:-}"
fi
if [ -z "$DB_PASSWORD" ] && [ -f "$DB_PASSWORD_FILE" ]; then
    DB_PASSWORD="$(tr -d '[:space:]' < "$DB_PASSWORD_FILE")"
fi
if [ -z "$DB_PASSWORD" ] && [ "$INTERACTIVE" -eq 1 ]; then
    printf '[render-env] Enter MariaDB ongrid user password: ' >&2
    read -s DB_PASSWORD >&2; echo >&2
fi
[ -n "$DB_PASSWORD" ] || die "DB password required (set --db-password / ONGRID_DB_PASSWORD / echo > $DB_PASSWORD_FILE)"

# --- LLM provider key -----------------------------------------------------
LLM_KEY="${LLM_KEY:-${!PROVIDER_ENV_VAR:-}}"
# 幂等：未显式传入时保留上次渲染写入 env 文件的 key（交互回车 skip 不清空既有 key）。
# 直接从 env 文件读（此处 CUR 尚未填充，见下方 declare -A CUR）。
if [ -z "$LLM_KEY" ] && [ -f "$ENV_FILE" ]; then
    _prev=$(grep -m1 "^${PROVIDER_ENV_VAR}=" "$ENV_FILE" | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//')
    [ -n "$_prev" ] && LLM_KEY="$_prev"
    unset _prev
fi
if [ -z "$LLM_KEY" ] && [ "$INTERACTIVE" -eq 1 ]; then
    printf '[render-env] Enter %s API key (empty to skip LLM): ' "$PROVIDER_ENV_VAR" >&2
    read -s LLM_KEY >&2; echo >&2
fi
# 仍为空 → 警告但不 fail（用户选了 "skip LLM" 模式）
if [ -z "$LLM_KEY" ]; then
    log "WARN: no LLM provider key provided — /api/v1/aiops/models will return empty ( soft-fail mode)"
fi

# --- embedding key（与 LLM key 复用即可）----------------------------------
EMBEDDING_KEY="${EMBEDDING_KEY:-${ONGRID_EMBEDDING_API_KEY:-${LLM_KEY}}}"

# --- 读既有 env（如果存在）------------------------------------------------
# strip_surrounding_quotes 去除值两侧的所有成对引号，防止每次重渲染堆叠多层
# 引号（"""value""" → value）。DSN 内部不含 "，安全。
strip_quotes() {
    local v="$1"
    # 循环剥离外层成对的双引号或单引号
    while :; do
        case "$v" in
            \"*\")
                local inner="${v#\"}"; inner="${inner%\"}"
                # 如果剥离后内部仍有成对引号包裹整个串，继续剥
                if [ "${#inner}" -lt "${#v}" ] && \
                   { case "$inner" in \"*\"|\'*\'*) true ;; *) false ;; esac; }; then
                    v="$inner"
                else
                    v="$inner"
                    break
                fi ;;
            # 单引号分支显式 break，使"只剥一层外层单引号"
            # 语义明确，不依赖下次循环命中 *) break 隐式退出。与双引号
            # 分支的 break 行为对齐。env 文件不会出现多层
            # 嵌套单引号（''value''），单层剥离足够。
            \'*\') v="${v#\'}"; v="${v%\'}"; break ;;
            *) break ;;
        esac
    done
    printf '%s' "$v"
}

declare -A CUR=()
if [ -f "$ENV_FILE" ]; then
    # || [ -n "$key" ] 防御文件末尾无换行符时最后一行被静默丢失。
    # POSIX read 在 EOF 但已读取部分内容时返回非 0（循环退出），此时 $key
    # 仍被填充。vim 部分版本或 echo -n 写入会导致末行无换行。
    while IFS='=' read -r key val || [ -n "$key" ]; do
        # 跳过注释与空行
        case "$key" in ''|'#'*) continue ;; esac
        CUR["$key"]="$(strip_quotes "$val")"
    done < "$ENV_FILE"
fi

# --- JWT_SECRET（永不重新生成）------------------------------------
JWT_SECRET="${CUR[ONGRID_JWT_SECRET]:-}"
if [ -z "$JWT_SECRET" ] || [ "$JWT_SECRET" = "$DEFAULT_JWT_SECRET" ]; then
    JWT_SECRET="$(openssl rand -hex 32)"
    log "generated new ONGRID_JWT_SECRET (64-char)"
else
    log "preserved existing ONGRID_JWT_SECRET"
fi

# --- ADMIN_PASSWORD（一次性打印）----------------------------------
ADMIN_PASSWORD="${CUR[ONGRID_ADMIN_PASSWORD]:-}"
if [ -z "$ADMIN_PASSWORD" ]; then
    # 20-char base64url，去除 /+= 防 shell quoting 问题
    ADMIN_PASSWORD="$(openssl rand -base64 18 | tr -d '/+=' | cut -c1-20)"
    # 注意：stderr 重定向（如 2>&1 | tee install.log）会把密码永久
    # 写入日志文件，违背"一次性打印"承诺。检测非 TTY 时额外 warn 提示
    # operator 不要 tee 此输出。不强制拒绝（CI 场景可能确实需要捕获输出
    # 做记录，由 operator 自行权衡）。
    if [[ ! -t 2 ]]; then
        log "WARN: stderr is not a TTY — ADMIN_PASSWORD below may be captured"
        log "  by pipe/tee. Avoid 'render-ongrid-env.sh 2>&1 | tee install.log'"
        log "  if the log file is shared or persisted."
    fi
    log "*** generated ONGRID_ADMIN_PASSWORD (save this now, shown once):"
    printf 'ADMIN_PASSWORD=%s\n' "$ADMIN_PASSWORD" >&2
else
    log "preserved existing ONGRID_ADMIN_PASSWORD (not re-printed)"
fi

# --- DB DSN ---------------------------------------------------------------
DB_DSN="ongrid:${DB_PASSWORD}@tcp(127.0.0.1:3306)/ongrid?parseTime=true&charset=utf8mb4&loc=Local"

# --- 组装最终 env ---------------------------------------------------------
# 保留 install-systemd.sh 已写的所有项，覆盖/补充 PLAN 指定的项。
# 通过 associative array 实现去重，最后按固定 key 顺序输出。
declare -A OUT=()
# 1. 先把既有的全部装进 OUT（保留 install-systemd.sh 写的 PROM_URL 等）
for k in "${!CUR[@]}"; do OUT["$k"]="${CUR[$k]}"; done
# 2. 覆盖/补充
OUT[ONGRID_HTTP_ADDR]="${CUR[ONGRID_HTTP_ADDR]:-:8080}"
# DB_DIALECT 永远是 mysql（项目仅支持 mysql），强制固定防止历史污染值。
OUT[ONGRID_DB_DIALECT]="mysql"
OUT[ONGRID_DB_DSN]="$DB_DSN"
OUT[ONGRID_JWT_SECRET]="$JWT_SECRET"
OUT[ONGRID_ADMIN_EMAIL]="$ADMIN_EMAIL"
OUT[ONGRID_ADMIN_PASSWORD]="$ADMIN_PASSWORD"
# --broker-public-addr 优先级最高（DDNS 域名），其次保留既有值，
# 最后默认 127.0.0.1:40011（向后兼容单机部署）。
if [ -n "$BROKER_PUBLIC_ADDR" ]; then
    OUT[ONGRID_FRONTIER_ADDR]="$BROKER_PUBLIC_ADDR"
else
    OUT[ONGRID_FRONTIER_ADDR]="${CUR[ONGRID_FRONTIER_ADDR]:-127.0.0.1:40011}"
fi
# manager TLS CA 文件路径（--tls-ca-file 或保留既有值）
if [ -n "$TLS_CA_FILE" ]; then
    OUT[ONGRID_FRONTIER_TLS_CA_FILE]="$TLS_CA_FILE"
elif [ -n "${CUR[ONGRID_FRONTIER_TLS_CA_FILE]:-}" ]; then
    OUT[ONGRID_FRONTIER_TLS_CA_FILE]="${CUR[ONGRID_FRONTIER_TLS_CA_FILE]}"
fi
OUT[ONGRID_FRONTIER_SERVICE_NAME]="${CUR[ONGRID_FRONTIER_SERVICE_NAME]:-ongrid-manager}"
OUT[ONGRID_PROM_ENABLED]="${CUR[ONGRID_PROM_ENABLED]:-true}"
OUT[ONGRID_PROM_URL]="${CUR[ONGRID_PROM_URL]:-http://127.0.0.1:9090}"
# Go config.go 消费 ONGRID_PROM_QUERY_URL，
# manager 与 Prometheus 同机时用 loopback 访问。Prom 的 systemd drop-in配置：
#   --web.external-url=http://<manager-host>/prometheus/   # 外网入口（nginx 反代路径）
#   --web.route-prefix=/                                   # loopback 实际服务路径（根路径）
# external-url 是给浏览器/UI 用的外网路径，route-prefix 才是 manager loopback 拨号时
# Prom 实际服务的路径。原注释混淆了两者，默认值 http://127.0.0.1:9090/prometheus
# 与 route-prefix=/ 冲突，导致 manager POST /prometheus/api/v1/write → 404。
# 默认值改为 http://127.0.0.1:9090（无子路径，对齐 route-prefix=/）。
# operator 若在 nginx 后面部署 manager（manager 与 Prometheus 不同机），可手动覆盖为
# http://<prom-host>/prometheus/（此时 manager 走 nginx 反代）。
OUT[ONGRID_PROM_QUERY_URL]="${CUR[ONGRID_PROM_QUERY_URL]:-http://127.0.0.1:9090}"
OUT[ONGRID_LOKI_URL]="${CUR[ONGRID_LOKI_URL]:-http://127.0.0.1:3100}"
OUT[ONGRID_TEMPO_URL]="${CUR[ONGRID_TEMPO_URL]:-http://127.0.0.1:3200}"
OUT[ONGRID_QDRANT_URL]="${CUR[ONGRID_QDRANT_URL]:-http://127.0.0.1:6333}"
# LLM provider
OUT[$PROVIDER_ENV_VAR]="$LLM_KEY"
OUT[$PROVIDER_MODEL_VAR]="${CUR[$PROVIDER_MODEL_VAR]:-$PROVIDER_DEFAULT_MODEL}"
OUT[$PROVIDER_BASE_VAR]="${CUR[$PROVIDER_BASE_VAR]:-}"
# LLM provider key 非空时无条件写 ONGRID_INVESTIGATOR_ENABLED=true，
# 让 main.go `os.Getenv("ONGRID_INVESTIGATOR_ENABLED") == "true"` gate 通过。
# 显式写 false（非省略）当 LLM key 为空，避免 operator 忘记开关导致的歧义状态。
OUT[ONGRID_INVESTIGATOR_ENABLED]="$([ -n "$LLM_KEY" ] && echo true || echo false)"
#  （ONGRID_AGENT_KERNEL）：默认 kernel=legacy → structured RCA investigator
# skip（main.go concreteRt == nil）。LLM key 非空时（即 investigator 实际可用）默认
# 切到 graph kernel，让 POST /v1/alerts/incidents/<id>/investigation 不再 503 feature_disabled。
# operator 显式设 legacy（回退旧行为）时 CUR 保留覆盖。
OUT[ONGRID_AGENT_KERNEL]="${CUR[ONGRID_AGENT_KERNEL]:-$([ -n "$LLM_KEY" ] && echo graph || echo legacy)}"
# Embedding — 保留既有 provider
# 强制覆盖 EMBEDDING_PROVIDER=openai 会让 operator 选 local（install-
# systemd.sh 默认配置，bundled offline ONNX + ONGRID_EMBEDDING_CACHE_DIR）
# 后被 render 重置为 openai（需 API key，无 key 时知识库 RAG 功能整体
# 不工作）。故保留既有 ONGRID_EMBEDDING_PROVIDER（如 CUR 已设）；CUR 无
# 值时用脚本默认（仍为 openai，向后兼容）。operator 在 install-systemd.sh
# 写入 local 后，重跑 render 不会覆盖。如需切换到 openai，operator 编辑
# ongrid.env 的 ONGRID_EMBEDDING_PROVIDER=openai + 注入 EMBEDDING_API_KEY
# 后重跑 render 即可。
OUT[ONGRID_EMBEDDING_PROVIDER]="${CUR[ONGRID_EMBEDDING_PROVIDER]:-$EMBEDDING_PROVIDER}"
OUT[ONGRID_EMBEDDING_MODEL]="${CUR[ONGRID_EMBEDDING_MODEL]:-$EMBEDDING_MODEL}"
OUT[ONGRID_EMBEDDING_DIM]="${CUR[ONGRID_EMBEDDING_DIM]:-$EMBEDDING_DIM}"
# 配套：显式纳入 KEY_ORDER，保留 install-systemd.sh 写入的 cache dir。
# 默认 /var/lib/ongrid/embeddings（与 install-systemd.sh 一致）。
OUT[ONGRID_EMBEDDING_CACHE_DIR]="${CUR[ONGRID_EMBEDDING_CACHE_DIR]:-/var/lib/ongrid/embeddings}"
OUT[ONGRID_EMBEDDING_API_KEY]="$EMBEDDING_KEY"
OUT[ONGRID_EMBEDDING_BASE_URL]="${CUR[ONGRID_EMBEDDING_BASE_URL]:-}"
# --- 固定顺序输出（便于 diff / review）-----------------------------------
KEY_ORDER=(
    ONGRID_HTTP_ADDR
    ONGRID_DB_DIALECT
    ONGRID_DB_DSN
    ONGRID_JWT_SECRET
    ONGRID_ADMIN_EMAIL
    ONGRID_ADMIN_PASSWORD
    ONGRID_FRONTIER_ADDR
    ONGRID_FRONTIER_SERVICE_NAME
    ONGRID_FRONTIER_TLS_CA_FILE
    ONGRID_PROM_ENABLED
    ONGRID_PROM_URL
    ONGRID_PROM_QUERY_URL
    ONGRID_LOKI_URL
    ONGRID_TEMPO_URL
    ONGRID_QDRANT_URL
    "$PROVIDER_ENV_VAR"
    "$PROVIDER_MODEL_VAR"
    "$PROVIDER_BASE_VAR"
    ONGRID_INVESTIGATOR_ENABLED
    ONGRID_AGENT_KERNEL
    ONGRID_EMBEDDING_PROVIDER
    ONGRID_EMBEDDING_MODEL
    ONGRID_EMBEDDING_DIM
    ONGRID_EMBEDDING_CACHE_DIR
    ONGRID_EMBEDDING_API_KEY
    ONGRID_EMBEDDING_BASE_URL
)

# --- 原子写：临时文件 + mv -----------------------------------------------
TMP="${ENV_FILE}.tmp.$$"
{
    echo "# ongrid manager environment — rendered by render-ongrid-env.sh"
    echo "# Edit then \`systemctl restart ongrid\`. Re-run this script to re-render."
    echo "# Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo
    for k in "${KEY_ORDER[@]}"; do
        # 引号包值防止 shell 特殊字符（DSN 的 & 等）
        printf '%s="%s"\n' "$k" "${OUT[$k]}"
    done
    # 追加 install-systemd.sh 写的、但不在 KEY_ORDER 里的其他变量（如 ONGRID_GRAFANA_*）
    for k in "${!OUT[@]}"; do
        local_skip=0
        for kk in "${KEY_ORDER[@]}"; do
            if [ "$k" = "$kk" ]; then local_skip=1; break; fi
        done
        [ "$local_skip" -eq 1 ] && continue
        printf '%s="%s"\n' "$k" "${OUT[$k]}"
    done
} > "$TMP"
chmod 0640 "$TMP"
chown root:ongrid "$TMP"
mv -f "$TMP" "$ENV_FILE"
# 注意：stat -c %a 是 GNU coreutils（Linux）语法，BSD/macOS 用
# stat -f %Lp。本项目部署目标是 Linux，但开发者本地测试或在 macOS 上
# 跑会失败。双语法 fallback + 最终 echo "???" 兜底，保证可移植性。
perm=$(stat -c %a "$ENV_FILE" 2>/dev/null || stat -f %Lp "$ENV_FILE" 2>/dev/null || echo "???")
log "wrote $ENV_FILE (0$perm root:ongrid)"

# --- 摘要（不含敏感值）---------------------------------------------------
log "summary:"
log "  ADMIN_EMAIL      = $ADMIN_EMAIL"
log "  JWT_SECRET       = ${JWT_SECRET:0:8}... (64-char)"
log "  EMBEDDING        = $EMBEDDING_PROVIDER / $EMBEDDING_MODEL / dim=$EMBEDDING_DIM"
_key_state="EMPTY (aiops disabled)"; [ -n "$LLM_KEY" ] && _key_state="set"
log "  LLM provider     = ${PROVIDER:-openai}  key=${_key_state}"
unset _key_state
log "  FRONTIER_ADDR    = ${OUT[ONGRID_FRONTIER_ADDR]}"
log "  PROM_QUERY_URL   = ${OUT[ONGRID_PROM_QUERY_URL]}"
log "  INVESTIGATOR     = ${OUT[ONGRID_INVESTIGATOR_ENABLED]}"
log "  DB DSN           = ongrid:***@tcp(127.0.0.1:3306)/ongrid"
