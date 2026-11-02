#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
hook="$repo_root/deploy/install/apply-pending-upgrade.sh"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

stage="$tmp_dir/stage"
bin_dir="$tmp_dir/bin"
lib_dir="$tmp_dir/lib"
mkdir -p "$stage" "$bin_dir" "$lib_dir"

sha256_file() {
  sha256sum "$1" | awk '{print $1}'
}

run_hook() {
  ONGRID_EDGE_UPGRADE_STAGE_DIR="$stage" \
  ONGRID_EDGE_UPGRADE_BIN_DIR="$bin_dir" \
  ONGRID_EDGE_UPGRADE_LIB_DIR="$lib_dir" \
  ONGRID_EDGE_UPGRADE_LEGACY_TARGET="$bin_dir/ongrid-edge" \
    bash "$hook"
}

write_bundle() {
  local version=$1 agent_payload=$2 plugin_payload=$3
  rm -rf "$stage/incoming"
  mkdir -p "$stage/incoming"
  printf '%s\n' "$version" > "$stage/incoming/VERSION"
  printf '%s\n' "$agent_payload" > "$stage/incoming/ongrid-edge"
  printf '%s\n' "$plugin_payload" > "$stage/incoming/plugin"
  {
    printf '%s 0755 ongrid-edge %s\n' \
      "$(sha256_file "$stage/incoming/ongrid-edge")" "$bin_dir/ongrid-edge"
    printf '%s 0755 plugin %s\n' \
      "$(sha256_file "$stage/incoming/plugin")" "$lib_dir/plugin"
  } > "$stage/incoming/MANIFEST.txt"
}

# A complete bundle is applied once. A matching healthy marker commits it and
# removes rollback copies without applying the deleted incoming tree again.
printf 'old-agent\n' > "$bin_dir/ongrid-edge"
write_bundle v1 new-agent new-plugin
run_hook
grep -Fxq new-agent "$bin_dir/ongrid-edge"
grep -Fxq new-plugin "$lib_dir/plugin"
grep -Fxq old-agent "$bin_dir/ongrid-edge.previous"
test ! -e "$stage/incoming"
test -s "$stage/last_upgrade_at"
test -s "$stage/last_upgrade_ver"
printf 'v1\n' > "$stage/healthy_marker"
run_hook
grep -Fxq new-agent "$bin_dir/ongrid-edge"
test ! -e "$bin_dir/ongrid-edge.previous"
test ! -e "$stage/last_upgrade_at"
test ! -e "$stage/last_upgrade_ver"

# Without a matching healthy marker, the next start restores existing files,
# removes targets introduced by the failed bundle, and disarms rollback.
rm -f "$lib_dir/plugin"
write_bundle v2 broken-agent broken-plugin
run_hook
grep -Fxq broken-agent "$bin_dir/ongrid-edge"
grep -Fxq broken-plugin "$lib_dir/plugin"
run_hook
grep -Fxq new-agent "$bin_dir/ongrid-edge"
test ! -e "$lib_dir/plugin"
test ! -e "$stage/last_upgrade_at"

# Validation is bundle-wide: a bad second destination must leave the first
# live file untouched and discard the rejected incoming tree.
mkdir -p "$lib_dir/not-a-file"
mkdir -p "$stage/incoming"
printf 'v3\n' > "$stage/incoming/VERSION"
printf 'should-not-apply\n' > "$stage/incoming/ongrid-edge"
printf 'bad-target\n' > "$stage/incoming/plugin"
{
  printf '%s 0755 ongrid-edge %s\n' \
    "$(sha256_file "$stage/incoming/ongrid-edge")" "$bin_dir/ongrid-edge"
  printf '%s 0755 plugin %s\n' \
    "$(sha256_file "$stage/incoming/plugin")" "$lib_dir/not-a-file"
} > "$stage/incoming/MANIFEST.txt"
run_hook
grep -Fxq new-agent "$bin_dir/ongrid-edge"
test -d "$lib_dir/not-a-file"
test ! -e "$stage/incoming"

