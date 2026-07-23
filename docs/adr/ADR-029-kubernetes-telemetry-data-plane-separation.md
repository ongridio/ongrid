# ADR-029：拆分 Kubernetes Controller 与遥测数据面

> 完整规范见 `spec/02-architecture/architecture-decision-record.md`（何时写、状态流转、评审流程）。

- 状态：已接受
- 日期：2026-07-22
- 作者：Codex
- 替代：不适用

## 背景

当前 Kubernetes `ongrid-edge-controller` 是单副本 Deployment，同时承担两类职责：

1. 控制面：controller enroll/tunnel、Kubernetes inventory list/watch、实时查询和审批后的写动作；
2. 遥测数据面：在同一个 Pod cgroup 内运行 `otelcol-contrib` 接收 traces、logs、OTLP metrics，并由 Controller 抓取 Collector Prometheus exporter 和 kube-state-metrics（KSM），再通过 controller tunnel 推送 metrics。

Controller 默认内存上限为 `512Mi`。遥测吞吐、高基数时序、batch/queue、Kubernetes metadata cache 或大 KSM scrape 都会与 inventory cache 和控制面操作竞争同一个内存与 CPU 配额。Controller Pod 被 OOMKill 时，遥测、资源同步、查询和写动作会同时中断。

直接增加 Controller 副本不安全：相同 controller identity 会产生 tunnel 冲突；多个副本会重复 inventory watch/upload 和 K8s action；多个副本抓同一 KSM endpoint 还会重复写入相同 Prometheus 时序。

## 决策

我们决定将 Kubernetes 控制面、OTLP 数据面和 KSM scrape 拆成三个独立故障域。

### 1. Controller 只承担控制面职责

`ongrid-edge-controller` 保持单副本和唯一 controller identity，仅负责：

- controller enroll 和 controller tunnel；
- Kubernetes inventory list/watch、缓存和同步；
- 实时只读查询；
- 审批后的 Kubernetes 写动作；
- 发布数据面配置、endpoint 和凭据 Secret。

Controller 不再：

- 监听 OTLP 4317/4318；
- 在自身 cgroup 内启动遥测 Collector；
- 接收或转发业务 logs、traces、OTLP metrics；
- 抓取 KSM；
- 通过 controller tunnel 传输 OTLP metrics 或 KSM metrics。

Controller 自身的健康检查和自监控指标不属于业务遥测数据面，仍可通过独立 `/metrics` 暴露。

### 2. 多副本 Telemetry Gateway 承担 OTLP 数据面

新增独立 `ongrid-edge-telemetry-gateway` Deployment：

- 保留现有 telemetry Gateway Service DNS 和 4317/4318 端口；
- 默认至少两个副本，可独立配置 HPA、PDB 和 topology spread；
- 每条 signal pipeline 使用 memory limiter、独立 batch、有界 queue 和有界 retry；
- traces 写入现有 Tempo 数据面入口；
- logs 写入现有 Loki 数据面入口；
- OTLP metrics 直接 remote_write 到当前启用的 Prometheus-compatible backend；
- 不建立 controller tunnel，不具备 Kubernetes 写权限。

Gateway 使用 cluster-scoped、仅允许数据写入的 `telemetry_ingest` credential，不复用 controller credential。

### 3. 单活 Metrics Scraper 承担 KSM scrape

新增独立 `ongrid-edge-metrics-scraper` Deployment：

- 第一阶段固定一个副本；
- 使用固定 KSM Service DNS 抓取 `/metrics`；
- 复用现有增量解析、label drop、sample limit 和按样本数/字节数分批逻辑；
- 直接 remote_write 到当前启用的 Prometheus-compatible backend；
- 强制写入 `cluster_id` 和 `ongrid_source="k8s:kube-state-metrics"`；
- 不建立 controller tunnel，不启动 inventory，也不需要 Kubernetes API RBAC；
- 使用 `Recreate` 或等价无重叠更新策略，避免多个 Scraper 同时抓取同一 KSM endpoint。

KSM 继续使用自己的只读 ServiceAccount list/watch Kubernetes API 并暴露资源状态指标。KSM 的对象 cache、Metrics Scraper 的解析/batch 和 Controller inventory cache 分属不同 Pod 与 cgroup。

不得让每个 Telemetry Gateway 副本抓取同一 KSM endpoint。后续若要求 KSM scrape 高可用或突破单实例容量，必须先引入 Lease leader election、target allocator，或者 KSM 与 scrape target 的明确分片。

### 4. 保持兼容且禁止双写

保持以下兼容边界：

- 业务 Pod 的 OTLP endpoint 不变；
- Prometheus 中现有 `cluster_id`、`ongrid_source` 和业务标签语义不变；
- 现有 K8s PromQL 和告警无需改写；
- Gateway 和 KSM scrape 使用独立模式开关：`telemetryGateway.mode=embedded|deployment`、`kubernetesMetrics.mode=controller|scraper`；KSM 另有 `kubernetesMetrics.enabled` 迁移闸门；
- KSM 切换必须先停止旧路径，再启动新路径；不允许 Controller 和 Metrics Scraper 同时写入同一组时序。

