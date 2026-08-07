#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

for installer in install.sh upgrade.sh; do
  grep -Fq 'chmod 0755 "$EDGE_STAGE_DIR"' "$repo_root/deploy/install/$installer" || {
    echo "$installer does not make the staged Edge directory container-readable" >&2
    exit 1
  }
done

install_help=$(bash "$repo_root/deploy/install/install.sh" --help)
uninstall_help=$(bash "$repo_root/deploy/install/uninstall.sh" --help)

if grep -Eqi -- 'systemd|--mode|--with-deps' <<<"$install_help"; then
  echo "install help still advertises removed Manager systemd support" >&2
  exit 1
fi
if grep -Eqi -- 'systemd|--mode' <<<"$uninstall_help"; then
  echo "uninstall help still advertises removed Manager systemd support" >&2
  exit 1
fi
if bash "$repo_root/deploy/install/install.sh" --mode=systemd >"$tmp_dir/install-mode.log" 2>&1; then
  echo "install unexpectedly accepted --mode=systemd" >&2
  exit 1
fi
if bash "$repo_root/deploy/install/uninstall.sh" --mode=systemd >"$tmp_dir/uninstall-mode.log" 2>&1; then
  echo "uninstall unexpectedly accepted --mode=systemd" >&2
  exit 1
fi
if [[ -d "$repo_root/deploy/install/systemd" ]] \
    && find "$repo_root/deploy/install/systemd" -type f -print -quit | grep -q .; then
  echo "Manager systemd install files still exist" >&2
  exit 1
fi

edge_bin_root="$tmp_dir/edge-bin"
mkdir -p "$edge_bin_root/linux-amd64"
for binary in \
  ongrid-edge promtail otelcol-contrib node_exporter process_exporter \
  mysqld_exporter postgres_exporter redis_exporter mongodb_exporter; do
  printf '%s test payload\n' "$binary" >"$edge_bin_root/linux-amd64/$binary"
done

for arch in amd64 arm64; do
  package_target="linux-$arch"
  package_root="ongrid-vtest-$package_target"
  stage="$tmp_dir/stage/$package_root"
  out="$tmp_dir/out-$arch"
  package_log="$tmp_dir/package-$arch.log"

  PACKAGE_TARGET="$package_target" \
  EDGE_TARGETS='linux-amd64 linux-arm64' \
  EDGE_BIN_ROOT="$edge_bin_root" \
  ONGRID_EDGE_DEPS_TAG=edge-deps-test \
  ONGRID_BUNDLE_EMBEDDING_MODEL=0 \
    bash "$repo_root/dist/package.sh" vtest "$stage" "$out" \
      >"$package_log" 2>&1 || {
        cat "$package_log" >&2
        exit 1
      }

  archive="$out/$package_root.tar.xz"
  test -s "$archive"
  tar -tf "$archive" >"$tmp_dir/archive-$arch.list"

  for required in \
    "$package_root/install.sh" \
    "$package_root/uninstall.sh" \
    "$package_root/upgrade.sh" \
    "$package_root/public-url.sh" \
    "$package_root/data-permissions.sh" \
    "$package_root/docker-compose.yml" \
    "$package_root/prometheus.yml" \
    "$package_root/edge/fetch-edge-assets.sh" \
    "$package_root/edge/verify-edge-deps-archive.sh" \
    "$package_root/edge/edge-assets-lib.sh" \
    "$package_root/edge/build-edge-bundle.sh" \
    "$package_root/edge/edge-artifacts.env"; do
    grep -Fxq "$required" "$tmp_dir/archive-$arch.list"
  done

  extract_dir="$tmp_dir/extracted-$arch"
  mkdir -p "$extract_dir"
  tar -xf "$archive" -C "$extract_dir"
  grep -Fqx 'vtest' "$extract_dir/$package_root/VERSION"
  grep -Fqx 'ONGRID_VERSION=vtest' "$extract_dir/$package_root/.env.example"
  grep -Fxq 'ONGRID_EDGE_DEPS_TAG=edge-deps-test' \
    "$extract_dir/$package_root/edge/edge-artifacts.env"
  grep -Fxq 'ONGRID_EDGE_TARGETS=linux-amd64 linux-arm64' \
    "$extract_dir/$package_root/edge/edge-artifacts.env"
  bash "$extract_dir/$package_root/install.sh" --help >/dev/null
  bash "$extract_dir/$package_root/upgrade.sh" --help >/dev/null
  grep -Fq 'EDGE_SWAP_COMPLETE=1' "$extract_dir/$package_root/install.sh"
  grep -Fq 'restored the previous Edge directory after install failure' \
    "$extract_dir/$package_root/install.sh"
  grep -Fq -- '--no-deps --force-recreate ongrid nginx' \
    "$extract_dir/$package_root/install.sh"

  if find "$extract_dir/$package_root/edge" -maxdepth 1 -type f \
      -name '*-linux-*' -print -quit | grep -q .; then
    echo "$package_target thin package unexpectedly embeds Edge binaries" >&2
    exit 1
  fi
done

# Opting into an offline package is a completeness promise. Missing binaries
# must fail the package build instead of producing an archive that cannot be
# installed.
mkdir -p "$tmp_dir/empty-edge-bin/linux-amd64"
if PACKAGE_TARGET=linux-amd64 \
  EDGE_TARGETS=linux-amd64 \
  EDGE_BIN_ROOT="$tmp_dir/empty-edge-bin" \
  ONGRID_BUNDLE_EDGE_ASSETS=1 \
  ONGRID_BUNDLE_EMBEDDING_MODEL=0 \
    bash "$repo_root/dist/package.sh" vtest \
      "$tmp_dir/offline-stage/ongrid-vtest-linux-amd64" "$tmp_dir/offline-out" \
      >"$tmp_dir/offline-package.log" 2>&1; then
  echo "offline package unexpectedly accepted an empty Edge binary root" >&2
  exit 1
fi

offline_stage="$tmp_dir/offline-complete-stage/ongrid-vtest-linux-amd64"
offline_out="$tmp_dir/offline-complete-out"
PACKAGE_TARGET=linux-amd64 \
EDGE_TARGETS=linux-amd64 \
EDGE_BIN_ROOT="$edge_bin_root" \
ONGRID_BUNDLE_EDGE_ASSETS=1 \
ONGRID_BUNDLE_EMBEDDING_MODEL=0 \
  bash "$repo_root/dist/package.sh" vtest "$offline_stage" "$offline_out" \
    >"$tmp_dir/offline-complete.log" 2>&1 || {
      cat "$tmp_dir/offline-complete.log" >&2
      exit 1
    }
offline_archive="$offline_out/ongrid-vtest-linux-amd64.tar.xz"
tar -tf "$offline_archive" > "$tmp_dir/offline-archive.list"
mkdir -p "$tmp_dir/offline-extracted"
tar -xf "$offline_archive" -C "$tmp_dir/offline-extracted"
grep -Fxq 'ONGRID_EDGE_TARGETS=linux-amd64' \
  "$tmp_dir/offline-extracted/ongrid-vtest-linux-amd64/edge/edge-artifacts.env"
for binary in \
  ongrid-edge promtail otelcol-contrib node_exporter process_exporter \
  mysqld_exporter postgres_exporter redis_exporter mongodb_exporter; do
  grep -Fxq "ongrid-vtest-linux-amd64/edge/${binary}-linux-amd64" \
    "$tmp_dir/offline-archive.list" \
    || { echo "complete offline package omitted $binary" >&2; exit 1; }
done
