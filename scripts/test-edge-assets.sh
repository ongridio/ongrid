#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
build_script="$repo_root/dist/build-edge-attachments.sh"
fetch_script="$repo_root/deploy/install/edge/fetch-edge-assets.sh"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

fail() {
    printf 'edge assets test failed: %s\n' "$*" >&2
    exit 1
}

components=(
    node_exporter process_exporter mysqld_exporter postgres_exporter
    redis_exporter mongodb_exporter otelcol-contrib
)
deps_tag=edge-deps-layout2-o2-n3-pr4-my5-pg6-r7-m8
next_deps_tag=edge-deps-layout2-o12-n13-pr14-my15-pg16-r17-m18
bin_root="$tmp_dir/bin"
mkdir -p "$bin_root/linux-amd64"
for component in "${components[@]}"; do
    printf '%s payload\n' "$component" > "$bin_root/linux-amd64/$component"
done
printf 'ongrid-edge payload\n' > "$bin_root/linux-amd64/ongrid-edge"

attachments="$tmp_dir/attachments"
EDGE_BIN_ROOT="$bin_root" \
OTELCOL_VERSION=2 NODE_EXPORTER_VERSION=3 \
PROCESS_EXPORTER_VERSION=4 MYSQLD_EXPORTER_VERSION=5 \
POSTGRES_EXPORTER_VERSION=6 REDIS_EXPORTER_VERSION=7 \
MONGODB_EXPORTER_VERSION=8 \
    bash "$build_script" deps "$deps_tag" "$attachments" linux-amd64
EDGE_BIN_ROOT="$bin_root" \
    bash "$build_script" edge vtest "$attachments" linux-amd64

# Immutable dependency archives must be byte-reproducible. Otherwise a rerun
# rebuilds a different sidecar and cannot reuse an already complete Release.
rebuilt_attachments="$tmp_dir/attachments-rebuilt"
sleep 2
EDGE_BIN_ROOT="$bin_root" \
OTELCOL_VERSION=2 NODE_EXPORTER_VERSION=3 \
PROCESS_EXPORTER_VERSION=4 MYSQLD_EXPORTER_VERSION=5 \
POSTGRES_EXPORTER_VERSION=6 REDIS_EXPORTER_VERSION=7 \
MONGODB_EXPORTER_VERSION=8 \
    bash "$build_script" deps "$deps_tag" "$rebuilt_attachments" linux-amd64
cmp -s \
    "$attachments/edge-deps-linux-amd64.tar.xz" \
    "$rebuilt_attachments/edge-deps-linux-amd64.tar.xz" \
    || fail "identical dependency inputs produced different immutable archives"

(cd "$attachments" && sha256sum -c edge-deps-linux-amd64.tar.xz.sha256)
(cd "$attachments" && sha256sum -c ongrid-edge-linux-amd64-vtest.sha256)
bash "$repo_root/deploy/install/edge/verify-edge-deps-archive.sh" \
    "$attachments/edge-deps-linux-amd64.tar.xz" linux-amd64 "$deps_tag" >/dev/null

# An outer checksum alone must not turn arbitrary bytes into a valid shared
# dependency attachment.
invalid_root="$tmp_dir/invalid-releases"
mkdir -p "$invalid_root/$deps_tag"
printf 'not an xz archive\n' > "$invalid_root/$deps_tag/edge-deps-linux-amd64.tar.xz"
(cd "$invalid_root/$deps_tag" && sha256sum edge-deps-linux-amd64.tar.xz \
    > edge-deps-linux-amd64.tar.xz.sha256)
if make -s -C "$repo_root" verify-edge-deps-release \
    EDGE_ATTACHMENT_TARGETS=linux-amd64 EDGE_DEPS_TAG="$deps_tag" \
    CNB_RELEASE_BASE_URL="file://$invalid_root" >/dev/null 2>&1; then
    fail "arbitrary bytes with a matching outer sidecar passed dependency verification"
fi

fixture_root="$tmp_dir/releases"
mkdir -p "$fixture_root/$deps_tag" "$fixture_root/vtest"
cp "$attachments/edge-deps-linux-amd64.tar.xz"* "$fixture_root/$deps_tag/"
cp "$attachments/ongrid-edge-linux-amd64-vtest"* "$fixture_root/vtest/"

fake_bin="$tmp_dir/fake-bin"
mkdir -p "$fake_bin"
cat > "$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
out=""
url=""
probe=0
while (( $# > 0 )); do
    case "$1" in
        -o) out=$2; shift 2 ;;
        --version|--help) probe=1; shift ;;
        http://*|https://*) url=$1; shift ;;
        *) shift ;;
    esac