Controller active-active、高可用 KSM scrape 和超大集群 KSM 分片不包含在本决策的第一阶段范围内。

## 备选方案

### 方案 A：只提高 Controller 内存上限

**优点：**

- 改动最小，可以快速缓解当前 OOM；
- 不增加新的 Deployment 和凭据。

**缺点：**

- 只能推迟 OOM，不能隔离控制面和数据面；
- 下游阻塞、高基数或突发流量仍可能耗尽更大的内存；
- Controller 升级和故障仍会同时中断全部能力。

**未选择原因：** 不能消除单一故障域，可作为短期止血但不能作为目标架构。

### 方案 B：增加完整 Controller 副本并加入 leader election

**优点：**

- 单个 Deployment 同时获得一定的控制面和数据面可用性；
- 表面上减少新增组件数量。

**缺点：**

- 必须解决 tunnel identity、leader 切换、非 leader 行为和 K8s action 幂等；
- 每个副本仍保留 Collector metadata cache 和 inventory cache；
- 控制面与遥测数据面仍共享 Pod 故障域；
- KSM scrape 必须额外选主，否则产生重复写入。

**未选择原因：** 复杂度高且没有解决控制面与数据面的资源隔离。Controller HA 应作为后续独立决策。

### 方案 C：所有 Telemetry Gateway 副本都抓取 KSM

**优点：**

- 不新增 Metrics Scraper Deployment；
- KSM scrape 表面上随 Gateway 副本增加。

**缺点：**

- 每个副本会抓取并写入相同 `cluster_id` 和标签的时序；
- HPA 扩缩容会动态改变重复写入倍数；
- 可能产生重复、乱序样本和错误告警。

**未选择原因：** 没有 target ownership 或分片时，多副本 scrape 不具备正确性。

### 方案 D：要求客户提供 OpenTelemetry Operator 或全局采集平台

**优点：**

- Ongrid 不必维护默认 Gateway 和 Scraper 生命周期；
- 已有成熟观测平台的客户可以复用其能力。

**缺点：**

- 把版本、RBAC、升级和故障排查责任转移给客户；
- 无法提供开箱即用的一致安装与回滚路径。

**未选择原因：** 保留为高级接入方式，但不能作为默认架构。

## 后果

### 正面影响

- 遥测吞吐和 KSM scrape 大小不再直接增长 Controller RSS；
- Gateway OOM、扩缩容或升级不影响 inventory 和 K8s action；
- KSM/Scraper 故障只造成 Kubernetes 状态指标缺口，不拖垮 Controller；
- OTLP Gateway 可以按流量独立横向扩容；
- OTLP metrics 和 KSM metrics 不再经过 controller tunnel 和 Manager 重复编解码；
- signal queue、内存和重试边界可以分别配置和告警。

### 负面影响 / 权衡

- 启用目标模式后增加至少两个 Gateway Pod 和一个 Metrics Scraper Pod，集群基础资源开销上升；
- Gateway 需要只读 Kubernetes metadata 权限，仍会产生独立 `k8sattributes` cache；
- 第一阶段 Metrics Scraper 单活，故障或无重叠更新时可能缺失若干 scrape interval；
- 新增数据面 Secret、credential 轮换和 remote_write 入口，运维与安全面扩大；
- 切换顺序错误可能产生 KSM 双写或指标缺口；
- KSM 自身的大集群对象 cache 问题没有被消除，只是被隔离。

### 中性影响

- Controller 第一阶段仍是单副本，控制面 HA 另行决策；
- KSM 仍独立 list/watch Kubernetes API；
- Loki、Tempo 和 Prometheus-compatible backend 继续作为现有数据后端；
- 旧 embedded/controller 模式暂时保留为灰度和回滚路径。

## 实现说明

实现和迁移细节以 RFC-002 为准，并必须遵守以下不变量：

1. `deployment` 模式下 Controller Pod 不存在 `otelcol-contrib` 进程，也不监听 4317/4318；
2. `scraper` 模式下 Controller 不请求 KSM endpoint，controller tunnel 不出现来源为 `k8s:kube-state-metrics` 的 `push_prom_samples`；
3. Metrics Scraper 不携带 controller credential，也不能建立 tunnel 或执行 K8s action；
4. 任一时刻至多一个有效 KSM scrape writer；
5. 新旧链路的 `cluster_id`、`ongrid_source` 和业务标签通过 golden test 保持一致；
6. 新路径必须具有明确的内存、batch、queue、retry 上限和至少 30% 容量余量；
7. 第一轮发布保持旧模式默认，完成压测、故障演练和至少 7 天灰度观察后，再调整新安装默认值。

本 ADR 已接受。如需改变上述职责边界、允许多副本重复 scrape 或复用 controller credential，必须新增 ADR 替代本决策。

## 相关文档

- [RFC-002：Kubernetes Controller 遥测数据面拆分与弹性扩容](../rfc/RFC-002-kubernetes-controller-telemetry-scalability.md)
- [RFC-001：Kubernetes Full-node 接入适配方案](../rfc/RFC-001-kubernetes-edge-adaptation.md)
