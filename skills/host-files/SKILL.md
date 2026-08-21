---
name: host_files
description: 设备文件系统排查工具组 — 找大文件、看目录占用、stat 单文件（一次最多 16 个 path 批量查询）
when_to_use: |
  当用户问这些时调用本组工具：
    • "找大文件" / "哪些文件最大" / "top N 大文件"
    • "磁盘占用 / 哪个目录在涨 / du"
    • "看一下 /var/xxx 这个目录 / 这个文件 size"
    • "为什么磁盘满了"

  明确不要用于：
    • 查日志内容 → 用 search_logs
    • 查 metric / Prom 看磁盘用率 → 用 query_promql（拿宏观数；具体哪些文件用 host_files）
    • 查进程在写哪些 fd → 用 get_host_processes（看 IO 列）

  常见组合：先用 query_promql 看 disk_used_pct 趋势 → 拿到设备 ID 后用 host_du_summary 下钻
metadata:
  os: [linux, darwin]
  requires:
    bins: [find, du, stat]
  ongrid:
    scope: edge
    edge_runtime: subprocess
    edge_capabilities:
      # 只读路径采用 denylist 策略（2026-05-08 修订）：默认放开整机
      # 文件系统读，仅拒虚拟文件系统 + 高敏材料。运维想加额外白名单
      # 时再通过 operator override 设置 filesystem.read.path。
      - filesystem.read.deny: ["/proc", "/sys", "/dev", "/run", "/etc/shadow", "/etc/sudoers", "/root/.ssh", "/root/.gnupg"]
      - process.exec: [find, du, stat, ls]
    activation:
      mode: keyword
      keywords: [文件, 目录, 磁盘, file, disk, du, size, "大文件", "占用"]
    min_ongrid_version: ">=0.7.30"
---

[能力: host_files]

本组工具帮你看 **设备文件系统的具体内容**：哪些文件大、哪些目录在涨、单个文件的 size / mtime / mode / owner。

> **批量协议**：每个工具的 `paths` 都是 **数组**（1..16 条）。**一定一次给多个相关 path** —— 单 path 多次调用是反模式，浪费 LLM 轮次。返回 `results: [{path, ...data, error?}]` 与 `paths` 同序，per-path 失败不影响其他成功项（partial success）。还会顺手返回 `success_count / error_count` 让你一眼看出整体情况。

## 调用规则

**找最大的 N 个文件（一次多个起点）**：

```
host_find_large_files(
  device_id=<num>,
  paths=["/var/log", "/var/cache", "/opt"],   # 一次 3 个候选大目录
  top_n=10,
)
```

- `paths` 必填，1..16 条，**优先一次问 3..8 个**
- top_n 默认 20，按 size 降序，每条带 `path / size_bytes / size_human / mtime / owner`
- 默认排除 `/proc /sys /dev /run`（虚拟文件系统）
- 返回 `results[i] = {path, scanned_path, files[], error?}` 与 paths 同序

**看哪些目录在涨（一次多个根）**：

```
host_du_summary(
  device_id=<num>,
  paths=["/", "/var", "/var/log", "/opt", "/home", "/tmp"],   # 一次 6 个常见路径
  depth=1,
)
```

- depth 默认 1：每个 path 只看一层子目录，下钻一次问一层
- 不要一上来 `depth=10` —— 慢且 LLM 看不过来
- 返回 `results[i] = {path, subpaths[], total_size_bytes, total_size_human, error?}`

**stat 多个具体文件 / 目录**：

```
host_stat_file(
  device_id=<num>,
  paths=["/var/log/messages", "/var/log/syslog", "/var/cache/apt/archives"],
)
```

- 单点查询，最便宜，**一次给一组相关 path**
- 返回 `results[i] = {path, type, size_bytes, size_human, mtime, atime, mode, owner, group, error?}`
- `type ∈ {file, dir, symlink}`

## 协作范式

**用户问"磁盘满了排查一下"** —— 推荐流程（注意每步都用 batch）：

1. `query_promql` 看 `node_filesystem_used_bytes / node_filesystem_size_bytes` 趋势，确认设备 + 挂载点
2. 拿到 device_id 后 **一次** `host_du_summary(device_id, paths=["/", "/var", "/opt", "/home", "/tmp"], depth=1)` 找全局占用 top
3. 看到 `/var` 占大头 → **一次** `host_du_summary(device_id, paths=["/var/log", "/var/cache", "/var/lib"], depth=1)` 下钻
4. 看到 `/var/log` 占满 → `host_find_large_files(device_id, paths=["/var/log"], top_n=10)` 找具体文件
5. 输出给用户：分层下钻路径 + 最终文件列表 + 建议（清理 / 轮转 / 扩容）

**用户问"node-01 上有哪些大文件"** —— 一步 batch：

```
host_find_large_files(device_id=1, paths=["/", "/var", "/opt", "/home"], top_n=20)
```

直接拿到 4 个根的对比，简短回答。

## 反模式（不要做）

- **不要单 path 多次调用** —— 这是 #1 反模式：
  ```
  ✗ host_du_summary(device_id, paths=["/"])
  ✗ host_du_summary(device_id, paths=["/var"])
  ✗ host_du_summary(device_id, paths=["/opt"])
  ✗ host_du_summary(device_id, paths=["/home"])
  ```
  上面 4 轮浪费 4 倍 token，应该并成一次：
  ```
  ✓ host_du_summary(device_id, paths=["/", "/var", "/opt", "/home"])
  ```
- 不要写文件 / 删文件 —— 本组都是 read class，没有写权限工具
- 不要假设 path 存在 —— 直接放进 `paths`，失败的会进 `results[i].error`，不影响其他成功项
- 不要在敏感路径调用（`/proc /sys /dev /run /etc/shadow /root/.ssh /home/<user>/.ssh /root/.gnupg` 等）—— 这些命中沙箱 deny prefix，仅这条 path 进 error，其他正常返回
- 不要在 `/proc /sys` 跑 `du` —— 永远跑不完（虽然沙箱已经拒，这条提醒 LLM 别尝试）
- 不要超 16 个 path —— maxItems=16 是 hard limit，超过工具会拒绝整批

## 看到 results 含 error 项怎么办

- 看 `success_count / error_count` 摘要先判断整体
- 失败项的 `path` + `error` 字符串会告诉你原因（sandbox / 找不到 / find 错误等）
- **不要重试同一条失败 path** —— sandbox reject 一定是配置问题，找不到的文件再问也是找不到
- 直接基于成功项的数据答复，**附带说明**哪些 path 没拿到 + 原因
