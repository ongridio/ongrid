#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

fail() { printf 'edge bundle test failed: %s\n' "$*" >&2; exit 1; }

# Exercise both entry points without writing fixtures into the checkout's bin/.
fixture_repo="$tmp_dir/repo"
mkdir -p "$fixture_repo/dist" "$fixture_repo/deploy/install"
cp "$repo_root/dist/build-edge-bundle.sh" "$fixture_repo/dist/"
cp "$repo_root/deploy/install/apply-pending-upgrade.sh" "$fixture_repo/deploy/install/"
components=(ongrid-edge node_exporter process_exporter mysqld_exporter postgres_exporter
    redis_exporter mongodb_exporter otelcol-contrib obi obi.NOTICES)

build_bundle() {
    if [[ "$builder" == host ]]; then
        bash "$repo_root/deploy/install/edge/build-edge-bundle.sh" "$assets" "$1" "$arch"
    else
        bash "$fixture_repo/dist/build-edge-bundle.sh" "$1" "$arch" "$assets"
    fi
}

for arch in linux-amd64 linux-arm64; do
    for builder in host dist; do
        assets="$tmp_dir/$arch-$builder"
        mkdir -p "$assets" "$fixture_repo/bin/$arch"
        for component in "${components[@]}"; do
            if [[ "$builder" == host ]]; then
                destination="$assets/$component-$arch"
            else
                destination="$fixture_repo/bin/$arch/$component"
            fi
            printf '#!/bin/sh\nprintf "%%s\\n" "%s %s"\n' "$component" "$arch" > "$destination"
        done
        cp "$repo_root/deploy/install/apply-pending-upgrade.sh" "$assets/"
        build_bundle vtest > "$tmp_dir/build.log"
        bundle="$assets/edge-bundle-$arch-vtest.tar.gz"
        [[ "$(sha256sum "$bundle" | awk '{print $1}')" == "$(cat "$bundle.sha256")" ]] \
            || fail "$builder/$arch bundle checksum mismatch"
        extracted="$assets/extracted"
        mkdir -p "$extracted"
        tar -xzf "$bundle" -C "$extracted"

        # Check the complete payload and manifest, including the newly added
        # collector and its non-executable license notices.
        for component in "${components[@]}" apply-pending-upgrade.sh; do
            [[ -s "$extracted/$component" ]] || fail "$builder/$arch omitted $component"
            mode=0755
            root=/usr/local/lib/ongrid-edge
            [[ "$component" != ongrid-edge ]] || root=/usr/local/bin
            [[ "$component" != obi.NOTICES ]] || mode=0644
            sha=$(sha256sum "$extracted/$component" | awk '{print $1}')
            grep -Eq "^$sha[[:space:]]+$mode[[:space:]]+$component[[:space:]]+$root/$component$" \
                "$extracted/MANIFEST.txt" || fail "$builder/$arch invalid manifest for $component"
        done
        [[ ! -x "$extracted/obi.NOTICES" ]] || fail "$builder/$arch made notices executable"

        # Apply the generated bundle to a clean root and to an older Edge that
        # has no OBI. Use the production hook, redirecting only managed paths.
        for scenario in fresh upgrade; do
            target="$assets/$scenario"
            mkdir -p "$target/stage/incoming" "$target/bin" "$target/lib"
            cp -R "$extracted/." "$target/stage/incoming/"
            sed -e "s|/usr/local/bin/|$target/bin/|g" \
                -e "s|/usr/local/lib/ongrid-edge/|$target/lib/|g" \
                "$extracted/MANIFEST.txt" > "$target/stage/incoming/MANIFEST.txt"
            [[ "$scenario" != upgrade ]] || printf 'old-agent\n' > "$target/bin/ongrid-edge"
            run_hook() {
                ONGRID_EDGE_UPGRADE_STAGE_DIR="$target/stage" \
                ONGRID_EDGE_UPGRADE_BIN_DIR="$target/bin" \
                ONGRID_EDGE_UPGRADE_LIB_DIR="$target/lib" \
                    bash "$repo_root/deploy/install/apply-pending-upgrade.sh"
            }
            run_hook
            [[ "$("$target/lib/obi" --version)" == "obi $arch" ]] \
                || fail "$builder/$arch/$scenario did not install runnable OBI"
            [[ ! -x "$target/lib/obi.NOTICES" ]] || fail "installed notices are executable"
            cmp "$extracted/obi.NOTICES" "$target/lib/obi.NOTICES"
            printf 'vtest\n' > "$target/stage/healthy_marker"
            run_hook
            [[ -x "$target/lib/obi" ]] || fail "healthy restart removed OBI"
        done

        # An absent, empty or symlinked required asset must reject the build,
        # rather than publishing a checksum-valid but incomplete archive.
        for component in obi obi.NOTICES; do
            source_file="$assets/$component-$arch"
            [[ "$builder" != dist ]] || source_file="$fixture_repo/bin/$arch/$component"
            cp "$source_file" "$tmp_dir/saved-asset"
            for invalid in missing empty symlink; do
                rm -f "$source_file"
                case "$invalid" in
                    empty) : > "$source_file" ;;
                    symlink) ln -s "$tmp_dir/saved-asset" "$source_file" ;;
                esac
                version="vbad-$component-$invalid"
                if build_bundle "$version" > "$tmp_dir/rejected.log" 2>&1; then
                    fail "$builder/$arch accepted $invalid $component"
                fi
                [[ ! -e "$assets/edge-bundle-$arch-$version.tar.gz" ]] \
                    || fail "$builder/$arch published an incomplete bundle"
            done
            rm -f "$source_file"
            cp "$tmp_dir/saved-asset" "$source_file"
        done
        printf 'PASS %s %s: complete bundle, apply, restart and missing-asset rejection\n' "$builder" "$arch"
    done
done

# A failure on the first architecture must reach Make even when the second
# architecture has all its inputs; otherwise the loop masks the failed build.
rm "$fixture_repo/bin/linux-amd64/obi"
if make --no-print-directory -C "$fixture_repo" -f "$repo_root/Makefile" \
    build-edge-bundle VERSION=vfailed OUT="$tmp_dir/make-out" \
    EDGE_PLUGIN_ARCHES='linux-amd64 linux-arm64' > "$tmp_dir/make.log" 2>&1; then
    fail 'Make hid an incomplete first architecture behind a successful second build'
fi
grep -Fq 'missing regular non-empty file' "$tmp_dir/make.log" \
    || fail 'Make failed before checking the incomplete bundle'
[[ ! -e "$tmp_dir/make-out/edge-bundles/edge-bundle-linux-arm64-vfailed.tar.gz" ]] \
    || fail 'Make continued after an incomplete architecture'
printf 'PASS Make propagates bundle failures\n'
