#!/usr/bin/env bash
# Behaviour test for the shared-asset mode helpers in data-permissions.sh.
#
# Reproduces the failure this guards against: on a host whose root umask is
# restrictive, `cp` and shell redirection create 0640 files, and the containers
# that bind-mount them run as non-root with a non-zero gid, so they resolve
# against the other-permission bits and cannot read them at all.
#
# The negative assertions matter as much as the positive ones: normalizing must
# never widen .env (database passwords) or certs/ (TLS key).
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
permissions_lib="$repo_root/deploy/install/data-permissions.sh"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

fail() {
    printf 'install asset-modes test failed: %s\n' "$*" >&2
    exit 1
}

[[ -f "$permissions_lib" ]] || fail "missing data-permissions.sh"
# shellcheck source=../deploy/install/data-permissions.sh
source "$permissions_lib"

mode_of() {
    local path="$1" mode
    if mode=$(stat -c '%a' "$path" 2>/dev/null) && [[ "$mode" =~ ^[0-7]+$ ]]; then
        printf '%s\n' "$mode"
        return 0
    fi
    if mode=$(stat -f '%Lp' "$path" 2>/dev/null) && [[ "$mode" =~ ^[0-7]+$ ]]; then
        printf '%s\n' "$mode"
        return 0
    fi
    fail "could not stat mode of $path"
}

assert_mode() {
    local expected="$1" path="$2" actual
    actual=$(mode_of "$path")
    [[ "$actual" == "$expected" ]] \
        || fail "expected mode $expected on $path, got $actual"
}

install_dir="$tmp_dir/opt/ongrid"

# Build a fake install the way a hardened host would: everything created under
# umask 027 so files land 0640 and directories 0750.
(
    umask 027
    mkdir -p "$install_dir/edge" "$install_dir/searxng" \
        "$install_dir/grafana/provisioning/datasources" \
        "$install_dir/prometheus"
    for f in docker-compose.yml prometheus.yml prometheus-rules.yml \
        loki-config.yaml tempo-config.yaml profiles-gateway.yaml frontier.yaml nginx.conf VERSION; do
        printf 'x\n' > "$install_dir/$f"
    done
    printf 'x\n' > "$install_dir/searxng/settings.yml"
    printf 'x\n' > "$install_dir/grafana/provisioning/datasources/loki.yml"
    printf 'x\n' > "$install_dir/prometheus/prometheus.yml"
    # Edge directory: a bundle + checksum + ref written by redirection/tar, and a
    # binary that fetch-edge-assets.sh installs with an explicit exec mode.
    printf 'bundle\n' > "$install_dir/edge/edge-bundle-linux-amd64-v9.9.9.tar.gz"
    printf 'sha\n' > "$install_dir/edge/edge-bundle-linux-amd64-v9.9.9.tar.gz.sha256"
    printf 'ref\n' > "$install_dir/edge/edge-assets-linux-amd64.ref"
    printf 'bin\n' > "$install_dir/edge/ongrid-edge-linux-amd64"
    chmod 0755 "$install_dir/edge/ongrid-edge-linux-amd64"
)

# Credentials, created the way the installer does.
printf 'ONGRID_MYSQL_PASSWORD=secret\n' > "$install_dir/.env"
chmod 600 "$install_dir/.env"
mkdir -p "$install_dir/certs"
chmod 700 "$install_dir/certs"
printf 'key\n' > "$install_dir/certs/tls.key"
chmod 600 "$install_dir/certs/tls.key"

# Sanity-check the fixture actually reproduces the bug, otherwise the test would
# pass vacuously on a host whose umask is already 022.
assert_mode 640 "$install_dir/edge/edge-bundle-linux-amd64-v9.9.9.tar.gz"
assert_mode 640 "$install_dir/frontier.yaml"
assert_mode 750 "$install_dir/grafana/provisioning"

ongrid_normalize_shared_asset_modes "$install_dir"

# Bind-mounted files must be readable by a non-root, non-zero-gid container.
for f in docker-compose.yml prometheus.yml prometheus-rules.yml \
    loki-config.yaml tempo-config.yaml profiles-gateway.yaml frontier.yaml nginx.conf VERSION; do
    assert_mode 644 "$install_dir/$f"
done
assert_mode 644 "$install_dir/edge/edge-bundle-linux-amd64-v9.9.9.tar.gz"
assert_mode 644 "$install_dir/edge/edge-bundle-linux-amd64-v9.9.9.tar.gz.sha256"
assert_mode 644 "$install_dir/edge/edge-assets-linux-amd64.ref"
assert_mode 644 "$install_dir/searxng/settings.yml"
assert_mode 644 "$install_dir/grafana/provisioning/datasources/loki.yml"
assert_mode 644 "$install_dir/prometheus/prometheus.yml"

