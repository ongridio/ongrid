#!/usr/bin/env bash
# Build the official contrib distribution with the pinned SkyWalking bind fix.
set -euo pipefail
version=${1:?version required}
target=${2:?os-arch required}
dest=${3:?output binary required}
[[ "$version" == 0.157.0-ongrid.1 ]] || { echo "unsupported patched Collector version: $version" >&2; exit 1; }
case "$target" in linux-amd64|linux-arm64|darwin-amd64|darwin-arm64) ;; *) echo "unsupported Collector target: $target" >&2; exit 1 ;; esac
go_bin=$(go env GOROOT)/bin/go
root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$(dirname "$dest")"
dest=$(cd "$(dirname "$dest")" && pwd)/$(basename "$dest")
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'; }
revision="$version $(sha256 "$root/dist/patches/skywalking-grpc-bind.patch")"
if [[ -s "$dest" && -f "$dest.version" && "$(cat "$dest.version")" == "$revision $(sha256 "$dest")" ]]; then
    echo "[otelcol] verified cached $version: $dest"
    exit 0
fi
work=$(mktemp -d /tmp/ongrid-otelcol.XXXXXX)
trap 'rm -rf "$work"' EXIT
curl -fsSL --retry 3 --connect-timeout 15 https://codeload.github.com/open-telemetry/opentelemetry-collector-releases/tar.gz/refs/tags/v0.157.0 -o "$work/releases.tgz"
[[ "$(sha256 "$work/releases.tgz")" == b8058145292617c87b5acf7c316a801790a58899dfb88114ea71395795706c09 ]] || { echo 'Collector source checksum mismatch' >&2; exit 1; }
tar -xzf "$work/releases.tgz" --strip-components=1 -C "$work"
# Go verifies the receiver module through go.sum / the module checksum database.
module=github.com/open-telemetry/opentelemetry-collector-contrib/receiver/skywalkingreceiver
"$go_bin" mod download "$module@v0.157.0"
cp -R "$("$go_bin" env GOMODCACHE)/$module@v0.157.0" "$work/skywalkingreceiver"
chmod -R u+w "$work/skywalkingreceiver"
(cd "$work/skywalkingreceiver" && patch -p1 < "$root/dist/patches/skywalking-grpc-bind.patch")
bash "$work/scripts/prepare-obi.sh" otelcol-contrib
cd "$work/distributions/otelcol-contrib"
printf '\n  - "%s => %s"\n' "$module" "$work/skywalkingreceiver" >> manifest.yaml
sed "s/version: 0.157.0/version: $version/" manifest.yaml > patched.yaml
"$go_bin" run go.opentelemetry.io/collector/cmd/builder@v0.157.0 --config=patched.yaml --skip-compilation
cd _build
CGO_ENABLED=0 GOOS=${target%-*} GOARCH=${target##*-} "$go_bin" build -p "${ONGRID_COLLECTOR_BUILD_JOBS:-4}" -trimpath -tags grpcnotrace -ldflags='-s -w' -o "$work/otelcol-contrib" .
# Replace only a successful build; leave the previous binary intact on failure.
install -m 0755 "$work/otelcol-contrib" "$dest.new"
mv -f "$dest.new" "$dest"
printf '%s %s\n' "$revision" "$(sha256 "$dest")" > "$dest.version"
echo "[otelcol] built $version: $dest"
