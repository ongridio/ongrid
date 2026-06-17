# Oracle 等待事件分析

## 现象

- 数据库响应变慢
- `v$session` 中大量会话处于等待状态
- AWR 报告中 Top 5 Timed Events 异常

## 排查步骤

### 1. 查看当前等待事件

```sql
SELECT sid, serial#, username, program, event, wait_class,
       seconds_in_wait, state
FROM v$session
WHERE wait_class != 'Idle'
ORDER BY seconds_in_wait DESC;
```

### 2. 查看 TOP 等待事件（按累计等待时间）

```sql
SELECT event, wait_class, total_waits, time_waited_micro/1000000 AS wait_secs,
       average_wait_micro/1000 AS avg_wait_ms
FROM v$system_event
WHERE wait_class != 'Idle'
ORDER BY time_waited_micro DESC;
```

### 3. 常见等待事件处理

| 等待事件 | 可能原因 | 处理方法 |
|----------|----------|----------|
| `db file sequential read` | 索引扫描的单块读慢 | 检查 IO 能力、SQL 执行计划 |
| `db file scattered read` | 全表扫描多块读慢 | 检查是否需要索引、IO 压力 |
| `log file sync` | 日志写入慢，commit 频繁 | 批量提交、redo log 放快速存储 |
| `enq: TX - row lock contention` | 行锁争用 | 查找锁冲突的会话和对象 |
| `buffer busy waits` | 缓冲区争用 | 检查热块、增加 PCTFREE |
| `library cache lock` | SQL 解析争用 | 检查硬解析比例、cursor_sharing |

### 4. 查看 IO 情况

```sql
SELECT name, value/1024/1024 AS mb
FROM v$statname n, v$mystat s
WHERE n.statistic# = s.statistic#
  AND name IN ('physical reads', 'physical writes',
               'physical read total bytes', 'physical write total bytes');
```

### 5. 查看 SGA/PGA 内存

```sql
-- SGA 概况
SELECT * FROM v$sgastat WHERE pool IS NOT NULL ORDER BY bytes DESC;

-- PGA 使用
SELECT name, value/1024/1024 AS mb
FROM v$pgastat
WHERE name IN ('total PGA allocated', 'maximum PGA allocated', 'PGA memory freed');
```

## 参考

- Oracle 官方文档: Troubleshooting Wait Events
- [[oracle-await-events]]
