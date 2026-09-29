#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

fail() {
    printf 'install data-directories test failed: %s\n' "$*" >&2
    exit 1
}

# 只解析 Compose 配置，不访问 Docker daemon，也不启动容器。
docker compose version >/dev/null 2>&1 || fail "Docker Compose is required"
# shellcheck source=../deploy/install/data-permissions.sh
source "$repo_root/deploy/install/data-permissions.sh"
declare -F ongrid_resolve_data_directories >/dev/null \
    || fail "missing ongrid_resolve_data_directories"

install_dir="$tmp_dir/install root"
env_file="$install_dir/.env"
resolver_log="$tmp_dir/resolver.log"
mkdir -p "$install_dir"

reset_environment() {
    unset ONGRID_DATA_DIR ONGRID_LOG_DIR ONGRID_DIRECTORY_TEST_ROOT
    : >"$env_file"
}

assert_resolved() {
    local name="$1" expected_data="$2" expected_log="$3"
    ongrid_resolve_data_directories "$env_file" "$install_dir" >>"$resolver_log" 2>&1 \
        || fail "$name: resolution failed"
    [[ "$ONGRID_DATA_DIR" == "$expected_data" ]] || fail "$name: incorrect data directory"
    [[ "$ONGRID_LOG_DIR" == "$expected_log" ]] || fail "$name: incorrect log directory"
    bash -c '[[ "$ONGRID_DATA_DIR" == "$1" && "$ONGRID_LOG_DIR" == "$2" ]]' \
        bash "$expected_data" "$expected_log" || fail "$name: directories were not exported"
}

reset_environment
printf 'ONGRID_DATA_DIR=%s/data\nONGRID_LOG_DIR=%s/log\n' "$tmp_dir" "$tmp_dir" >"$env_file"
assert_resolved dotenv "$tmp_dir/data" "$tmp_dir/log"

reset_environment
printf 'ONGRID_DATA_DIR=%s/data\nONGRID_LOG_DIR=%s/log\n' "$tmp_dir" "$tmp_dir" >"$env_file"
export ONGRID_DATA_DIR="$tmp_dir/shell data" ONGRID_LOG_DIR="$tmp_dir/shell log"
assert_resolved shell-override "$tmp_dir/shell data" "$tmp_dir/shell log"

reset_environment
assert_resolved unset /var/lib/ongrid /var/log/ongrid
reset_environment
printf 'ONGRID_DATA_DIR=\nONGRID_LOG_DIR=\n' >"$env_file"
assert_resolved empty-dotenv /var/lib/ongrid /var/log/ongrid
reset_environment
printf 'ONGRID_DATA_DIR=%s/data\nONGRID_LOG_DIR=%s/log\n' "$tmp_dir" "$tmp_dir" >"$env_file"
export ONGRID_DATA_DIR='' ONGRID_LOG_DIR=''
assert_resolved empty-shell /var/lib/ongrid /var/log/ongrid

reset_environment
printf 'ONGRID_DATA_DIR=%s/data\n' "$tmp_dir" >"$env_file"
assert_resolved data-only "$tmp_dir/data" /var/log/ongrid
reset_environment
printf 'ONGRID_LOG_DIR=%s/log\n' "$tmp_dir" >"$env_file"
assert_resolved log-only /var/lib/ongrid "$tmp_dir/log"

reset_environment
cat >"$env_file" <<EOF
ONGRID_DATA_DIR='$tmp_dir/single quoted # data' # ignored comment
ONGRID_LOG_DIR="$tmp_dir/double quoted log" # ignored comment
EOF
assert_resolved quoted "$tmp_dir/single quoted # data" "$tmp_dir/double quoted log"
reset_environment
printf 'ONGRID_DATA_DIR = %s/data # ignored\nONGRID_LOG_DIR=%s/log#kept\n' \
    "$tmp_dir" "$tmp_dir" >"$env_file"
assert_resolved whitespace-and-comments "$tmp_dir/data" "$tmp_dir/log#kept"

reset_environment
cat >"$env_file" <<EOF
ONGRID_DIRECTORY_TEST_ROOT=$tmp_dir/reference
ONGRID_DATA_DIR=\${ONGRID_DIRECTORY_TEST_ROOT}/data
ONGRID_LOG_DIR=\${ONGRID_DIRECTORY_TEST_ROOT}/log
EOF
assert_resolved dotenv-reference "$tmp_dir/reference/data" "$tmp_dir/reference/log"
reset_environment
export ONGRID_DIRECTORY_TEST_ROOT="$tmp_dir/shell reference"
cat >"$env_file" <<'EOF'
ONGRID_DATA_DIR=${ONGRID_DIRECTORY_TEST_ROOT}/data
ONGRID_LOG_DIR=${ONGRID_DATA_DIR}/logs
EOF
assert_resolved shell-reference "$tmp_dir/shell reference/data" "$tmp_dir/shell reference/data/logs"

