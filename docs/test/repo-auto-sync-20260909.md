# 代码仓库自动同步与数量展示

## 需求与实现

- 新增仓库后唤醒后台工作器；启动时检查已有仓库，此后每分钟扫描，将距离上次尝试满 5 分钟的仓库排队同步。逐仓库执行，大仓库可能使后续仓库等待。
- 复用既有全分支、Tag、完整历史同步；手动同步和删除共享同仓库并发保护。
- Commit 数为本地全部 refs 可达提交数，Tag 数为本地标签数，分支数为 origin 远程跟踪分支数（不含符号 HEAD）。数量与成功拉取时间持久化；未拉取显示未知，避免把未知写成 0。
- 成功拉取且不是浅克隆才记录完整历史；界面只描述最近成功拉取的快照，不保证远端此刻没有新提交。
- 单独保存已成功索引的 Commit；该 Commit 未改变且上次索引无错误时跳过向量重建。首次升级会重建一次索引；失败后必须重试，不能凭工作区 HEAD 相同跳过。
- 源码拉取不依赖 embedding 配置；索引失败仍显示错误并自动重试。
- 页面每 5 秒读取状态（后台页暂停），展示 Commit、Tag、分支、完整历史、最近拉取时间及后台同步状态。

## 验证

- `go test -race ./internal/manager/biz/knowledge ./internal/manager/data/knowledge/store` 通过。实际临时 Git 仓库和 SQLite 覆盖新增自动同步、统计落库、周期未到跳过、后续标签更新、文档索引 Tag 固定、重复索引跳过、索引标记缺失重试、并发同步/删除拦截及同步状态。
- 仓库页面 3 项测试和定向 ESLint 通过，包含已同步/等待/同步中及真实 0 与未知值的区分。
- `make proto` 通过。

## 回滚

新增字段为兼容性扩展，回滚应用镜像时保留列。SQL 位于 `db/migrations/20260909180000_add_repo_source_stats.*.sql`。本地部署前备份镜像，沿用 `ongrid-native-deps` Compose 项目与原有数据卷。

## 本地运行

- 管理端已更新至 `ongrid:dev-auto-sync`，镜像 `1f87f73282ea69bdabcd86d564c5d7c02ac7ebaea14087d0a878a9d12c227391`。
- 无页面同步操作，启动后于 2026-09-09 17:08:10（Asia/Shanghai）记录 `knowledge: auto sync complete, repo_id=1`。
- 仓库 HTTP 层 `go test -race ./internal/manager/server/knowledge` 通过。

本地回滚命令：

```sh
docker tag ongrid:dev-before-auto-sync-20260909 ongrid:dev
docker tag ongrid-web:dev-before-auto-sync-20260909 ongrid-web:dev
make compose-up VERSION=dev \
  COMPOSE_ARGS='-p ongrid-native-deps -f deploy/docker-compose.yml -f output/native/deps.override.yml' \
  COMPOSE_SERVICES='--no-build --no-deps --pull never --force-recreate ongrid nginx'
```

- 前端 ARM64 镜像构建通过并部署，镜像 `51ae9bcbfdf5e62bc66916285745f570868062e110785eaf0e19d3f641ed94b8`。
- 17:14:15 第二轮周期同步成功，仍无手动同步操作；两轮日志在 `output/apm-acceptance/auto-sync/manager.log`。
- 页面实际展示 `Commit 3 / Tag 4 / 分支 1 / 完整历史已同步`；文档索引仍是 `1.1.0-demo`，文件数 4。Git 命令核对相同数量，healthz/readyz 均通过。
- 明暗页面截图已实看，统计行与按钮正常显示；截图位于 `output/apm-acceptance/auto-sync/{light,dark}.png`。
- 当前为本地工作区变更，未提交或推送。
