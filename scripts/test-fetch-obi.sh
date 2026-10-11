#!/usr/bin/env bash
set -euo pipefail
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/payload/NOTICES"
printf 'upstream license\n' > "$work/payload/LICENSE"
printf 'upstream and Ongrid notices\n' > "$work/payload/NOTICE"
printf 'dependency notice\n' > "$work/payload/NOTICES/dependency"
cat > "$work/bin/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
while (( $# )); do
    case "$1" in
        -o) output=$2; shift 2 ;;
        --retry|--connect-timeout|--retry-delay|--speed-time|--speed-limit) shift 2 ;;
        -fL|--fail|--location|--silent|--show-error|--retry-all-errors) shift ;;
        *) url=$1; shift ;;
    esac
done
printf '%s\n' "$url" >> "$MOCK_REQUESTS"
[[ "$url" == "$MOCK_BASE/"* ]] || exit 22
[[ "${MOCK_DOWNLOAD_FAIL:-0}" != 1 ]] || exit 22
cp "$MOCK_ASSETS/${url##*/}" "$output"
SH
chmod +x "$work/bin/curl"
export PATH="$work/bin:$PATH" MOCK_REQUESTS="$work/requests"
for version in 0.14.0 0.14.0-ongrid.1; do
    export MOCK_BASE="https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation/releases/download/v$version"
    tag="edge-deps-layout2-obi$version-o0.157.0-n1.8.2-pr0.8.4-my0.19.0-pg0.19.1-r1.86.0-m0.51.0"
    [[ "$version" != *-ongrid.* ]] || export MOCK_BASE="https://cnb.cool/ongridio/ongrid-edge/-/releases/download/$tag"
    for arch in amd64 arm64; do
        export MOCK_ASSETS="$work/$version-$arch"
        mkdir -p "$MOCK_ASSETS"
        printf '#!/bin/sh\nprintf "%%s\\n" "%s %s"\n' "$version" "$arch" > "$work/payload/obi"
        tar -C "$work/payload" -czf "$MOCK_ASSETS/obi-v$version-linux-$arch.tar.gz" obi LICENSE NOTICE NOTICES
        asset="obi-v$version-linux-$arch.tar.gz"
        sums=SHA256SUMS
        (cd "$MOCK_ASSETS" && sha256sum *.tar.gz > SHA256SUMS)
        if [[ "$version" == *-ongrid.* ]]; then
            mkdir -p "$work/mock-bin/linux-$arch"
            for component in node_exporter process_exporter mysqld_exporter postgres_exporter redis_exporter mongodb_exporter otelcol-contrib obi; do
                cp "$work/payload/obi" "$work/mock-bin/linux-$arch/$component"
            done
            cat "$work/payload/LICENSE" "$work/payload/NOTICE" "$work/payload/NOTICES/dependency" > "$work/mock-bin/linux-$arch/obi.NOTICES"
            EDGE_BIN_ROOT="$work/mock-bin" OBI_VERSION="$version" OTELCOL_VERSION=0.157.0 NODE_EXPORTER_VERSION=1.8.2 \
                PROCESS_EXPORTER_VERSION=0.8.4 MYSQLD_EXPORTER_VERSION=0.19.0 POSTGRES_EXPORTER_VERSION=0.19.1 \
                REDIS_EXPORTER_VERSION=1.86.0 MONGODB_EXPORTER_VERSION=0.51.0 \
                bash "$repo_root/dist/build-edge-attachments.sh" deps "$tag" "$MOCK_ASSETS" "linux-$arch" > "$work/build.log"
            asset="edge-deps-linux-$arch.tar.xz"
            sums="$asset.sha256"
        fi
        dest="$work/installed-$version-$arch"
        bash "$repo_root/scripts/fetch-obi.sh" "$version" "$arch" "$dest" "$tag" > "$work/fetch.log"
        [[ "$("$dest/obi" --version)" == "$version $arch" ]]
        [[ ! -x "$dest/obi.NOTICES" ]]
        grep -q 'upstream license' "$dest/obi.NOTICES"
        grep -q 'Ongrid notices' "$dest/obi.NOTICES"
        grep -q 'dependency notice' "$dest/obi.NOTICES"

        cp "$MOCK_ASSETS/$sums" "$work/good-sums"
        for failure in mismatch missing download; do
            cp "$work/good-sums" "$MOCK_ASSETS/$sums"
            export MOCK_DOWNLOAD_FAIL=0
            case "$failure" in
                mismatch) printf '%064d  %s\n' 0 "$asset" > "$MOCK_ASSETS/$sums" ;;
                missing) : > "$MOCK_ASSETS/$sums" ;;
                download) export MOCK_DOWNLOAD_FAIL=1 ;;
            esac
            printf 'previous binary\n' > "$dest/obi"
            if bash "$repo_root/scripts/fetch-obi.sh" "$version" "$arch" "$dest" "$tag" > "$work/rejected.log" 2>&1; then
                echo "fetch-obi accepted $failure for $version/$arch" >&2; exit 1
            fi
            [[ "$(cat "$dest/obi")" == 'previous binary' ]]
        done
        export MOCK_DOWNLOAD_FAIL=0
        if [[ "$version" == *-ongrid.* ]]; then
            cp "$work/good-sums" "$MOCK_ASSETS/$sums"
            if bash "$repo_root/scripts/fetch-obi.sh" 0.14.0-ongrid.99 "$arch" "$dest" "$tag" > "$work/rejected.log" 2>&1; then
                echo 'fetch-obi accepted mismatched OBI metadata' >&2; exit 1
            fi
            [[ "$(cat "$dest/obi")" == 'previous binary' ]]
            mkdir -p "$work/corrupt-$arch"
            tar -xJf "$MOCK_ASSETS/$asset" -C "$work/corrupt-$arch"
            printf 'tampered binary\n' > "$work/corrupt-$arch/obi"
            tar -cJf "$MOCK_ASSETS/$asset" -C "$work/corrupt-$arch" .
            (cd "$MOCK_ASSETS" && sha256sum "$asset" > "$sums")
            if bash "$repo_root/scripts/fetch-obi.sh" "$version" "$arch" "$dest" "$tag" > "$work/rejected.log" 2>&1; then
                echo 'fetch-obi accepted corrupt inner manifest' >&2; exit 1
            fi
            [[ "$(cat "$dest/obi")" == 'previous binary' ]]
        fi
    done
