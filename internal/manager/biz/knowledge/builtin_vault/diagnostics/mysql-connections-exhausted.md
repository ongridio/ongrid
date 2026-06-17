# MySQL 连接池耗尽

## 现象

- 应用报错 `Too many connections`
- `threads_connected` 接近或等于 `max_connections`
- 新连接无法建立，服务不可用

## 排查步骤

### 1. 查看当前连接数

```sql
SHOW GLOBAL STATUS LIKE 'Threads_connected';
SHOW GLOBAL STATUS LIKE 'Max_used_connections';
```

### 2. 查看连接详情

```sql
-- 按用户统计
SELECT user, COUNT(*) FROM information_schema.processlist GROUP BY user;

-- 按状态统计
SELECT command, COUNT(*) FROM information_schema.processlist GROUP BY command;

-- 按主机统计
SELECT host, COUNT(*) FROM information_schema.processlist GROUP BY host;

-- 查看长时间运行的事务
SELECT trx_id, trx_state, trx_started, TIMESTAMPDIFF(SECOND, trx_started, NOW()) AS seconds
FROM information_schema.innodb_trx
ORDER BY trx_started;
```

### 3. 判断根因

| 模式 | 可能原因 |
|------|----------|
| Sleep 连接占大部分 | 连接池未合理回收，`wait_timeout` 过长 |
| 大量 `Query` / `Execute` | 业务流量突增或慢查询堆积 |
| 大量 `Locked` | 锁阻塞导致连接不释放 |
| 大量 `Connect` | 应用频繁创建新连接，连接池配置过小 |

### 4. 紧急处理

```sql
-- 查看阻塞源头
SELECT p.id, p.user, p.host, p.time, p.state, p.info,
       t.trx_id, t.trx_state, t.trx_started
FROM information_schema.processlist p
LEFT JOIN information_schema.innodb_trx t ON p.id = t.trx_mysql_thread_id
WHERE p.command != 'Sleep'
ORDER BY p.time DESC;

-- Kill 长时间运行的查询（确认业务影响后执行）
-- KILL <thread_id>;
```

### 5. 参数调优

```
max_connections = 500          # 根据内存调整
wait_timeout = 60              # 非交互连接超时
interactive_timeout = 120      # 交互连接超时
thread_cache_size = 64         # 线程缓存
```

## 告警规则

```
expr: mysql_global_status_threads_connected / mysql_global_variables_max_connections > 0.8
severity: warning → critical at 0.95
```

## 参考

- [[mysql-connections-exhausted]]
