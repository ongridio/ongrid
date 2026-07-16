#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

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

stage="$tmp_dir/stage/ongrid-vtest-linux-amd64"
out="$tmp_dir/out"
PACKAGE_TARGET=linux-amd64 \
EDGE_TARGETS=linux-test \
ONGRID_BUNDLE_EMBEDDING_MODEL=0 \
  bash "$repo_root/dist/package.sh" vtest "$stage" "$out" \
    >"$tmp_dir/package.log" 2>&1 || {
      cat "$tmp_dir/package.log" >&2
      exit 1
    }

archive="$out/ongrid-vtest-linux-amd64.tar.xz"
test -s "$archive"
tar -tf "$archive" >"$tmp_dir/archive.list"

for required in \
  ongrid-vtest-linux-amd64/install.sh \
  ongrid-vtest-linux-amd64/uninstall.sh \
  ongrid-vtest-linux-amd64/docker-compose.yml \
  ongrid-vtest-linux-amd64/prometheus.yml; do
  grep -Fxq "$required" "$tmp_dir/archive.list"
done

for forbidden in \
  ongrid-vtest-linux-amd64/bin/ \
  ongrid-vtest-linux-amd64/systemd/ \
  ongrid-vtest-linux-amd64/prometheus/prometheus.yml; do
  if grep -Fq "$forbidden" "$tmp_dir/archive.list"; then
    echo "release package contains removed path: $forbidden" >&2
    exit 1
  fi
done
