---
title: 磁盘满了怎么排查
kind: howto
tags: [磁盘, disk, inode, du, df, 清理]
applies_to: [edge, manager]
---

# 磁盘满了怎么排查（中文）

适用场景：`df -h` 显示某分区使用率 >85%，或者应用报 `No space left on device`，或者
日志写不进、新文件 touch 不出来。先分清是 **块满** 还是 **inode 满** —— 两者解决思路完全不同。

## 第 1 步 — 先看是哪个分区、是块满还是 inode 满

```bash
df -h          # 块使用率（你直觉里的"磁盘满"通常是这个）
df -ih         # inode 使用率
```

| 现象 | 结论 |
|---|---|
| `df -h` 接近 100%，`df -ih` 不到 30% | 块满 → 第 2-3 步 |
| `df -h` 还有空间，但 `df -ih` 接近 100% | inode 满（小文件爆炸） → 第 4 步 |
| 两个都满 | 罕见，按块满处理通常一并解决 |

注意 `df` 默认按 1K 计；ext4 / xfs 等会保留 5% 块给 root，所以普通用户看到的"已满"
其实是 95%。`/var` 单独分区且没有 reserved 块时，95% 就真满。

## 第 2 步 — 找占用最大的目录（块满路径）

不要 `du -sh /*` 一次扫到底（会跨挂载点、扫到 /proc 永远不结束）。**用 `-x` 限定不
跨文件系统，从根分批下钻**：

```bash
# 一次只看一层；找到大头再下钻
du -xh --max-depth=1 / 2>/dev/null | sort -hr | head -15
du -xh --max-depth=1 /var 2>/dev/null | sort -hr | head -15
du -xh --max-depth=1 /var/log 2>/dev/null | sort -hr | head -15
```

ongrid 用户优先用专用工具（速度快 + 自带覆盖率检查）：

```
host_du_summary(device_id=N, paths=["/", "/var", "/opt", "/home"], depth=1)
```

`host_du_summary` 返回结果里有 `coverage_pct` 字段 —— 如果你扫的路径加起来只解释
了 `df` 用量的 < 80%，说明你还在错的方向，要扩大扫描范围。

## 第 3 步 — 找最大的文件（精准清理）

```bash
# 全盘找 > 100MB 的文件，按大小倒排
find / -xdev -type f -size +100M -printf '%s\t%p\n' 2>/dev/null \
  | sort -rn | head -20

# ongrid 工具
# host_find_large_files(device_id=N, paths=["/var"], min_size_bytes=104857600, top_n=20)
```

常见大头清单（按概率排）：
1. **`/var/log/journal/*`** — systemd journal。`journalctl --vacuum-size=200M` 安全。
2. **`/var/log/*.log` `*.gz`** — 应用日志没轮转。`logrotate -f /etc/logrotate.conf` 触发一次。
3. **`/var/lib/docker/{overlay2,containers}/*`** — 容器层 + stdout 日志。`docker system df` 然后 `docker system prune -af --filter "until=720h"`（30 天前的镜像/容器）。
4. **`/var/cache/apt/archives`** — 包缓存。`apt clean`。
5. **`/tmp` `/var/tmp`** — 残留临时文件。`find /tmp -mtime +7 -delete`（谨慎，先 ls 看下）。
6. **数据库 binlog / WAL / oplog** — MySQL `/var/lib/mysql/*-bin.*`、PostgreSQL `pg_wal/`。**不要直接删** —— `PURGE BINARY LOGS BEFORE ...`、`pg_archivecleanup`。
7. **`/swap.img` 或 `/swapfile`** — 不是膨胀，是配置好的 swap 文件，正常占地方。

## 第 4 步 — inode 满（小文件爆炸）

```bash
# 找谁文件多
for d in /var /tmp /home /opt; do
  echo "== $d =="; sudo find "$d" -xdev -type f 2>/dev/null | wc -l
done

# 进入候选目录精细找
sudo find /var -xdev -type d -exec sh -c \
  'printf "%s %s\n" "$(ls -A1 "$1" 2>/dev/null | wc -l)" "$1"' _ {} \; \
  | sort -rn | head -20
```

inode 爆炸通常是：
- **会话目录**（PHP session、`/tmp/sess_*`）
- **邮件队列**（`/var/spool/postfix/*`）
- **应用碎片日志**（每请求一个文件那种）
- **核心转储残留**（`/var/lib/systemd/coredump/*`、`/var/crash/*`）

清理同步骤 3，但**清的是文件数量，不是大小** —— 批量 `find ... -delete` 比 `rm -rf` 更稳。

## 第 5 步 — 在清理之前必看的三个 trap

1. **空间已"释放"但 `df` 没变** —— 文件被进程持续打开（`lsof | grep deleted`）。`fuser -k` 杀进程，或重启对应服务才真正回收。
2. **磁盘看着没满，但写入失败** —— quota 满（`quota -uv <user>` 或 `xfs_quota -x`）、reserved blocks 用尽、或 cgroup IO 限制触发。
3. **删了但空间没回收** —— 文件系统是 btrfs / zfs / 带 snapshot：删除只是新版本，老快照还在。`btrfs subvolume list /`、`zfs list -t snapshot`。

## 决策树

| 信号 | 操作 |
|---|---|
| `df -h` 95%+，`du` 大头在 `/var/log` | logrotate / journalctl vacuum，是否日志级别太低 |
| 大头在 `/var/lib/docker` | docker system prune |
| 大头在 DB 数据目录 | binlog / WAL 清理（**用 DB 命令，不要 rm**） |
| `df -ih` 满，`/tmp/sess_*` 几十万文件 | 应用 session 没清理 → 配置 cron 定期清 |
| 删完 `df` 不降 | `lsof \| grep deleted` 找未关闭句柄 |
| 一切清完仍接近满 | 看 reserved blocks (`tune2fs -l <dev> \| grep Reserved`) |

## 参考资料

- [Linux 文件缓存回写简述 — wowotech](http://www.wowotech.net/memory_management/writeback-overview.html)
- [LWN — Filesystem notification: dnotify and inotify](https://lwn.net/Articles/604686/)
- vault: `systems/storage/io-scheduling.md`、`diagnostics/disk-pressure.md`、`diagnostics/conntrack-table-full.md`（如果是日志爆炸来自 conntrack 满）
