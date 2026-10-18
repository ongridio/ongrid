---
title: 集群异常 / 告警风暴怎么排查
kind: howto
tags: [集群健康, 告警风暴, sre, slo, 噪音, 优先级]
applies_to: [manager]
---

# 集群异常 / 告警风暴怎么排查（中文）

适用：监控看板红一片，或告警渠道在刷屏，或用户问"集群整体怎么样"。**先聚类、再排
优先级、最后定位根因** —— 不要见告警就追。多数风暴可以归到 1-2 个根因。

## 第 1 步 — 拿到全局视图，先别看单条

```bash
# ongrid 平台
get_topology()                  # 集群节点数 / 在线数 / 监控组件状态
query_incidents(status=open)    # 当前所有未恢复告警
```

平台外：
```
# 看告警系统的当前状态（不同栈不同 API）
curl -s http://alertmanager:9093/api/v2/alerts | jq '.[] | {alertname, severity, instance}'
curl -s http://prometheus:9090/api/v1/alerts | jq '.data.alerts | length'
```

**先数三件事**：
1. 活跃告警总数 —— >100 通常是风暴，不是 100 个独立问题
2. 影响的 instance 数 —— 大部分告警挂在 1-2 台机器 = 单点；散在很多机器 = 真集群级
3. 严重度分布 —— `critical` 几个 / `warning` 几个

## 第 2 步 — 按 (rule, instance) 聚类

告警风暴 90% 是**同一 alertname 在多 instance 触发**（基础设施层 incident），或者
**同一 instance 触发多 alertname**（单点雪崩）。两种模式诊治不同：

| 模式 | 信号 | 根因方向 |
|---|---|---|
| **横向**：同 rule 在 N 个 instance | `count by(alertname)(ALERTS{alertstate=\"firing\"})` 某 rule > 5 | 基础设施（网络分区 / DNS / NTP / 中心 LDAP / 共享存储） |
| **纵向**：单 instance 多 rule | `count by(instance)(ALERTS{alertstate=\"firing\"})` 某 instance > 5 | 单机问题（OOM / 磁盘满 / 服务连锁挂） |
| **混合**：很多 (rule × instance) | 总数大 + 分散 | 通常是监控系统自己抖了（Prometheus / Exporter / 告警系统） |

```promql
# 横向：哪条规则触发最多
topk(10, count by(alertname)(ALERTS{alertstate="firing"}))

# 纵向：哪台机器告警最多
topk(10, count by(instance)(ALERTS{alertstate="firing"}))
```

## 第 3 步 — 怀疑监控系统自身抖了（最常见误判）

如果一瞬间几十条告警同时触发，**第一怀疑对象不是业务而是监控自己**。check：

```bash
# Prometheus 自身健康
curl -s http://prometheus:9090/-/healthy
curl -s 'http://prometheus:9090/api/v1/query?query=up' | jq '.data.result | length'

# scrape 失败率
curl -s 'http://prometheus:9090/api/v1/query?query=count(up==0)' | jq '.data.result[0].value[1]'
```

监控自身抖动的信号：
- 大量 `<service_name>_up == 0` 同时变 0 —— 不是服务真挂，是 Prometheus 抓不到
- `prom_scrape_duration_seconds` 飙高 —— scrape 在超时
- alertmanager 自己 `up==0` —— 告警都发不出去，但你能收到？看你的兜底
- "突然全恢复" —— 监控 channel 抖了一下，重新通了

**这种情况，先别叫醒人**，等 1-2 分钟看告警是否自愈。

## 第 4 步 — 真业务问题：按 SLO 重要性排序

不是所有告警值得马上响应。**用业务影响 + 时间窗口**排：

| 级别 | 判定 | 响应 |
|---|---|---|
| **P0** | 用户感知 + 错误预算消耗速率 > 配额 × 5 | 立即响应，alarm 真人 |
| **P1** | 用户感知 + 还在边界，或者无感知但关键路径 | 工作时间响应 |
| **P2** | 内部影响 / 资源紧张但有冗余 | 当天找出原因，可延迟修 |
| **P3** | 趋势预警 / 噪音偏多的规则 | 看是否要调阈值，不是真要响应 |

