# Oracle 表空间满

## 现象

- 应用报错 `ORA-01653: unable to extend table`
- 监控告警 `db_oracle_tablespace`
- DML 操作失败

## 排查步骤

### 1. 查看表空间使用率

```sql
SELECT df.tablespace_name,
       ROUND(df.total_size/1024/1024/1024, 2) AS total_gb,
       ROUND((df.total_size - fs.free_size)/1024/1024/1024, 2) AS used_gb,
       ROUND(fs.free_size/1024/1024/1024, 2) AS free_gb,
       ROUND((1 - fs.free_size/df.total_size)*100, 1) AS used_pct
FROM (SELECT tablespace_name, SUM(bytes) total_size
      FROM dba_data_files GROUP BY tablespace_name) df,
     (SELECT tablespace_name, SUM(bytes) free_size
      FROM dba_free_space GROUP BY tablespace_name) fs
WHERE df.tablespace_name = fs.tablespace_name
ORDER BY used_pct DESC;
```

### 2. 查看表空间增长趋势

```sql
SELECT df.tablespace_name,
       ROUND(df.total_size/1024/1024/1024, 2) AS total_gb,
       ROUND(SUM(s.bytes)/1024/1024/1024, 2) AS segment_gb,
       ROUND(SUM(s.bytes)/1024/1024/1024/1024, 2) AS segment_tb
FROM dba_data_files df
JOIN dba_segments s ON df.tablespace_name = s.tablespace_name
GROUP BY df.tablespace_name, df.total_size
ORDER BY segment_gb DESC;
```

### 3. 查看表空间中 TOP10 大对象

```sql
SELECT * FROM (
  SELECT segment_name, segment_type, tablespace_name,
         ROUND(bytes/1024/1024/1024, 2) AS size_gb
  FROM dba_segments
  WHERE tablespace_name = '&TABLESPACE_NAME'
  ORDER BY bytes DESC
) WHERE ROWNUM <= 10;
```

### 4. 处理方案

| 方案 | 命令 | 说明 |
|------|------|------|
| 增加数据文件 | `ALTER TABLESPACE ts_name ADD DATAFILE 'file.dbf' SIZE 10G;` | 需确认磁盘空间 |
| 自动扩展 | `ALTER DATABASE DATAFILE 'file.dbf' AUTOEXTEND ON NEXT 1G MAXSIZE 32G;` | 推荐设置为 ON |
| 清理历史数据 | `TRUNCATE/PARTITION DROP` | 需要业务确认 |
| 压缩段 | `ALTER TABLE t MOVE COMPRESS;` | 需要维护窗口 |

### 5. 预防措施

- 设置 `AUTOEXTEND ON` + `MAXSIZE` 限制
- 监控 `used_pct > 85%` 时预警
- 对大表使用分区，定期清理历史分区
- 配置表空间阈值告警

## 告警规则

```
expr: oracle_tablespace_used_pct{db_type="oracle"} > 90
severity: critical
```
