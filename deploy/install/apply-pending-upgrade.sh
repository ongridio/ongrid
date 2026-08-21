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
# Idempotent + best-effort: if nothing's staged or anything goes wrong,
# preserve or restore the complete previous bundle and exit 0 so systemd
# starts the known-good binaries already on disk.

set -uo pipefail

STAGE_DIR=${ONGRID_EDGE_UPGRADE_STAGE_DIR:-/var/lib/ongrid-edge/.upgrade}
INCOMING_DIR=$STAGE_DIR/incoming
MANIFEST=$INCOMING_DIR/MANIFEST.txt
LAST_UPGRADE_AT=$STAGE_DIR/last_upgrade_at
LAST_UPGRADE_VER=$STAGE_DIR/last_upgrade_ver
HEALTHY_MARKER=$STAGE_DIR/healthy_marker
NEW_TARGETS=$STAGE_DIR/last_upgrade_new_targets
TARGET_BIN_DIR=${ONGRID_EDGE_UPGRADE_BIN_DIR:-/usr/local/bin}
TARGET_LIB_DIR=${ONGRID_EDGE_UPGRADE_LIB_DIR:-/usr/local/lib/ongrid-edge}

# Legacy single-file paths (kept for back-compat — see mode 3 below).
LEGACY_TARGET=${ONGRID_EDGE_UPGRADE_LEGACY_TARGET:-/usr/local/bin/ongrid-edge}
LEGACY_PENDING=$STAGE_DIR/pending
LEGACY_PENDING_SHA=$STAGE_DIR/pending.sha256
LEGACY_PREVIOUS=$STAGE_DIR/previous

log() { logger -t ongrid-edge-upgrade "$*"; }

# ----- Pre-start: ensure log-read group membership -------------------------
#
# install-edge.sh adds ongrid-edge to adm + systemd-journal so the logs
# logs Collector can read /var/log/* (root:adm 640) and the journal.
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
  [[ -s $LAST_UPGRADE_AT && -s $LAST_UPGRADE_VER ]] || return 0
  if [[ -f $HEALTHY_MARKER ]]; then
    last_ver=$(tr -d '[:space:]' < "$LAST_UPGRADE_VER")
    healthy_ver=$(tr -d '[:space:]' < "$HEALTHY_MARKER")
    if [[ -n $last_ver && $last_ver == "$healthy_ver" ]]; then
      # Last upgrade was healthy — nothing to roll back. Clean the
      # marker now that we've confirmed it, so the next upgrade
      # cycle starts from a clean slate.
      rm -f "$LAST_UPGRADE_AT" "$LAST_UPGRADE_VER" "$NEW_TARGETS"
      # Best-effort: prune the .previous side of every swap target so
      # the disk doesn't fill with old bundles.
      find "$TARGET_BIN_DIR" "$TARGET_LIB_DIR" -name '*.previous' -type f -delete 2>/dev/null || true
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
  done < <(find "$TARGET_BIN_DIR" "$TARGET_LIB_DIR" -name '*.previous' -type f -print0 2>/dev/null)
  if [[ -f $NEW_TARGETS ]]; then
    while IFS= read -r target; do
      [[ -n $target ]] || continue
      rm -f -- "$target" 2>/dev/null || log "auto-rollback: could not remove newly introduced target $target"
    done < "$NEW_TARGETS"
  fi
  log "auto-rollback: restored $rolled_back file(s)"
  # Remove the armed marker so a stable boot following the rollback does not
  # enter another rollback cycle. Keep LAST_UPGRADE_VER for diagnostics.
  rm -f "$LAST_UPGRADE_AT" "$HEALTHY_MARKER" "$NEW_TARGETS"
  # Also drop the staged incoming/ — the bundle's bad, no point keeping it.
  rm -rf "$INCOMING_DIR"
  return 1   # signal "we already touched files; don't run mode 2 this boot"
}

# ----- Mode 2: bundle apply -------------------------------------------------
BUNDLE_DESTS=()
BUNDLE_EXISTED=()
BUNDLE_COUNT=0

rollback_bundle_files() {
  local i dest failed=0
  for ((i=0; i<BUNDLE_COUNT; i++)); do
    dest=${BUNDLE_DESTS[$i]}
    rm -f -- "$dest.new" 2>/dev/null || failed=1
    if [[ -f $dest.previous ]]; then
      mv -f -- "$dest.previous" "$dest" 2>/dev/null || failed=1
    elif [[ ${BUNDLE_EXISTED[$i]} == 0 ]]; then
      rm -f -- "$dest" 2>/dev/null || failed=1
    fi
  done
  return "$failed"
}

