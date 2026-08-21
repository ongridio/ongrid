# ADR-031：Edge 直写外部 Elasticsearch

- 状态：已接受
- 日期：2026-08-18
- 作者：Codex
- 替代：不适用
- 关联：[ADR-029：拆分 Kubernetes Controller 与遥测数据面](./ADR-029-kubernetes-telemetry-data-plane-separation.md)

## 背景

Ongrid 当前把外部 Loki/Tempo endpoint 解析后直接下发给 Edge，内置后端才通过 Manager Nginx 鉴权反向代理。若新增 Elasticsearch 后把日志正文先转发给 Manager，会引入额外带宽、CPU、单点和背压故障域，并与 ADR-029 的遥测数据面分离原则相冲突。

同时，外部 Elasticsearch 需要独立写凭证，而普通插件快照有意不携带第三方密钥；写入链路和查询链路也可能使用不同网络地址与权限。

## 决策

1. 配置外部 Elasticsearch 时，Host Edge、Kubernetes Node Collector 和独立 Telemetry Gateway 直接连接 Elasticsearch，日志正文不经过 Manager、Frontier 或控制隧道。
2. Manager 只负责后端配置、加密凭证、当前选中项、独立设备连接检查、查询和审计。
3. Edge 只获得目标 data stream 的只写凭证；Manager 只保留只读查询凭证。两者独立轮换。
4. 内置 Loki 继续通过 Manager Nginx 精确 OTLP 写入口鉴权并代理；Manager Go 不接收日志正文。
5. 外部 Elasticsearch 与内置 Loki 都使用同一个 `otelcol-contrib` logs 流水线，不再保留 Promtail 备用链路。
6. 日志查询通过后端无关服务接口；浏览器、告警和 AIOps 不直接提交 Elasticsearch DSL。
7. Loki 与 Elasticsearch 互斥选择，持久化状态只有 `selected` 与 `unselected`。选择 Elasticsearch 时，Manager 完成查询端点、写入端点和 API Key 权限测试后立即切换；选择 Loki 时直接切换。失败返回错误且不改变当前选中项。
8. 设备在线状态不参与选择。独立的“设备连接检查”会为所有启用了 `logs` 的 Host Edge 生成当前 generation 的一次性探针；在线 Edge 回报已加载配置且探针日志可从当前后端查询到，离线 Edge 只记入检查结果。
9. Edge 配置始终只渲染当前选中的一个日志后端，不存在候选、基线、影子双写或自动回退状态。离线 Edge 重连后按当前选中项同步配置。

## 备选方案

### 方案 A：Manager 中转全部日志

优点是 Edge 只访问一个地址，凭证集中。缺点是 Manager 承担所有日志带宽和背压，外部 ES 故障会影响控制面，且日志包需要额外编解码。未采用。

### 方案 B：部署中心 OTel Gateway 后再写 ES

优点是集中处理、凭证不下发到 Edge。缺点是新增高吞吐数据面组件、容量和高可用责任，仍增加一跳。可以作为严格出口网络场景的后续可选部署，不作为默认。

### 方案 C：继续只支持 Loki

改动最小，但无法补足成熟的正文全文检索、字段聚合和分页体验。未采用。

### 方案 D：Edge 永久双写 Loki 和 ES

便于对比，但会使存储成本翻倍，产生两套 delivery 语义和结果不一致。未采用。设备连接检查只向当前选中的后端写入唯一探针，不产生跨后端双写。

## 后果

### 正面影响

- Manager 控制面资源不随日志流量增长。
- 外部 ES 故障不会拖垮 Edge tunnel 和 Manager API。
- 写入延迟和网络跳数更少。
- OTel 同一采集流水线可以在 Loki 与 ES 间切换，checkpoint 不随后端变化。
- 写、读权限可独立收敛。

### 负面影响与权衡

- 每个 Edge 必须能访问外部 ES，网络和证书排障面扩大。
- 写 API Key 必须安全下发到 Edge；共享 Key 会扩大单 Edge 泄露影响面。
- Manager 连接测试不能证明 Edge 可达，因此必须保留切换后的真实 Edge 探针和逐台 generation 状态。
- 离线 Edge 不阻断全局切换；检查时不进入在线验证分母，并在重连后同步当前选中 generation。
- 旧后端数据继续保留，但产品查询只访问当前选中的后端，不自动归并或迁移历史数据。
- at-least-once 重试可能产生重复日志，不承诺 exactly-once。