done
# Capability probes do not touch the network, so they must not land in the log the
# no-network assertions check. Model real curl: --version succeeds and prints.
if (( probe )); then
    printf 'curl 8.0.0 (fake)\n'
    exit 0
fi
# Explicit exit: `set -e` does not abort on a failing `[[ a && b ]]`, so a bare
# test here would fall through and log an empty line for non-download calls.
if [[ -z "$out" || -z "$url" ]]; then
    printf 'fake curl: expected -o <file> and a URL\n' >&2
    exit 2
fi
printf '%s\n' "$url" >> "$FAKE_CURL_LOG"
relative=${url#*releases/download/}
cp "$FAKE_RELEASE_ROOT/$relative" "$out"
EOF
chmod 0755 "$fake_bin/curl"
cat > "$fake_bin/uname" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' x86_64
EOF
chmod 0755 "$fake_bin/uname"

dest="$tmp_dir/dest"
cache="$tmp_dir/cache"
FAKE_CURL_LOG="$tmp_dir/curl.log" \
FAKE_RELEASE_ROOT="$fixture_root" \
PATH="$fake_bin:$PATH" \
ONGRID_EDGE_DEPS_TAG="$deps_tag" \
ONGRID_EDGE_ARTIFACT_CACHE_DIR="$cache" \
ONGRID_EDGE_ARTIFACT_BASE_URL=https://cnb.test/repo/-/releases/download \
    bash "$fetch_script" "$dest" vtest linux-amd64

grep -Fqx "https://cnb.test/repo/-/releases/download/$deps_tag/edge-deps-linux-amd64.tar.xz" "$tmp_dir/curl.log" \
    || fail "public dependency archive was not downloaded from its immutable release"
grep -Fqx 'https://cnb.test/repo/-/releases/download/vtest/ongrid-edge-linux-amd64-vtest' "$tmp_dir/curl.log" \
    || fail "versioned ongrid-edge binary was not downloaded directly"
for component in "${components[@]}"; do
    [[ -x "$dest/${component}-linux-amd64" ]] || fail "missing staged $component"
done
[[ -x "$dest/ongrid-edge-linux-amd64" ]] || fail "missing staged ongrid-edge"
grep -Fq "$deps_tag/edge-deps-linux-amd64.tar.xz" "$dest/edge-assets-linux-amd64.ref" \
    || fail "dependency source was not recorded"
grep -Fq 'vtest/ongrid-edge-linux-amd64-vtest' "$dest/edge-assets-linux-amd64.ref" \
    || fail "edge source was not recorded"

# A thin universal package carries no target. The downloader must select the
# current host architecture before resolving the CNB attachment names.
auto_dest="$tmp_dir/auto-dest"
FAKE_CURL_LOG="$tmp_dir/auto-curl.log" \
FAKE_RELEASE_ROOT="$fixture_root" \
PATH="$fake_bin:$PATH" \
ONGRID_EDGE_DEPS_TAG="$deps_tag" \
ONGRID_EDGE_ARTIFACT_CACHE_DIR="$tmp_dir/auto-cache" \
ONGRID_EDGE_ARTIFACT_BASE_URL=https://cnb.test/repo/-/releases/download \
    bash "$fetch_script" "$auto_dest" vtest
[[ -x "$auto_dest/ongrid-edge-linux-amd64" ]] \
    || fail "host architecture auto-detection did not stage the matching Edge binary"
grep -Fqx 'https://cnb.test/repo/-/releases/download/vtest/ongrid-edge-linux-amd64-vtest' \
    "$tmp_dir/auto-curl.log" \
    || fail "host architecture auto-detection requested the wrong Edge attachment"

# Dependency archives keep stable filenames across release tags. Reusing one
# cache directory after a dependency version bump must fetch and stage the new
# tag instead of silently accepting the old archive and recording false source
# provenance.
for component in "${components[@]}"; do
    printf '%s next payload\n' "$component" > "$bin_root/linux-amd64/$component"
done
next_attachments="$tmp_dir/attachments-next"
EDGE_BIN_ROOT="$bin_root" \
OTELCOL_VERSION=12 NODE_EXPORTER_VERSION=13 \
PROCESS_EXPORTER_VERSION=14 MYSQLD_EXPORTER_VERSION=15 \
POSTGRES_EXPORTER_VERSION=16 REDIS_EXPORTER_VERSION=17 \
MONGODB_EXPORTER_VERSION=18 \
    bash "$build_script" deps "$next_deps_tag" "$next_attachments" linux-amd64
mkdir -p "$fixture_root/$next_deps_tag"
cp "$next_attachments/edge-deps-linux-amd64.tar.xz"* "$fixture_root/$next_deps_tag/"
: > "$tmp_dir/next-curl.log"
FAKE_CURL_LOG="$tmp_dir/next-curl.log" \
FAKE_RELEASE_ROOT="$fixture_root" \
PATH="$fake_bin:$PATH" \
ONGRID_EDGE_DEPS_TAG="$next_deps_tag" \
ONGRID_EDGE_ARTIFACT_CACHE_DIR="$cache" \
ONGRID_EDGE_ARTIFACT_BASE_URL=https://cnb.test/repo/-/releases/download \
    bash "$fetch_script" "$tmp_dir/next-dest" vtest linux-amd64
grep -Fqx "https://cnb.test/repo/-/releases/download/$next_deps_tag/edge-deps-linux-amd64.tar.xz" "$tmp_dir/next-curl.log" \
    || fail "a new dependency tag reused the previous tag's cached archive"
if grep -Fq '/vtest/ongrid-edge-linux-amd64-vtest' "$tmp_dir/next-curl.log"; then
    fail "an unchanged versioned Edge binary was not reused from its tag-scoped cache"
fi
for component in "${components[@]}"; do
    cmp -s "$bin_root/linux-amd64/$component" "$tmp_dir/next-dest/${component}-linux-amd64" \
        || fail "dependency tag change did not stage the new $component payload"
done
grep -Fq "$next_deps_tag/edge-deps-linux-amd64.tar.xz" "$tmp_dir/next-dest/edge-assets-linux-amd64.ref" \
    || fail "dependency tag change recorded the wrong source"

# A valid local cache must make a repeated installation independent of CNB.
rm -rf "$fixture_root"
: > "$tmp_dir/cache-curl.log"
FAKE_CURL_LOG="$tmp_dir/cache-curl.log" \
FAKE_RELEASE_ROOT="$fixture_root" \
PATH="$fake_bin:$PATH" \
ONGRID_EDGE_DEPS_TAG="$deps_tag" \
ONGRID_EDGE_ARTIFACT_CACHE_DIR="$cache" \
ONGRID_EDGE_ARTIFACT_BASE_URL=https://cnb.test/repo/-/releases/download \
    bash "$fetch_script" "$tmp_dir/cache-dest" vtest linux-amd64
[[ ! -s "$tmp_dir/cache-curl.log" ]] || fail "verified cache unexpectedly hit the network"

# The archive's checksum-protected dependency metadata must agree with the
# release tag requested by the installer. A valid archive copied under a
# different tag is not an interchangeable dependency release.
mkdir -p "$fixture_root/edge-deps-forged" "$fixture_root/vtest"
cp "$next_attachments/edge-deps-linux-amd64.tar.xz"* "$fixture_root/edge-deps-forged/"
cp "$attachments/ongrid-edge-linux-amd64-vtest"* "$fixture_root/vtest/"
if FAKE_CURL_LOG="$tmp_dir/forged-tag-curl.log" \
    FAKE_RELEASE_ROOT="$fixture_root" \
    PATH="$fake_bin:$PATH" \
    ONGRID_EDGE_DEPS_TAG=edge-deps-forged \
    ONGRID_EDGE_ARTIFACT_CACHE_DIR="$tmp_dir/forged-tag-cache" \
    ONGRID_EDGE_ARTIFACT_BASE_URL=https://cnb.test/repo/-/releases/download \
    bash "$fetch_script" "$tmp_dir/forged-tag-dest" vtest linux-amd64 \
        >"$tmp_dir/forged-tag.out" 2>"$tmp_dir/forged-tag.log"; then
    fail "dependency archive metadata from another release tag was accepted"
fi
grep -Fq 'dependency archive release tag does not match edge-deps-forged' "$tmp_dir/forged-tag.log" \
    || fail "dependency tag mismatch did not produce an actionable error"

# Required archive entries must be rejected as non-regular before manifest
# hashing. This guard wrapper records if sha256sum is ever asked to follow the
# injected symlink; the installer must fail without touching it.
real_sha256sum=$(command -v sha256sum)
nonregular_stage="$tmp_dir/nonregular-stage"
nonregular_root="$tmp_dir/nonregular-releases"
nonregular_release="$nonregular_root/$deps_tag"
mkdir -p "$nonregular_stage" "$nonregular_release" "$nonregular_root/vtest"
tar -xJf "$attachments/edge-deps-linux-amd64.tar.xz" -C "$nonregular_stage"
rm -f "$nonregular_stage/node_exporter"
ln -s process_exporter "$nonregular_stage/node_exporter"
(cd "$nonregular_stage" && "$real_sha256sum" TARGET DEPENDENCIES "${components[@]}" > MANIFEST.sha256)
tar -cJf "$nonregular_release/edge-deps-linux-amd64.tar.xz" \
    -C "$nonregular_stage" TARGET DEPENDENCIES MANIFEST.sha256 "${components[@]}"
(cd "$nonregular_release" && "$real_sha256sum" edge-deps-linux-amd64.tar.xz > edge-deps-linux-amd64.tar.xz.sha256)
cp "$attachments/ongrid-edge-linux-amd64-vtest"* "$nonregular_root/vtest/"

guard_bin="$tmp_dir/guard-bin"
mkdir -p "$guard_bin"
cp "$fake_bin/curl" "$guard_bin/curl"
cat > "$guard_bin/sha256sum" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
for path in "$@"; do
    if [[ -L "$path" ]]; then
        printf '%s\n' "$path" >> "$FAKE_SHA_SYMLINK_LOG"
        exit 97
    fi
done
exec "$REAL_SHA256SUM" "$@"
EOF
chmod 0755 "$guard_bin/curl" "$guard_bin/sha256sum"
: > "$tmp_dir/sha-symlink.log"
if FAKE_CURL_LOG="$tmp_dir/nonregular-curl.log" \
    FAKE_RELEASE_ROOT="$nonregular_root" \
    FAKE_SHA_SYMLINK_LOG="$tmp_dir/sha-symlink.log" \
    REAL_SHA256SUM="$real_sha256sum" \
    PATH="$guard_bin:$PATH" \
    ONGRID_EDGE_DEPS_TAG="$deps_tag" \
    ONGRID_EDGE_ARTIFACT_CACHE_DIR="$tmp_dir/nonregular-cache" \
    ONGRID_EDGE_ARTIFACT_BASE_URL=https://cnb.test/repo/-/releases/download \
    bash "$fetch_script" "$tmp_dir/nonregular-dest" vtest linux-amd64 \
        >"$tmp_dir/nonregular.out" 2>"$tmp_dir/nonregular.log"; then
    fail "dependency archive with a symlink component was accepted"
fi
[[ ! -s "$tmp_dir/sha-symlink.log" ]] \
    || fail "manifest verification hashed a non-regular archive entry before rejecting it"

# A mismatched direct binary and sidecar must fail before staging anything.
mkdir -p "$fixture_root/$deps_tag" "$fixture_root/vtest"
cp "$attachments/edge-deps-linux-amd64.tar.xz"* "$fixture_root/$deps_tag/"
cp "$attachments/ongrid-edge-linux-amd64-vtest"* "$fixture_root/vtest/"
printf 'tampered\n' >> "$fixture_root/vtest/ongrid-edge-linux-amd64-vtest"
if FAKE_CURL_LOG="$tmp_dir/bad-curl.log" \
    FAKE_RELEASE_ROOT="$fixture_root" \
    PATH="$fake_bin:$PATH" \
    ONGRID_EDGE_DEPS_TAG="$deps_tag" \
    ONGRID_EDGE_ARTIFACT_CACHE_DIR="$tmp_dir/bad-cache" \
    ONGRID_EDGE_ARTIFACT_BASE_URL=https://cnb.test/repo/-/releases/download \
    bash "$fetch_script" "$tmp_dir/bad-dest" vtest linux-amd64 >/dev/null 2>&1; then
    fail "tampered direct binary passed checksum verification"
fi

# The sidecar must describe the file currently being verified. Pointing the
# Edge sidecar at the already downloaded dependency archive must not allow a
# modified Edge binary to pass.
cp "$attachments/edge-deps-linux-amd64.tar.xz.sha256" \
    "$fixture_root/vtest/ongrid-edge-linux-amd64-vtest.sha256"
if FAKE_CURL_LOG="$tmp_dir/wrong-name-curl.log" \
    FAKE_RELEASE_ROOT="$fixture_root" \
    PATH="$fake_bin:$PATH" \
    ONGRID_EDGE_DEPS_TAG="$deps_tag" \
    ONGRID_EDGE_ARTIFACT_CACHE_DIR="$tmp_dir/wrong-name-cache" \
    ONGRID_EDGE_ARTIFACT_BASE_URL=https://cnb.test/repo/-/releases/download \
    bash "$fetch_script" "$tmp_dir/wrong-name-dest" vtest linux-amd64 >/dev/null 2>&1; then
    fail "checksum sidecar for another attachment verified a tampered Edge binary"
fi
[[ ! -e "$tmp_dir/wrong-name-dest/ongrid-edge-linux-amd64" ]] \
    || fail "tampered Edge binary was staged after sidecar filename mismatch"

printf 'edge attachment tests passed\n'
