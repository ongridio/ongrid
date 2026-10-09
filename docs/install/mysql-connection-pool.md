# MySQL 连接池与心跳重试

Manager 共用一个 MySQL 连接池。并发请求超过池上限时，会等待空闲连接，并遵守请求的 context 取消和超时。连接池大小按数据库预算配置，不按设备数量配置。

## 配置

| 环境变量 | 默认值 | 约束 |
| --- | --- | --- |
| `ONGRID_DB_MAX_OPEN_CONNS` | `50` | 每个 Manager 的最大连接数，必须为正整数 |
| `ONGRID_DB_MAX_IDLE_CONNS` | `10` | 最多保留的空闲连接数，必须为正整数，且不超过最大连接数 |

未配置时使用默认值。无效值会使 Manager 启动失败，不会退回无限连接。MySQL 连接空闲满 1 分钟后回收，连接最长复用 3 分钟；这些期限不会中断正在执行的查询。SQLite 不使用上述池设置。

所有 Manager 实例的最大连接数之和，加上其他应用、监控、迁移和运维连接，必须低于 MySQL 的 `max_connections`，并留出余量。例如 `max_connections=151` 时，两个 Manager 各 50 个连接还需核对其余客户端预算；三个实例各 50 个连接则没有足够余量。默认值是起点，不是设备容量承诺。

Docker Compose 用户在安装目录的 `.env` 中设置参数，然后重建 Manager 容器以加载环境变量：

```dotenv
ONGRID_DB_MAX_OPEN_CONNS=50
ONGRID_DB_MAX_IDLE_CONNS=10
```

```sh
docker compose up -d --no-deps ongrid
```

原生部署把同名变量放入 Manager 的启动环境，然后重启 Manager。无需修改数据库 schema。连接复用及等待行为使用 Go 标准库的 [database/sql 连接池](https://go.dev/doc/database/manage-connections)。

## Edge 恢复行为

心跳或首次注册重试失败后，Edge 从正常心跳间隔的一半开始重试，后续连续失败时逐步延长等待，并加入抖动。默认 30 秒心跳间隔下，前三次失败后的等待依次约为 13.5–15 秒、27–30 秒、54–60 秒，给单次超时后的恢复留出余量，避免超过默认 90 秒离线阈值。退避上限为 1 分钟；如果配置的心跳间隔更长，则以该间隔为上限。成功后恢复正常间隔，退出时取消等待。每次心跳和注册 RPC 的期限为 10 秒。自定义心跳间隔和离线阈值时，需同时计入 RPC 耗时及重试等待；持续故障仍会按阈值标记离线。

普通数据库错误、超时不会触发立即重注册或进程重启。已有隧道逻辑仍负责识别失效路由和绑定错误，恢复连接后重新注册。建议先升级 Manager，使连接池限制生效，再逐步升级 Edge，使退避策略生效。只升级 Manager 时，旧 Edge 的重注册行为仍然存在。

## 验证与排查

1. 查看 Manager 启动日志中的 `max_open_conns` 和 `max_idle_conns`，确认配置生效。
2. 观察 `ongrid_db_pool_open_connections`、`ongrid_db_pool_in_use`、`ongrid_db_pool_idle`、`ongrid_db_pool_wait_count_total`。短时等待不等于故障；持续等待应结合 API 延迟、错误和慢查询分析。
3. 核对 MySQL 当前连接及新增拒绝连接数。`Max_used_connections` 是历史峰值，不能单独证明当前连接仍然耗尽：

   ```sql
   SHOW GLOBAL VARIABLES LIKE 'max_connections';
   SHOW GLOBAL STATUS WHERE Variable_name IN (
     'Threads_connected', 'Threads_running', 'Max_used_connections',
     'Connections', 'Connection_errors_max_connections', 'Uptime'
   );
   ```

4. 在隔离环境执行 220 台设备并发注册、3 轮并发心跳，以及池满时的取消和恢复测试。此命令会创建并清理独立的 MySQL 容器：

   ```sh
   ONGRID_TEST_DB_POOL=1 go test -race -tags=integration ./tests/integration -run '^TestMySQLPoolHeartbeatBurst$' -count=1 -v
   ```

现场验收还需覆盖正常采集和页面请求，并确认短暂故障后心跳恢复、没有持续重注册放大负载。慢查询、锁等待、数据库资源不足可能仍导致排队或超时；不能只提高连接上限。

## 回滚

连接预算过小且数据库仍有余量时，调整两个环境变量并重建 Manager 容器。代码回滚使用原 Manager/Edge 版本，无 schema 回滚。旧版本 Manager 不读取这些新变量，回滚会恢复原来的无限连接池；原故障风险也会恢复。
