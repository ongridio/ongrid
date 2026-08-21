# PRD-004：外部 Elasticsearch 日志中心

## 元信息

- 状态：开发中
- 作者：Codex
- 日期：2026-08-18
- 关联 Epic：不适用
- 关联 RFC：[RFC-003：OTel 日志采集与 Elasticsearch 直写](../rfc/RFC-003-otel-elasticsearch-logs.md)
- 关联 ADR：[ADR-031：Edge 直写外部 Elasticsearch](../adr/ADR-031-edge-direct-elasticsearch-logs.md)
- 关联 HLD：[HLD-001：日志采集、存储与查询解耦](../design/HLD-001-log-pipeline-backend-abstraction.md)

## 背景

现有日志中心以 Loki/LogQL 为唯一存储和查询模型，Edge 的 `logs` 插件由 Promtail 实现。它适合基于标签缩小日志流，但产品缺少面向日志正文的稳定全文检索、短语搜索、字段聚合、上下文和深分页能力。仅让 Edge 把数据写进 Elasticsearch 不能形成可用产品，因为当前页面、告警、AIOps、Incident 关联和 Grafana 跳转都直接绑定 Loki/LogQL。

## 目标

- 新增外部 Elasticsearch 8.16+ 日志后端，日志正文由 Edge 直接写入，Manager 不承载写入字节流。
- 用 `otelcol-contrib` 替换 Promtail，同时保留 `logs` 插件名和现有控制通道。
- 提供后端无关的日志查询 API，支持 Loki 与 Elasticsearch，并让页面不再拼接后端 DSL。
- Loki 与 Elasticsearch 互斥启用；Edge 写入和日志中心查询始终指向同一个当前后端，不做跨后端联邦查询。
- 支持关键字、短语、包含/排除、字段筛选、时间直方图、上下文和游标分页。
- 保留内置 Loki 作为默认后端；Loki 与 Elasticsearch 可直接互相选择，无需切换采集器实现。
- 正常状态端到端日志可见延迟 P95 不超过 10 秒；15 分钟单集群查询 P95 不超过 3 秒。

## 功能需求

### 外部 Elasticsearch 接入

- 管理员可以在日志后端表单中直接粘贴 Elasticsearch 创建接口返回的 encoded 写 API Key 和只读 API Key，无需先跳转到通用凭证页创建记录；设置页不再暴露通用凭证复用入口。
- 直接提交的 Key 是只写字段：Manager 立即将其写入加密 secret vault，并只在 `log_backends` 保存托管引用；敏感值不可由读取 API 返回。空值表示保留当前 Key。
- 生产模式下写凭证与查询凭证必须分离；兼容测试可显式复用同一个 Key，但 Manager 仍生成两个独立托管引用，且页面必须提示扩大权限的风险。
- 设为当前前必须通过 Manager 的查询端点、写入端点和 API Key 权限测试；Edge 在线状态不阻断选择。
- 后端状态只包含 `selected` 与 `unselected`。连接失败通过本次请求错误返回，不产生中间、失败或自动回退状态。
- Elasticsearch 与 Loki 设置页都提供独立的“检查设备连接”操作：Manager 为当前权威后端创建真实写入探针，页面以进度条展示在线设备的已验证数/在线总数并从 0 推进至完成，不展示逐设备明细，离线设备不进入分母。
- 从日志中心点击“采集与后端配置”进入集成设置时，日志集成必须置于页面首位并默认展示当前日志后端；从设置侧栏普通进入集成页时保留全局集成的默认排序。
- 配置主流程保留“保存”和“设为当前”：保存不改变当前读写链路；Elasticsearch 选择执行 Manager 端点与认证探测后切换，Loki 直接切换。切换后可点击“检查设备连接”触发当前 generation 的逐 Edge 加载与真实写入验证；检查不改变选中状态。
- 设置页不向用户暴露 Elasticsearch Data Stream 的 dataset 与 namespace；新配置使用产品默认值 `ongrid.system/default`，编辑已有配置时保留其已存值。