done
before=$(wc -l < "$MOCK_REQUESTS")
for version in '../0.14.0' 0.14.0-ongrid.0 0.14.0-other.1 0.14.0-ongrid.1/extra; do
    if bash "$repo_root/scripts/fetch-obi.sh" "$version" arm64 "$work/invalid" > "$work/rejected.log" 2>&1; then
        echo "fetch-obi accepted invalid version $version" >&2; exit 1
    fi
done
if bash "$repo_root/scripts/fetch-obi.sh" 0.14.0-ongrid.1 riscv64 "$work/invalid" > "$work/rejected.log" 2>&1; then
    echo 'fetch-obi accepted unsupported architecture' >&2; exit 1
fi
[[ "$(wc -l < "$MOCK_REQUESTS")" == "$before" ]]
version=$(sed -n 's/^OBI_VERSION ?= //p' "$repo_root/Makefile")
grep -Fxq "ARG OBI_VERSION=$version" "$repo_root/deploy/Dockerfile.ongrid-edge"
python3 - "$repo_root/patches/obi/manifest.json" "$version" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    assert json.load(f)['release_version'] == sys.argv[2]
PY
tag=$(make -s -C "$repo_root" edge-deps-tag)
grep -Fxq "ARG EDGE_DEPS_TAG=$tag" "$repo_root/deploy/Dockerfile.ongrid-edge"
printf 'PASS upstream/CNB bundle routing, both architectures, notices, outer and inner checksums, metadata and version consistency\n'
