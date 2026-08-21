#!/usr/bin/env bash
# Download checksum-verified Edge binaries directly from CNB Release assets.

set -euo pipefail

DEST_DIR=${1:?usage: fetch-edge-assets.sh <destination> <version> [linux-amd64|linux-arm64 ...]}
VERSION=${2:?version}
shift 2

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CONFIG_FILE=${ONGRID_EDGE_ARTIFACT_CONFIG:-$SCRIPT_DIR/edge-artifacts.env}
CONFIG_DEPS_TAG=""
CONFIG_TARGETS=""
if [[ -f "$CONFIG_FILE" ]]; then
    CONFIG_DEPS_TAG=$(sed -n 's/^ONGRID_EDGE_DEPS_TAG=//p' "$CONFIG_FILE" | tail -n 1)
    CONFIG_TARGETS=$(sed -n 's/^ONGRID_EDGE_TARGETS=//p' "$CONFIG_FILE" | tail -n 1)
fi
VERIFY_DEPS_SCRIPT="$SCRIPT_DIR/verify-edge-deps-archive.sh"
EDGE_ASSETS_LIB="$SCRIPT_DIR/edge-assets-lib.sh"

BASE_URL=${ONGRID_EDGE_ARTIFACT_BASE_URL:-https://cnb.cool/ongridio/ongrid-edge/-/releases/download}
DEPS_TAG=${ONGRID_EDGE_DEPS_TAG:-$CONFIG_DEPS_TAG}
[[ -n "$DEPS_TAG" ]] || { echo "ONGRID_EDGE_DEPS_TAG is required (normally provided by edge-artifacts.env)" >&2; exit 1; }
CACHE_DIR=${ONGRID_EDGE_ARTIFACT_CACHE_DIR:-/var/cache/ongrid/edge-artifacts}
CURL_FLAGS=(
    --fail --location --show-error
    --retry 3 --retry-delay 3
    --connect-timeout 15 --speed-time 60 --speed-limit 1024
)
# --retry-all-errors widens `--retry` to cover non-transient HTTP errors, but it
# only exists in curl 7.71+. AliOS / CentOS 8 ship 7.61, where an unknown option
# makes curl exit 2 and every download fail. Ask curl whether it accepts the flag
# rather than parsing versions: on 7.71+ the flag set is unchanged, and on older
# curl we keep plain `--retry 3` instead of failing outright. The checksum gate in
# download_verified is unaffected either way, so a bad download still fails hard.
if curl --retry-all-errors --version >/dev/null 2>&1; then
    CURL_FLAGS+=(--retry-all-errors)
fi

if (( $# > 0 )); then
    TARGETS=("$@")
else
    raw_targets=${ONGRID_EDGE_TARGETS:-$CONFIG_TARGETS}
    if [[ -z "$raw_targets" ]]; then
        [[ -r "$EDGE_ASSETS_LIB" ]] || {
            echo "missing Edge target selector: $EDGE_ASSETS_LIB" >&2
            exit 1
        }
        # shellcheck source=deploy/install/edge/edge-assets-lib.sh
        source "$EDGE_ASSETS_LIB"
        raw_targets=$(ongrid_detect_host_edge_target)
    fi
    read -r -a TARGETS <<<"$raw_targets"
fi

log() { printf '[edge-assets] %s\n' "$*" >&2; }
die() { printf '[edge-assets] error: %s\n' "$*" >&2; exit 1; }

[[ "$VERSION" =~ ^v?[0-9A-Za-z][0-9A-Za-z._-]*$ ]] || die "invalid Edge version: $VERSION"
[[ "$DEPS_TAG" =~ ^[0-9A-Za-z][0-9A-Za-z._-]*$ ]] || die "invalid dependency release tag: $DEPS_TAG"

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
[[ -x "$VERIFY_DEPS_SCRIPT" ]] || die "missing dependency verifier: $VERIFY_DEPS_SCRIPT"
mkdir -p "$DEST_DIR" "$CACHE_DIR"

work=$(mktemp -d "${TMPDIR:-/tmp}/ongrid-edge-assets.XXXXXX")
trap 'rm -rf "$work"' EXIT

verify_checksum_line() {
    local file=$1 expected_name=$2 line=$3
    local expected_sha recorded_name extra actual_sha

    IFS=' ' read -r expected_sha recorded_name extra <<<"$line"
    recorded_name=${recorded_name#\*}
    [[ "$expected_sha" =~ ^[0-9a-f]{64}$ ]] || return 1
    [[ "$recorded_name" == "$expected_name" && -z "${extra:-}" ]] || return 1
    actual_sha=$(sha256sum "$file" | awk 'NR == 1 {print $1}')
    [[ "$actual_sha" == "$expected_sha" ]]
}

verify_checksum_sidecar() {
    local file=$1 sidecar=$2 line
    [[ -s "$file" && -s "$sidecar" ]] || return 1
    if ! line=$(awk 'NF {line=$0; count++} END {if (count != 1) exit 1; print line}' "$sidecar"); then
        return 1
    fi
    verify_checksum_line "$file" "$(basename "$file")" "$line"
}

download_verified() {
    local tag=$1 filename=$2
    local cache_tag_dir="$CACHE_DIR/$tag"
    local cached="$cache_tag_dir/$filename" sidecar="$cache_tag_dir/$filename.sha256"
    if verify_checksum_sidecar "$cached" "$sidecar"; then
        log "using verified cache $tag/$filename"
        printf '%s\n' "$cached"
        return 0
    fi

    local incoming="$work/incoming/$tag/$filename"
    mkdir -p "$(dirname "$incoming")" "$cache_tag_dir"
    log "downloading $BASE_URL/$tag/$filename"
    curl "${CURL_FLAGS[@]}" -o "$incoming" "$BASE_URL/$tag/$filename"
    curl "${CURL_FLAGS[@]}" -o "$incoming.sha256" "$BASE_URL/$tag/$filename.sha256"
    verify_checksum_sidecar "$incoming" "$incoming.sha256" \
        || die "checksum verification failed for $filename"
    install -m 0644 "$incoming" "$cached"
    install -m 0644 "$incoming.sha256" "$sidecar"
    printf '%s\n' "$cached"
}

deps=(
    node_exporter process_exporter mysqld_exporter postgres_exporter
    redis_exporter mongodb_exporter otelcol-contrib
)

for target in "${TARGETS[@]}"; do
    case "$target" in
        linux-amd64|linux-arm64) ;;
        *) die "unsupported Edge target: $target" ;;
    esac

    deps_name="edge-deps-${target}.tar.xz"
    edge_name="ongrid-edge-${target}-${VERSION}"
    deps_archive=$(download_verified "$DEPS_TAG" "$deps_name")
    edge_binary=$(download_verified "$VERSION" "$edge_name")

    extract_dir="$work/extract-$target"
    "$VERIFY_DEPS_SCRIPT" "$deps_archive" "$target" "$DEPS_TAG" "$extract_dir" \
        >/dev/null || die "dependency archive validation failed for $target"

    for component in "${deps[@]}"; do
        install -m 0755 "$extract_dir/$component" "$DEST_DIR/${component}-${target}"
    done
    install -m 0755 "$edge_binary" "$DEST_DIR/ongrid-edge-${target}"

    deps_sha=$(sha256sum "$deps_archive" | awk 'NR == 1 {print $1}')
    edge_sha=$(sha256sum "$edge_binary" | awk 'NR == 1 {print $1}')
    printf 'deps=%s/%s/%s sha256:%s\nedge=%s/%s/%s sha256:%s\n' \
        "$BASE_URL" "$DEPS_TAG" "$deps_name" "$deps_sha" \
        "$BASE_URL" "$VERSION" "$edge_name" "$edge_sha" \
        > "$DEST_DIR/edge-assets-${target}.ref"
    # Redirection applies the caller's umask, unlike the `install -m 0755` above.
    # The Manager container reads this file as non-root, so pin it explicitly.
    chmod 0644 "$DEST_DIR/edge-assets-${target}.ref"
    log "verified and staged $target"
done
