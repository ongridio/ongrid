#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
permissions_lib="$repo_root/deploy/install/data-permissions.sh"
upgrade_script="$repo_root/deploy/install/upgrade.sh"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

fail() {
    printf 'upgrade data-permissions test failed: %s\n' "$*" >&2
    exit 1
}

[[ -f "$permissions_lib" ]] || fail "missing data-permissions.sh"

# shellcheck source=../deploy/install/data-permissions.sh
source "$permissions_lib"

command_log="$tmp_dir/commands.log"
chown_should_fail=0
chmod_should_fail=0
stat_owner_state=expected
stat_mode_state=expected
docker_should_fail=0
docker_log="$tmp_dir/docker.log"
chown() {
    printf 'chown' >>"$command_log"
    printf ' %s' "$@" >>"$command_log"
    printf '\n' >>"$command_log"
    [[ "$chown_should_fail" == 0 ]]
}
chmod() {
    printf 'chmod' >>"$command_log"
    printf ' %s' "$@" >>"$command_log"
    printf '\n' >>"$command_log"
    [[ "$chmod_should_fail" == 0 ]]
}
expected_owner_for_path() {
    case "$1" in
        "$data_dir/mysql") printf '999:999\n' ;;
        "$data_dir/prometheus") printf '65534:65534\n' ;;
        "$data_dir/loki"|"$data_dir/tempo") printf '10001:10001\n' ;;
        "$data_dir/grafana") printf '472:472\n' ;;
        "$data_dir/embeddings"|"$data_dir/skills"|"$data_dir/pages"|"$data_dir/packet-captures"|\
            "$data_dir/workspace"|"$data_dir/tools") printf '65532:65532\n' ;;
        *) printf '0:0\n' ;;
    esac
}
stat() {
    local format="$2" path="${!#}"

    case "$format" in
        %u:%g)
            case "$stat_owner_state" in
                expected) expected_owner_for_path "$path" ;;
                wrong) printf '0:0\n' ;;
                *) return 1 ;;
            esac
            ;;
        %a|%Lp)
            case "$stat_mode_state" in
                expected) printf '755\n' ;;
                wrong) printf '700\n' ;;
                *) return 1 ;;
            esac
            ;;
        *) return 1 ;;
    esac
}
docker() {
    printf '%s|docker' "$PWD" >>"$docker_log"
    printf ' %s' "$@" >>"$docker_log"
    printf '\n' >>"$docker_log"
    [[ "$docker_should_fail" == 0 ]]
}

data_dir="$tmp_dir/data"
log_dir="$tmp_dir/log"
mkdir -p "$data_dir/loki/chunks"
printf 'existing data\n' >"$data_dir/loki/chunks/000001"

ongrid_prepare_data_directories "$data_dir" "$log_dir"

[[ -f "$data_dir/loki/chunks/000001" ]] || fail "normal preparation touched existing Loki data"
grep -Fqx "chown 10001:10001 $data_dir/loki" "$command_log" \
    || fail "normal preparation did not set the Loki root directory owner"
grep -Fqx "chown 65532:65532 $data_dir/skills" "$command_log" \
    || fail "normal preparation did not set the skills root directory owner"
grep -Fqx "chown 65532:65532 $data_dir/packet-captures" "$command_log" \
    || fail "normal preparation did not set the packet capture root directory owner"
if grep -Eq '^(chown|chmod) -R ' "$command_log"; then
    fail "normal preparation recursively traversed a data directory"
fi

chown_should_fail=1
stat_owner_state=expected
ongrid_prepare_data_directories "$data_dir" "$log_dir" \
    || fail "normal preparation rejected directories whose owners were already correct"

stat_owner_state=wrong
if ongrid_prepare_data_directories "$data_dir" "$log_dir" 2>"$tmp_dir/owner-error.log"; then
    fail "normal preparation ignored incorrect owners after chown failures"
fi
grep -Fq '[ERROR] could not set owner' "$tmp_dir/owner-error.log" \
    || fail "incorrect top-level owner did not produce an error"
chown_should_fail=0
stat_owner_state=expected

chmod_should_fail=1
stat_mode_state=expected
ongrid_prepare_data_directories "$data_dir" "$log_dir" \
    || fail "normal preparation rejected directories whose modes were already correct"

stat_mode_state=wrong
if ongrid_prepare_data_directories "$data_dir" "$log_dir" 2>"$tmp_dir/mode-error.log"; then
    fail "normal preparation ignored incorrect modes after chmod failures"
fi
grep -Fq '[ERROR] could not set mode 0755' "$tmp_dir/mode-error.log" \
    || fail "incorrect top-level mode did not produce an error"
chmod_should_fail=0
stat_mode_state=expected

: >"$command_log"
ongrid_repair_data_permissions_if_enabled 0 "$data_dir"
[[ ! -s "$command_log" ]] \
    || fail "disabled permission repair still traversed a data directory"

