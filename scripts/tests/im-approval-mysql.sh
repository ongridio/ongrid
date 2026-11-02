#!/usr/bin/env bash
set -euo pipefail
# 专用临时容器，仅绑定本机随机端口；退出时删除本次创建的容器和匿名数据卷。
name="ongrid-im-approval-test-$$-${RANDOM}"
cleanup() { docker rm -fv "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
docker run --detach --rm --name "$name" -p 127.0.0.1::3306 -e MYSQL_ALLOW_EMPTY_PASSWORD=yes -e MYSQL_DATABASE=ongrid_im_test mysql:8.4 >/dev/null
ready=false
for ((attempt=0; attempt<90; attempt++)); do
 if docker exec "$name" mysqladmin -h127.0.0.1 ping --silent >/dev/null 2>&1; then ready=true; break; fi
 sleep 1
done
if [[ "$ready" != true ]]; then echo 'MySQL fixture failed to start' >&2; exit 1; fi
port="$(docker port "$name" 3306/tcp)"
port="${port##*:}"
ONGRID_IM_TEST_MYSQL_DSN="root@tcp(127.0.0.1:${port})/ongrid_im_test?parseTime=true&loc=UTC" go test -race ./tests/imapproval -count=1
