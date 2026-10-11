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
        --retry|--connect-timeout) shift 2 ;;
        -fL) shift ;;
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
    [[ "$version" != *-ongrid.* ]] || export MOCK_BASE="https://github.com/ongridio/ongrid/releases/download/obi-v$version"
    for arch in amd64 arm64; do
        export MOCK_ASSETS="$work/$version-$arch"
        mkdir -p "$MOCK_ASSETS"
        printf '#!/bin/sh\nprintf "%%s\\n" "%s %s"\n' "$version" "$arch" > "$work/payload/obi"
        tar -C "$work/payload" -czf "$MOCK_ASSETS/obi-v$version-linux-$arch.tar.gz" obi LICENSE NOTICE NOTICES
        (cd "$MOCK_ASSETS" && sha256sum *.tar.gz > SHA256SUMS)
        dest="$work/installed-$version-$arch"
        bash "$repo_root/scripts/fetch-obi.sh" "$version" "$arch" "$dest" > "$work/fetch.log"
        [[ "$("$dest/obi" --version)" == "$version $arch" ]]
        [[ ! -x "$dest/obi.NOTICES" ]]
        grep -q 'upstream license' "$dest/obi.NOTICES"
        grep -q 'Ongrid notices' "$dest/obi.NOTICES"
        grep -q 'dependency notice' "$dest/obi.NOTICES"

        cp "$MOCK_ASSETS/SHA256SUMS" "$work/good-sums"
        for failure in mismatch missing download; do
            cp "$work/good-sums" "$MOCK_ASSETS/SHA256SUMS"
            export MOCK_DOWNLOAD_FAIL=0
            case "$failure" in
                mismatch) printf '%064d  obi-v%s-linux-%s.tar.gz\n' 0 "$version" "$arch" > "$MOCK_ASSETS/SHA256SUMS" ;;
                missing) : > "$MOCK_ASSETS/SHA256SUMS" ;;
                download) export MOCK_DOWNLOAD_FAIL=1 ;;
            esac
            printf 'previous binary\n' > "$dest/obi"
            if bash "$repo_root/scripts/fetch-obi.sh" "$version" "$arch" "$dest" > "$work/rejected.log" 2>&1; then
                echo "fetch-obi accepted $failure for $version/$arch" >&2; exit 1
            fi
            [[ "$(cat "$dest/obi")" == 'previous binary' ]]
        done
        export MOCK_DOWNLOAD_FAIL=0
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
printf 'PASS OBI upstream/patched routing, both architectures, notices, fail-closed downloads and version consistency\n'