### 日志采集

- Host 支持 journald 和多个独立文件源。
- 普通 Host Edge 根据其 Device 在通用拓扑中的 `member_of` 关系，由 Manager 向采集配置注入稳定的 `cluster_id` 和 `cluster_name`；设备未绑定集群时不伪造集群字段。
- Kubernetes Node 支持 CRI 容器日志，并在不访问 Kubernetes API 的前提下从日志路径和节点环境补全 cluster、namespace、pod、container 和 node；workload 字段仅在中央元数据补全可用时提供。
- 每个文件源可以配置稳定 source id、service name、dataset、include/exclude、JSON/regex/plain parser 和 multiline。
- Collector 重启后必须从持久化 file offset/journal cursor 继续读取。
- 下游暂时失败时使用有界持久化发送队列，不允许无限占满系统盘或静默丢弃。

### 日志查询

- 页面通过结构化查询请求搜索，不直接提交 Elasticsearch DSL 或新建 LogQL。
- 支持设备、角色、集群、namespace、pod、container、service、source、level 和文件筛选；workload 筛选依赖中央元数据补全能力。
- 日志后端互斥，因此逐条日志和左侧字段面板不展示 `backend`；页面标题仍可展示当前启用后端。左侧字段面板展示 `level` 与通用拓扑集群。
- 支持全文关键字、短语、包含任一/全部、排除关键字。
- 支持时间直方图、字段名/字段值、上下文日志、游标分页、自动刷新和受限导出。
- 时间直方图支持单击柱体下钻到该聚合粒度，也支持按住鼠标横向拖拽选择任意绝对时间窗；选中后立即重新查询、暂停实时模式，并可返回上一级时间范围。键盘用户仍可通过自定义起止时间完成同一操作。
- 选择通过 Manager 验证后一次性切换；旧后端数据保留但不自动归并或迁移。设备连接检查只向当前选中的后端发送唯一探针，不产生跨后端双写。

### 兼容功能

- 新增后端无关 `search_logs` AIOps 工具；旧 `query_logql` 仅在 Loki 可用时保留。
- 新增结构化日志匹配告警；原始 LogQL 规则标记为 Loki-only，存在未迁移启用规则时阻止选择 ES。
- Incident 日志关联消费统一日志结果，不再解析 Loki stream。
- ES 模式统一使用产品日志页，不依赖 Kibana，也不在默认设置页提供 Kibana 或自定义 CA 配置。

## 边界情况

- Edge 能访问写 endpoint 但 Manager 不能访问查询 endpoint 时，不允许选中该 Elasticsearch 配置。
- Manager 验证成功后允许选择；任一 Edge 无法完成真实写探针时，在设备连接检查中标记失败，但不改变已经完成的选择。
- 离线 Edge 不阻断选择。设备连接检查将离线 Edge 排除在在线验证分母之外；离线 Edge 重连后自动获取当前选中的 generation。
- 用户查询只返回当前权威后端；不接受无提示的日志缺口，交付语义为 at-least-once。
- 队列达到高水位后暂停读取并告警；源文件或 journal 自身被轮转淘汰导致的损失必须可观测。
- 用户配置的任意字段不能直接成为 data stream 名或动态 mapping 根字段。
- 不允许通过查询 API 访问 Ongrid 日志数据流之外的索引。

## 非功能需求

- **兼容性**：第一期支持 Elasticsearch 8.16+；不声明 OpenSearch、ES 7、Cloud ID 或原始 DSL 支持。
- **安全**：HTTPS 默认开启；凭证加密存储、最小权限、可轮换且不进入普通插件快照、参数和日志。
- **可靠性**：配置原子写入并在重启前校验；失败保持上一份工作配置。
- **性能**：默认查询窗口 1 小时、结果上限 1000；后端使用 PIT + `search_after`，禁止无界深分页。
- **可观测性**：暴露 receiver、exporter、队列、存储、generation、最后成功时间和错误类别。
- **运维**：保存与选择分离；选择只依赖 Manager 侧验证，切换后独立检查设备连接，不做生产双写或自动回退。

