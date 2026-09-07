# ADR-033: 复用现有遥测后端建设应用性能监控

Status: Accepted — 用户确认方案并授权实现，2026-09-07。

## 决策

复用应用侧 OpenTelemetry SDK/Agent，经现有 Collector 写入 Tempo、Prometheus 和当前日志后端。APM 以 `(environment, service.namespace, service.name)` 为服务视图身份，实例单独关联。Manager 聚合查询，前端新增「监控告警 → 应用性能」。

Tempo 2.10 的 spanmetrics 是首期请求观测数据源，默认统计 SERVER，CONSUMER 单独选择；指标来源和未知采样覆盖率明确显示。应用原生请求指标与 Trace 采样解耦属于接入建议，不将多个来源重复相加。服务关系使用已有 service-graphs 的双端维度。

## 理由与影响

现有存储和标准 SDK 已覆盖数据采集，新增自研探针、图数据库或另一套 spanmetrics 生成器会增加重复维护。请求级告警用同源 PromQL 模板进入既有 metric_raw 规则流程，不更改旧 Trace 告警含义。

原始数据不写入 APM 数据库，时段内发现的服务不等同永久服务目录。缺失 Span、采样和外部服务识别只能提示，不能恢复未上报内容。保持现有平台级访问范围，不宣称多租户隔离。持续 Profiling 不进入本次范围。

详见 [PRD-006](../requirements/PRD-006-application-performance-monitoring.md) 与 [HLD-002](../design/HLD-002-application-performance-monitoring.md)。
