# ADR-033: 复用现有遥测后端建设应用性能监控

Status: Accepted — 用户确认方案并授权实现，2026-09-07。

## 决策

复用应用侧 OpenTelemetry SDK/Agent，经现有 Collector 写入 Tempo、Prometheus 和当前日志后端。APM 以 `(environment, service.namespace, service.name)` 为服务视图身份，实例单独关联。Manager 聚合查询，前端新增「监控告警 → 应用性能」。

2026-09-07 修订：用户确认将请求监控与 Trace 采样解耦，并要求支持 RPC。APM 默认查询应用原生 HTTP/RPC 请求指标，服务关系继续使用 Tempo service-graphs。Trace 派生指标保留为明确选择的样本视图，禁止静默回退或与应用指标相加，请求级告警只使用应用指标。

应用复用官方 OTel SDK/Agent，HTTP 采用 `http.server.request.duration`（秒），RPC 采用 `rpc.server.call.duration`（秒）。兼容模式分别查询旧 `http.server.duration` / `rpc.server.duration`（毫秒；旧 RPC 错误口径限 gRPC），整次查询只选一个指标版本，避免双发重复计数。切换兼容模式是显式操作，不对不同桶边界或单位的直方图混合求分位数。错误使用 error.type，兼容 HTTP 5xx 和 gRPC 非 OK 状态码，原始序列先并集去重再聚合。要求资源属性进入 Prometheus 标签，接口使用路由模板或 RPC 完整方法名。

主机复用 traces 插件的 OTLP 接收器和本机 Prometheus exporter，再由现有 metrics 插件通过已鉴权 tunnel 上报；Kubernetes 复用现有 Telemetry Gateway 的 Metrics remote_write。保持默认监听 loopback，不新增公开写入口或向业务应用下发管理凭据。

## 理由与影响

现有存储和标准 SDK 已覆盖数据采集，新增自研探针、图数据库或另一套 spanmetrics 生成器会增加重复维护。请求级告警用同源 PromQL 模板进入既有 metric_raw 规则流程，不更改旧 Trace 告警含义。

原始数据不写入 APM 数据库，时段内发现的服务不等同永久服务目录。缺失 Span、采样和外部服务识别只能提示，不能恢复未上报内容。保持现有平台级访问范围，不宣称多租户隔离。持续 Profiling 不进入本次范围。

详见 [PRD-006](../requirements/PRD-006-application-performance-monitoring.md) 与 [HLD-002](../design/HLD-002-application-performance-monitoring.md)。
