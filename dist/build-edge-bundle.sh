#!/usr/bin/env bash
# build-edge-bundle.sh — assemble the ADR-024 edge upgrade bundle.
#
# A bundle is `edge-bundle-<arch>-<version>.tar.gz` whose flat root
# carries every binary install-edge.sh would have placed + a
# MANIFEST.txt that the edge-side apply-pending-upgrade.sh consumes
# (per-file sha256 + dest path).
#
# Inputs (must already exist; this script does NOT cross-compile):
#   bin/<arch>/ongrid-edge
#   bin/<arch>/node_exporter
#   bin/<arch>/process_exporter
#   bin/<arch>/mysqld_exporter
#   bin/<arch>/postgres_exporter
#   bin/<arch>/redis_exporter
#   bin/<arch>/mongodb_exporter
#   bin/<arch>/promtail
#   bin/<arch>/otelcol-contrib
#   deploy/install/apply-pending-upgrade.sh
#
# Outputs:
#   $OUT/edge-bundle-<arch>-<version>.tar.gz
#   $OUT/edge-bundle-<arch>-<version>.tar.gz.sha256
#
# Usage: build-edge-bundle.sh <version> <arch> <out_dir>

set -euo pipefail

VERSION=${1:?usage: build-edge-bundle.sh <version> <arch> <out_dir>}
ARCH=${2:?arch}
OUT=${3:?out_dir}

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
BIN_DIR=$REPO_ROOT/bin/$ARCH
APPLY_SCRIPT=$REPO_ROOT/deploy/install/apply-pending-upgrade.sh

# (src_in_bundle, mode, dest, source_file_on_disk)
ENTRIES=(
  "ongrid-edge            0755 /usr/local/bin/ongrid-edge                            $BIN_DIR/ongrid-edge"
  "node_exporter          0755 /usr/local/lib/ongrid-edge/node_exporter              $BIN_DIR/node_exporter"
  "process_exporter       0755 /usr/local/lib/ongrid-edge/process_exporter           $BIN_DIR/process_exporter"
  "mysqld_exporter        0755 /usr/local/lib/ongrid-edge/mysqld_exporter            $BIN_DIR/mysqld_exporter"
  "postgres_exporter      0755 /usr/local/lib/ongrid-edge/postgres_exporter          $BIN_DIR/postgres_exporter"
  "redis_exporter         0755 /usr/local/lib/ongrid-edge/redis_exporter             $BIN_DIR/redis_exporter"
  "mongodb_exporter       0755 /usr/local/lib/ongrid-edge/mongodb_exporter           $BIN_DIR/mongodb_exporter"
  "promtail               0755 /usr/local/lib/ongrid-edge/promtail                   $BIN_DIR/promtail"
  "otelcol-contrib        0755 /usr/local/lib/ongrid-edge/otelcol-contrib            $BIN_DIR/otelcol-contrib"
  "apply-pending-upgrade.sh 0755 /usr/local/lib/ongrid-edge/apply-pending-upgrade.sh $APPLY_SCRIPT"
)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

mkdir -p "$OUT"
manifest=$work/MANIFEST.txt
{
  echo "# ADR-024 bundle manifest"
  echo "# fields: sha256  mode  src_in_bundle  dest_path"
} > "$manifest"

echo "$VERSION" > "$work/VERSION"

for entry in "${ENTRIES[@]}"; do
  # shellcheck disable=SC2086
  set -- $entry
  src_in_bundle=$1
  mode=$2
  dest=$3
  src_file=$4

  if [[ ! -f "$src_file" ]]; then
    echo "build-edge-bundle: missing $src_file — skipping (bundle will be incomplete)" >&2
    continue
  fi
  install -m 0755 "$src_file" "$work/$src_in_bundle"
  sha=$(sha256sum "$work/$src_in_bundle" | awk '{print $1}')
  echo "$sha  $mode  $src_in_bundle  $dest" >> "$manifest"
done

tarball=$OUT/edge-bundle-$ARCH-$VERSION.tar.gz
tar -C "$work" -czf "$tarball" .
sha256sum "$tarball" | awk '{print $1}' > "$tarball.sha256"

echo "edge-bundle:"
ls -lh "$tarball"
echo "sha256: $(cat "$tarball.sha256")"
