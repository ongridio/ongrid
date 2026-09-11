# 应用性能：接入、验收与告警排障

适用版本：本分支使用 Tempo 2.10.0、Prometheus 2.54.0、Collector Contrib 0.157.0，复用当前日志后端和 Pyroscope。入口为「监控告警 → 应用性能」。不需要安装 Ongrid 自研语言探针。

## 内置 Manager 标识

启用 Trace 后，`ongrid-manager` 自动上报标准 SDK 名称、语言（`go`）及 SDK 版本，默认 `service.namespace=ongrid`、`deployment.environment.name=internal`，用于区分内置服务。部署时可通过 `OTEL_RESOURCE_ATTRIBUTES` 覆盖命名空间和环境，例如 `service.namespace=ongrid,deployment.environment.name=production`。服务名保持 `ongrid-manager`。

修改资源身份只影响重启后的新 Trace；查询时间窗包含旧数据时，原先“未设置”的服务记录仍可能出现，可筛选环境 `internal` 和命名空间 `ongrid` 查看新数据。

## 1. 接入应用

先启用本机 Edge 的 traces 和 metrics 插件，或在 Kubernetes 安装 Telemetry Gateway。主机的 Collector 在 `127.0.0.1:9464` 暴露应用指标，由 metrics 插件经认证隧道上报；显式关闭的插件不会被自动开启。OTLP 基础地址可用 `http://127.0.0.1:4318`；Docker 使用容器可达地址，K8s 使用网关 Service 的实际 DNS。不要向业务应用分发 Manager/Edge 管理密钥。

在「接入管理」填写语言、目标地址、服务、业务命名空间和环境，复制配置。应用身份为 `(environment, service.namespace, service.name)`；service.namespace 不等同于 K8s namespace。缺失属性会进入“未设置”。接收端将旧 deployment.environment 补为 deployment.environment.name，已有规范属性优先。身份保持稳定，路由使用 `/orders/{id}`，不要使用带参数的原始 URL、用户 ID 或 SQL 作为指标标签。

多副本部署须在 `OTEL_RESOURCE_ATTRIBUTES` 中额外设置每个实例唯一的 `service.instance.id`，例如由部署系统注入 Pod UID。不要把同一个固定示例值复制到所有副本；仅有 `device_id` 只能关联设备，无法区分同机的多个应用实例。用实际部署清单与观测列表逐项核对接入覆盖。