reset_environment
printf 'ONGRID_DATA_DIR=relative data\nONGRID_LOG_DIR=relative logs\n' >"$env_file"
assert_resolved relative "$install_dir/relative data" "$install_dir/relative logs"
reset_environment
printf 'ONGRID_DATA_DIR=relative data\nONGRID_LOG_DIR=relative logs\n' >"$env_file"
(
    cd "$tmp_dir"
    install_dir='install root'
    assert_resolved relative-install "$tmp_dir/install root/relative data" "$tmp_dir/install root/relative logs"
)
reset_environment
printf 'ONGRID_DATA_DIR=~/ongrid-data\nONGRID_LOG_DIR=~/ongrid-logs\n' >"$env_file"
assert_resolved home-relative "$HOME/ongrid-data" "$HOME/ongrid-logs"
reset_environment
printf 'ONGRID_DATA_DIR=~name/data\nONGRID_LOG_DIR=~\n' >"$env_file"
assert_resolved home-prefix "${HOME%/}/name/data" "${HOME%/}/"
reset_environment
printf 'ONGRID_DATA_DIR=~/data\nONGRID_LOG_DIR=~name/log\n' >"$env_file"
for home_state in unset empty; do
    home_env=(-u HOME)
    [[ "$home_state" != empty ]] || home_env=(HOME=)
    env "${home_env[@]}" bash -c '
        set -euo pipefail
        source "$1"
        ongrid_resolve_data_directories "$2" "$3"
        [[ "$ONGRID_DATA_DIR" == "$3/~/data" && "$ONGRID_LOG_DIR" == "$3/~name/log" ]]
    ' bash "$repo_root/deploy/install/data-permissions.sh" "$env_file" "$install_dir" \
        >>"$resolver_log" 2>&1 || fail "$home_state HOME changed literal tilde paths"
done
reset_environment
printf 'ONGRID_DATA_DIR=%s/crlf data\r\nONGRID_LOG_DIR=%s/crlf log\r\n' \
    "$tmp_dir" "$tmp_dir" >"$env_file"
assert_resolved crlf "$tmp_dir/crlf data" "$tmp_dir/crlf log"

reset_environment
cat >"$env_file" <<EOF
ONGRID_DATA_DIR=$tmp_dir/data
ONGRID_LOG_DIR=$tmp_dir/log
UNRELATED_SECRET='directory-test-secret
ONGRID_DATA_DIR=/wrong-data
ONGRID_LOG_DIR=/wrong-log'
EOF
assert_resolved unrelated-multiline "$tmp_dir/data" "$tmp_dir/log"

reset_environment
sentinel="$tmp_dir/must-not-exist"
literal_data="$tmp_dir/\$(touch $sentinel)"
literal_log="$tmp_dir/\`touch $sentinel\`"
printf "ONGRID_DATA_DIR='%s'\nONGRID_LOG_DIR='%s'\nUNRELATED_SECRET=directory-test-secret\n" \
    "$literal_data" "$literal_log" >"$env_file"
assert_resolved literal-command "$literal_data" "$literal_log"
[[ ! -e "$sentinel" ]] || fail "dotenv content was executed as shell code"

for control in '\r' '\n'; do
    reset_environment
    printf 'ONGRID_DATA_DIR="%s/invalid%bpath"\n' "$tmp_dir" "$control" >"$env_file"
    if ongrid_resolve_data_directories "$env_file" "$install_dir" >>"$resolver_log" 2>&1; then
        fail "directory containing $control was accepted"
    fi
done

reset_environment
printf 'ONGRID_DATA_DIR=%s/data\nONGRID_LOG_DIR="%s/log\n"\n' "$tmp_dir" "$tmp_dir" >"$env_file"
if ongrid_resolve_data_directories "$env_file" "$install_dir" >>"$resolver_log" 2>&1; then
    fail "log directory ending in a newline was accepted"
fi
[[ -z "${ONGRID_DATA_DIR+x}" && -z "${ONGRID_LOG_DIR+x}" ]] \
    || fail "failed resolution exported a partial directory configuration"

reset_environment
printf 'UNRELATED_SECRET="directory-test-secret\n' >"$env_file"
if ongrid_resolve_data_directories "$env_file" "$install_dir" >>"$resolver_log" 2>&1; then
    fail "malformed dotenv was accepted"
