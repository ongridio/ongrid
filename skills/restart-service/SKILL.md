---
name: host_restart_service
description: 重启允许列表内的 systemd service（mutating，触发 reviewer 二审）
when_to_use: |
  用户**明确**要求重启某个允许列表内的服务时使用。

  **本工具是 mutating，会触发 reviewer 二审（agents/reviewer.md），等待 approve 后才执行**。
  调用前 reviewer 会去查 SOP / 设备状态 / 并行操作，任一不满足即 reject — 你（coordinator）
  收到 reject 时**直接告知用户原因，不要重试**。

  不要主动建议重启 — 让用户先要求。常见的"我猜重启就好了"是诊断不深入的信号，
  应该先用 search_logs / query_promql / get_edge_summary 把根因摸清楚再让用户决定。

  允许列表（白名单）：nginx / redis / prometheus / loki / tempo / grafana / mysql / ongrid
  其他 service 直接拒绝（也不走 reviewer）。

metadata:
  os: [linux]
  requires:
    bins: [systemctl]
  ongrid:
    scope: edge
    edge_runtime: subprocess
    edge_capabilities:
      - process.exec: [systemctl]
    activation:
      mode: keyword
      keywords: [重启, 重起, restart, systemctl]
    min_ongrid_version: ">=0.7.30"
---

[能力: host_restart_service]

本工具**重启**目标设备上的 systemd 服务。属于 **mutating** class，调用前自动经过 reviewer 二审：

## 调用规则

```
host_restart_service(device_id=<num>, service="nginx", reason="...")
```

- `device_id` 必填：与 @-mention chip 和 Prom `device_id` label 一致
- `service` 必填：systemd 短名（**不带** `.service` 后缀）
- `reason` 强烈建议填：写入审计行，事后复盘看得到为什么重启的

## 白名单

调用前自动检查 service ∈ 下列之一：

- `nginx` / `redis` / `prometheus` / `loki` / `tempo` / `grafana` / `mysql` / `ongrid`

其他 service 直接拒绝（不走 reviewer，省一次 LLM 调用）。

## 二审流程（HLD-003 SOP）

```
你 (coordinator) 发出 host_restart_service tool_call
      ↓
ReviewGate 装饰器拦截
      ↓ class="write"|"destructive"
      ↓ spawn reviewer worker (agents/reviewer.md)
      ↓ 等 reviewer 输出 "Decision: approve" 或 "Decision: reject"
      ↓
approve → 真正调用边端 systemctl restart
reject  → 返回 reject 理由给你，**不要重试，转告用户**
```

reviewer 自己会用 `get_edge_summary` / `search_logs` / `get_incident_detail` 看 SOP /
并行操作 / 上次 mutating 时间 / 关联告警再决议；你不需要重复这些查询。

## 不要做的事

- 不要在用户没明说"重启 X"时调用 — 主动建议重启是反模式
- 不要 reject 后立刻重试 — reviewer 已经给了理由，转告用户让他决定
- 不要试图绕过白名单 — 边端会再校验一次，绕不过
- 不要传 `reason=""` — 审计行会一片空白，post-mortem 会骂

## 一期实现 (PR-7)

- 边端 handler 是 **mock**（`Mocked: true`），不会真跑 `systemctl restart`
- 真实 shell-out 留 follow-up；本期目标是把 reviewer 二审端到端跑通
