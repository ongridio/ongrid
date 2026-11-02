---
title: 内存高 / OOM 怎么排查
kind: howto
tags: [内存, memory, oom, swap, cache, cgroup]
applies_to: [edge, manager]
---

# 内存高 / OOM 怎么排查（中文）

适用：`free -h` 显示 `available` 很低，或者监控告警 `mem_used_pct > 80%`，或者
进程被 OOM killer 杀了。**先分清是真不够还是看着不够 —— page cache 一向把空闲全占了**。

## 第 1 步 — 看清真实可用内存

```bash
free -h
```

| 列 | 含义 |
|---|---|
| `total` | 物理内存总量 |
| `used` | 应用 + 内核占用（不含可回收 cache） |
| `free` | 完全空闲（基本永远很低，不是问题） |
| `available` | **真正可用**（free + 可回收 cache）→ 看这一列！ |
| `buff/cache` | 页缓存 + 缓冲；遇到压力会被回收，不算占用 |

**判断标准**：
- `available > 20% × total` —— 健康
- `available < 10%` —— 真有压力
- `free` 低但 `available` 高 —— 误报，page cache 在工作

## 第 2 步 — 是否启用了 swap？swap 用了多少？

```bash
swapon --show              # 列出 swap 设备/文件
cat /proc/meminfo | grep -E '^Swap'
```

`SwapTotal > 0` 但 `SwapFree` 远小于 `SwapTotal` → 在用 swap → 物理内存吃紧。
**用 swap 不等于性能崩** —— 长期挂着不活跃的进程被 swap out 是正常的。看 swap 是不
是在 **持续** 增长（监控 `node_memory_SwapFree_bytes` 1h 趋势）。

如果 swap 增长 + load avg 高 + iostat 看 swap 设备 busy → **真在 swap thrashing**，
这时候系统性能崩溃，进 OOM 倒计时。

## 第 3 步 — 谁在吃内存

```bash
# 按 RSS（驻留内存）倒排
ps -eo pid,user,rss,vsz,comm --sort=-rss | head -15

# ongrid 工具
# get_host_processes(device_ids=[N], sort_by=mem, top_n=10)
```

看 RSS（实际占的物理内存），不要看 VSZ（虚拟空间，常常吓人但不真实占用）。

**异常进程模式**：
- 单进程 RSS > 50% total —— 嫌疑很大，看下面 step 4
- 多个同名子进程吃内存（fork 大量 worker）—— 配置问题（nginx workers / postgres connections 太多）
- `kswapd0` CPU 占高 —— **内核在拼命回收内存**，强烈压力信号
- `khugepaged` CPU 高 —— 透明大页问题，考虑关闭

## 第 4 步 — 怀疑泄漏：从 RSS 增长到具体原因

```bash
# 监控某进程 RSS 1 小时变化
while true; do date; ps -o rss= -p <pid>; sleep 60; done
```

RSS 持续单调增长 = 疑似泄漏。继续：

```bash
# pmap 看进程内存布局；diff 找哪个 mapping 在涨
pmap -x <pid> > /tmp/p1
sleep 600
pmap -x <pid> > /tmp/p2
diff /tmp/p1 /tmp/p2
```

哪一段在涨给你提示：
- `[heap]` 涨 → 应用堆泄漏（C 用 valgrind / heaptrack；Go 用 pprof；Java 用 jmap）
- `[anon]` 涨 → mmap 大块持续不释放（DB、JIT、缓存层）
- 某 `.so` 涨 → 该库的 allocator 泄漏

## 第 5 步 — OOM Killer 已经动手了

```bash
# 查 OOM 历史
dmesg -T | grep -iE 'oom|killed process' | tail -30
# 或者
journalctl -k --since '1 hour ago' | grep -i oom
```

完整 OOM 报告（dmesg 里 `Out of memory:` 行的上下文）包含：
- 被杀的进程 PID + 名字
- 当时全机 RSS 倒排前 N
- **`oom-kill:constraint=`** —— `CONSTRAINT_NONE` 系统级 OOM；`CONSTRAINT_MEMCG` cgroup 超限

cgroup OOM 比系统级常见得多，特别是容器化环境：

```bash
# 容器内 OOM 痕迹
docker inspect <name> | grep -A 5 OOM
cat /sys/fs/cgroup/<unit>/memory.events       # cgroup v2
cat /sys/fs/cgroup/memory/<unit>/memory.oom_control  # cgroup v1
```

## 第 6 步 — 内核内存（不是应用层泄漏）

`free` 显示 `used` 高、`ps` 加起来却没那么多 → 内核占的。

```bash
cat /proc/meminfo | grep -E 'Slab|SReclaimable|SUnreclaim|KernelStack|PageTables'
slabtop -o -s c | head -20   # 按大小排，看哪个内核对象暴涨
```

| 异常增长的 slab | 可能原因 |
|---|---|
| `nf_conntrack` | conntrack 表满 → 见 `conntrack-table-full.md` |
| `dentry` / `inode_cache` | 大量 open/close 或 `find /` 扫飞了 |
| `kmalloc-1024`/`kmalloc-2048` | 驱动 / 模块泄漏（看最近 `lsmod` 改动） |
| `vm_area_struct` | 进程 mmap 失控 |

```bash
# 拍打 cache 看是否能回收
sync && echo 3 > /proc/sys/vm/drop_caches
free -h   # 对比前后，能释放说明是 cache，不能释放说明是真泄漏
```

## 第 7 步 — 紧急释放（生产慎用）

```bash
# 优先级从低到高
sync && echo 3 > /proc/sys/vm/drop_caches    # 安全：只清 page cache
systemctl restart <hot service>              # 中等：只重启吃内存的进程
echo 1 > /proc/sys/vm/compact_memory         # 安全：触发内存碎片整理（CPU 短暂↑）
```

最后才考虑：增加 swap、加内存、调 cgroup limit。

## 决策树

| 信号 | 下一步 |
|---|---|
| `available > 20%` | 没问题，关警报 |
| `available < 10%` + 单进程 RSS 大头 | 看进程是否泄漏 → 第 4 步 |
| `available < 10%` + 没明显大头 + `SUnreclaim` 涨 | 内核 / 容器层泄漏 → 第 6 步 |
| swap 在涨 + iostat 看 swap 设备 busy | 真 thrashing，紧急扩容 / kill 大户 |
| OOM 已发生 | 看 dmesg 完整报告，识别 cgroup vs 系统级 → 调对应 limit / kill 大进程 |
| RSS 全部正常但 `free` `used` 仍然高 | 内核 slab（`slabtop` 看哪类涨） |

## 参考资料

- [Linux Memory Anatomy — Many But Finite](https://manybutfinite.com/post/anatomy-of-a-program-in-memory/)
- [Page Cache — Many But Finite](https://manybutfinite.com/post/page-cache-the-affair-between-memory-and-files/)
- [Kernel Memory Leak Detector — kernel.org](https://www.kernel.org/doc/html/latest/dev-tools/kmemleak.html)
- [Capacity Tuning — RHEL Performance Tuning Guide](https://access.redhat.com/documentation/en-us/red_hat_enterprise_linux/7/html/performance_tuning_guide/sect-red_hat_enterprise_linux-performance_tuning_guide-memory-capacity_tuning)
- vault: `systems/linux/memory.md`, `diagnostics/memory-leak-hunt.md` (English), `diagnostics/high-load-low-cpu.md`（D 状态进程也常因为内存压力）