- Java：下载官方 [Java Agent](https://opentelemetry.io/docs/zero-code/java/agent/)，将其路径传给 `-javaagent`。
- Node.js：安装 `@opentelemetry/api` 和 `@opentelemetry/auto-instrumentations-node`，在框架加载前注册。页面命令针对 CommonJS；ESM 按[官方指引](https://github.com/open-telemetry/opentelemetry-js/blob/main/doc/esm-support.md)使用 loader。
- Python：安装 `opentelemetry-distro`、`opentelemetry-exporter-otlp`，执行 `opentelemetry-bootstrap -a install`，使用 `opentelemetry-instrument` 启动应用。
- Go：在现有项目初始化官方 SDK，HTTP/gRPC/DB 客户端分别接入对应 instrumentation。仓库中的 `examples/apm-go` 是最小可运行应用示例，不是新的 SDK。

Go 示例，从仓库根目录执行：

```bash
export OTEL_SERVICE_NAME=apm-go-example
export OTEL_RESOURCE_ATTRIBUTES='service.namespace=trade,deployment.environment.name=development'
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_METRICS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=cumulative
export OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION=explicit_bucket_histogram
export OTEL_SEMCONV_STABILITY_OPT_IN=http,rpc
export OTEL_METRIC_EXPORT_INTERVAL=5000
# 示例支持 0..1；0 会关闭本地根 Span 采样，Metrics 继续记录。
export OTEL_TRACES_SAMPLER_ARG=0
export OTEL_LOGS_EXPORTER=none
go run ./examples/apm-go
# 另一个终端：
curl http://127.0.0.1:18080/orders/42
curl 'http://127.0.0.1:18080/orders/42?fail=1'
curl 'http://127.0.0.1:18080/orders/42?slow=1'
```

示例将 JSON 日志写到标准输出。环境示例固定为 development/trade；在业务应用中应让日志和 Trace 使用同一资源配置。默认 ParentBased 采样会尊重上游 sampled 标志；即使本服务 AlwaysSample，也不能据此确认全链路无采样。示例同时初始化 TracerProvider、MeterProvider 和官方 HTTP/gRPC instrumentation；仅设置环境变量不能替代 Go SDK 初始化。gRPC 标准 Health 服务监听 `127.0.0.1:18081`，空 service 的 Check 成功，未知 service 返回 NotFound。Logs 仍走现有文件采集。

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

这是合并到已有配置的片段，需保留 receivers/processors 及原鉴权。各 exporter 使用独立队列/重试；迁移完成删除旧出口。另外为 Metrics pipeline 配置 Ongrid OTLP 出口，携带相同资源属性。主 RED 来自应用 Metrics，Trace 分支可独立采样；依赖图和 Trace 样本视图仍受采样影响。不要将重复出口或新旧两套指标相加。

## 3. 日志关联

OTLP 日志使用标准 trace_id/span_id；文件或 CRI 日志可写单行 JSON：

```json
{"message":"order handled","trace_id":"1234567890abcdef1234567890abcdef","span_id":"1234567890abcdef","service.name":"orders","service.namespace":"trade","deployment.environment.name":"production"}
```

现有日志采集器会提取有效的 32/16 位十六进制 ID，补充尚未设置的服务属性。采集端配置的服务身份优先，device_id/cluster_id 不从应用日志覆盖。文件采集源应配置正确的 service_name；不要保留“file”一类占位名再期待日志正文覆盖。无需将 ID 加进 Loki 索引标签。

查询通过当前选择的 Loki/Elasticsearch 后端，环境和服务命名空间使用结构化字段；“未设置”匹配空或缺失属性。页面会显示关联筛选，点击日志上的 Trace ID 返回链路。

**已配置 Elasticsearch 的环境继续使用现有 ES 日志通道，无需迁移到 Loki。** Node Agent 的容器/文件日志会按当前日志后端配置写 ES。另需区分：K8s Gateway 的 OTLP 日志出口仍是 Loki，切换查询后端不会自动修改这个独立出口。ES 场景使用已有 Node Agent 的容器/文件日志采集（按当前日志后端下发的配置写 ES），应用保留 `OTEL_LOGS_EXPORTER=none`，避免写入与查询分离。已有官方 OTel ES 直写管道也可复用，但必须核验该服务身份和 trace_id 确实写到当前查询索引。

## 4. 验收和数据语义

服务列表默认查询全部 HTTP/RPC 应用指标，按服务名、环境、服务命名空间分组；同一服务仅列一行，不展示协议列。环境与命名空间位于搜索框前。点击表头排序：RPS 按服务总速率，错误率按请求加权的服务整体比例，「最高 P95」展示各协议 P95 的最高值，不代表合并后的服务分位数；任一协议 P95 缺失时显示未知。点击服务后，HTTP/RPC 在同一页面分段展示 RED 和重点接口，无协议切换；接口页的两组接口分别排序、分页，保留滚动位置和返回上下文。依赖与实例仍按服务展示。在「接入管理」选择旧版指标格式或 Trace 样本来源；Trace 样本视图单独选择 SERVER/CONSUMER。延迟默认 P95，可切换 P50/P99。调用链沿用绝对时间与完整服务身份，服务级入口包含全部协议，接口入口保留具体协议和接口条件；运行时与按需 pprof 在「实例」，接入诊断在「接入管理」，请求告警由顶部「创建告警」进入，可分别预览 HTTP/RPC 规则。

发送真实成功、失败、慢请求，然后在服务列表选取覆盖请求的时间段。指标生成存在延迟，至少两个 counter 采集点后才能计算 rate。服务列表只发现窗口内存在指标的服务。

服务名旁显示请求指标或 Trace 指标中的 `telemetry.sdk.language`（Prometheus 标签 `telemetry_sdk_language`），例如 Go、Java、Node.js、Python。同一服务跨 HTTP/RPC 观测到的语言去重展示，不改变服务身份或 RED 聚合；未上报此属性时显示“未知”，不会按服务名推断语言。

- 应用指标只统计 HTTP/RPC 服务端完成的请求，客户端/内部 Span 不重复纳入。RPC 流式调用以整个调用完成计数，不是每条消息计数。
- 应用错误率：`error.type` 非空，或 HTTP 5xx；gRPC 另支持非 OK 的 `rpc.response.status_code`，旧版支持非零 `rpc.grpc.status_code`。同一序列先去重再聚合。其他 RPC 协议依赖 SDK 正确设置 `error.type`。这不是自定义业务成功率。没有错误序列但有请求时为 0%，无请求时为 `—`。
- P50/P95/P99 在所选协议内合并兼容直方图后计算，单位 ms；不是各实例分位数平均。请求数由 rate × 窗口估算，原始 counter 才用于固定样本精确计数验收。
- 概览摘要使用整个所选窗口；趋势使用至少 5 分钟滚动窗口。应用指标独立于 Trace 采样，但必须核验所有实例埋点和 Metrics 导出是否完整。Trace 样本视图显示采样覆盖率未知，不用于新建请求级告警。
- 依赖图包含 Tempo 观测边和虚拟外部调用方；丢失配对/采样会形成缺边。它不会覆盖业务拓扑。
- 接入诊断最多抽查 3 条 Trace，核对资源、上下游、缺失父 Span 和第一条样本日志；它是抽样检查，不是自动根因结论。

指标契约（Collector 的资源转标签必须开启，使用 cumulative explicit-bucket histogram）：

| 协议 / 格式 | Prometheus 直方图基名 | 单位 | 接口标签 |
|---|---|---|---|
| HTTP / 当前 | `http_server_request_duration_seconds` | 秒 | `http_request_method` + `http_route` |
| RPC / 当前 | `rpc_server_call_duration_seconds` | 秒 | `rpc_method`，完整 `Service/Method` |
| HTTP / 旧版 | `http_server_duration_milliseconds` | 毫秒 | `http_method` + `http_route` |
| gRPC / 旧版 | `rpc_server_duration_milliseconds` | 毫秒 | `rpc_service` + `rpc_method` |

四种格式分别查询 `_count` 和 `_bucket`。必需资源标签为 `service_name`，环境与命名空间使用 `deployment_environment_name`、`service_namespace`。HTTP/RPC 分别计算指标后按完整服务身份分组；只有请求速率和按请求量加权的错误率可以跨协议汇总，分位数保持协议独立。新旧格式不混合，不回退到 Trace 指标。不同语言版本可能尚未实现当前 RPC 约定，需检查实际导出名称；任意自定义 RPC 指标不能直接套用。

已验证的语言兼容性（2026-09-08）：

| 接入组件 | HTTP 请求指标 | gRPC 请求指标 | HTTP/gRPC Trace 和 JSON 日志关联 |
|---|---|---|---|
| Go 官方 SDK 1.43 / instrumentation 0.68 | 支持 | 支持 | 支持 |
| Java Agent 2.31.1 | 支持 | 支持 | 支持 |
| Node auto-instrumentations 0.80.0 / grpc instrumentation 0.222.0 | 支持 | 注册示例 gRPC 拦截器，使用官方 Metrics API | 支持 |
| Python distro / instrumentation 0.65b0 / SDK 1.44.0 | 支持 | 额外注册官方 grpcio-observability 1.83.1 | 支持 |
| .NET SDK / ASP.NET Core instrumentation 1.18.0 | 支持 | 本示例未验证 | HTTP 支持 |
| PHP SDK 1.15.0 / Slim instrumentation 1.5.0 | 官方 Metrics API 显式记录 | 本示例未验证 | HTTP 支持 |
| C++ SDK 1.28.0 | 官方 Metrics API 显式记录 | 本示例未验证 | HTTP 支持 |
| Rust SDK 0.32.0（Beta） | 官方 Metrics API 显式记录 | 本示例未验证 | HTTP 支持 |
| Ruby SDK 1.13.0 / Sinatra instrumentation 0.30.0 | 未提供 | 未提供 | HTTP 支持 |

Node.js/Python 的 gRPC 全量请求指标需按 [接入示例](../../examples/apm-languages/README.md#python--nodejs-grpc-指标接入) 注册拦截器或官方插件，不能只设置环境变量。Python 官方插件的 `grpc.server.call.duration` 经更新后的 Edge / Gateway Collector 映射为 `rpc.server.call.duration`，`grpc.method` / `grpc.status` 映射为 `rpc.method` / `rpc.response.status_code`，单位和桶保持不变；绕过该 Collector 的外部路径需同等映射。指标独立于 Trace 采样，未注册时仍只有 gRPC Trace。样例与锁定依赖在 `examples/apm-languages`，使用官方 SDK/自动埋点，不手工拼造 OTLP 数据。PHP 使用长驻 worker 保持累计计数；普通 PHP-FPM 需另行设计指标聚合，不能直接照搬。C++/Rust 需在请求处理处初始化并调用官方 SDK，环境变量本身不会自动埋点。

Ruby 的官方指标 SDK 尚未稳定。服务列表通过 SERVER Span 指标发现此类服务。没有原生请求指标时，HTTP 列表、概览、趋势和接口统计回退到 Tempo span-metrics，标注“Trace 样本”；RPS 是样本速率，错误率与延迟只覆盖已采样请求，不推算全量请求或采样比例。已有原生指标时始终优先使用，不叠加样本数据。仅使用包含 `http.request.method` 或 `http.method` 的 SERVER Span（两种属性同时存在也只计一次），排除 RPC、内部和消息消费 Span。发现需 Tempo span-metrics 处理器，并配置上述 HTTP 维度与 `telemetry.sdk.language`；新维度只影响后续 Span，旧数据不会自动补齐。若未启用该处理器，仍可直接在链路页面查询。请求级告警应先接入真实业务指标。

新增五语言示例、能力边界、持续运行与隔离验收命令见 [示例说明](../../examples/apm-languages/README.md)。`scripts/apm-test/run-more-languages.sh` 检查开启/关闭采样实例各 40 个真实请求、25% 错误、慢请求、Histogram、Trace/JSON 日志关联与正式 APM 查询适配器。

自动化验收：

```bash
# 完整验收含多语言、ES、本地 Webhook、容量及埋点开销：
# 需 Docker、Go、Node.js/npm、Python3、JDK17+；首次自动下载固定测试依赖。
make test-apm-acceptance
# 独立容器、仅本机端口；退出清理测试容器/卷。需 Docker + Go。
scripts/apm-test/run.sh
scripts/apm-test/run-logs.sh
scripts/apm-test/run-profiles.sh
# 单元回归：
GOCACHE=/tmp/ongrid-go-build-cache go test -race ./internal/manager/biz/apm ./internal/manager/server/apm ./internal/pkg/logquery ./internal/manager/server/profiles
cd web && npm test -- src/api/apm.test.ts src/pages/Apm.test.tsx src/pages/Logs.test.tsx src/pages/DailyTools.test.tsx src/pages/Traces.test.tsx
```

## 5. 运行时与按需 Profile

运行时页面复用已有官方采集组件，不要求安装 Ongrid SDK。指标需带 `service_name`（或 `service`）、`service_namespace`、`deployment_environment_name`，并建议带 `service_instance_id` 与 `service_version`；所有图表沿用设备、集群、版本、实例筛选。未收到支持的指标时提示检查运行时采集与资源标识，不按服务同名猜测主机指标归属。

| 图表 | 采集来源与口径 |
| --- | --- |
| CPU / RSS | 现有进程或 JVM CPU 累计时间经 `rate` 换算为占用核数；RSS 为进程常驻内存，显示 MiB |
| Go 堆 / Goroutines | 示例使用官方 Prometheus Go collector，经官方 OTel Prometheus bridge 导出 |
| Go 内存分配速率 | `go_memstats_alloc_bytes_total` 的 `rate`，显示 MiB/s；不是当前堆大小或净增长率 |
| Go GC 平均暂停 | `go_gc_duration_seconds_sum` 与 `_count` 分别求 `rate` 再相除，显示 ms；不累加 Summary 的 quantile |
| JVM 堆 / 非堆 | 官方 Java Agent 的 `jvm.memory.used` 按 `jvm.memory.type=heap/non_heap` 分别汇总内存池；无类型标签的旧数据保留“未分类型”图，均不等于 RSS |
| JVM GC 平均耗时 | 官方 `jvm.gc.duration` 的 sum/count 增量比，显示 ms；是 GC 动作耗时，不直接等同于应用暂停时间 |
| JVM 线程 / Node 事件循环 | 沿用已有线程数与事件循环延迟指标，延迟显示 ms |

Gauge 取结束时间前 5 分钟内的最近值；CPU、分配速率和 GC 使用至少 5 分钟的滑动窗口，并先对原始序列求 `rate` 再聚合以处理计数器重置。窗口内没有 GC 时平均耗时为空，曲线保留断点，不显示为 0 ms。默认仅展示收到的指标；Java Agent 的 runtime telemetry 需保持启用，Go 使用现有 collector/bridge 即可，不需要修改业务请求埋点。

来源：[JVM 官方指标语义](https://opentelemetry.io/docs/specs/semconv/runtime/jvm-metrics/)、[Prometheus Go collector](https://github.com/prometheus/client_golang/tree/main/prometheus/collectors)。上述查询结果名称是页面内部标识，不是要求用户额外上报的新指标。

验证：`APM_TEST_PROMTOOL="$PWD/scripts/apm-test/promtool.sh" go test -race -run TestRuntimePromQL ./internal/manager/biz/apm` 检查真实 PromQL 的重置计数、内存池汇总和实例隔离。`APM_RUNTIME_PROMETHEUS=<Prometheus 基础地址> go test -race -v -run TestRuntimeMetricsIntegration ./internal/manager/biz/apm` 只读验证两副本 Go/Java 演示数据；Java 已停止时可用 `APM_RUNTIME_JAVA_END=<Unix 秒>` 明确选取历史窗口，不能把历史验收写成当前在线状态。

实例页合并所选时段内原生请求指标与 Trace 样本中的实例身份，因此关闭 Trace 采样的实例仍可出现。缺少实例 ID 时只使用实际上报的设备/Pod 字段，不按同名补猜；这不是部署实例清单，全部实例是否完成接入还需与业务部署清单核对。

从诊断中的实例跳转「按需 pprof 采集」会预选关联设备和服务身份，采集 URL 留空，必须输入该实例真实、可达的端点再开始。默认查询新采集的最近 15 分钟，可切换为跳转时的绝对历史区间。API 新增可选 start/end、environment、service_namespace、instance_id，旧 range 查询兼容。没有同期 Profile 就是无数据；新采集无法还原历史。pprof 的时间关联不是 Span 级函数归因，也不代表所有语言都支持 pprof。

## 6. 告警处理

服务详情 → 请求告警：错误率或 P95，5 分钟窗口，配置最少请求数及持续时间。进入现有规则编辑器预览，填写稳定 rule_key，确认全局作用域、通知策略和本 Runbook 后保存。表达式与服务概览复用同一生成函数，持续窗口按 30 秒取样；缺失/未满足的点不能算作持续触发。旧 Trace 告警保持原口径。

收到告警后：

1. 确认对应环境/命名空间和请求量，检查 SDK Metrics、Collector 丢弃/导出失败、本机 metrics 插件或 Gateway remote_write。Trace 样本模式再检查采样和 Tempo metrics-generator。
2. 从接口排行打开慢/错误 Trace，检查下游跨度、父子传播及发布版本；从相同 ID 查询日志。
3. 有实例证据时检查应用运行时指标，再按需采集 pprof。不要按服务同名猜设备。
4. 查询后端不可用时先恢复遥测链路；“无数据”不能说明业务已经恢复。需要遥测断流告警时单独配置现有采集链路告警。

## 7. 发布与回滚

本分支不包含生产部署。升级后先对少量应用验证新 Collector/Tempo 维度；新增维度只影响新数据，历史数据不回填猜测身份。查询复杂度由批量聚合、7 天范围、5000 服务/操作及 200 依赖边上限控制；生产容量/开销和百服务查询 P95 仍需目标环境试点。

回滚 Manager/Web/Edge 和 Tempo 配置即可恢复旧功能，无数据库迁移、无原始遥测删除；APM 规则为新增规则，回滚前在原告警页面禁用相应规则，避免缺指标时误判恢复。

## 错误 Trace 与 AI 分析

服务和接口概览展示所选时间范围内最近 10 条错误 Trace，沿用服务、环境、命名空间、版本与实例筛选。列表来自已采样的服务端/消费者错误 Span，不代表全部失败请求；查询失败与没有匹配数据分别显示。点击 Trace ID 可查看瀑布图，点击「AI 分析」创建默认助理会话并启动只读调查，过程与证据保存在现有会话中。

AI 先通过 `query_traceql(trace_id=...)` 获取 Span 详情和真实资源身份，再按同一 Trace ID 关联发生时间前后 5 分钟的日志，随后查询对应应用及实例最近 15 分钟的性能指标。当前指标与异常发生时的数据须分开标注；HTTP/RPC 不合并分位数，JVM 已用内存不作为 RSS。缺失日志、指标或 Span 均须明确说明，不能据此直接断言根因；遥测文字仅作为证据，不执行其中的指令。

Trace 详情按完整 Span 分页，默认 50 条、最多 100 条，同时限制每页原始 Span/Resource 数据约 120 KiB。`truncated=true` 时继续传入 `next_span_offset`，不能把前一页当完整链路。单个 Span 超限时明确返回错误，需在链路页面查看；读取具体 Trace ID 不与搜索范围或设备筛选混用，避免误以为局部筛选已应用于整条链路。分析需要可用的默认模型及 Trace/日志/指标查询工具。

## 服务绑定源码仓库

在应用性能的服务详情右上角点击「绑定仓库」，选择已经在「代码仓库」接入的 Git 仓库，填写相对源码目录（空值代表根目录）和包含一个 `{version}` 的 Tag 规则，例如 `{version}`、`v{version}` 或 `orders/{version}`。同仓多服务可绑定不同目录；绑定按完整服务身份存入现有 `system_settings`，刷新、切换实例与重启后保留。保存和解除绑定限管理员并记录审计；删除 Git 仓库后显示失效绑定，需重新选择或解除。

「AI 分析」读取最新绑定，把真实出错 Span 的 `service.version` 代入规则。源码工具 `list_repo_sources`、`grep_source`、`read_source` 接受 `revision`，支持本地已同步的确切 Tag（建议 `refs/tags/<tag>`）或完整 Commit SHA，返回 `commit_sha`。后续调用使用该 SHA，结果引用提交、文件与行号。工具通过 Git 对象读取，不 checkout，不改变当前同步版本；并发分析不相互切换工作目录。

源码工具不自动拉取远端历史。如果目标 Tag 尚未同步，先在「代码仓库」以该 Tag 同步，或将目标版本同步到受控的本地仓库，再重试分析。找不到目标版本会明确失败，不能省略 `revision` 改读 HEAD。未绑定、绑定仓库被移除、出错 Span 属于其他服务或缺少版本时，AI 仍可分析遥测，但需报告源码证据缺口。代码命中只有在堆栈、请求参数或其他证据支持时才能作为故障原因，不能把 HTTP 500 单独当作精确代码定位。

回滚 Manager/Web 即可恢复旧入口；绑定是独立配置，无表结构变更，也不修改 Git 历史。旧源码工具调用不传 `revision` 时仍读取同步 HEAD。

开发 Compose 使用 `ongrid_repos` 卷保存 `/var/lib/ongrid/repos`。从未挂载仓库目录的旧容器升级时，先备份已有克隆，或升级后重新同步目标 Tag；数据库里的仓库登记和服务绑定不会自动重建 Git 对象。

2026-09-09 本地验收：24 项前端回归、TypeScript/Vite 构建和相关 Go `-race` 测试通过；凭证折叠状态刷新显示真实数量，仓库卡片短名称及绑定弹窗经明暗主题截图检查。Java 服务保存绑定后刷新与 Manager 重建仍可读取；持久化卷中的目标 Tag 在容器重建后保留。只读 AI 会话 `56695614-d3f2-4e5b-add4-0fc494959192` 实际通过 `grep_source` / `read_source` 返回提交 `9cf93884fa5d2f851dec73526bf5774c0e7d3731`，定位 `examples/apm-languages/java/src/main/java/App.java:33` 的显式 500 分支。首次源码读取因旧部署未持久化克隆失败，补充卷并同步后续查成功；日志和运行时指标缺口仍按证据报告，本验收不代表生产容量或完整遥测闭环。

## 接入诊断与日志范围

服务详情的「接入管理」同时显示 HTTP/RPC 接入诊断，沿用当前环境、命名空间、版本、实例、设备、集群与时间范围。检查服务身份、原生指标中的实例 ID、同一 ID 的多位置记录、Prometheus 样本时间，以及最多三条采样链路的上下游和日志关联。缺少 Trace 时，指标与实例检查仍会返回；Trace 相关检查明确显示无法判断。多位置记录可能来自滚动部署，不能直接认定实例 ID 冲突。

`last_metric_timestamp` 是查询结束时 Prometheus 回看范围内的最新样本，既不是最后一次请求时间，也不能保证所有实例持续上报。没有预期部署实例清单，因此覆盖率和上游采样率始终为未知；不要据观测实例数报告 100% 接入。

服务日志已支持 `service_version` / `instance_id` 精确筛选，对应 `service.version` / `service.instance.id`。Loki 使用结构化元数据，Elasticsearch 使用 OTel 资源属性；缺失字段不匹配非空筛选。文件日志需要更新 Edge Collector 配置，将 JSON 中这两个字段提升为资源属性；历史日志不会被回填。筛选无结果时，应先检查实际日志字段。

Java Agent 2.31.1 的 RPC 秒级桶配置见 [语言示例](../../examples/apm-languages/README.md#java-rpc-延迟桶)。扩展随应用重启生效；已有应用需主动更新，不能只更新 Manager。回滚时移除 `OTEL_JAVAAGENT_EXTENSIONS` 或对应 JVM 参数；界面与 Manager 可回退到上一镜像，新字段均为向后兼容的可选字段。

配置文件接入见 [应用配置文件指南](../guides/apm-configuration-files.md)，包含配置读取者、覆盖优先级及容器挂载说明。

## 错误聚合与版本对比

「错误」按接口、异常类型、状态码和堆栈归组，HTTP/RPC 各最多检查最近 50 条已采样错误链路。统计的是当前服务、资源范围、版本/实例、接口和时间窗内匹配的错误 SERVER/CONSUMER Span；同一链路中其他服务或客户端错误不计入。同 Span 去重，组内保留样本首次/最近时间、涉及版本/实例、代表 Trace 和 AI 分析入口。Go 堆栈按函数和源码位置匹配，排除 goroutine 编号、参数地址和 PC 偏移；其他格式目前按原文匹配，代码行号变化可能拆分分组；未提供异常类型或堆栈时不能由分组推断相同根因。

这些数量不是全量失败请求，也不是故障历史首次发生时间。达到采样上限或部分 Trace 详情读取失败会显示不完整提示；全部详情失败显示查询错误。聚合以当前窗口快照展示，点击顶部刷新更新，不自动每 30 秒重复读取链路详情。单个 Trace 详情最多 2 MiB，每次查询最多并发读取 8 条，APM 服务全部错误查询合计最多并发读取 16 条；Tempo 默认客户端保留最多 16 个同主机空闲连接，遵循现有 APM 请求超时和并发限制。

错误聚合响应可携带 `snapshot_id`，同一完整查询翻页时回传该 ID，复用已聚合的全部分组，避免每页重新读取 50 条 Trace。快照绑定当前调用者、服务、时间、协议、版本/实例及解析后的资源范围；有效期 2 分钟，最多保留 8 份、每份 2 MiB。超大结果仍正常返回，只跳过缓存；过期或被淘汰时点击重试重新查询。HTTP/RPC 独立翻页，已查看的快照页在页面内直接复用。顶部刷新及筛选变化不携带旧快照，每次重读 Tempo，纳入新上报、迟到的 Span 和之前读取失败的详情。

「版本对比」在相同服务身份、时间窗口和设备/集群范围内比较两个版本的全部已观测实例，独立展示 HTTP/RPC 请求数、RPS、错误率、P95/P99。支持从接口详情进入，继续使用该接口范围。差值为“对比版本减基准版本”，错误率用百分点；数据缺失保持未知，不作零处理。原生指标与 Trace 样本或不同采样口径之间不计算差值；即使双方都是 Trace 样本，也不能假定采样策略一致或代表全量流量。

默认两个版本只是候选顺序，不自动判断哪个是新发布版本。可手动选择基准/对比版本；明确选择写入 URL，切换服务时清除。不同流量组成可能影响指标，不能仅据差值判定发布导致。版本卡片可跳转对应版本指标、错误和服务日志。

查询减负：非实例页使用 `/api/v1/apm/instances`，只发现筛选所需实例及资源范围，不查询运行时数值或曲线；实例页继续使用 `/runtime`。版本对比使用 `/summary`，复用概览的指标口径和采样回退，但不查询趋势；错误聚合使用 `/error-groups`。这些只读接口复用现有认证、范围校验、超时和并发限额，不新增存储。