## API 变更

- 新增日志后端保存、测试、选择 Elasticsearch、选择 Loki API；选择 Elasticsearch 时完成 Manager 连接与权限检查并立即切换。
- 新增当前日志后端的设备连接检查启动/查询 API；Elasticsearch 使用持久化 backend generation，Loki 使用一次性检查 generation，二者都创建逐 Edge 真实写入探针并返回在线、已验证、失败与离线汇总，不改变当前选中项。
- 新增 `/api/v1/logs/search`、`/histogram`、`/fields`、`/context`。
- 保留现有 `/api/v1/logs/query_range` 和 label API 作为兼容接口。
- 新增 tunnel `write_plugin_secrets` RPC；普通插件快照只增加 backend generation 和非敏感目标信息。

## 数据库变更

- 新增 `log_backends`：保存非敏感配置、凭证引用、状态和 generation。
- 新增 `log_backend_assignments`：只保存独立设备连接检查所需的 Edge 期望/已加载 generation 与探针结果。
- 复用 `secrets` 表保存加密的 Elasticsearch 写、读凭证。
- 所有 schema 变更使用 expand-contract，旧 Manager/Edge 在滚动升级期仍可读取原配置。

## 依赖与阻塞

- 依赖现有凭证库、Edge 插件控制通道和 `otelcol-contrib` 制品。
- 依赖用户准备匹配 `logs-ongrid.*.otel-*` 的 data stream template/生命周期策略。
- journald receiver 为上游 alpha 组件，必须通过产品 Linux 矩阵验证后才能默认启用。
- 无产品决策阻塞项。

## 风险与假设

- Elasticsearch exporter 和 filelog 为 beta、journald 为 alpha；通过固定版本、配置校验、故障注入和上一 Ongrid 发布版本的紧急回退缓解。
- 旧 Promtail positions 不能无损转换为 OTel checkpoint；使用预热 checkpoint 和短重复窗口避免缺口。
- 第一阶段共享 fleet 写 API Key 的泄露影响面较大；写权限必须限定到产品 data stream，后续支持按集群/Edge 分配。
- 外部 ES 容量、ILM、备份由客户负责；产品只做应用前检查和状态展示。

## 验收标准

- [ ] Host journald、文件和 Kubernetes CRI 日志均可由 OTel 收集并写入 ES。
- [ ] 抓包证明外部 ES 日志正文不经过 Manager。
- [ ] 日志页面在 ES 下支持关键字、短语、字段筛选、直方图、上下文和游标分页。
- [ ] 单击直方图柱体可查询对应 bucket；横向拖拽可查询所选绝对时间窗，选区、起止时间和返回上一级范围均有可见反馈。
- [ ] Collector/Edge 重启后 offset/cursor 可恢复，文件轮转测试无从头回放。
- [ ] 断网 30 分钟后队列续传，达到边界时有明确告警且不占满系统盘。
- [ ] 写、读 API Key 不出现在数据库明文、普通快照、进程参数、环境变量、日志或 API 响应。
- [ ] ES 设为当前前 Manager 查询/写入端点及 API Key 权限测试成功；Edge 离线不阻断切换。
- [ ] 设置页明确展示当前日志后端，并可独立检查所有日志采集 Edge 是否应用当前 generation、是否完成真实写探针；离线设备显示待上线确认。
- [ ] Loki/ES 切换前后不产生同一查询内的跨后端归并。
- [ ] 未迁移的启用 LogQL 告警会阻止选择 ES。
- [ ] ES 故障可在 5 分钟内将同一个 OTel 流水线切回内置 Loki。
- [ ] AMD64/ARM64 配置校验、Go race 测试、前端测试、构建和深浅主题截图通过。

