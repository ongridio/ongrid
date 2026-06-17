# SelectDB 查询超时 / 延迟高

## 现象

- 监控中 `doris_fe_query_latency_ms` 持续升高
- 应用报错查询超时
- `doris_be_online` 节点数减少

## 排查步骤

### 1. 查看集群节点状态

```sql
-- FE 节点
SHOW FRONTENDS;

-- BE 节点
SHOW BACKENDS;
```

确认所有节点状态为 `true` / `alive`。

### 2. 查看慢查询

```sql
-- 通过 profiling 抓取慢查询
SET enable_profile = true;
-- 执行查询后查看 profile
SHOW QUERY PROFILE "/";

-- 查看 information_schema 中的慢查询
SELECT query_id, query_type, query_duration_ms, query_state, scan_rows, scan_bytes, sql
FROM information_schema.query_log
WHERE query_duration_ms > 1000
ORDER BY query_duration_ms DESC
LIMIT 20;
```

### 3. 查看 BE 节点负载

```sql
SHOW PROC '/backends';
```

关注指标：
- `Active` 中的查询数量
- `MaxDiskUsedPct` 是否接近 100%
- `MemUsed` 内存使用率

### 4. 常见根因

| 现象 | 可能原因 | 处理 |
|------|----------|------|
| 查询扫描大量行 | 缺少分区裁剪 | 检查 WHERE 条件是否包含分区键 |
| 大表关联无 join 优化 | 数据倾斜或布隆过滤失效 | 检查 join key 分布 |
| BE 节点磁盘满 | 存储空间不足 | 扩盘或清理数据 |
| BE 节点内存高 | 查询并发大 | 限制查询并发或扩容 |
| 查询等待队列 | 资源组耗尽 | 检查 workload group 配置 |

### 5. 优化建议

```sql
-- 查看表分区信息
SHOW PARTITIONS FROM table_name;

-- 查看物化视图
SHOW MATERIALIZED VIEWS;

-- 查询计划分析（通过 explain 前置）
EXPLAIN SELECT ...;
```

### 6. 参数调优

```
exec_mem_limit = 4G          # 单查询内存限制
parallel_fragment_exec_instance_num = 8  # 并行度
query_timeout = 300          # 查询超时秒数
load_parallel_count = 8      # 导入并发
```

## 告警规则

```
expr: doris_fe_query_latency_ms{db_type="selectdb"} > 5000
severity: warning

expr: doris_be_online{db_type="selectdb"} == 0
severity: critical
```
