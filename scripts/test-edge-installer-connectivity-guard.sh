#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
INSTALLERS=(
    "$ROOT/deploy/install/edge/install.sh"
    "$ROOT/deploy/install/edge/install-edge.sh"
)

for installer in "${INSTALLERS[@]}"; do
    if grep -En '/dev/tcp|bash[[:space:]]+-c.*exec[[:space:]]+[0-9]+<>' "$installer"; then
        echo "edge installer must not use shell TCP redirection: $installer" >&2
        exit 1
    fi
done

echo "edge installer connectivity guard: ok"
