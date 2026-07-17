#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd -- "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

render_images() {
    local compose_file="$1" output="$2"
    VERSION=v9.9.9 \
    ONGRID_VERSION=v9.9.9 \
    MYSQL_ROOT_PASSWORD=ci-root \
    MYSQL_PASSWORD=ci-user \
    ONGRID_JWT_SECRET=ci-jwt-secret \
    GRAFANA_ADMIN_PASSWORD=ci-grafana \
        docker compose -f "$compose_file" config --images | sort -u >"$output"
}

render_images "$repo_root/deploy/install/docker-compose.yml" "$tmp_dir/install.actual"
cat >"$tmp_dir/install.expected" <<'EOF'
docker.cnb.cool/ongridio/ongrid/ongrid-web:v9.9.9
docker.cnb.cool/ongridio/ongrid:v9.9.9
docker.cnb.cool/ongridio/ongrid/frontier:v1.2.4
docker.cnb.cool/ongridio/ongrid/grafana-oss:11.1.4
docker.cnb.cool/ongridio/ongrid/loki:3.4.0
docker.cnb.cool/ongridio/ongrid/mysql:8.0
docker.cnb.cool/ongridio/ongrid/prometheus:v2.54.0
docker.cnb.cool/ongridio/ongrid/qdrant:v1.11.3
docker.cnb.cool/ongridio/ongrid/searxng:latest
docker.cnb.cool/ongridio/ongrid/tempo:2.10.0
EOF
sort -o "$tmp_dir/install.expected" "$tmp_dir/install.expected"
diff -u "$tmp_dir/install.expected" "$tmp_dir/install.actual"

render_images "$repo_root/deploy/docker-compose.yml" "$tmp_dir/dev.actual"
cat >"$tmp_dir/dev.expected" <<'EOF'
docker.cnb.cool/ongridio/ongrid/frontier:v1.2.4
docker.cnb.cool/ongridio/ongrid/grafana-oss:11.1.4
docker.cnb.cool/ongridio/ongrid/loki:3.4.0
docker.cnb.cool/ongridio/ongrid/mysql:8.0
docker.cnb.cool/ongridio/ongrid/prometheus:v2.54.0
docker.cnb.cool/ongridio/ongrid/qdrant:v1.11.3
docker.cnb.cool/ongridio/ongrid/searxng:latest
docker.cnb.cool/ongridio/ongrid/tempo:2.10.0
ongrid-web:v9.9.9
ongrid:v9.9.9
EOF
sort -o "$tmp_dir/dev.expected" "$tmp_dir/dev.expected"
diff -u "$tmp_dir/dev.expected" "$tmp_dir/dev.actual"

printf 'verified CNB image routing for install and dev Compose files\n'
