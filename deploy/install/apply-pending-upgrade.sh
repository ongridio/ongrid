#!/bin/bash
# apply-pending-upgrade.sh — pre-start upgrade hook for ongrid-edge.
#
# Runs as root via the ongrid-edge-upgrade.service oneshot, which
# ongrid-edge.service pulls in (Wants= + After=) so this runs before every
# agent start — including each Restart=always auto-restart. It runs as a
# separate root unit (not the agent's own ExecStartPre) because ongrid-edge
# runs sandboxed + non-root and cannot write /usr/local; the old
# `ExecStartPre=-+...` relied on the `+` root-exec prefix, which systemd < 231
# (CentOS 7 = 219) silently ignores, so the swap never applied there.
#
# Looks for a staged upgrade bundle the agent dropped at
# /var/lib/ongrid-edge/.upgrade/incoming/ and atomically swaps every file
# listed in MANIFEST.txt into its declared dest path.
#
# Three modes covered, in priority order:
#
#  1. Rollback     — last upgrade booted but never reported "healthy"
#                    (no healthy_marker matching last_upgrade_ver), so
#                    restore each <dest>.previous over <dest> and clear
#                    last_upgrade_at to prevent a rollback loop.
#  2. Bundle apply — incoming/MANIFEST.txt exists: verify each file's
#                    sha256, then for each one back up to <dest>.previous
#                    and rename a fresh copy into place.
#  3. Single-file  — legacy path (ADR-018 / C11 Phase-B): one binary at
#                    .upgrade/pending swapped over /usr/local/bin/
#                    ongrid-edge. Kept for back-compat with edges that
#                    haven't been bundle-upgraded yet.
#
# Idempotent + best-effort: if nothing's staged or anything goes wrong
# at validate-time, exit 0 so systemd starts whatever binary is on disk.
# We never block the unit on upgrade-side mistakes.

set -uo pipefail

STAGE_DIR=/var/lib/ongrid-edge/.upgrade
INCOMING_DIR=$STAGE_DIR/incoming
MANIFEST=$INCOMING_DIR/MANIFEST.txt
LAST_UPGRADE_AT=$STAGE_DIR/last_upgrade_at
LAST_UPGRADE_VER=$STAGE_DIR/last_upgrade_ver
HEALTHY_MARKER=$STAGE_DIR/healthy_marker

# Legacy single-file paths (kept for back-compat — see mode 3 below).
LEGACY_TARGET=/usr/local/bin/ongrid-edge
LEGACY_PENDING=$STAGE_DIR/pending
LEGACY_PENDING_SHA=$STAGE_DIR/pending.sha256
LEGACY_PREVIOUS=$STAGE_DIR/previous

log() { logger -t ongrid-edge-upgrade "$*"; }

# ----- Pre-start: ensure log-read group membership -------------------------
#
# install-edge.sh adds ongrid-edge to adm + systemd-journal so the logs
# plugin (promtail) can read /var/log/* (root:adm 640) and the journal.
# Bundle upgrades (ADR-024) DON'T re-run install-edge.sh, so a box that was
# installed before that grant — or one whose groups got dropped — would
# silently ship empty logs forever. Re-assert membership here: this hook
# runs as root on every start (via the ongrid-edge-upgrade.service oneshot,
# ordered Before=ongrid-edge.service), and systemd resolves supplementary
# groups when it forks the agent's ExecStart, so a group added now takes
# effect for the agent that starts moments later. Idempotent
# (usermod -aG is a no-op if already a member).
ensure_log_groups() {
  id ongrid-edge >/dev/null 2>&1 || return 0
  for grp in adm systemd-journal; do
    if getent group "$grp" >/dev/null 2>&1; then
      usermod -aG "$grp" ongrid-edge 2>/dev/null || true
    fi
  done
}
ensure_log_groups

# ----- Mode 1: auto-rollback ------------------------------------------------
#
# Trigger: a prior boot ran apply (LAST_UPGRADE_AT exists) AND the agent
# never wrote HEALTHY_MARKER matching LAST_UPGRADE_VER. The agent writes
# HEALTHY_MARKER once it's accepted by the manager (see edgeagent supervisor).
# If we get here without that marker, the new bundle is broken — roll back
# every file whose <dest>.previous still exists.
maybe_rollback() {
  [[ -f $LAST_UPGRADE_AT && -f $LAST_UPGRADE_VER ]] || return 0
  if [[ -f $HEALTHY_MARKER ]]; then
    last_ver=$(tr -d '[:space:]' < "$LAST_UPGRADE_VER")
    healthy_ver=$(tr -d '[:space:]' < "$HEALTHY_MARKER")
    if [[ -n $last_ver && $last_ver == "$healthy_ver" ]]; then
      # Last upgrade was healthy — nothing to roll back. Clean the
      # marker now that we've confirmed it, so the next upgrade
      # cycle starts from a clean slate.
      rm -f "$LAST_UPGRADE_AT" "$LAST_UPGRADE_VER"
      # Best-effort: prune the .previous side of every swap target so
      # the disk doesn't fill with old bundles.
      find /usr/local/bin /usr/local/lib/ongrid-edge -name '*.previous' -type f -delete 2>/dev/null || true
      return 0
    fi
  fi

  log "auto-rollback: prior upgrade ($(cat "$LAST_UPGRADE_VER" 2>/dev/null)) never reported healthy — restoring .previous files"
  rolled_back=0
  while IFS= read -r -d '' prev; do
    target=${prev%.previous}
    if [[ -f $prev ]]; then
      mv -f "$prev" "$target" 2>/dev/null && rolled_back=$((rolled_back+1))
    fi
  done < <(find /usr/local/bin /usr/local/lib/ongrid-edge -name '*.previous' -type f -print0 2>/dev/null)
  log "auto-rollback: restored $rolled_back file(s)"
  # Clear the marker so a stable boot following the rollback doesn't
  # rollback AGAIN in an infinite loop. We deliberately keep
  # LAST_UPGRADE_VER around (just empty out the at-timestamp) so an
  # observer can still see which version was attempted.
  : > "$LAST_UPGRADE_AT"
  # Also drop the staged incoming/ — the bundle's bad, no point keeping it.
  rm -rf "$INCOMING_DIR"
  return 1   # signal "we already touched files; don't run mode 2 this boot"
}

