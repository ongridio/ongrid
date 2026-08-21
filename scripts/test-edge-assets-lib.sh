#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=deploy/install/edge/edge-assets-lib.sh
source "$repo_root/deploy/install/edge/edge-assets-lib.sh"

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

fail() {
    printf 'edge target selection test failed: %s\n' "$*" >&2
    exit 1
}

mkdir -p "$tmp_dir/installed" "$tmp_dir/package" "$tmp_dir/existing"
printf 'ONGRID_EDGE_TARGETS=linux-amd64\n' \
    > "$tmp_dir/installed/edge-artifacts.env"
printf 'ONGRID_EDGE_TARGETS=linux-amd64 linux-arm64\n' \
    > "$tmp_dir/package/edge-artifacts.env"

resolved=$(ongrid_resolve_edge_targets "" \
    "$tmp_dir/package/edge-artifacts.env" \
    "$tmp_dir/installed/edge-artifacts.env" "$tmp_dir/existing")
[[ "$resolved" == 'linux-amd64 linux-arm64' ]] \
    || fail "a dual-architecture package did not supersede the old single-architecture selection"

resolved=$(ongrid_resolve_edge_targets linux-arm64 \
    "$tmp_dir/package/edge-artifacts.env" \
    "$tmp_dir/installed/edge-artifacts.env" "$tmp_dir/existing")
[[ "$resolved" == linux-arm64 ]] || fail "an explicit target did not override persisted state"

resolved=$(ongrid_resolve_edge_targets "" /does/not/exist \
    "$tmp_dir/installed/edge-artifacts.env" "$tmp_dir/existing")
[[ "$resolved" == linux-amd64 ]] \
    || fail "an installed selection was not preserved when the package had no target metadata"

: > "$tmp_dir/existing/ongrid-edge-linux-arm64"
: > "$tmp_dir/existing/ongrid-edge-linux-amd64"
resolved=$(ongrid_resolve_edge_targets "" /does/not/exist /does/not/exist "$tmp_dir/existing")
[[ "$resolved" == 'linux-amd64 linux-arm64' ]] \
    || fail "legacy installed binaries were not converted into a persistent target selection"

if ongrid_normalize_edge_targets 'linux-amd64 linux-ppc64le' >/dev/null; then
    fail "an unsupported Edge target was accepted"
fi

[[ "$(ongrid_detect_host_edge_target x86_64)" == linux-amd64 ]] \
    || fail "x86_64 host architecture was not mapped to linux-amd64"
[[ "$(ongrid_detect_host_edge_target aarch64)" == linux-arm64 ]] \
    || fail "aarch64 host architecture was not mapped to linux-arm64"
if ongrid_detect_host_edge_target ppc64le >/dev/null 2>&1; then
    fail "an unsupported host architecture was accepted"
fi

host_bin="$tmp_dir/host-bin"
mkdir -p "$host_bin"
printf '#!/usr/bin/env bash\nprintf "%%s\\n" aarch64\n' > "$host_bin/uname"
chmod 0755 "$host_bin/uname"
resolved=$(PATH="$host_bin:$PATH" ongrid_resolve_edge_targets "" \
    /does/not/exist /does/not/exist "$tmp_dir/no-existing-assets")
[[ "$resolved" == linux-arm64 ]] \
    || fail "a fresh universal package did not select the host architecture"

ongrid_write_edge_artifact_config "$tmp_dir/written.env" edge-deps-test \
    'linux-amd64 linux-arm64'
grep -Fxq 'ONGRID_EDGE_DEPS_TAG=edge-deps-test' "$tmp_dir/written.env"
grep -Fxq 'ONGRID_EDGE_TARGETS=linux-amd64 linux-arm64' "$tmp_dir/written.env"

embedded="$tmp_dir/embedded"
mkdir -p "$embedded"
components=(
    ongrid-edge node_exporter process_exporter mysqld_exporter postgres_exporter
    redis_exporter mongodb_exporter otelcol-contrib
)
for target in linux-amd64 linux-arm64; do
    for component in "${components[@]}"; do
        printf '%s %s\n' "$component" "$target" > "$embedded/${component}-${target}"
    done
done
ongrid_validate_embedded_edge_assets "$embedded" 'linux-amd64 linux-arm64' \
    || fail "a complete embedded dual-architecture package was rejected"
rm "$embedded/otelcol-contrib-linux-arm64"
if ongrid_validate_embedded_edge_assets "$embedded" 'linux-amd64 linux-arm64' \
    >/dev/null 2>&1; then
    fail "an incomplete embedded package was accepted"
fi

bundle_dir="$tmp_dir/bundle"
mkdir -p "$bundle_dir"
for component in "${components[@]}"; do
    printf '%s\n' "$component" > "$bundle_dir/${component}-linux-amd64"
done
printf 'apply\n' > "$bundle_dir/apply-pending-upgrade.sh"
bash "$repo_root/deploy/install/edge/build-edge-bundle.sh" \
    "$bundle_dir" vtest linux-amd64 >/dev/null
[[ -s "$bundle_dir/edge-bundle-linux-amd64-vtest.tar.gz" \
    && -s "$bundle_dir/edge-bundle-linux-amd64-vtest.tar.gz.sha256" ]] \
    || fail "a complete Edge tree did not produce an upgrade bundle"
rm "$bundle_dir/process_exporter-linux-amd64"
if bash "$repo_root/deploy/install/edge/build-edge-bundle.sh" \
    "$bundle_dir" vnext linux-amd64 >/dev/null 2>&1; then
    fail "an incomplete Edge tree produced an upgrade bundle"
fi

printf 'edge target selection tests passed\n'
