#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
hook=/usr/local/lib/ongrid-edge/apply-pending-upgrade.sh
condition="ConditionFileIsExecutable=${hook}"
unit="$repo_root/deploy/install/edge/ongrid-edge-upgrade.service"
sources=(
    "$unit"
    "$repo_root/deploy/install/edge/install.sh"
)

for source in "${sources[@]}"; do
    grep -Fqx "$condition" "$source" \
        || { echo "missing upgrade-hook condition: $source" >&2; exit 1; }
done

if command -v systemd-analyze >/dev/null 2>&1 && [[ -x "$hook" ]]; then
    systemd-analyze verify "$unit"
fi

echo "edge upgrade hook guard: ok"