# ----- Mode 2: bundle apply -------------------------------------------------
apply_bundle() {
  [[ -f $MANIFEST ]] || return 0

  # Pre-flight: every src must exist + every sha must verify before we
  # touch anything. Half a bundle is worse than no bundle.
  while IFS= read -r line; do
    # skip blank + comment
    [[ -z $line || $line == \#* ]] && continue
    set -- $line
    [[ $# -ge 4 ]] || { log "bundle apply: malformed manifest line: $line"; return 0; }
    sha=$1; mode=$2; src=$3; dest=$4
    src_path=$INCOMING_DIR/$src
    if [[ ! -f $src_path ]]; then
      log "bundle apply: src missing: $src_path"
      return 0
    fi
    actual=$(sha256sum "$src_path" | awk '{print $1}')
    if [[ $actual != "$sha" ]]; then
      log "bundle apply: sha mismatch for $src (expected=$sha actual=$actual)"
      return 0
    fi
  done < "$MANIFEST"

  # All-or-nothing swap.
  while IFS= read -r line; do
    [[ -z $line || $line == \#* ]] && continue
    set -- $line
    sha=$1; mode=$2; src=$3; dest=$4
    src_path=$INCOMING_DIR/$src
    # Snapshot the live file for rollback.
    if [[ -f $dest ]]; then
      cp -p "$dest" "$dest.previous" 2>/dev/null || {
        log "bundle apply: backup of $dest failed; aborting this file"
        continue
      }
    fi
    # Stage the new file as <dest>.new on the SAME filesystem so the
    # final rename is atomic (POSIX guarantees same-fs rename is
    # atomic; cross-fs rename falls back to copy + remove which isn't).
    target_dir=$(dirname "$dest")
    mkdir -p "$target_dir"
    new_path=$dest.new
    if ! cp -p "$src_path" "$new_path"; then
      log "bundle apply: copy $src_path → $new_path failed"
      continue
    fi
    chmod "$mode" "$new_path" 2>/dev/null || true
    if ! mv -f "$new_path" "$dest"; then
      log "bundle apply: atomic rename $new_path → $dest failed"
      rm -f "$new_path"
      continue
    fi
    log "bundle apply: swapped $dest"
  done < "$MANIFEST"

  # Record the apply for the next-boot health check.
  date -u +%Y-%m-%dT%H:%M:%SZ > "$LAST_UPGRADE_AT"
  if [[ -f $INCOMING_DIR/VERSION ]]; then
    cp -f "$INCOMING_DIR/VERSION" "$LAST_UPGRADE_VER"
  fi
  rm -f "$HEALTHY_MARKER"
  log "bundle apply: complete — health check armed for next boot"
  return 0
}

# ----- Mode 3: legacy single-file apply ------------------------------------
apply_legacy() {
  [[ -f $LEGACY_PENDING && -f $LEGACY_PENDING_SHA ]] || return 0
  expected=$(tr -d '[:space:]' < "$LEGACY_PENDING_SHA")
  actual=$(sha256sum "$LEGACY_PENDING" | awk '{print $1}')
  if [[ -z $expected || $expected != "$actual" ]]; then
    log "legacy apply: sha mismatch (expected=$expected actual=$actual); discarding"
    rm -f "$LEGACY_PENDING" "$LEGACY_PENDING_SHA"
    return 0
  fi
  if [[ -f $LEGACY_TARGET ]]; then
    cp -p "$LEGACY_TARGET" "$LEGACY_PREVIOUS" || {
      log "legacy apply: backup failed; skipping swap"
      return 0
    }
  fi
  chmod 0755 "$LEGACY_PENDING" || true
  if ! mv -f "$LEGACY_PENDING" "$LEGACY_TARGET" 2>/dev/null; then
    cp -p "$LEGACY_PENDING" "$LEGACY_TARGET.new" && mv -f "$LEGACY_TARGET.new" "$LEGACY_TARGET"
    rm -f "$LEGACY_PENDING"
  fi
  rm -f "$LEGACY_PENDING_SHA"
  log "legacy apply: applied pending single-file upgrade"
}

# Run mode 1 first. If it touched anything (rolled back), skip mode 2 so
# the rollback effect lands cleanly on the next boot. Mode 3 runs
# unconditionally since the two staging dirs don't overlap.
if maybe_rollback; then
  apply_bundle
fi
apply_legacy
exit 0
