# 应用性能：接入、验收与告警排障

适用版本：本分支使用 Tempo 2.10.0、Prometheus 2.54.0、Collector Contrib 0.157.0，复用当前日志后端和 Pyroscope。入口为「监控告警 → 应用性能」。不需要安装 Ongrid 自研语言探针。

## 1. 接入应用

先启用本机 Edge 的 Trace 接收，或在 Kubernetes 安装 Telemetry Gateway。主机同进程网络可用 `http://127.0.0.1:4318/v1/traces`；Docker 使用容器可达地址，K8s 使用网关 Service 的实际 DNS。不要向业务应用分发 Manager/Edge 管理密钥。

在「接入管理」填写语言、目标地址、服务、业务命名空间和环境，复制配置。应用身份为 `(environment, service.namespace, service.name)`；service.namespace 不等同于 K8s namespace。缺失属性会进入“未设置”。接收端将旧 deployment.environment 补为 deployment.environment.name，已有规范属性优先。身份保持稳定，路由使用 `/orders/{id}`，不要使用带参数的原始 URL、用户 ID 或 SQL 作为指标标签。

- Java：下载官方 [Java Agent](https://opentelemetry.io/docs/zero-code/java/agent/)，将其路径传给 `-javaagent`。
- Node.js：安装 `@opentelemetry/api` 和 `@opentelemetry/auto-instrumentations-node`，在框架加载前注册。页面命令针对 CommonJS；ESM 按[官方指引](https://github.com/open-telemetry/opentelemetry-js/blob/main/doc/esm-support.md)使用 loader。
- Python：安装 `opentelemetry-distro`、`opentelemetry-exporter-otlp`，执行 `opentelemetry-bootstrap -a install`，使用 `opentelemetry-instrument` 启动应用。
- Go：在现有项目初始化官方 SDK，HTTP/gRPC/DB 客户端分别接入对应 instrumentation。仓库中的 `examples/apm-go` 是最小可运行应用示例，不是新的 SDK。

Go 示例，从仓库根目录执行：

```bash
export OTEL_SERVICE_NAME=apm-go-example
export OTEL_RESOURCE_ATTRIBUTES='service.namespace=trade,deployment.environment.name=development'
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces
export OTEL_METRICS_EXPORTER=none
export OTEL_LOGS_EXPORTER=none
go run ./examples/apm-go
# 另一个终端：
curl http://127.0.0.1:18080/orders/42
curl 'http://127.0.0.1:18080/orders/42?fail=1'
curl 'http://127.0.0.1:18080/orders/42?slow=1'
```

示例将 JSON 日志写到标准输出。环境示例固定为 development/trade；在业务应用中应让日志和 Trace 使用同一资源配置。默认 ParentBased 采样会尊重上游 sampled 标志；即使本服务 AlwaysSample，也不能据此确认全链路无采样。配置只开启 Trace，不假设 Logs/Metrics 已启用。

## 2. 已有埋点 / 双目标迁移

已有 OTel 应用复用 SDK，只修改资源和 Collector 目标；不要重复初始化。需要短期保留旧平台时，在可信 Collector 的同一个 Trace pipeline 中配置两个 exporter（使用该 Collector 所在环境可达的接收端）：

```yaml
exporters:
  otlphttp/existing:
    endpoint: https://existing-collector.example
  otlphttp/ongrid:
    traces_endpoint: http://ongrid-telemetry-gateway.ongrid-system.svc:4318/v1/traces
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: [otlphttp/existing, otlphttp/ongrid]
```

这是合并到已有配置的片段，需保留 receivers/processors 及原鉴权。各 exporter 使用独立队列/重试；迁移完成删除旧出口。发送给 Ongrid 的分支必须在会丢弃数据的采样之前，否则 APM 只能显示抽样口径。不要额外开启第二套 spanmetrics 再与 Tempo 的指标相加。

## 3. 日志关联

OTLP 日志使用标准 trace_id/span_id；文件或 CRI 日志可写单行 JSON：

```json
{"message":"order handled","trace_id":"1234567890abcdef1234567890abcdef","span_id":"1234567890abcdef","service.name":"orders","service.namespace":"trade","deployment.environment.name":"production"}
```

现有日志采集器会提取有效的 32/16 位十六进制 ID，补充尚未设置的服务属性。采集端配置的服务身份优先，device_id/cluster_id 不从应用日志覆盖。文件采集源应配置正确的 service_name；不要保留“file”一类占位名再期待日志正文覆盖。无需将 ID 加进 Loki 索引标签。

查询通过当前选择的 Loki/Elasticsearch 后端，环境和服务命名空间使用结构化字段；“未设置”匹配空或缺失属性。页面会显示关联筛选，点击日志上的 Trace ID 返回链路。

**K8s 网关现有 OTLP 日志出口是 Loki 通道。选择 Elasticsearch 不会自动改变它。** ES 场景使用已有 Node Agent 的容器/文件日志采集（按当前日志后端下发的配置写 ES），应用保留 `OTEL_LOGS_EXPORTER=none`，避免写入与查询分离。已有官方 OTel ES 直写管道也可复用，但必须核验该服务身份和 trace_id 确实写到当前查询索引。

## 4. 验收和数据语义

服务列表提供环境、命名空间及时间范围筛选；入口类型在高级筛选中。打开服务后，概览集中展示 RED、重点接口与上下游；延迟默认 P95，可切换 P50/P99。调用链沿用绝对时间与完整服务身份；运行时与按需 pprof 在「实例」，接入诊断在「接入管理」，请求告警由顶部「创建告警」进入原规则编辑器。

发送真实成功、失败、慢请求，然后在服务列表选取覆盖请求的时间段。指标生成存在延迟，至少两个 counter 采集点后才能计算 rate。服务列表只发现窗口内存在指标的服务。

- 请求只统计 SERVER；CONSUMER 通过入口类型单独查看。CLIENT/INTERNAL 不重复纳入入口请求。
- 错误率为 ERROR Span / 入口 Span；非业务成功率。没有错误序列但有请求时为 0%，无请求时为 `—`。
- P50/P95/P99 合并直方图后计算，单位 ms；不是各实例分位数平均。样本请求数由 rate × 窗口估算，原始 counter 才用于固定样本精确计数验收。
- 概览摘要使用整个所选窗口；趋势使用至少 5 分钟滚动窗口。采样覆盖率显示“未知”，不能把观测请求当成已确认的业务总量。
- 依赖图包含 Tempo 观测边和虚拟外部调用方；丢失配对/采样会形成缺边。它不会覆盖业务拓扑。
- 接入诊断最多抽查 3 条 Trace，核对资源、上下游、缺失父 Span 和第一条样本日志；它是抽样检查，不是自动根因结论。

自动化验收：

```bash
# 独立容器、仅本机端口；退出清理测试容器/卷。需 Docker + Go。
scripts/apm-test/run.sh
scripts/apm-test/run-logs.sh
scripts/apm-test/run-profiles.sh
# 单元回归：
GOCACHE=/tmp/ongrid-go-build-cache go test -race ./internal/manager/biz/apm ./internal/manager/server/apm ./internal/pkg/logquery ./internal/manager/server/profiles
cd web && npm test -- src/api/apm.test.ts src/pages/Apm.test.tsx src/pages/Logs.test.tsx src/pages/DailyTools.test.tsx src/pages/Traces.test.tsx
```

## 5. 运行时与按需 Profile

运行时页面读取已有应用 gauge：Go goroutines/heap、process RSS、JVM memory/threads、Node event-loop lag。指标需带 `service_name`（或 service）、`service_namespace`、`deployment_environment_name`，以及可选 `service_instance_id`。没有这些标签时不猜测主机指标属于哪个应用。页面显示结束时间前 5 分钟内的最近值。

从诊断中的实例跳转「按需 pprof 采集」会预选关联设备和服务身份，采集 URL 留空，必须输入该实例真实、可达的端点再开始。默认查询新采集的最近 15 分钟，可切换为跳转时的绝对历史区间。API 新增可选 start/end、environment、service_namespace、instance_id，旧 range 查询兼容。没有同期 Profile 就是无数据；新采集无法还原历史。pprof 的时间关联不是 Span 级函数归因，也不代表所有语言都支持 pprof。

## 6. 告警处理

服务详情 → 请求告警：错误率或 P95，5 分钟窗口，配置最少样本请求数及持续时间。进入现有规则编辑器预览，填写稳定 rule_key，确认全局作用域、通知策略和本 Runbook 后保存。表达式与服务概览复用同一生成函数，持续窗口按 30 秒取样；缺失/未满足的点不能算作持续触发。旧 Trace 告警保持原口径。

收到告警后：

1. 确认对应环境/命名空间和请求量，检查采样、Collector 丢弃/导出失败、Tempo metrics-generator 和 Prometheus remote_write。
2. 从接口排行打开慢/错误 Trace，检查下游跨度、父子传播及发布版本；从相同 ID 查询日志。
3. 有实例证据时检查应用运行时指标，再按需采集 pprof。不要按服务同名猜设备。
4. 查询后端不可用时先恢复遥测链路；“无数据”不能说明业务已经恢复。需要遥测断流告警时单独配置现有采集链路告警。

## 7. 发布与回滚

本分支不包含生产部署。升级后先对少量应用验证新 Collector/Tempo 维度；新增维度只影响新数据，历史数据不回填猜测身份。查询复杂度由批量聚合、7 天范围、5000 服务/操作及 200 依赖边上限控制；生产容量/开销和百服务查询 P95 仍需目标环境试点。

回滚 Manager/Web/Edge 和 Tempo 配置即可恢复旧功能，无数据库迁移、无原始遥测删除；APM 规则为新增规则，回滚前在原告警页面禁用相应规则，避免缺指标时误判恢复。
