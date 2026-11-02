#!/usr/bin/env bash
# 新装模板与安装器必须一致，防止升级/重装恢复错误的隔离视图。
set -euo pipefail
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
for source in "$repo_root/deploy/install/edge/ongrid-edge.service" "$repo_root/deploy/install/edge/install.sh"; do
    for setting in ProtectSystem=false ProtectHome=false PrivateTmp=false NoNewPrivileges=true; do
        grep -Fqx "$setting" "$source" || { echo "missing $setting: $source" >&2; exit 1; }
    done
    if grep -Eq '^(ProtectSystem=strict|ProtectHome=true|PrivateTmp=true|ReadWritePaths=)' "$source"; then
        echo "unexpected filesystem namespace restriction: $source" >&2
        exit 1
    fi
done
echo 'edge host filesystem configuration: ok'
