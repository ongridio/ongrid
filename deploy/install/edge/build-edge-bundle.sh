#!/usr/bin/env bash
# build-edge-bundle.sh — rebuild the ADR-024 one-button upgrade bundle on the
# manager host from the loose per-arch edge binaries already staged under the
# install's edge/ dir.
#
# Why this exists: the release tarball used to carry a pre-built
# edge-bundle-<arch>-<version>.tar.gz *in addition to* the loose binaries it
# is a copy of (one for install-edge.sh, one for the upgrade path). That
# double-pack added ~120 MB of incompressible payload to every release. We now
# ship only the loose binaries and reassemble the bundle here at install /
# upgrade time. nginx serves the result statically from /edge/ exactly as
# before, so the edge-side upgrade flow is unchanged.
#
# The bundle layout + MANIFEST format MUST stay byte-compatible with what
# dist/build-edge-bundle.sh produces and what apply-pending-upgrade.sh
# consumes (fields: sha256  mode  src_in_bundle  dest_path).
#
# Usage: build-edge-bundle.sh <edge_dir> <version> [arch]
#   edge_dir   the installed edge dir holding the loose binaries
#              (e.g. /opt/ongrid/edge); also where the bundle is written.
#   version    e.g. v0.7.159
#   arch       bundle target arch; default linux-amd64 for legacy callers.

set -euo pipefail

EDGE_DIR=${1:?usage: build-edge-bundle.sh <edge_dir> <version> [arch]}
VERSION=${2:?version}
ARCH=${3:-linux-amd64}

# (src_in_bundle  mode  dest_path  loose_file_in_edge_dir)
ENTRIES=(
  "ongrid-edge              0755 /usr/local/bin/ongrid-edge                          ongrid-edge-${ARCH}"
  "node_exporter            0755 /usr/local/lib/ongrid-edge/node_exporter            node_exporter-${ARCH}"
  "process_exporter         0755 /usr/local/lib/ongrid-edge/process_exporter         process_exporter-${ARCH}"
  "mysqld_exporter          0755 /usr/local/lib/ongrid-edge/mysqld_exporter          mysqld_exporter-${ARCH}"
  "postgres_exporter        0755 /usr/local/lib/ongrid-edge/postgres_exporter        postgres_exporter-${ARCH}"
  "redis_exporter           0755 /usr/local/lib/ongrid-edge/redis_exporter           redis_exporter-${ARCH}"
  "mongodb_exporter         0755 /usr/local/lib/ongrid-edge/mongodb_exporter         mongodb_exporter-${ARCH}"
  "promtail                 0755 /usr/local/lib/ongrid-edge/promtail                 promtail-${ARCH}"
  "otelcol-contrib          0755 /usr/local/lib/ongrid-edge/otelcol-contrib          otelcol-contrib-${ARCH}"
  "apply-pending-upgrade.sh 0755 /usr/local/lib/ongrid-edge/apply-pending-upgrade.sh apply-pending-upgrade.sh"
)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

manifest=$work/MANIFEST.txt
{
  echo "# ADR-024 bundle manifest"
  echo "# fields: sha256  mode  src_in_bundle  dest_path"
} > "$manifest"

echo "$VERSION" > "$work/VERSION"

staged=0
for entry in "${ENTRIES[@]}"; do
  # shellcheck disable=SC2086
  set -- $entry
  src_in_bundle=$1
  mode=$2
  dest=$3
  loose=$4
  src_file="$EDGE_DIR/$loose"

  if [[ ! -f "$src_file" ]]; then
    echo "build-edge-bundle(host): missing $src_file — skipping (bundle will be incomplete)" >&2
    continue
  fi
  install -m 0755 "$src_file" "$work/$src_in_bundle"
  sha=$(sha256sum "$work/$src_in_bundle" | awk '{print $1}')
  echo "$sha  $mode  $src_in_bundle  $dest" >> "$manifest"
  staged=$((staged + 1))
done

if [[ "$staged" -eq 0 ]]; then
  echo "build-edge-bundle(host): no loose binaries found under $EDGE_DIR for $ARCH — bundle NOT built" >&2
  exit 1
fi

tarball="$EDGE_DIR/edge-bundle-$ARCH-$VERSION.tar.gz"
tar -C "$work" -czf "$tarball" .
sha256sum "$tarball" | awk '{print $1}' > "$tarball.sha256"

echo "build-edge-bundle(host): wrote $tarball ($staged file(s))"