## 优先级

P0

## 排期

- 开始：2026-08-18
- 目标完成：2026-09-18

## 任务拆分（PRD → Tasks）

- [ ] Task 1：规格、版本支持矩阵和配置 golden tests。
- [ ] Task 2：统一日志领域模型、查询 API 和 Loki adapter。
- [ ] Task 3：Elasticsearch query adapter、data stream 约束和查询保护。
- [ ] Task 4：后端配置、数据库、加密凭证和互斥选择。
- [ ] Task 5：通用插件密钥投递、Edge 原子加载和连接检查上报。
- [ ] Task 6：OTel Host/Kubernetes receivers、持久化 checkpoint/queue。
- [ ] Task 7：Loki native OTLP 与 Elasticsearch exporter。
- [ ] Task 8：Logs UI 与设置页改造。
- [ ] Task 9：告警、AIOps、Incident 和外部跳转解耦。
- [ ] Task 10：全量选择验证、互斥切换、安装包和运维文档。
- [ ] Task 11：故障注入、E2E、性能与视觉验收。

## 变更记录

| 日期 | 变更人 | 变更内容 | 原因 |
| --- | --- | --- | --- |
| 2026-08-18 | Codex | 初始版本并进入开发 | 用户确认技术方案并要求实现 |
| 2026-08-19 | Codex | 增加直方图单击下钻、拖拽选时与范围回退 | 用户要求补齐高级日志中心的时间钻取能力 |
| 2026-08-20 | Codex | Loki 与 Elasticsearch 改为互斥启用，查询只读当前后端 | 外部 ES 启用时不再查询或展示内置 Loki 历史 |
| 2026-08-20 | Codex | 精简日志后端设置页，隐藏 Edge 明细、凭证高级复用、自定义 CA 与 Kibana 配置 | 默认配置路径只保留启用日志后端所需字段 |
| 2026-08-20 | Codex | 日志中心进入集成设置时将日志集成置顶 | 保留入口上下文，减少定位当前日志后端的滚动和认知成本 |
| 2026-08-20 | Codex | 将 Elasticsearch 配置操作收敛为保存与应用 | 保存只持久化配置，应用时统一完成验证并切换全部 Edge |
| 2026-08-20 | Codex | 普通 Host 日志携带拓扑集群，页面字段统一为 level/cluster 并隐藏逐条 backend | 对齐普通 Edge 集群绑定能力和互斥日志后端语义 |
| 2026-08-21 | Codex | 应用与设备收敛检查解耦 | Manager 验证通过即可切换；新增逐 Edge 当前 generation 与真实写入检查，离线设备待上线确认 |
| 2026-08-21 | Codex | 移除 Elasticsearch Data Stream 高级配置 | 使用产品默认值，避免非专业用户误改数据流命名并导致日志分散 |
| 2026-08-21 | Codex | 将设备连接检查结果收敛为在线验证计数 | 仅展示已验证数/在线总数，降低逐设备状态明细的认知负担 |
| 2026-08-21 | Codex | Loki 支持设备连接检查与动态进度反馈 | 当前后端统一执行真实写入验证，在线计数从 0 推进到全部完成 |
| 2026-08-21 | Codex | Loki 应用与设备探针彻底解耦 | 应用直接切换当前读写后端；真实写入验证只由“检查设备连接”触发 |
| 2026-08-21 | Codex | 日志后端状态收敛为选中/未选中 | 删除 rollout、rollback、shadow 双写及自动回退状态；连接检查保持独立 |

## 上线后复盘

- 实际指标：上线后四周补充。
- 是否达成：待复盘。
- 未达成原因：待复盘。
- 经验教训：待复盘。
- 下一步：评估 per-edge API Key、ES|QL、日志聚类和长期移除 Loki 兼容接口。
