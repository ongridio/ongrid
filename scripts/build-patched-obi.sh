#!/usr/bin/env bash
# Build both Linux OBI artifacts from the pinned upstream source and patch.
set -euo pipefail
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
manifest="$repo_root/patches/obi/manifest.json"
out=${1:?output directory (must be empty)}
mkdir -p "$out"
out=$(cd "$out" && pwd)
[[ -z "$(ls -A "$out")" ]] || { echo 'OBI output directory must be empty' >&2; exit 1; }
version=$(jq -er .release_version "$manifest")
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+-ongrid\.[1-9][0-9]*$ ]] || exit 1
upstream=$(jq -er .upstream_repository "$manifest")
tag=$(jq -er .upstream_tag "$manifest")
commit=$(jq -er .upstream_commit "$manifest")
patch="$repo_root/patches/obi/$(jq -er .patch "$manifest")"
patch_sha=$(jq -er .patch_sha256 "$manifest")
image=$(jq -er '.build_tools.generator_image | split(":")[0]' "$manifest")
image="$image@$(jq -er .build_tools.generator_image_digest "$manifest")"
printf '%s  %s\n' "$patch_sha" "$patch" | sha256sum -c -
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
git clone --quiet --depth 1 --branch "$tag" "$upstream" "$work/source"
[[ "$(git -C "$work/source" rev-parse HEAD)" == "$commit" ]] || { echo 'OBI upstream commit mismatch' >&2; exit 1; }
git -C "$work/source" apply --check "$patch"
git -C "$work/source" apply "$patch"
# The upstream tag includes the Java agent and its native libraries for both architectures.
printf '%s  %s\n' "$(jq -er .embedded_java_agent_sha256 "$manifest")" \
    "$work/source/pkg/internal/java/embedded/obi-java-agent.jar" | sha256sum -c -
cp "$manifest" "$out/manifest.json"
cp "$patch" "$out/"
docker run --rm \
    -v "$work/source:/src" -v "$out:/out" \
    -v ongrid-obi-go-mod:/go/pkg -v ongrid-obi-go-build:/root/.cache/go-build \
    -e "OBI_RELEASE_VERSION=$version" -e "OBI_PATCH_SHA=$patch_sha" -e "OBI_BUILD_OWNER=$(id -u):$(id -g)" \
    -w /src --entrypoint /bin/sh "$image" -ec '
    set -o pipefail
    # Return root-created build directories to the caller for Linux runner cleanup.
    trap '\''chown -R "$OBI_BUILD_OWNER" /src /out'\'' EXIT
    export PATH="/usr/lib/llvm22/bin:$PATH" BPF2GO=/go/bin/bpf2go
    make generate/all
    BPF_CLANG=clang BPF_CFLAGS="-O2 -g -Wall -Werror" \
        go generate ./pkg/internal/ebpf/gotracer
    for arch in amd64 arm64; do
        GOFLAGS="-buildvcs=false -trimpath" make compile GOOS=linux GOARCH="$arch" \
            RELEASE_VERSION="$OBI_RELEASE_VERSION" RELEASE_REVISION="ongrid-${OBI_PATCH_SHA}"
        stage=$(mktemp -d)
        cp bin/obi LICENSE NOTICE "$stage/"
        mkdir "$stage/NOTICES"
        cp -R NOTICES/bpf NOTICES/java "$stage/NOTICES/"
        cp -R "NOTICES/$arch/." "$stage/NOTICES/"
        printf "\nOngrid modified build %s; source patch SHA256 %s.\nSource: https://github.com/ongridio/ongrid/tree/obi-v%s/patches/obi\n" \
            "$OBI_RELEASE_VERSION" "$OBI_PATCH_SHA" "$OBI_RELEASE_VERSION" >> "$stage/NOTICE"
        chmod 0755 "$stage/obi"
        tar -C "$stage" -cf - obi LICENSE NOTICE NOTICES | gzip -n > "/out/obi-v$OBI_RELEASE_VERSION-linux-$arch.tar.gz"
        rm -rf "$stage"
    done
    cp /out/manifest.json ONGRID-PATCH.json
    tar --exclude=./.git --exclude=./bin -cf - . | gzip -n > "/out/obi-v$OBI_RELEASE_VERSION-source-generated.tar.gz"
    cd /out
    sha256sum *.tar.gz *.patch manifest.json > SHA256SUMS
    sha256sum -c SHA256SUMS
    '
