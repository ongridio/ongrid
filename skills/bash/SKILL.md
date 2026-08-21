---
name: host_bash
description: 设备上跑只读 shell 命令做诊断探索（沙箱化 / read-only policy）
when_to_use: |
  当结构化工具（host_load / host_processes / host_find_large_files / search_logs / query_promql）
  不够用、需要灵活的 shell 一行 / pipe 时使用。**只读**：写操作（rm / mv / chmod / systemctl restart 等）
  会被边端 cmdpolicy 沙箱拒掉。

  典型场景：
    • 复杂条件的 ps / grep / awk / sed pipe（"找 cpu top 10 + 名字含 nginx 的"）
    • iptables -L / ip addr / tc qdisc show / mount 等只读运维查询
    • systemctl status / cat / list-units / is-active（不能 restart / stop）
    • journalctl / dmesg 配合 grep / awk 做日志快速定位
    • find / du / stat 配合 sort / head 做文件系统下钻
    • 网络探测 nc -z / curl --head / dig / ping（host 在 operator allowlist 内）

  明确不要用于：
    • 重启服务 → 用 host_restart_service（mutating，走 reviewer）
    • 改文件 / 改配置 → 不支持（也不应让 LLM 自主改）
    • 简单单一动作 → 用结构化工具更省 token：
      - 看 cpu/mem 用 get_host_load
      - 看进程列表用 get_host_processes
      - 找大文件用 host_find_large_files
      - 看磁盘占用用 host_du_summary

  **被沙箱拒绝时（response.allowed=false）读 reason，按提示改命令重试**。
  常见拒绝：sed -i / find -delete / shell 嵌套 ($()/`) / redirect (> >>) /
  systemctl restart / 任何 ClassDenied 二进制（rm / bash / python / ...）。
metadata:
  os: [linux]
  requires:
    bins: [ps, grep, awk, sed, find, du, stat, ls, cat, head, tail, wc, sort, uniq]
  ongrid:
    scope: edge
    edge_runtime: subprocess
    edge_capabilities:
      - process.exec:
          - cat
          - head
          - tail
          - tac
          - less
          - ls
          - find
          - du
          - stat
          - readlink
          - file
          - tree
          - wc
          - grep
          - egrep
          - fgrep
          - awk
          - sed
          - ps
          - top
          - uptime
          - free
          - df
          - iostat
          - vmstat
          - mpstat
          - pidstat
          - lsof
          - ss
          - netstat
          - dmesg
          - who
          - w
          - uname
          - id
          - groups
          - hostname
          - date
          - journalctl
          - iptables
          - ip6tables
          - tc
          - systemctl
          - ip
          - mount
          - crontab
          - nc
          - curl
          - wget
          - dig
          - host
          - nslookup
          - ping
          - traceroute
          # 高级网络探测（网研场景）—— 都是 read/write 分流：
          # ovs-* 看流表 / nft 看 ruleset / conntrack 看流 / bpftool 看 BPF 程序
          - ovs-vsctl
          - ovs-ofctl
          - ovs-dpctl
          - ovs-appctl
          - nft
          - conntrack
          - ipset
          - ethtool
          - bpftool
      # bash 不再做 path allowlist 限制（denylist 由命令本身约束 + cmdpolicy
      # 类型沙箱负责）。read 子命令默认放开整机文件系统，写命令仍被 binary
      # 类型沙箱拒。如需 operator 收紧，再设 filesystem.read.path 白名单。
    activation:
      mode: always
    min_ongrid_version: ">=0.7.30"
---

[能力: host_bash]

本工具在目标设备上运行**单条 shell 命令或 pipe 管道**，由边端 `internal/edgeagent/cmdpolicy` 沙箱
做策略校验。**v1 是 read-only policy**，写操作一律被拒。

## 调用规则

```
host_bash(device_id=<num>, cmd="<command>")
```

- `device_id` 必填：与 @-mention chip 和 Prom `device_id` label 一致
- `cmd` 必填：单条命令或单层 pipe；**不支持** `;` `&&` `||` `$()` `<()` `>` `>>` 反引号 / heredoc
- `timeout_seconds` 可选：默认 30s，最大 300s

## 允许的命令分类

| 分类 | 示例 binary | 备注 |
|------|-------------|------|
| 文件系统读 | ls / cat / head / tail / find / du / stat / grep / awk / sed / wc | sed -i / find -delete / awk system() 拒 |
| 系统状态读 | ps / df / free / uptime / iostat / vmstat / lsof / ss / netstat / dmesg / journalctl | journalctl --rotate / --vacuum-* 拒 |
| 网络配置读 | iptables -L / ip addr / tc qdisc show / mount -l / crontab -l | iptables -A / ip addr add / mount /a /b 拒 |
| OVS 探测 | ovs-vsctl show / list-br / get / find / list；ovs-ofctl dump-flows / dump-ports / show；ovs-dpctl show；ovs-appctl fdb/show / lacp/show 等 | add-br / del-br / set / add-flow / del-flows 全拒 |
| nftables / conntrack | nft list ruleset / list table；conntrack -L / -S / -G / -E；ipset list；ethtool -i \<iface\> | nft add/delete/flush；conntrack -F / -D；ethtool -K/-G/-s 全拒 |
| eBPF 只读 | bpftool prog show / map dump / btf dump / net show / link show / iter list / feature probe | bpftool prog load / attach；map create / update 全拒（要写 BPF 走 Layer-3 preset） |
| 网络命名空间 | ip netns list / identify / pids；ip netns（裸= list） | ip netns exec / add / del 拒（exec 会重入沙箱）。深入 inspect 走 host_netns_inspect skill（未上） |
| 服务状态读 | systemctl status / cat / list-units / is-active | systemctl restart / stop / start 拒（用 host_restart_service） |
| 网络探测 | nc -z / curl --head / dig / host / ping / traceroute | curl -o / wget / nc -e/-l 拒；目标 host 须在 operator allowlist 内 |

## 推荐套路（诊断 / 探索）

**问"哪个进程吃 cpu"**：

```
host_bash(device_id=N, cmd="ps aux --sort=-%cpu | head -10")
```

**问"nginx 最近一小时报错了哪些"**：

```
host_bash(device_id=N, cmd="journalctl -u nginx --since '1 hour ago' | grep -i error | head -50")
```

**问"看下 /var 哪些子目录占用最大"**（也可以用 `host_du_summary`）：

```
host_bash(device_id=N, cmd="du -sh /var/* | sort -rh | head -10")
```

**问"iptables 当前规则"**：

```
host_bash(device_id=N, cmd="iptables -L -n")
```

**问"哪个端口开着"**：

```
host_bash(device_id=N, cmd="ss -tlnp")
```

## 不要做的事

- 不要尝试 redirect 写文件（`> /tmp/x`）—— 沙箱拒
- 不要尝试用 `bash -c` 套子壳 —— bash / sh / zsh / dash 整个 ClassDenied
- 不要尝试 `sed -i` —— 用 sed 看就行，改文件不在本工具职责
- 不要尝试 `systemctl restart` —— 用 host_restart_service skill（带 reviewer 二审）
- 被拒后**不要重试同一条命令** —— 读 response.reason，调整命令再发
- 不要用本工具替代结构化查询 —— 简单 cpu/mem/进程查询用 get_host_load / get_host_processes

## v1 实现说明

- Policy = `cmdpolicy.DefaultReadOnly()`，operator 可在 `/etc/ongrid-edge/bash-policy.yaml`
  扩展或收紧（替换某个 binary 的 matchers / 缩短 path allowlist / 改 timeout / 加 network allowlist）
- 网络出站默认全拒（`network_host_allowlist=[]`），operator 显式添加 CIDR 或 hostname suffix 才放行
- Path allowlist 默认 `/var /opt /home /tmp /srv /data`（与 host_files 共享同一份）
- stdout cap 64 KiB / stderr cap 16 KiB / 默认 timeout 30s / 单段 argv ≤32
