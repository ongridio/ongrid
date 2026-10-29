#!/usr/bin/env bash
# Deploy only the isolated demo project. No existing proxy or service is changed.
set -euo pipefail
enterprise_repo=$(cd "$(dirname "$0")/.." && pwd)
enterprise_host=${ENTERPRISE_SSH_HOST:-root@192.168.1.11}
enterprise_release="$enterprise_repo/output/enterprise-release"
enterprise_version=$(cat "$enterprise_release/VERSION")
if [[ ! "$enterprise_version" =~ ^[0-9a-f]{12}$ ]]; then
  echo 'Invalid release version' >&2
  exit 1
fi
tar -C "$enterprise_release" -czf "$enterprise_repo/output/enterprise-release.tar.gz" .
ssh -o BatchMode=yes -o ConnectTimeout=10 "$enterprise_host" 'mkdir -p /opt/enterprise-ai-demo/releases'
scp "$enterprise_repo/output/enterprise-release.tar.gz" "$enterprise_host:/opt/enterprise-ai-demo/upload.tar.gz"
ssh "$enterprise_host" bash -s -- "$enterprise_version" <<'REMOTE'
set -euo pipefail
enterprise_version=$1
enterprise_dir="/opt/enterprise-ai-demo/releases/$enterprise_version"
if ss -Hlnt 'sport = :18860' | read -r _; then
  if [ -z "$(docker ps -q --filter label=com.docker.compose.project=enterprise-ai-demo --filter label=com.docker.compose.service=web)" ]; then
    echo 'Port 18860 is already occupied by another service' >&2
    exit 1
  fi
fi
mkdir -p "$enterprise_dir"
tar -xzf /opt/enterprise-ai-demo/upload.tar.gz -C "$enterprise_dir"
cd "$enterprise_dir"
docker compose config --quiet
docker compose build
docker compose up -d --wait --wait-timeout 60
curl -fsS http://127.0.0.1:18860/healthz
curl -fsS http://127.0.0.1:18860/readyz >/dev/null
if [ -L /opt/enterprise-ai-demo/current ]; then
  enterprise_previous=$(readlink /opt/enterprise-ai-demo/current)
  if [ "$enterprise_previous" != "$enterprise_dir" ]; then
    ln -sfn "$enterprise_previous" /opt/enterprise-ai-demo/previous
  fi
fi
ln -sfn "$enterprise_dir" /opt/enterprise-ai-demo/current
rm /opt/enterprise-ai-demo/upload.tar.gz
docker compose ps
REMOTE
