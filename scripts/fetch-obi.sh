#!/usr/bin/env bash
# Fetch a pinned, checksum-verified OBI release. Called by Make and Docker.
set -euo pipefail
version=${1:?version}
arch=${2:?amd64 or arm64}
dest=${3:?destination directory}
case "$arch" in amd64|arm64) ;; *) echo 'unsupported OBI architecture' >&2; exit 1 ;; esac
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'invalid OBI version' >&2; exit 1; }
mkdir -p "$dest"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
asset="obi-v${version}-linux-${arch}.tar.gz"
base="https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation/releases/download/v${version}"
curl -fL --retry 3 --connect-timeout 15 -o "$work/$asset" "$base/$asset"
curl -fL --retry 3 --connect-timeout 15 -o "$work/SHA256SUMS" "$base/SHA256SUMS"
expected=$(awk -v asset="$asset" '$2 == asset {print $1}' "$work/SHA256SUMS")
[[ "$expected" =~ ^[a-fA-F0-9]{64}$ ]] || { echo 'OBI checksum missing' >&2; exit 1; }
printf '%s  %s\n' "$expected" "$work/$asset" | sha256sum -c -
tar -xzf "$work/$asset" -C "$work" obi LICENSE NOTICE NOTICES
install -m 0755 "$work/obi" "$dest/obi"
# Keep the upstream license and all bundled dependency notices with the binary.
(
    cd "$work"
    find LICENSE NOTICE NOTICES -type f -print0 | sort -z | while IFS= read -r -d '' notice; do
        printf '\n===== %s =====\n' "$notice"
        cat "$notice"
    done
) > "$dest/obi.NOTICES"
chmod 0644 "$dest/obi.NOTICES"
