# PostgreSQL 复制延迟深度排查

## 现象

- `pg_stat_replication` 中 `write_lag` / `flush_lag` / `replay_lag` 持续增大
- 从库查询返回的数据落后于主库
- 监控告警 `db_pg_replication_lag`

## 排查步骤

### 1. 查看复制状态

```sql
SELECT application_name, client_addr, state,
       write_lag, flush_lag, replay_lag,
       pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), write_lsn)) AS write_bytes,
       pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn)) AS replay_bytes
FROM pg_stat_replication;
```

### 2. 检查从库回放进度

```sql
-- 从库执行
SELECT pg_last_wal_receive_lsn(),
       pg_last_wal_replay_lsn(),
       pg_last_xact_replay_timestamp(),
       NOW() - pg_last_xact_replay_timestamp() AS replay_delay;
```

### 3. 常见原因分析

| 现象 | 原因 |
|------|------|
| `write_lag` 大但 `replay_lag` 小 | 网络带宽不足或延迟高 |
| `replay_lag` 大 | 从库 IO 能力不足或有大事务回放 |
| 从库 CPU 高 | 从库在执行查询，挤占 WAL 回放资源 |
| `replay_lag` 持续增长 | 从库 `max_standby_streaming_delay` 设置短，频繁被查询打断 |

### 4. 关键参数

```
# 主库
wal_level = logical            # 至少 replica
max_wal_senders = 10           # 足够支持所有从库
wal_keep_size = 1024           # MB，防止从库 WAL 被回收

# 从库
hot_standby = on
max_standby_streaming_delay = -1  # 不限制，避免回放被查询打断
hot_standby_feedback = on      # 防止查询冲突导致 vacuum 不工作
```

### 5. 监控视图

```
pg_stat_wal_receiver          # 从库 WAL 接收状态
pg_stat_replication           # 主库复制状态
pg_replication_slots          # 复制槽状态
```

## 告警规则

```
expr: pg_replication_lag{db_type="postgresql"} > 104857600    # 100MB
severity: warning → critical at 1GB
```
