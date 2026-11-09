# CMDB 独立服务部署说明

CMDB 从 manager 进程拆出后由 `ongrid-cmdb` 独立提供服务。manager 继续提供登录、设备、监控、告警等 API；nginx 按路径把 `/api/v1/cmdb/*` 转给 CMDB。

## 服务与端口

| 服务 | 容器端口 | 宿主机端口 | 用途 |
| --- | --- | --- | --- |
| `ongrid` | 8080 / 9100 | 仅 9100 | manager API 与 metrics |
| `ongrid-cmdb` | 8082 / 9102 | 仅 `${ONGRID_CMDB_METRICS_PORT:-9102}` | CMDB API 与 metrics |
| `ongrid-nginx` | 443 / 80 | `${ONGRID_HTTP_PORT:-443}` 等 | TLS、SPA、反代 |

CMDB API 不直接发布到宿主机；外部访问走 `https://<host>/api/v1/cmdb/*`。

## 必需环境变量

| 变量 | 说明 |
| --- | --- |
| `ONGRID_JWT_SECRET` | 与 manager 必须一致；CMDB 只验签。轮换时同时更新两个容器。 |
| `ONGRID_SECRET_KEY` | `secretbox` 的 at-rest key；发现账号与触发器凭据依赖它。未设置时回退到 JWT 派生 key，但仍建议显式设置。 |
| `ONGRID_DB_DSN` | 默认由 install compose 用 `MYSQL_PASSWORD` 拼出，指向同一个 `ongrid` 库。 |
| `ONGRID_CMDB_HTTP_ADDR` / `ONGRID_CMDB_METRICS_ADDR` | 默认 `:8082` / `:9102`，一般不需要改。 |
| `ONGRID_CMDB_DB_MAX_OPEN_CONNS` / `ONGRID_CMDB_DB_MAX_IDLE_CONNS` | 默认 `20` / `5`；与 manager 的 `50` / `10` 一起计入 MySQL 连接预算。 |
| `ONGRID_CMDB_METRICS_PORT` | 宿主机 metrics 端口，默认 `9102`。 |
| `ONGRID_OTEL_ENDPOINT` / `ONGRID_OTEL_SAMPLING_RATIO` | 可选；默认 `tempo:4318`，采样 `1`。 |

## 健康检查

```bash
# 容器内 liveness
docker exec ongrid-cmdb /ongrid-cmdb --healthcheck

# compose healthcheck 状态
docker inspect --format '{{.State.Health.Status}}' ongrid-cmdb

# 通过 nginx 的 readiness（安装/升级也用它）
curl -fsSk https://localhost/cmdb-readyz

# 业务健康（需要 bearer token）
curl -fsSk -H "Authorization: Bearer $TOKEN" https://localhost/api/v1/cmdb/health
```

`/healthz` 只表示进程存活；`/readyz` 还要求数据库可达且 `c_schema_meta` 存在。首次启动或升级时 CMDB 需要先跑完 AutoMigrate，ready 之前 nginx 会返回 502。

## 升级顺序

1. 拉取新镜像（install/upgrade 脚本用 `docker compose config --images` 自动包含 `ongrid-cmdb`）。
2. 同时 recreate `ongrid` 与 `ongrid-cmdb`。**不要**只升级 CMDB 而保留旧 manager：旧 manager 仍挂载 CMDB 路由，两个进程会竞争同一批表。
3. 等待 `/healthz` 与 `/cmdb-readyz` 同时通过。`upgrade.sh` 在任一失败时退出 1，并保留上一版 Edge 资产以便诊断。

CMDB 的迁移锁是 MySQL advisory lock `ongrid_cmdb_schema`。多副本启动时只有一个实例执行 AutoMigrate，其余等待；SQLite 本地模式使用数据库文件旁的 `.migrate.lock`。

## 回滚

- 恢复上一版 `deploy/install/docker-compose.yml`、`nginx.conf` 和 `ONGRID_VERSION`，重新 `up -d`。上一版 manager 会重新挂载 `/v1/cmdb`。
- CMDB 表只做增量变更，旧版本可忽略新增表；若新版本已写入旧代码不认识的数据，回滚前先备份数据库。
- 只回滚 `ongrid-cmdb` 服务时，`/api/v1/cmdb/*` 返回 502，manager 其它 API 不受影响；这适合先隔离 CMDB 故障。

## 排障

```bash
docker compose -f /opt/ongrid/docker-compose.yml logs --tail=200 ongrid-cmdb
docker compose -f /opt/ongrid/docker-compose.yml logs --tail=200 nginx
curl -fsSk https://localhost/api/v1/cmdb/health -H "Authorization: Bearer $TOKEN"
```

- `readyz` 503 且日志有 `cmdb schema missing`：迁移未完成或数据库连错。
- 502 且 `ongrid-cmdb` 容器在重启：看日志中的 `run cmdb migrations` 或 DSN 错误。
- 401：确认两个容器的 `ONGRID_JWT_SECRET` 一致，且浏览器 token 未过期。
- 403：CMDB 的写接口按 admin gate 拒绝；用管理员账号或检查 JWT 的 `role`/`is_superuser`。
