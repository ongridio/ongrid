# RFC-003：OTel 日志采集与 Elasticsearch 直写

## 元信息

- 状态：实施中
- 作者：Codex
- 日期：2026-08-18
- 关联 PRD：[PRD-004：外部 Elasticsearch 日志中心](../requirements/PRD-004-elasticsearch-log-center.md)
- 关联 ADR：[ADR-031：Edge 直写外部 Elasticsearch](../adr/ADR-031-edge-direct-elasticsearch-logs.md)
- 关联 HLD：[HLD-001：日志采集、存储与查询解耦](../design/HLD-001-log-pipeline-backend-abstraction.md)
- 关联 issue：用户直接需求

## 背景

当前 `logs` 插件渲染 Promtail 配置并写 Loki，页面和下游能力使用 LogQL。目标是在不改变 Edge 控制通道和插件身份的前提下，用 `otelcol-contrib` 统一采集，新增外部 Elasticsearch 直写，并形成后端无关的查询和运维闭环。

仓库统一固定 `otelcol-contrib 0.157.0`，logs 和 traces 共用同一制品但保持独立进程。日志流水线使用 filelog、journald、file_storage、OTLP HTTP 和 Elasticsearch exporter；发布前用该真实二进制同时校验 logs 与 traces 配置。

## 方案

### 1. 版本与发布

- 单独升级并固定通过验证的 Collector 版本，下载时校验官方 checksum。
- CI 使用实际捆绑二进制分别验证 traces、host logs、Kubernetes logs、Loki 和 ES 配置。
- logs 和 traces 初期使用同一二进制、独立进程及端口，避免一次性合并生命周期。

### 2. Edge OTel logs pipeline

- Host receivers：journald + 每 source 一个 filelog。
- Kubernetes receiver：filelog 读取 `/var/log/pods`，container parser + k8sattributes。
- extensions：独立 file_storage、health_check。
- processors：memory_limiter、resource、transform、filter/redaction、batch。
- exporters：一次只启用 `otlphttp/builtin-loki` 或 `elasticsearch/external`。
- filelog/journald 开启 storage 与 retry_on_failure；exporter 开启持久化 queue/retry。
- 配置写临时文件、先 validate、再 rename；失败不覆盖当前配置。

### 3. Elasticsearch 文档与权限

- 仅支持 ES 8.16+、OTel mapping 和动态 data stream。
- runtime write Key 仅能 create_doc/auto_configure 产品 data stream，不授予 cluster monitor；Manager 通过 `_has_privileges` 校验其有效权限。
- query Key 授予 cluster monitor（用于所有 query/write endpoint 的 8.16+ 版本探测）以及产品 data stream 的 read/view_index_metadata；该 Key 只保留在 Manager。
- 激活前逐个检查 endpoint 版本、write/read 权限，并通过 Edge 写 probe。

### 4. 控制面

- 新增 `log_backends`、`log_backend_assignments` 和 repository/usecase。
- 普通 config snapshot 只包含 backend type、endpoint、CA 引用、generation 和 credential version。
- 新增 `get_plugin_secret` tunnel method；仅允许认证 Edge 按目标 generation 读取 `logs/elasticsearch_api_key` 固定 slot，普通 snapshot 不含密钥。
- 激活状态：DRAFT → DISTRIBUTING → VERIFYING → ACTIVE；失败进入 DEGRADED，可回滚上一 generation。
- DISTRIBUTING/VERIFYING 时 Edge 有界双写当前权威后端与候选 ES；候选 exporter 独立排队且不得阻塞权威链路。ACTIVE 后停止双写。
- 全量激活枚举所有启用 logs 的 Edge，存在离线 Edge 时拒绝切换，避免离线 Agent 继续写旧后端而查询时间线已前移。

### 5. 查询面

- 新增稳定 SearchRequest/SearchResult 类型和 Backend interface。
- Loki adapter 维持旧数据可查；Elasticsearch adapter 使用 PIT + search_after、bool/filter、match/match_phrase、date_histogram 和 field caps。
- HTTP 不接收原始 Elasticsearch DSL。限制时间范围、limit、字段、并发和超时。
- 灰度查询继续读取权威后端；全局切换后，planner 按每个 generation 的 `cutover_at` / `ended_at` 重建真实写入时间线，可读取 Loki 与多个历史 ES generation。

### 6. 产品耦合迁移

- Logs 页面改为结构化筛选和 query text，不再构造 LogQL。
- 新增 `search_logs` AIOps tool；旧 `query_logql` 兼容保留。
- 新日志告警使用结构化 matcher；原始 LogQL 规则作为激活 blocker。
- Incident correlation 使用统一 LogRecord。
- Grafana Loki datasource 不自动替换；ES 下优先产品日志页，可选 Kibana URL。

### 7. Promtail 迁移

