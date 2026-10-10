#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
hook="$repo_root/deploy/install/apply-pending-upgrade.sh"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

stage="$tmp_dir/stage"
bin_dir="$tmp_dir/bin"
lib_dir="$tmp_dir/lib"
unit_dir="$tmp_dir/systemd"
dropin_dir="$unit_dir/ongrid-edge.service.d"
unit_file="$unit_dir/ongrid-edge.service"
sbin="$tmp_dir/sbin"
systemctl_log="$tmp_dir/systemctl.log"
logger_log="$tmp_dir/logger.log"
mkdir -p "$stage" "$bin_dir" "$lib_dir" "$unit_dir" "$dropin_dir" "$sbin"
: > "$systemctl_log"
: > "$logger_log"

# Command shims come first, before the hook is invoked even once: the hook must
# never reach a real systemd or a real journal from this suite, and its reload
# calls and log lines have to stay observable.
cat > "$sbin/systemctl" <<'SHIM'
#!/bin/sh
printf '%s\n' "$*" >> "$ONGRID_EDGE_SYSTEMCTL_LOG"
SHIM
cat > "$sbin/logger" <<'SHIM'
#!/bin/sh
printf '%s\n' "$*" >> "$ONGRID_EDGE_LOGGER_LOG"
SHIM
chmod 0755 "$sbin/systemctl" "$sbin/logger"

# One isolation entry shared by every invocation: upgrade stage, binary and
# library dirs, legacy target, the unit and drop-in paths, the shims. Overriding
# the unit paths matters — left at their defaults they point at
# /etc/systemd/system/ongrid-edge.service, so running this suite as root on a
# host that really has an edge installed would rewrite that host's unit.
run_hook_env() {
  local unit=$1
  ONGRID_EDGE_UPGRADE_STAGE_DIR="$stage" \
  ONGRID_EDGE_UPGRADE_BIN_DIR="$bin_dir" \
  ONGRID_EDGE_UPGRADE_LIB_DIR="$lib_dir" \
  ONGRID_EDGE_UPGRADE_LEGACY_TARGET="$bin_dir/ongrid-edge" \
  ONGRID_EDGE_UNIT_FILE="$unit" \
  ONGRID_EDGE_DROPIN_DIR="$dropin_dir" \
  ONGRID_EDGE_SYSTEMCTL_LOG="$systemctl_log" \
  ONGRID_EDGE_LOGGER_LOG="$logger_log" \
  PATH="$sbin:$PATH" \
    bash "$hook"
}

# Bundle-level runs must not touch any unit, so they are pointed at a path that
# never exists.
run_hook() { run_hook_env "$unit_dir/absent.service"; }

sha256_file() {
  sha256sum "$1" | awk '{print $1}'
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

# A node installed before the ProtectHome=read-only fix keeps ProtectHome=true,
# because a bundle upgrade only swaps the files listed in MANIFEST.txt and never
# rewrites the unit. The pre-start hook must migrate exactly that directive and
# reload systemd, exercised through the real hook entry point.
# Isolation check for every bundle-level run above: they were pointed at an
# absent unit, so nothing may have reloaded systemd or mentioned the migration,
# and no unit file may have been created anywhere. The last guard catches a run
# that forgot to override the unit path at all — on a host that really has an
# edge install, the hook's default /etc/systemd/system/ongrid-edge.service would
# otherwise show up here.
test ! -s "$systemctl_log"
test ! -e "$unit_file"
if grep -q 'protect-home' "$logger_log"; then
  echo "bundle-level runs must not touch any unit" >&2
  exit 1
fi
if grep -q '/etc/systemd' "$logger_log"; then
  echo "tests must never operate on the host unit path" >&2
  exit 1
fi

run_hook_with_unit() { run_hook_env "$unit_file"; }

cat > "$unit_file" <<'UNIT'
[Unit]
Description=Ongrid Edge
[Service]
User=ongrid-edge
ProtectSystem=strict
ProtectHome=true
LimitNOFILE=65536
Environment=ONGRID_EDGE_CUSTOM=keep-me
UNIT
: > "$systemctl_log"
: > "$logger_log"

# The operator's own drop-in is reported, never rewritten.
printf '[Service]\nProtectHome=true\n' > "$dropin_dir/override.conf"
run_hook_with_unit
grep -Fxq 'ProtectHome=read-only' "$unit_file"
grep -Fxq 'ProtectSystem=strict' "$unit_file"
grep -Fxq 'LimitNOFILE=65536' "$unit_file"
grep -Fxq 'Environment=ONGRID_EDGE_CUSTOM=keep-me' "$unit_file"
grep -Fxq 'ProtectHome=true' "$dropin_dir/override.conf"
grep -q 'still pins ProtectHome=true' "$logger_log"
test "$(grep -c 'daemon-reload' "$systemctl_log")" -eq 1

# Running the same upgrade again changes nothing and does not reload again.
cp "$unit_file" "$tmp_dir/unit.after-first"
run_hook_with_unit
cmp -s "$unit_file" "$tmp_dir/unit.after-first"
test "$(grep -c 'daemon-reload' "$systemctl_log")" -eq 1

# Only the stale value is migrated: an operator who chose ProtectHome=false, or
# a unit without the directive at all, must come back byte-identical.
printf '[Service]\nProtectHome=false\n' > "$unit_file"
cp "$unit_file" "$tmp_dir/unit.disabled"
run_hook_with_unit
grep -Fxq 'ProtectHome=false' "$unit_file"
cmp -s "$unit_file" "$tmp_dir/unit.disabled"
printf '[Service]\nProtectSystem=strict\n' > "$unit_file"
run_hook_with_unit
grep -Fxq 'ProtectSystem=strict' "$unit_file"
test "$(grep -c 'daemon-reload' "$systemctl_log")" -eq 1

# The spelling of the stale value varies across installs, so match it the way
# systemd does: any case, with optional surrounding spaces.
printf '[Service]\nProtectHome = True\n' > "$unit_file"
run_hook_with_unit
grep -Fxq 'ProtectHome=read-only' "$unit_file"

# A missing unit (package installs without it, or a first boot) is a no-op.
rm -f "$unit_file"
run_hook_with_unit
test ! -e "$unit_file"

echo "apply-pending-upgrade tests passed"