# Directories need the traversal bit, and executables must keep theirs.
assert_mode 755 "$install_dir"
assert_mode 755 "$install_dir/edge"
assert_mode 755 "$install_dir/grafana/provisioning"
assert_mode 755 "$install_dir/edge/ongrid-edge-linux-amd64"

# Credentials must be untouched — a recursive chmod over the install dir would
# have widened both of these.
assert_mode 600 "$install_dir/.env"
assert_mode 700 "$install_dir/certs"
assert_mode 600 "$install_dir/certs/tls.key"

# Idempotent: a second pass must not change anything.
ongrid_normalize_shared_asset_modes "$install_dir"
assert_mode 644 "$install_dir/edge/edge-bundle-linux-amd64-v9.9.9.tar.gz"
assert_mode 755 "$install_dir/edge/ongrid-edge-linux-amd64"
assert_mode 600 "$install_dir/.env"

# Missing install dir is a no-op, not an error.
ongrid_normalize_shared_asset_modes "$tmp_dir/absent" \
    || fail "normalizing a missing install dir should succeed"

# The umask wrapper must relax modes for the wrapped command and restore after.
outer_umask=$(umask)
(
    umask 027
    ongrid_with_shared_asset_umask bash -c "printf 'x\n' > '$tmp_dir/wrapped.conf'"
    printf 'x\n' > "$tmp_dir/unwrapped.conf"
    [[ "$(umask)" == "0027" ]] || fail "umask not restored inside subshell: $(umask)"
)
assert_mode 644 "$tmp_dir/wrapped.conf"
assert_mode 640 "$tmp_dir/unwrapped.conf"
[[ "$(umask)" == "$outer_umask" ]] || fail "umask leaked into the caller"

# A failing wrapped command must propagate its status so `set -e` and the ERR
# trap still fire in the installers.
set +e
ongrid_with_shared_asset_umask bash -c 'exit 7'
wrapped_rc=$?
set -e
[[ "$wrapped_rc" == 7 ]] || fail "wrapper swallowed the exit status: $wrapped_rc"

# Stale-staging prune: anchored name, depth 1, age floor.
# Regression guard: a retained .edge-backup.* must survive. After a health-check
# timeout upgrade.sh keeps that tree as the manual rollback source, and this
# prune runs at the start of the next upgrade attempt, before a new Edge swap.
# Pruning it by age would delete the operator's only known-good Edge tree.
mkdir -p "$install_dir/.edge-stage.OLDONE" "$install_dir/.edge-backup.RETAINED" \
    "$install_dir/.edge-stage.FRESH" "$install_dir/keepme" \
    "$install_dir/edge/.edge-stage.NESTED"
printf 'edge\n' > "$install_dir/.edge-backup.RETAINED/marker"
touch -d '3 hours ago' "$install_dir/.edge-stage.OLDONE" \
    "$install_dir/.edge-backup.RETAINED" "$install_dir/keepme" \
    "$install_dir/edge/.edge-stage.NESTED" 2>/dev/null \
    || touch -A -030000 "$install_dir/.edge-stage.OLDONE" \
        "$install_dir/.edge-backup.RETAINED" "$install_dir/keepme" \
        "$install_dir/edge/.edge-stage.NESTED"

ongrid_prune_stale_edge_staging "$install_dir"

[[ ! -d "$install_dir/.edge-stage.OLDONE" ]] || fail "stale staging dir was not pruned"
[[ -d "$install_dir/.edge-backup.RETAINED" ]] \
    || fail "a retained Edge backup must survive the next upgrade's startup prune (manual rollback source)"
[[ -f "$install_dir/.edge-backup.RETAINED/marker" ]] \
    || fail "retained Edge backup survived as an empty directory; its contents are the rollback source"
[[ -d "$install_dir/.edge-stage.FRESH" ]] || fail "a fresh staging dir must survive (concurrent installer)"
[[ -d "$install_dir/keepme" ]] || fail "prune matched a directory outside the name prefix"
[[ -d "$install_dir/edge/.edge-stage.NESTED" ]] || fail "prune must not recurse below depth 1"

# Repeated startups must keep leaving the backup alone, since an operator may
# take several attempts before deciding to roll back.
ongrid_prune_stale_edge_staging "$install_dir"
[[ -f "$install_dir/.edge-backup.RETAINED/marker" ]] \
    || fail "retained Edge backup was pruned on a second startup"

echo "install asset-modes test passed"
