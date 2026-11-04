# RFC-003：OTel 日志采集与 Elasticsearch 直写

## 元信息

- 状态：已接受
- 日期：2026-08-18
- 更新：2026-08-24
- 关联 PRD：[PRD-004：外部 Elasticsearch 日志中心](../requirements/PRD-004-elasticsearch-log-center.md)
- 关联 ADR：[ADR-031：Edge 直写外部 Elasticsearch](../adr/ADR-031-edge-direct-elasticsearch-logs.md)
- 关联 HLD：[HLD-001：日志采集、存储与查询解耦](../design/HLD-001-log-pipeline-backend-abstraction.md)

## 背景

目标是用 `otelcol-contrib` 统一 Host、Kubernetes 和文件日志采集，同时允许用户在内置 Loki 与外部 Elasticsearch 之间选择一个当前后端。日志正文不经过 Manager，查询方也不接触 Elasticsearch DSL。

## 决策

### 1. 单一采集链路

- logs 和 traces 使用同一固定版本 Collector 制品，但保持独立进程和生命周期。
- Host 使用 journald/filelog，Kubernetes 使用 `/var/log/pods` filelog 和 container parser。
- filelog/journald、exporter 使用持久化 checkpoint、queue 和 retry。
- Edge 配置先写临时文件并校验，成功后原子替换。
- 当前进程一次只配置一个日志 exporter：内置 Loki 或外部 Elasticsearch。

### 2. Elasticsearch 边界

- 支持 Elasticsearch 8.16+ 和 OTel data stream mapping。
- Edge 写 Key 只允许产品 data stream 的 `auto_configure`/`create_doc`。
- Manager 查询 Key 只允许所需的版本探测、`read` 和 `view_index_metadata`。
- Manager 固定 endpoint、data stream pattern、时间窗、字段、limit、超时和响应大小。

认证支持 API Key 和 Basic Auth，默认保留 API Key。Basic Auth 可使用独立的读写账号，也可共用一个同时具备上述读写权限的账号；用户名与密码作为一组加密保存，配置响应不回显。编辑时两项同时留空保留原凭证，切换认证方式必须提供新凭证。保存、连接测试、查询和 Grafana 同步使用同一认证方式。

Basic Auth 的 Edge 写凭证通过独立的 `elasticsearch_basic_auth` secret slot 下发，在本地以 0600 文件保存编码后的 Authorization 值，Collector 通过文件引用读取。启用前需升级 Manager 和所有日志采集 Edge；旧 Edge 会拒绝未知 slot。回滚到旧版本前，先切回 API Key 或 Loki 并确认设备同步；仅保存配置不会切换当前日志链路。

### 3. 简单选择状态

- `log_backends` 只保存 ES 配置及 `selected`/`unselected`。
- 没有选中的 ES 时，Loki 即当前后端。
- 保存与测试连接不切换后端。
- 选择 ES 时验证端点和凭证，成功后直接选中；选择 Loki 时直接取消 ES 选中项。
- 选择请求的成功或失败是唯一切换结果，不维护中间状态，也不自动回退。

### 4. 独立设备连接检查

- 连接检查仅针对当前后端，选择成功后由用户显式发起。
- Manager 为在线 Host Edge 生成探针，展示已验证/在线设备数。
- 离线设备不阻止选择；重连后同步当前选中配置。
- 检查失败只影响检查结果，不修改后端选择。

### 5. 查询面

- Logs UI、告警和 AIOps 使用后端无关的 `SearchRequest`/`SearchResult`。
- Manager 直接调用当前后端适配器；不规划历史后端、不合并结果、不包裹多阶段 cursor。
- Loki adapter 编译产品查询为 LogQL；ES adapter 使用 PIT/search_after 和受限聚合。
- 浏览器与模型均不能提交原始 Elasticsearch DSL。
- 稳定工具 `query_logql` 在 Loki 上执行原生 LogQL；在 ES 上仅编译 stream selector 与 line filter 的安全子集，结果保持各自后端类型。
- ES 分页 cursor 封装 PIT id；自然耗尽、续页失败、Web 刷新/卸载和 AIOps 提前达到公开上限时主动关闭 PIT。

### 6. 字段边界

- 适配器负责把存储字段转换为产品字段；UI 不维护历史别名表。
- 字段面板只展示服务端 allowlist 中、当前结果实际存在的字段。
- OTel 内部字段、`attributes_*`、`resource_attributes_*`、碰撞产生的 `*_extracted` 不进入产品字段目录。
- `service_name` 不作为日志页展示字段；未知的 `unknown_service` 不显示。

### 7. Promtail

- 当前制品只使用 OTel logs pipeline，不保留 Promtail 备用运行路径。
- OTel 复用现有 checkpoint 语义避免进程重启后全量重读。
- 需要紧急回退时回到上一 Ongrid 发布版本，而不是在当前版本维护双采集兼容层。

### 8. 日志告警迁移

- `log_search` 是唯一跨后端日志告警形态；新建接口与 UI 不再产生旧 `log_match`/`log_volume`。
- 选择 Elasticsearch 时在写选中项前扫描旧规则，把可移植 LogQL selector/line filter 编译为同一结构化查询契约，并在单事务内更新全部候选；选择 Loki 仍直接切换。
- 事务提交后必须刷新 evaluator 缓存，再切换当前后端，避免旧 Loki evaluator 在 ES 已选中后把无结果解释成恢复。
- 迁移使用后端无关的 `group_by` 保留 Loki stream 的逐组 incident 语义，host scope 自动包含 `device_id` 分组和约束；安全子集外的自定义 LogQL 仍会让选择请求失败并保留原后端，系统不自动禁用规则，也不伪造兼容结果。

## 不采用的方案

### Manager 中转日志

会扩大 Manager 的带宽、背压和单点故障域，未采用。

### Loki 与 ES 双写或灰度状态机

会引入候选、切换时间线、回滚和重复数据语义，与“只选一个后端”的产品模型冲突，未采用。

### 历史后端联邦查询

会要求跨后端排序、分页和 cursor 协议，并让旧数据影响当前查询语义，未采用。旧数据留在原后端，由外部工具按需访问。

### ES 模拟 Loki 响应

会制造假的 LogQL/Loki 语义并增加长期兼容成本，未采用。两个后端各自返回真实结果类型。

## 后果

### 正面

- Manager 资源不随日志流量增长。
- 状态、失败语义和 UI 操作简单明确。
- 查询路径只有一个当前后端，分页和性能更可控。
- 采集与存储解耦，写、读权限可独立收敛。

### 权衡

- 每个 Edge 必须能访问所选后端。
- Manager 连接测试不能证明 Edge 网络可达，因此保留独立的真实写探针。
- 切换后产品不查询旧后端历史数据。
- at-least-once retry 可能产生重复日志，不承诺 exactly-once。

## 验收

- Loki 与 ES 的保存、测试、选择和设备连接检查相互独立。
- 任一后端被选中后，Edge 配置只包含该后端 exporter。
- Logs 页面搜索、分页、字段值和直方图只访问当前后端。
- `query_logql` 在 Loki 上支持原生 LogQL，在 ES 上正确执行受限日志查询。
- 旧日志告警在选择 Elasticsearch 前迁移为保留 stream/host `group_by` 的 `log_search`；无法映射安全查询子集时切换失败且当前后端不变。
- ES PIT 在分页完成、失败或调用方放弃时被主动释放。
- Go race tests、前端 test/typecheck/build 和真实 Edge→后端验收通过。