# A stale legacy single-file payload must never overwrite a newer whole-bundle
# Agent after the bundle's remaining plugins have already been swapped.
rmdir "$lib_dir/not-a-file"
write_bundle v4 bundle-agent bundle-plugin
printf 'stale-legacy-agent\n' > "$stage/pending"
sha256_file "$stage/pending" > "$stage/pending.sha256"
run_hook
grep -Fxq bundle-agent "$bin_dir/ongrid-edge"
grep -Fxq bundle-plugin "$lib_dir/plugin"
test ! -e "$stage/pending"
test ! -e "$stage/pending.sha256"

echo "apply-pending-upgrade tests passed"

# systemd bootstrap: an old hook has already installed the new binary/script.
# Only a matching one-shot request may defer rollback; reload only on change.
rm -rf "$stage"; mkdir -p "$stage"
systemd_dir="$tmp_dir/systemd"
mkdir -p "$systemd_dir" "$tmp_dir/mock-bin"
printf '[Service]\nProtectSystem=strict\nEnvironment=CUSTOM=preserved\n' > "$systemd_dir/ongrid-edge.service"
cat > "$tmp_dir/mock-bin/systemctl" <<'MOCK'
#!/bin/bash
[[ $* == daemon-reload ]] || exit 99
printf 'reload\n' >> "$SYSTEMD_RELOAD_LOG"
[[ ! -f "$SYSTEMD_FAIL_FLAG" ]]
MOCK
chmod +x "$tmp_dir/mock-bin/systemctl"
export PATH="$tmp_dir/mock-bin:$PATH"
export ONGRID_EDGE_UPGRADE_SYSTEMD_DIR="$systemd_dir"
export SYSTEMD_RELOAD_LOG="$tmp_dir/reloads" SYSTEMD_FAIL_FLAG="$tmp_dir/fail-reload"
: > "$SYSTEMD_RELOAD_LOG"
printf 'old-agent\n' > "$bin_dir/ongrid-edge.previous"
printf 'bootstrap-agent\n' > "$bin_dir/ongrid-edge"
printf 'v-bootstrap\n' > "$stage/last_upgrade_ver"
printf 'now\n' > "$stage/last_upgrade_at"
printf 'v-bootstrap\n' > "$stage/systemd_bootstrap_request"
run_hook
grep -Fxq bootstrap-agent "$bin_dir/ongrid-edge"
grep -Fxq v-bootstrap "$stage/systemd_config_ready"
test ! -e "$stage/systemd_bootstrap_request"
grep -Fxq Environment=CUSTOM=preserved "$systemd_dir/ongrid-edge.service"
grep -Fxq ProtectSystem=false "$systemd_dir/ongrid-edge.service.d/90-ongrid-host-filesystem.conf"
test "$(wc -l < "$SYSTEMD_RELOAD_LOG" | tr -d ' ')" = 1
# No health acknowledgement -> restore config and program together.
run_hook
grep -Fxq old-agent "$bin_dir/ongrid-edge"
test ! -e "$systemd_dir/ongrid-edge.service.d/90-ongrid-host-filesystem.conf"
test "$(wc -l < "$SYSTEMD_RELOAD_LOG" | tr -d ' ')" = 2
# New hook does migration in the same pre-start invocation.
write_bundle v-config agent-config plugin-config
run_hook
printf 'v-config\n' > "$stage/healthy_marker"
run_hook
reloads=$(wc -l < "$SYSTEMD_RELOAD_LOG")
write_bundle v-binary-only agent-next plugin-next
run_hook
grep -Fxq v-binary-only "$stage/systemd_config_ready"
test "$(wc -l < "$SYSTEMD_RELOAD_LOG")" = "$reloads"
printf 'v-binary-only\n' > "$stage/healthy_marker"
run_hook
# Reload failure must not advertise readiness; recovery metadata survives
# if even the restorative reload is unavailable.
printf '[Service]\nProtectSystem=strict\n' > "$systemd_dir/ongrid-edge.service.d/90-ongrid-host-filesystem.conf"
touch "$SYSTEMD_FAIL_FLAG"
write_bundle v-failed agent-failed plugin-failed
run_hook
test -e "$stage/systemd_config_previous_state"
rm "$SYSTEMD_FAIL_FLAG"
run_hook
grep -Fxq agent-next "$bin_dir/ongrid-edge"
grep -Fxq ProtectSystem=strict "$systemd_dir/ongrid-edge.service.d/90-ongrid-host-filesystem.conf"
echo 'edge upgrade systemd bootstrap / conditional reload / rollback: ok'
