#!/usr/bin/env bash
# Shared data-directory ownership and recovery helpers for Compose upgrades.
#
# Normal upgrades must stay O(number of top-level directories): files already
# written by a running service have the correct numeric owner, and recursively
# walking a large Loki/Tempo tree turns that invariant check into downtime.

# 借用 config --images 的原始字符串输出，让旧版 Compose 也能解析 dotenv。
# 合成模型只有一个用于承载目录的字段；仅渲染配置，不拉取镜像或启动容器。
ongrid_resolve_compose_directory() {
    local env_file="$1" install_dir="$2" expression="$3" resolved
    local suffix='/.ongrid-directory-end'

    # 原始解析错误可能包含 .env 密钥，失败时只报告文件位置。
    if ! resolved=$(docker compose --project-name ongrid-dir-resolver \
        --project-directory "$install_dir" --env-file "$env_file" \
        -f - config --images 2>/dev/null <<EOF
services:
  directory:
    image: $expression$suffix
EOF
    ); then
        printf '[ERROR] could not resolve data directories from %s with Docker Compose\n' "$env_file" >&2
        return 1
    fi

    # 后缀避免命令替换吞掉目录末尾的换行，不能把多行目录截断后继续安装。
    if [[ "$resolved" != *"$suffix" ]]; then
        printf '[ERROR] invalid directory output from Docker Compose\n' >&2
        return 1
    fi
    resolved=${resolved%"$suffix"}
    if [[ -z "$resolved" || "$resolved" == *$'\n'* || "$resolved" == *$'\r'* ]]; then
        printf '[ERROR] data and log directories must be non-empty, single-line paths\n' >&2
        return 1
    fi
    # Compose 将 ~ 前缀相对于进程 HOME 展开，不查询 ~user 的系统账号。
    if [[ "$resolved" == '~'* && -n "${HOME:-}" ]]; then
        resolved=${resolved#\~}
        resolved="${HOME%/}/${resolved#/}"
    fi
    # Compose 的相对 bind 路径以安装目录为基准，而脚本运行在解压目录。
    [[ "$resolved" == /* ]] || resolved="$install_dir/$resolved"
    printf '%s\n' "$resolved"
}

# 与 Compose 保持 shell env > .env > 默认值的优先级，不能 source 管理员的 .env。
ongrid_resolve_data_directories() {
    local env_file="$1" install_dir="$2" data_dir log_dir

    [[ "$install_dir" == /* ]] || install_dir="$PWD/$install_dir"
    data_dir=$(ongrid_resolve_compose_directory "$env_file" "$install_dir" \
        '${ONGRID_DATA_DIR:-/var/lib/ongrid}') || return 1
    log_dir=$(ongrid_resolve_compose_directory "$env_file" "$install_dir" \
        '${ONGRID_LOG_DIR:-/var/log/ongrid}') || return 1
    # 两个路径都解析成功后再导出，预检、权限准备、启动和恢复使用同一配置。
    export ONGRID_DATA_DIR="$data_dir" ONGRID_LOG_DIR="$log_dir"
}

ongrid_stat_path_owner() {
    local path="$1" owner

    if owner=$(stat -c '%u:%g' "$path" 2>/dev/null) \
        && [[ "$owner" =~ ^[0-9]+:[0-9]+$ ]]; then
        printf '%s\n' "$owner"
        return 0
    fi
    if owner=$(stat -f '%u:%g' "$path" 2>/dev/null) \
        && [[ "$owner" =~ ^[0-9]+:[0-9]+$ ]]; then
        printf '%s\n' "$owner"
        return 0
    fi
    return 1
}

ongrid_stat_path_mode() {
    local path="$1" mode

    if mode=$(stat -c '%a' "$path" 2>/dev/null) \
        && [[ "$mode" =~ ^[0-7]+$ ]]; then
        printf '%s\n' "$mode"
        return 0
    fi
    if mode=$(stat -f '%Lp' "$path" 2>/dev/null) \
        && [[ "$mode" =~ ^[0-7]+$ ]]; then
        printf '%s\n' "$mode"
        return 0
    fi
    return 1
}

ongrid_ensure_path_owner() {
    local expected="$1" path="$2" actual=""

    if chown "$expected" "$path" 2>/dev/null; then
        return 0
    fi
    if actual=$(ongrid_stat_path_owner "$path") && [[ "$actual" == "$expected" ]]; then
        return 0
    fi

    printf '[ERROR] could not set owner %s on %s (current owner: %s)\n' \
        "$expected" "$path" "${actual:-unknown}" >&2
    return 1
}

ongrid_ensure_path_mode() {
    local expected="$1" path="$2" actual=""
    local normalized_expected="${expected#0}"

    if chmod "$expected" "$path" 2>/dev/null; then
        return 0
    fi
    if actual=$(ongrid_stat_path_mode "$path") && [[ "$actual" == "$normalized_expected" ]]; then
        return 0
    fi

    printf '[ERROR] could not set mode %s on %s (current mode: %s)\n' \
        "$expected" "$path" "${actual:-unknown}" >&2
    return 1
}

ongrid_normalize_boolean() {
    local value
    value=$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')

    case "$value" in
        ''|0|false|no|off) printf '0\n' ;;
        1|true|yes|on) printf '1\n' ;;
        *) return 1 ;;
    esac
}

ongrid_chown_tree_required() {
    local owner="$1" path="$2"
    if ! chown -R "$owner" "$path" 2>/dev/null; then
        printf '[ERROR] could not recursively set owner %s on %s\n' "$owner" "$path" >&2
        return 1
    fi
}

ongrid_prepare_data_directories() {
    local data_dir="$1" log_dir="$2"
    local failed=0

    if ! mkdir -p \
        "$data_dir/mysql" \
        "$data_dir/prometheus" \
        "$data_dir/loki" \
        "$data_dir/tempo" \
        "$data_dir/qdrant" \
        "$data_dir/grafana" \
        "$data_dir/embeddings" \
        "$data_dir/skills" \
        "$data_dir/pages" \
        "$data_dir/packet-captures" \
        "$data_dir/chat-attachments" \
        "$data_dir/workspace" \
        "$data_dir/tools" \
        "$log_dir"; then
        printf '[ERROR] could not create one or more data directories under %s\n' \
            "$data_dir" >&2
        return 1
    fi

    # Only the mount-point directories need ownership initialization. Existing
    # descendants were created by these same container UIDs and must not be
    # traversed during every upgrade.
    ongrid_ensure_path_owner 999:999 "$data_dir/mysql" || failed=1
    ongrid_ensure_path_owner 65534:65534 "$data_dir/prometheus" || failed=1
    ongrid_ensure_path_owner 10001:10001 "$data_dir/loki" || failed=1
    ongrid_ensure_path_owner 10001:10001 "$data_dir/tempo" || failed=1
    ongrid_ensure_path_owner 472:472 "$data_dir/grafana" || failed=1
    ongrid_ensure_path_owner 65532:65532 "$data_dir/embeddings" || failed=1
    ongrid_ensure_path_owner 65532:65532 "$data_dir/skills" || failed=1
    ongrid_ensure_path_owner 65532:65532 "$data_dir/pages" || failed=1
    ongrid_ensure_path_owner 65532:65532 "$data_dir/packet-captures" || failed=1
    ongrid_ensure_path_owner 65532:65532 "$data_dir/chat-attachments" || failed=1
    ongrid_ensure_path_owner 65532:65532 "$data_dir/workspace" || failed=1
    ongrid_ensure_path_owner 65532:65532 "$data_dir/tools" || failed=1

    ongrid_ensure_path_mode 0755 "$data_dir" || failed=1
    ongrid_ensure_path_mode 0755 "$data_dir/embeddings" || failed=1
    ongrid_ensure_path_mode 0755 "$log_dir" || failed=1

    return "$failed"
}

ongrid_repair_data_permissions() {
    local data_dir="$1"
    local failed=0

    # Explicit recovery path only. This can walk every inode and therefore must
    # never be called by a normal upgrade.
    ongrid_chown_tree_required 999:999 "$data_dir/mysql" || failed=1
    ongrid_chown_tree_required 65534:65534 "$data_dir/prometheus" || failed=1
    ongrid_chown_tree_required 10001:10001 "$data_dir/loki" || failed=1
    ongrid_chown_tree_required 10001:10001 "$data_dir/tempo" || failed=1
    ongrid_chown_tree_required 472:472 "$data_dir/grafana" || failed=1
    ongrid_chown_tree_required 65532:65532 "$data_dir/embeddings" || failed=1
    ongrid_chown_tree_required 65532:65532 "$data_dir/skills" || failed=1
    ongrid_chown_tree_required 65532:65532 "$data_dir/pages" || failed=1
    ongrid_chown_tree_required 65532:65532 "$data_dir/packet-captures" || failed=1
    ongrid_chown_tree_required 65532:65532 "$data_dir/chat-attachments" || failed=1
    ongrid_chown_tree_required 65532:65532 "$data_dir/workspace" || failed=1
    ongrid_chown_tree_required 65532:65532 "$data_dir/tools" || failed=1
    if ! chmod -R 0755 "$data_dir/embeddings" 2>/dev/null; then
        printf '[ERROR] could not recursively set mode 0755 on %s\n' \
            "$data_dir/embeddings" >&2
        failed=1
    fi

    return "$failed"
}

ongrid_repair_data_permissions_if_enabled() {
    local enabled="$1" data_dir="$2"

    case "$enabled" in
        0) return 0 ;;
        1) ongrid_repair_data_permissions "$data_dir" ;;
        *)
            printf '[ERROR] invalid normalized permission-repair flag: %s\n' "$enabled" >&2
            return 2
            ;;
    esac
}

ongrid_restore_existing_stack() {
    local install_dir="$1"

    printf '[WARN] attempting to restore the existing stack\n' >&2
    if (
        cd "$install_dir" && docker compose --env-file .env up -d
    ); then
        printf '[INFO] existing stack restore requested successfully\n'
        return 0
    fi

    printf '[ERROR] failed to restore the existing stack; inspect docker compose status\n' >&2
    return 1
}

ongrid_restore_edge_directory() {
    local install_dir="$1" backup_dir="$2"
    local current_edge="$install_dir/edge" backup_edge="$backup_dir/edge"

    if [[ ! -d "$backup_edge" ]]; then
        printf '[ERROR] previous Edge directory is missing from %s\n' "$backup_dir" >&2
        return 1
    fi
    if [[ -e "$current_edge" || -L "$current_edge" ]]; then
        if ! rm -rf -- "$current_edge"; then
            printf '[ERROR] could not remove failed Edge directory at %s\n' "$current_edge" >&2
            return 1
        fi
    fi
    if ! mv -- "$backup_edge" "$current_edge"; then
        printf '[ERROR] could not restore previous Edge directory from %s\n' "$backup_dir" >&2
        return 1
    fi
    rmdir "$backup_dir" 2>/dev/null || true
    printf '[INFO] restored previous Edge directory from upgrade backup\n'
}

ongrid_prepare_data_directories_or_restore() {
    local data_dir="$1" log_dir="$2" install_dir="$3"

    if ongrid_prepare_data_directories "$data_dir" "$log_dir"; then
        return 0
    fi

    printf '[ERROR] data directory preparation failed after stopping the stack\n' >&2
    if ! ongrid_restore_existing_stack "$install_dir"; then
        printf '[ERROR] automatic recovery failed\n' >&2
    fi
    return 1
}

ongrid_repair_data_permissions_or_restore() {
    local enabled="$1" data_dir="$2" install_dir="$3"

    if ongrid_repair_data_permissions_if_enabled "$enabled" "$data_dir"; then
        return 0
    fi

    printf '[ERROR] permission repair failed; the new release was not installed\n' >&2
    if ! ongrid_restore_existing_stack "$install_dir"; then
        printf '[ERROR] automatic recovery failed\n' >&2
    fi
    return 1
}

# Run one command with a relaxed umask. Shared assets (config files, the nginx
# static Edge directory, bundle tarballs and checksums) enter non-root
# containers as root-owned read-only bind mounts, so they cannot be chown'd and
# must stay group/other readable. Child processes inherit the umask, so this
# also covers the tarball and .sha256 that build-edge-bundle.sh writes.
# Saving and restoring instead of using a subshell keeps the caller's `set -e`
# and ERR trap semantics intact for the wrapped command.
ongrid_with_shared_asset_umask() {
    local previous rc=0

    previous=$(umask)
    umask 022
    "$@" || rc=$?
    umask "$previous"
    return "$rc"
}

# Normalize shared-asset modes inside an existing install directory.
# This is not a belt-and-braces extra: `cp` leaves the mode of an existing
# destination untouched, so an install created under a restrictive umask keeps
# its 0640 config files forever, even once the installers relax their own umask.
# `a+rX` only adds read bits, never write bits, and is idempotent on 0755.
# The list is enumerated on purpose — recursing over the whole install dir would
# turn .env (0600, database passwords) and certs/ (0700, TLS key) world-readable.
ongrid_normalize_shared_asset_modes() {
    local install_dir="$1" path

    [[ -d "$install_dir" ]] || return 0

    chmod 755 "$install_dir" 2>/dev/null || true
    for path in \
        "$install_dir/docker-compose.yml" \
        "$install_dir"/prometheus*.yml \
        "$install_dir/loki-config.yaml" \
        "$install_dir/tempo-config.yaml" \
        "$install_dir/frontier.yaml" \
        "$install_dir/nginx.conf" \
        "$install_dir/VERSION"; do
        [[ -f "$path" ]] && { chmod 644 "$path" 2>/dev/null || true; }
    done
    for path in \
        "$install_dir/edge" \
        "$install_dir/searxng" \
        "$install_dir/grafana" \
        "$install_dir/prometheus"; do
        [[ -d "$path" ]] && { chmod -R a+rX "$path" 2>/dev/null || true; }
    done
    return 0
}

# Remove Edge staging directories left behind by an interrupted run.
# The installers only ever cleaned up on ERR, so `exit 1` paths and SIGINT /
# SIGTERM leaked a full bundle tree (~178 MB each) into the install directory.
# Three guards keep this from touching anything else: depth 1 only, an anchored
# name prefix, and an age floor so a concurrently running installer's staging
# directory is never removed.
#
# .edge-backup.* is deliberately NOT pruned here. After a health-check timeout
# upgrade.sh keeps the previous Edge tree and prints it as the manual rollback
# source, and install.sh keeps it when an automatic restore fails. That backup is
# then the only known-good copy, and this function runs at startup before the
# next Edge swap: pruning it by age would delete the operator's rollback source
# out from under a retry. Backups are removed by the successful-upgrade cleanup
# path, by a completed rollback, or by an explicit operator action.
ongrid_prune_stale_edge_staging() {
    local install_dir="$1"

    [[ -d "$install_dir" ]] || return 0
    find "$install_dir" -maxdepth 1 -type d -name '.edge-stage.*' \
        -mmin +120 -exec rm -rf {} + 2>/dev/null || true
    return 0
}