: >"$command_log"
ongrid_repair_data_permissions_if_enabled 1 "$data_dir"
grep -Fqx "chown -R 10001:10001 $data_dir/loki" "$command_log" \
    || fail "explicit repair did not recursively repair Loki"
grep -Fqx "chown -R 65532:65532 $data_dir/skills" "$command_log" \
    || fail "explicit repair did not recursively repair skills"

[[ "$(ongrid_normalize_boolean '')" == 0 ]] \
    || fail "empty boolean did not disable permission repair"
[[ "$(ongrid_normalize_boolean 0)" == 0 ]] \
    || fail "boolean 0 did not disable permission repair"
[[ "$(ongrid_normalize_boolean false)" == 0 ]] \
    || fail "boolean false did not disable permission repair"
[[ "$(ongrid_normalize_boolean OFF)" == 0 ]] \
    || fail "boolean OFF did not disable permission repair"
[[ "$(ongrid_normalize_boolean 1)" == 1 ]] \
    || fail "boolean 1 did not enable permission repair"
[[ "$(ongrid_normalize_boolean TRUE)" == 1 ]] \
    || fail "boolean TRUE did not enable permission repair"
if ongrid_normalize_boolean invalid >/dev/null; then
    fail "invalid boolean value was accepted"
fi

chown_should_fail=1
if ongrid_repair_data_permissions_if_enabled 1 "$data_dir" 2>"$tmp_dir/chown-error.log"; then
    fail "explicit repair ignored recursive chown failures"
fi
grep -Fq '[ERROR] could not recursively set owner' "$tmp_dir/chown-error.log" \
    || fail "recursive chown failure did not produce an error"
chown_should_fail=0

chmod_should_fail=1
if ongrid_repair_data_permissions_if_enabled 1 "$data_dir" 2>"$tmp_dir/chmod-error.log"; then
    fail "explicit repair ignored recursive chmod failures"
fi
grep -Fq '[ERROR] could not recursively set mode 0755' "$tmp_dir/chmod-error.log" \
    || fail "recursive chmod failure did not produce an error"
chmod_should_fail=0

install_dir="$tmp_dir/install root"
mkdir -p "$install_dir"
: >"$docker_log"
chown_should_fail=1
if ongrid_repair_data_permissions_or_restore \
    1 "$data_dir" "$install_dir" \
    >"$tmp_dir/recovery.out" 2>"$tmp_dir/recovery.log"; then
    fail "permission repair failure returned success after recovery"
fi
grep -Fqx "$install_dir|docker compose --env-file .env up -d" "$docker_log" \
    || fail "permission repair failure did not start the existing Compose stack"

: >"$docker_log"
ongrid_repair_data_permissions_or_restore 0 "$data_dir" "$install_dir" \
    || fail "disabled permission repair unexpectedly failed"
[[ ! -s "$docker_log" ]] \
    || fail "disabled permission repair unnecessarily restored the Compose stack"
chown_should_fail=0

: >"$docker_log"
docker_should_fail=1
if ongrid_restore_existing_stack "$install_dir" 2>"$tmp_dir/restore-error.log"; then
    fail "Compose recovery ignored docker failure"
fi
grep -Fq '[ERROR] failed to restore the existing stack' "$tmp_dir/restore-error.log" \
    || fail "Compose recovery failure did not produce an error"
docker_should_fail=0

edge_install_dir="$tmp_dir/edge install"
edge_backup_dir="$tmp_dir/edge backup"
mkdir -p "$edge_install_dir/edge" "$edge_backup_dir/edge"
printf 'new edge\n' >"$edge_install_dir/edge/version"
printf 'old edge\n' >"$edge_backup_dir/edge/version"
ongrid_restore_edge_directory "$edge_install_dir" "$edge_backup_dir" \
    || fail "Edge rollback helper rejected a valid backup"
grep -Fqx 'old edge' "$edge_install_dir/edge/version" \
    || fail "Edge rollback helper did not restore the previous directory"
[[ ! -e "$edge_backup_dir" ]] \
    || fail "Edge rollback helper left an empty backup directory behind"

missing_backup="$tmp_dir/missing edge backup"
mkdir -p "$missing_backup"
printf 'current edge\n' >"$edge_install_dir/edge/version"
if ongrid_restore_edge_directory "$edge_install_dir" "$missing_backup" \
    >"$tmp_dir/edge-restore.out" 2>"$tmp_dir/edge-restore.log"; then
    fail "Edge rollback helper accepted a missing backup"
fi
grep -Fqx 'current edge' "$edge_install_dir/edge/version" \
    || fail "failed Edge rollback removed the current directory"
grep -Fq '[ERROR] previous Edge directory is missing' "$tmp_dir/edge-restore.log" \
    || fail "missing Edge backup did not produce an actionable error"

