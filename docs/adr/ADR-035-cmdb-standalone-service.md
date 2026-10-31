# ADR-035：CMDB 作为独立服务部署

Status: Accepted — 2026-10-26

## 背景

CMDB 原先作为第四个 bounded context 挂在 `cmd/ongrid` 进程内，与 manager 共用端口、数据库连接和生命周期。产品要求 CMDB 的 API 单独部署，同时保留现有 `/api/v1/cmdb/*` 契约和 `internal/cmdb` 分层。把 CMDB 继续放在 manager 内会让 CMDB 的 schema 迁移、故障域和扩缩容与 manager 绑定；完全拆成独立仓库或另起顶层目录又会破坏 `AGENTS.md` 的 `internal/<domain>` 约束和 `.go-arch-lint.yml` 的组件边界。

## 决策

1. **目录**：`internal/cmdb/**` 保持为 CMDB API 的唯一源码目录（server → biz → data → model）；新增 `cmd/ongrid-cmdb` 作为进程入口。不新建顶层 `cmdb-api/`。
2. **进程与路由**：新进程监听 `:8082`（HTTP）和 `:9102`（metrics）。handler 仍注册 `/v1/cmdb/*`，进程把它们挂到 `/api` 下，因此外部路径仍是 `/api/v1/cmdb/*`，SPA 的 `BASE=/api/v1` 不变。
3. **认证**：复用 `internal/pkg/auth.Middleware` 与 manager 共享的 `ONGRID_JWT_SECRET`。CMDB 服务只验签，不查 IAM 数据库；admin gate 仍由 `tenantctx.IsSuperuser || Role=="admin"` 表达。本轮不移植 cantor ACL，也不实现行级过滤。
4. **数据**：默认共用 manager 的 MySQL `ongrid` 数据库；`c_*` 表与 iam/manager 表无命名冲突。CMDB 服务是这些表迁移的唯一所有者：`cmd/ongrid` 不再调用 `cmdbdata.Migrate`。多副本通过 MySQL `GET_LOCK('ongrid_cmdb_schema')` 串行化；SQLite 使用数据库文件旁的 `.migrate.lock`。
5. **健康与可观测性**：`/healthz` 是存活探针；`/readyz` ping 数据库并确认 `c_schema_meta` 存在；nginx 暴露 `/cmdb-readyz` 供安装/升级等待。`/metrics` 暴露 Go/process 指标；服务复用 `internal/pkg/tracing`，span 名为 `ongrid-cmdb`。
6. **部署**：新增 `deploy/Dockerfile.ongrid-cmdb`、dev/install compose 服务、nginx `upstream ongrid_cmdb_backend` 与 `location /api/v1/cmdb/`、Prometheus scrape、release 镜像。安装/升级同时等待 manager `/healthz` 与 `/cmdb-readyz`。
7. **范围**：本轮补齐 CMDB 菜单的核心缺失页面（偏好、关系、树视图、关系视图、批量导入、发现 UI、ADC、操作历史、拓扑视图）；ACL 与 BI 看板另立 Epic；发现采集器保留 `/discovery/sync` 契约，不在本轮实现。

## 数据与兼容性

- 表名保持 cantor 的 `c_*`，`c_schema_meta` 记录 fresh/adopted 血缘；接管现有 cantor 数据库仍需完成 `deleted=1` 回填与用户 ID 映射，不在本轮。
- 现有 145 条 CMDB 路由由 `internal/cmdb/server/testdata/routes.golden` 锁定，拆分不得改变路径、状态码或错误结构。
- 连接预算：manager 默认 50 个连接，CMDB 默认 20 个；部署时必须把两者与其他客户端计入 MySQL `max_connections`。

## 升级与回滚

- **同步升级**：旧版 manager 仍挂载 CMDB 路由且无迁移锁。升级时必须同时替换 `ongrid` 与 `ongrid-cmdb`；不要让新 CMDB 服务对着旧 manager 运行。
- **回滚**：恢复上一版 nginx/compose 与上一版 `ongrid` 镜像；CMDB 表结构只做增量变更，旧代码可忽略新表。若只回滚 CMDB 服务，nginx 的 `/api/v1/cmdb/*` 会 502，而 manager 的其他 API 不受影响。
- **故障隔离**：停止 `ongrid-cmdb` 只影响 CMDB 路由；manager 的 `/healthz`、登录、监控和告警继续工作。

## 后果

- 多一个进程、镜像、compose 服务、metrics target 和发布 job；换来 CMDB 独立扩缩容、独立迁移所有权和更清晰的故障域。
- CMDB 与 manager 通过共享 JWT 和共享数据库耦合；JWT 轮换必须同时更新两个容器。
- 本轮不做 ACL/BI/采集器，因此“CMDB 菜单全部功能”在本 ADR 中定义为已确认范围，而不是 cantor 的逐按钮 parity。