fi
if ongrid_resolve_data_directories "$tmp_dir/missing.env" "$install_dir" >>"$resolver_log" 2>&1; then
    fail "missing dotenv was accepted"
fi
if grep -Fq 'directory-test-secret' "$resolver_log"; then
    fail "resolver exposed an unrelated secret"
fi

# 实际目录准备与 Compose 挂载源必须采用同一组解析结果；只替换权限命令。
reset_environment
printf 'ONGRID_DATA_DIR=%s/custom data\nONGRID_LOG_DIR=%s/custom log\n' \
    "$tmp_dir" "$tmp_dir" >"$env_file"
assert_resolved preparation "$tmp_dir/custom data" "$tmp_dir/custom log"
command_log="$tmp_dir/permissions.log"
chown() { printf 'chown %s %s\n' "$1" "$2" >>"$command_log"; }
chmod() { printf 'chmod %s %s\n' "$1" "$2" >>"$command_log"; }
ongrid_prepare_data_directories "$ONGRID_DATA_DIR" "$ONGRID_LOG_DIR"
for directory in mysql prometheus loki tempo qdrant grafana embeddings skills pages \
    packet-captures chat-attachments workspace tools; do
    [[ -d "$ONGRID_DATA_DIR/$directory" ]] || fail "missing prepared directory: $directory"
done
[[ -d "$ONGRID_LOG_DIR" ]] || fail "missing prepared log directory"
grep -Fqx "chown 999:999 $ONGRID_DATA_DIR/mysql" "$command_log" \
    || fail "ownership preparation used a different data directory"
grep -Fqx "chmod 0755 $ONGRID_LOG_DIR" "$command_log" \
    || fail "permission preparation used a different log directory"

cat >"$install_dir/compose.yaml" <<'EOF'
services:
  fixture:
    image: busybox
    volumes:
      - type: bind
        source: ${ONGRID_DATA_DIR:-/var/lib/ongrid}/mysql
        target: /var/lib/mysql
      - type: bind
        source: ${ONGRID_LOG_DIR:-/var/log/ongrid}
        target: /var/log/ongrid
EOF
docker compose --project-directory "$install_dir" --env-file "$env_file" \
    -f "$install_dir/compose.yaml" config >"$tmp_dir/compose.yaml"
mount_sources=$(sed -n 's/^[[:space:]]*source: //p' "$tmp_dir/compose.yaml")
expected_sources=$(printf '%s\n%s' "$ONGRID_DATA_DIR/mysql" "$ONGRID_LOG_DIR")
[[ "$mount_sources" == "$expected_sources" ]] \
    || fail "Compose bind sources differ from prepared directories"

# 安装必须先创建或复用 .env，升级必须在预检渲染 Compose 之前解析。
line_of() {
    grep -nF "$2" "$1" | head -n 1 | cut -d: -f1
}
install_script="$repo_root/deploy/install/install.sh"
upgrade_script="$repo_root/deploy/install/upgrade.sh"
resolve_call='ongrid_resolve_data_directories "$ENV_FILE" "$INSTALL_DIR"'
install_resolve=$(line_of "$install_script" "$resolve_call") \
    || fail "install.sh does not resolve directories"
upgrade_resolve=$(line_of "$upgrade_script" "$resolve_call") \
    || fail "upgrade.sh does not resolve directories"
env_create=$(line_of "$install_script" 'cp "$SCRIPT_DIR/.env.example" "$INSTALL_DIR/.env"')
env_assignment=$(line_of "$install_script" 'ENV_FILE="$INSTALL_DIR/.env"')
install_prepare=$(line_of "$install_script" '"$ONGRID_DATA_DIR/mysql"')
install_edge_prepare=$(grep -nE '^prepare_edge_assets[[:space:]]*$' "$install_script" | cut -d: -f1)
install_compose_copy=$(line_of "$install_script" 'cp -f "$SCRIPT_DIR/docker-compose.yml"')
upgrade_preflight=$(grep -nE '^preflight_runtime_images[[:space:]]*$' "$upgrade_script" | cut -d: -f1)
[[ "$env_create" -lt "$install_resolve" && "$env_assignment" -lt "$install_resolve" \
    && "$install_resolve" -lt "$install_prepare" ]] \
    || fail "install.sh resolves directories outside .env creation / directory preparation order"
[[ "$install_resolve" -lt "$install_edge_prepare" && "$install_resolve" -lt "$install_compose_copy" ]] \
    || fail "install.sh modifies release assets before validating directories"
[[ "$upgrade_resolve" -lt "$upgrade_preflight" ]] \
    || fail "upgrade.sh resolves directories after Compose preflight"

printf 'install data-directories tests passed\n'