1. OTel 以 discard exporter、`start_at=end` 建立 checkpoint。
2. 停止 OTel并持久化 checkpoint。
3. 停止/flush Promtail。
4. OTel 从 checkpoint 启动并写内置 Loki native OTLP。
5. 全 fleet 稳定后才启用 ES canary。
6. 新版本制品不再下载、安装或启动 Promtail；回滚后仍由同一 OTel pipeline 写内置 Loki。

## 备选方案

### A. Promtail 保留，新增其他 ES shipper

会在 Edge 长期维护两套采集、解析、checkpoint 和权限模型，不利于统一可观测性。未采用。

### B. 所有日志先写 Manager OTel Gateway

避免下发 ES 凭证，但增加一跳和中心容量/高可用责任，破坏现有外部后端直写边界。未采用为默认，可作为受限网络的后续模式。

### C. Loki 与 ES 永久双写

迁移简单但成本翻倍、结果不一致且告警语义复杂。未采用。

### D. 暴露原始 Elasticsearch DSL

实现快但允许跨索引访问和昂贵查询，并把产品永久绑定 ES。未采用；使用受限查询 AST。

## 影响范围

- API：新增 logs proto、管理和查询 HTTP API、tunnel secret method。
- Manager：新增 model/data/biz/server wiring、ES client、迁移规划和健康状态。
- Edge：logs renderer、secret handler、健康指标和迁移状态。
- Kubernetes：Node/Gateway 配置、Secret/RBAC 和来源去重边界。
- Web：Logs 与 Integrations 设置页、告警/AIOps/Incident 跳转。
- Deploy：Nginx Loki OTLP 路由、Collector 版本、bundle、安装/升级/卸载脚本。
- 运行时：Edge 增加 OTel logs 进程内存；删除 Promtail 后制品减少一个二进制。

## 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| journald receiver alpha | 产品发行版矩阵、feature flag、真实二进制校验、上一发布版本紧急回退 |
| Collector 升级破坏 traces | 独立依赖 PR、真实二进制配置校验、保持上一制品 |
| ES mapping explosion | 固定 data stream、字段 allowlist、JSON 深度/数量/大小限制 |
| ES 长时间不可用 | 有界磁盘 queue、高水位暂停、明确告警和容量估算 |
| Edge 凭证泄露 | 只写 Key、data stream 限权、90 天轮换、后续 per-edge Key |
| 切换日志缺口 | 预热 checkpoint、短重复优先于缺口、记录 cutover_at / ended_at |
| 查询成本失控 | 时间/limit/字段/超时/并发限制、PIT/search_after |
| 旧 LogQL 功能失效 | 激活 blocker、兼容 adapter、分阶段迁移 |

## 里程碑与实施任务

| 里程碑 | 任务（每项 1–3 天） | 产物 |
| --- | --- | --- |
| M0 规格/基线 | PRD/RFC/HLD/ADR；Collector/ES 支持矩阵；golden logs | 文档与测试基线 |
| M1 查询解耦 | 公共模型；Loki adapter；Search API；前端切换 | 后端无关查询闭环 |
| M2 OTel→Loki | receivers/checkpoint；native OTLP；迁移开关 | 无 Promtail 默认链路 |
| M3 ES 控制面 | DB/凭证/RPC；ES exporter/query；探针/状态机 | canary 可激活 |
| M4 功能迁移 | 告警/AIOps/Incident/Grafana；历史读联邦 | 产品完整闭环 |
| M5 验收/清理 | 故障注入、容量、两架构、UI截图；删除 Promtail | 发布候选 |

## 回滚

- ES exporter 失败：先让所有 logs Edge 在本地保持 ES 权威并影子写 Loki；Loki 实写探针全部通过后才关闭 ES generation 并回到内置 Loki，继续使用同一 OTel checkpoint。
- OTel 配置失败：保持上一工作配置，不重启当前进程。
- OTel 运行时故障：回退到上一个携带 Promtail 的 Ongrid 发布版本；这是版本级紧急回退，不是当前制品内的运行开关。
- 数据库：新增表和字段为 additive；回滚旧版本时保留表，不删除凭证，显式停用新后端。
- Web：feature flag 回退旧 Logs 页面和 Loki API。

## 验收标准

- PRD-004 全部验收项通过。
- 新增配置均由固定 Collector 版本 validate。
- `go test ./... -race`、前端 test/typecheck/build、Helm/render/install 校验通过。
- 真实 Edge→ES 流量与 Manager→ES 查询流量可区分，日志正文不进入 Manager。
- 完成 OTel→Loki、Loki→ES、ES→Loki 和 Promtail 紧急回滚演练。

## 变更记录

| 日期 | 变更 | 作者 |
| --- | --- | --- |
| 2026-08-18 | 用户确认方案后进入实施 | Codex |
