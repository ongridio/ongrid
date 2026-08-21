#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
hook=/usr/local/lib/ongrid-edge/apply-pending-upgrade.sh
condition="=${hook}"
sources=(
    "$repo_root/deploy/install/edge/ongrid-edge-upgrade.service"
    "$repo_root/deploy/install/edge/install.sh"
)

for source in "${sources[@]}"; do
    rg -Fqx "$condition" "$source" >/dev/null \
        || { echo "missing upgrade-hook condition: $source" >&2; exit 1; }
done

echo "edge upgrade hook guard: ok"