apply_bundle() {
  [[ -f $MANIFEST ]] || return 0

  local line sha mode src dest extra src_path actual bundle_version existing i target_dir
  local -a bundle_modes=() bundle_srcs=()
  BUNDLE_DESTS=()
  BUNDLE_EXISTED=()
  BUNDLE_COUNT=0

  if [[ ! -s $INCOMING_DIR/VERSION ]]; then
    log "bundle apply: VERSION is missing or empty"
    rm -rf "$INCOMING_DIR"
    return 1
  fi
  bundle_version=$(tr -d '[:space:]' < "$INCOMING_DIR/VERSION")
  if [[ -z $bundle_version ]]; then
    log "bundle apply: VERSION is empty"
    rm -rf "$INCOMING_DIR"
    return 1
  fi

  # Pre-flight: every src must exist + every sha must verify before we
  # touch anything. Half a bundle is worse than no bundle.
  while IFS= read -r line; do
    # Skip blank/comment lines. read uses shell whitespace as a delimiter
    # without evaluating glob characters or shell syntax from the manifest.
    [[ $line =~ ^[[:space:]]*$ || $line =~ ^[[:space:]]*# ]] && continue
    sha=; mode=; src=; dest=; extra=
    read -r sha mode src dest extra <<< "$line"
    [[ -n $sha && -n $mode && -n $src && -n $dest && -z $extra ]] \
      || { log "bundle apply: malformed manifest line: $line"; rm -rf "$INCOMING_DIR"; return 1; }
    [[ $sha =~ ^[0-9a-fA-F]{64}$ ]] || { log "bundle apply: invalid sha for $src"; rm -rf "$INCOMING_DIR"; return 1; }
    [[ $mode =~ ^0?[0-7]{3}$ ]] || { log "bundle apply: invalid mode for $src: $mode"; rm -rf "$INCOMING_DIR"; return 1; }
    [[ $src != /* && $src != *..* ]] || { log "bundle apply: invalid src path: $src"; rm -rf "$INCOMING_DIR"; return 1; }
    case "$dest" in
      "$TARGET_BIN_DIR"/*|"$TARGET_LIB_DIR"/*) ;;
      *) log "bundle apply: destination outside managed roots: $dest"; rm -rf "$INCOMING_DIR"; return 1 ;;
    esac
    [[ $dest != *'/../'* && $dest != */.. && $dest != *'/./'* && $dest != */. ]] \
      || { log "bundle apply: invalid destination path: $dest"; rm -rf "$INCOMING_DIR"; return 1; }
    for ((i=0; i<BUNDLE_COUNT; i++)); do
      existing=${BUNDLE_DESTS[$i]}
      [[ $existing != "$dest" ]] || { log "bundle apply: duplicate destination: $dest"; rm -rf "$INCOMING_DIR"; return 1; }
    done
    src_path=$INCOMING_DIR/$src
    if [[ ! -f $src_path || -L $src_path ]]; then
      log "bundle apply: src missing: $src_path"
      rm -rf "$INCOMING_DIR"
      return 1
    fi
    if ! actual=$(sha256sum "$src_path" | awk '{print $1}'); then
      log "bundle apply: could not hash $src"
      rm -rf "$INCOMING_DIR"
      return 1
    fi
    if [[ $actual != "$sha" ]]; then
      log "bundle apply: sha mismatch for $src (expected=$sha actual=$actual)"
      rm -rf "$INCOMING_DIR"
      return 1
    fi
    if [[ -L $dest || ( -e $dest && ! -f $dest ) ]]; then
      log "bundle apply: destination is not a regular file: $dest"
      rm -rf "$INCOMING_DIR"
      return 1
    fi
    bundle_modes[$BUNDLE_COUNT]=$mode
    bundle_srcs[$BUNDLE_COUNT]=$src
    BUNDLE_DESTS[$BUNDLE_COUNT]=$dest
    [[ -f $dest ]] && BUNDLE_EXISTED[$BUNDLE_COUNT]=1 || BUNDLE_EXISTED[$BUNDLE_COUNT]=0
    BUNDLE_COUNT=$((BUNDLE_COUNT + 1))
  done < "$MANIFEST"

  if (( BUNDLE_COUNT == 0 )); then
    log "bundle apply: manifest declared zero files"
    rm -rf "$INCOMING_DIR"
    return 1
  fi

  # Prepare every backup and .new file before touching any live target.
  for ((i=0; i<BUNDLE_COUNT; i++)); do
    dest=${BUNDLE_DESTS[$i]}
    src=${bundle_srcs[$i]}
    mode=${bundle_modes[$i]}
    src_path=$INCOMING_DIR/$src
    target_dir=$(dirname "$dest")
    if ! mkdir -p "$target_dir"; then
      log "bundle apply: create target directory failed: $target_dir"
      rollback_bundle_files || log "bundle apply: cleanup after directory failure was incomplete"
      rm -rf "$INCOMING_DIR"
      return 1
    fi
    # Stale preparation files are safe to discard before the rollback marker
    # is armed; the live target has not been changed yet.
    rm -f -- "$dest.new" "$dest.previous" 2>/dev/null || true
    if [[ ${BUNDLE_EXISTED[$i]} == 1 ]] && ! cp -p -- "$dest" "$dest.previous"; then
      log "bundle apply: backup of $dest failed"
      rollback_bundle_files || log "bundle apply: cleanup after backup failure was incomplete"
      rm -rf "$INCOMING_DIR"
      return 1
    fi
    if ! cp -p -- "$src_path" "$dest.new" || ! chmod "$mode" "$dest.new"; then
      log "bundle apply: prepare $dest.new failed"
      rollback_bundle_files || log "bundle apply: cleanup after prepare failure was incomplete"
      rm -rf "$INCOMING_DIR"
      return 1
    fi
  done

  # Arm rollback before the first rename. A power loss during the commit is
  # recovered on the next systemd start.
  : > "$NEW_TARGETS.tmp"
  for ((i=0; i<BUNDLE_COUNT; i++)); do
    [[ ${BUNDLE_EXISTED[$i]} == 0 ]] && printf '%s\n' "${BUNDLE_DESTS[$i]}" >> "$NEW_TARGETS.tmp"
  done
  if ! mv -f "$NEW_TARGETS.tmp" "$NEW_TARGETS" \
      || ! cp -f "$INCOMING_DIR/VERSION" "$LAST_UPGRADE_VER" \
      || ! date -u +%Y-%m-%dT%H:%M:%SZ > "$LAST_UPGRADE_AT"; then
    log "bundle apply: could not arm rollback metadata"
    rollback_bundle_files || log "bundle apply: cleanup after metadata failure was incomplete"
    rm -f "$LAST_UPGRADE_AT" "$LAST_UPGRADE_VER" "$NEW_TARGETS" "$NEW_TARGETS.tmp"
    rm -rf "$INCOMING_DIR"
    return 1
  fi
  rm -f "$HEALTHY_MARKER"

  for ((i=0; i<BUNDLE_COUNT; i++)); do
    dest=${BUNDLE_DESTS[$i]}
    if ! mv -f -- "$dest.new" "$dest"; then
      log "bundle apply: atomic rename $dest.new → $dest failed; rolling back the complete bundle"
      if rollback_bundle_files; then
        rm -f "$LAST_UPGRADE_AT" "$LAST_UPGRADE_VER" "$NEW_TARGETS"
      else
        log "bundle apply: immediate rollback incomplete; rollback remains armed for next boot"
      fi
      rm -rf "$INCOMING_DIR"
      return 1
    fi
    log "bundle apply: swapped $dest"
  done

  # The live files and rollback copies are now sufficient. Removing incoming
  # prevents the same bundle from being applied again on every later restart.
  rm -rf "$INCOMING_DIR"
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

# Run mode 1 first. If it touched anything (rolled back), do not apply another
# payload during the same boot. A whole bundle supersedes the legacy single-
# file staging path: applying both would replace the freshly upgraded Agent
# with a stale pending binary while leaving the new plugins in place.
if maybe_rollback; then
  if [[ -f $MANIFEST ]]; then
    # Clear superseded legacy staging before committing the bundle. If power
    # is lost after the bundle swap, a later boot must never apply an older
    # single-file payload over the new Agent while retaining new plugins.
    rm -f "$LEGACY_PENDING" "$LEGACY_PENDING_SHA"
    apply_bundle
  else
    apply_legacy
  fi
fi
exit 0