: >"$docker_log"
chown_should_fail=1
stat_owner_state=wrong
if ongrid_prepare_data_directories_or_restore \
    "$data_dir" "$log_dir" "$install_dir" \
    >"$tmp_dir/prepare-recovery.out" 2>"$tmp_dir/prepare-recovery.log"; then
    fail "post-stop directory preparation failure returned success after recovery"
fi
grep -Fqx "$install_dir|docker compose --env-file .env up -d" "$docker_log" \
    || fail "post-stop directory preparation failure did not restore the existing stack"
chown_should_fail=0
stat_owner_state=expected

health_statuses="$tmp_dir/health-statuses"
printf 'starting\nunhealthy\nhealthy\n' >"$health_statuses"
docker() {
    local status
    status=$(sed -n '1p' "$health_statuses")
    sed '1d' "$health_statuses" >"$health_statuses.next"
    mv "$health_statuses.next" "$health_statuses"
    printf '%s\n' "$status"
}
sleep() { :; }
ongrid_wait_for_container_healthy ongrid-mysql 3 0 \
    || fail "container recovery was rejected before the final healthy state"

printf 'starting\nunhealthy\n' >"$health_statuses"
if ongrid_wait_for_container_healthy ongrid-mysql 2 0 2>"$tmp_dir/health-error.log"; then
    fail "container health wait accepted a permanently unhealthy container"
fi
grep -Fq 'last status: unhealthy' "$tmp_dir/health-error.log" \
    || fail "container health timeout did not report the final state"

grep -Fq -- '--repair-permissions' "$upgrade_script" \
    || fail "upgrade.sh does not expose the explicit repair flag"
grep -Fq 'REPAIR_PERMISSIONS=$(ongrid_normalize_boolean "$REPAIR_PERMISSIONS_RAW")' "$upgrade_script" \
    || fail "upgrade.sh does not normalize the permission-repair setting"
if grep -Fq '[[ -n "$REPAIR_PERMISSIONS" ]]' "$upgrade_script"; then
    fail "upgrade.sh still treats every non-empty permission-repair value as enabled"
fi
grep -Fq 'ongrid_prepare_data_directories "$ONGRID_DATA_DIR" "$ONGRID_LOG_DIR"' "$upgrade_script" \
    || fail "upgrade.sh does not validate directories before stopping the stack"
grep -Fq 'ongrid_prepare_data_directories_or_restore' "$upgrade_script" \
    || fail "upgrade.sh does not use the tested post-stop preparation recovery path"
grep -Fq 'ongrid_repair_data_permissions_or_restore' "$upgrade_script" \
    || fail "upgrade.sh does not use the tested repair-and-recovery path"
grep -Fq 'ongrid_restore_existing_stack "$INSTALL_DIR"' "$upgrade_script" \
    || fail "upgrade.sh does not restore the existing stack after preparation failure"
grep -Fq 'ongrid_restore_edge_directory "$INSTALL_DIR" "$EDGE_BACKUP_DIR"' "$upgrade_script" \
    || fail "upgrade.sh does not restore the previous Edge directory after a post-swap failure"
grep -Fq 'upgrade health check failed: ongrid did not become healthy within 90s' "$upgrade_script" \
    || fail "upgrade.sh still reports a failed health check as a warning"
grep -Fq 'manual Edge rollback: rm -rf --' "$upgrade_script" \
    || fail "upgrade.sh does not print an actionable Edge rollback command"
grep -Fq 'docker compose --env-file .env up -d --force-recreate ongrid nginx' "$upgrade_script" \
    || fail "manual Edge rollback does not recreate containers with Edge bind mounts"
grep -Fq 'trap - ERR' "$upgrade_script" \
    || fail "upgrade.sh health failure can trigger a partial automatic Edge-only rollback"
grep -Fq 'docker compose --env-file .env up -d mysql' "$upgrade_script" \
    || fail "upgrade.sh does not start MySQL separately"
grep -Fq 'ongrid_wait_for_container_healthy ongrid-mysql 150 2' "$upgrade_script" \
    || fail "upgrade.sh does not wait for MySQL recovery"
grep -Fq 'for d in /tmp/ongrid-v*-linux /tmp/ongrid-v*-linux-*; do' "$upgrade_script" \
    || fail "upgrade.sh does not prune both universal and legacy extracted release directories"
grep -Fq '"$INSTALL_DIR"/ongrid-v*-linux.tar.xz' "$upgrade_script" \
    || fail "upgrade.sh does not include universal xz release packages in retention cleanup"
grep -Fq '"$INSTALL_DIR"/ongrid-v*-linux-*.tar.xz' "$upgrade_script" \
    || fail "upgrade.sh no longer includes legacy architecture-specific xz packages in cleanup"
for persistent_dir in mysql prometheus loki tempo grafana skills pages packet-captures workspace tools; do
    if grep -Eq "chown -R .*ONGRID_DATA_DIR/${persistent_dir}" "$upgrade_script"; then
        fail "upgrade.sh directly recurses through $persistent_dir outside the repair helper"
    fi
done

printf 'upgrade data-permissions tests passed\n'
