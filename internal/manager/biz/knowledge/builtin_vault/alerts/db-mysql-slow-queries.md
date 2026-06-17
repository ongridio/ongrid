# 告警：MySQL 慢查询速率突增

## 触发条件

```
expr: rate(mysql_global_status_slow_queries[5m]) > 3σ 偏离基线
severity: warning
```

## 自动诊断流程

触发此告警时，AI Investigator 自动执行以下步骤：

### 1. 查看慢查询整体趋势

```
query_promql(expr: rate(mysql_global_status_slow_queries{db_type="mysql"}[5m]))
query_promql(expr: mysql_global_status_questions{db_type="mysql"})
比较 slow_query_ratio = rate(slow_queries) / rate(questions)
```

### 2. 获取当前 TOP SQL

通过 `query_database` 调用:

```sql
SELECT SCHEMA_NAME, DIGEST_TEXT, COUNT_STAR, SUM_TIMER_WAIT/1000000000 AS total_sec,
       AVG_TIMER_WAIT/1000000000 AS avg_sec, SUM_ROWS_EXAMINED, SUM_ROWS_SENT,
       FIRST_SEEN, LAST_SEEN
FROM performance_schema.events_statements_summary_by_digest
WHERE LAST_SEEN > NOW() - INTERVAL 30 MINUTE
  AND AVG_TIMER_WAIT/1000000000 > 1
ORDER BY SUM_TIMER_WAIT DESC
LIMIT 20;
```

### 3. 分析根因类型

| 特征 | 根因 |
|------|------|
| `rows_examined >> rows_sent` | 缺少索引或索引失效 |
| `avg_sec` 突然上升 | 表数据量增长导致执行计划变化 |
| 特定 SCHEMA_NAME 集中 | 业务模块异常 |
| `CREATE_TMP_TABLE` 多 | 缺少合适的索引 |

### 4. 查看表状态

```sql
SELECT TABLE_SCHEMA, TABLE_NAME, ENGINE, TABLE_ROWS, AVG_ROW_LENGTH,
       DATA_LENGTH, INDEX_LENGTH, CREATE_TIME, UPDATE_TIME
FROM information_schema.TABLES
WHERE TABLE_SCHEMA NOT IN ('mysql','sys','performance_schema','information_schema');
```

### 5. 检查锁争用

```sql
SHOW ENGINE INNODB STATUS\G
```

关注 `LATEST DETECTED DEADLOCK` 和 `TRANSACTIONS` 部分。

## 建议行动

1. **紧急**：如果慢查询导致连接堆积，KILL 异常会话
2. **短期**：加索引或改写 SQL（通过 SQL 审核流程）
3. **长期**：配置 `pt-query-digest` 定期采集，分析慢查询趋势

## 参考知识

- [[mysql-connections-exhausted]]
- [[db-slow-queries-locks]]