ongrid 平台：
- `correlate_incident(incident_id)` 一次拿到 metric+log+trace 三件套
- `query_incidents(severity=critical, status=open)` 只看 critical 未恢复

## 第 5 步 — 找根因（按横向 vs 纵向分支）

### 横向场景（同 rule 多 instance）：基础设施怀疑链

```bash
# DNS
for ns in $(awk '/^nameserver/{print $2}' /etc/resolv.conf); do
  echo "== $ns =="; dig +short +time=2 +tries=1 @"$ns" google.com
done

# NTP（时间漂移导致 cert / alert evaluation 错位）
chronyc tracking
timedatectl status

# 共享存储
mount | grep -E 'nfs|cephfs|glusterfs'
# 试试简单读写
time dd if=/dev/zero of=/<mount>/healthcheck bs=1M count=10 conv=fsync

# 中心认证
ldapsearch -x -H ldap://<server> -b "" -s base 2>&1 | tail
```

### 纵向场景（单 instance 多 rule）：那台机器去诊断

直接派对应 specialist：
- CPU/load/process → `specialist-compute`
- 磁盘满 → `specialist-disk` → `disk-full-cn.md`
- 网络 → `specialist-network` → `network-slow-cn.md`
- 服务挂 → `specialist-ops` → `service-down-cn.md`

很可能是一个根因（如 OOM）触发连锁告警（应用挂 → 接口失败 → 错误率 → 各种业务指标）。

## 第 6 步 — 抑制告警噪音（治标不治本，但能停止刷屏）

```yaml
# alertmanager inhibit rule 示例：node_down 时抑制该 node 的所有其它告警
inhibit_rules:
- source_match:
    alertname: NodeDown
  target_match_re:
    alertname: .+
  equal: [instance]
```

ongrid 平台：
- 在告警规则页可临时 silence 一条规则 N 分钟
- 永久调阈值要走变更（review 后改 PromQL 表达式）

## 第 7 步 — 后续行动：调规则 / 写 runbook

风暴恢复后，**当天**做 3 件事：
1. 总结 top-3 噪音规则（触发最多但很少真要响应的），调阈值或加 `for: 5m` 防抖
2. 写 / 更新对应 runbook（如果还没有），下次相同模式不用从头查
3. 如果是监控自身抖动 —— 加 self-monitoring 告警（Prometheus 自己挂的提醒走独立渠道）

## ongrid 工具提示

- `specialist-sre`：专门看集群健康度 / 告警优先级 / 趋势异常
- `incident-investigator`：单条 incident 端到端关联分析
- `correlate_incident(incident_id)`：一次拉 metric / log / trace 三件套
- `find_outlier_hosts` / `rank_hosts`：找集群里"不一样"的那台

## 决策树

| 信号 | 操作 |
|---|---|
| 告警瞬间几十条同时触发 | 先怀疑监控自己，看 prometheus / scrape 状态 |
| 同一 alertname 多 instance | 横向 → 基础设施（DNS/NTP/共享存储/网络分区） |
| 单 instance 多 alertname | 纵向 → 派对应 specialist 看那台 |
| critical < warning | 看 critical 那几条，warning 通常是噪音 |
| 全部已恢复但告警还在 | 看 alertmanager resolve timeout |
| 持续触发 1h+ 没收敛 | 真问题，按 SLO 优先级响应 |

## 参考资料

- [Linux 系统性能监控工具 Tsar — 运维之美](https://hi-linux.com/posts/13961.html)
- [三张图看遍 Linux 性能监控测试优化工具 — 程序师](https://www.techug.com/post/three-pic-linux-performance.html)
- [SRE Workbook — Practical Alerting](https://sre.google/workbook/practical-alerting/) （Google 官方）
- [Brendan Gregg — Linux Performance](https://www.brendangregg.com/linuxperf.html)
- vault: `concepts/incident-response.md`、`concepts/alerting.md`、`diagnostics/*-cn.md` 系列（按下钻方向选）
