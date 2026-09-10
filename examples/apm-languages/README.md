# 官方 OpenTelemetry 验收应用

Java、Node.js、Python 各提供 HTTP `/orders/42` 和 gRPC Health Check，包含成功、失败和 80ms 慢请求。使用官方 Agent/自动埋点；不实现自研 SDK。JSON 标准输出日志继续交给现有文件采集通道。

从仓库根目录执行 `scripts/apm-test/run-languages.sh`。需要 Docker、Go、Node/npm、Python 3、Java 17+，以及下载锁定依赖的网络权限。脚本将依赖与 Java 构建缓存放在 `/tmp/ongrid-apm-languages`，Python gRPC 代码也在该临时目录生成，不写回源码。

测试使用独立 Collector、Tempo 和 Prometheus，退出自动删除这些测试容器与卷。端口 13200、19090、14319、18080、18081 必须空闲；应用端口可通过 `HTTP_PORT` / `RPC_PORT` 覆盖。默认依次检查开/关 Trace 采样的实例、真实原生指标、链路和告警生命周期。结果写入 `output/apm-acceptance`。

- `APM_TEST_LOAD=1`：增加 100/500/1000 服务的合成指标查询测试。
- `APM_TEST_OVERHEAD=1`：增加无埋点、仅指标、指标加全采样的 HTTP 开销对照，各三轮、每轮十秒。
- `make test-apm-acceptance`：再验证真实请求日志经 Collector 写入 ES 并精确查回，以及 Go HTTP/gRPC、按需 pprof 和相关前端回归。

开销已独立验收时，可用 `APM_TEST_OVERHEAD=0 make test-apm-acceptance` 只重跑功能与容量；开销单独执行 `APM_OVERHEAD_ONLY=1 APM_TEST_OVERHEAD=1 scripts/apm-test/run-languages.sh`。报告必须区分这两个执行结果。

Java、Node.js、Python 示例均提供 HTTP/gRPC 全量请求指标。Java 使用官方 Agent；Python 在自动埋点之外注册官方 `grpcio-observability`；Node.js 在官方自动 Trace 之外，通过 gRPC 服务端拦截器调用官方 Metrics API。验收在开启和关闭 Trace 采样时均检查每协议 12 次请求、3 次失败。旧版本边界见 [历史验收报告](../../docs/test/apm-acceptance-20260908.md)。

使用文件管理配置时，参见 [配置文件接入指南](../../docs/guides/apm-configuration-files.md)：Java Agent properties、Spring Boot YAML、.NET appsettings.json，以及业务配置到 SDK 的映射。

### Python / Node.js gRPC 指标接入

Python 安装与 `grpcio` 同版本的 `grpcio-observability`（示例锁定 1.83.1）。在创建 gRPC 服务和 Channel 前注册 `OpenTelemetryPlugin`，按 [app.py](app.py) 使用官方 `MeterProvider`、OTLP HTTP exporter 和 `View` 配置秒级延迟桶（5ms 到 10s）。插件默认桶不适合毫秒级 P95，不能省略该 View。

已有应用自行初始化 MeterProvider 时，可直接把 View 配在同一个 Provider 并交给插件。示例使用 `opentelemetry-instrument`，其全局 Provider 已在应用启动前初始化、无法通过公开 API 追加 View，因此另建一个仅供 gRPC 插件使用的官方 Provider，使用相同 OTLP 环境变量和标准 Resource；HTTP 指标仍由自动埋点导出。退出时停止服务、注销插件并关闭该 Provider。

继续使用 `opentelemetry-instrument python app.py` 启动。插件导出 `grpc.server.call.duration`；更新后的 Edge / Telemetry Gateway Collector 将其映射为 APM 已支持的 `rpc.server.call.duration` 和对应方法、状态属性，保留秒单位及原始桶。已有 Collector 需更新并重新生成配置；直接绕过该 Collector 的外部指标路径需配置同等映射。

Node.js 复制同目录 [grpc-metrics.cjs](grpc-metrics.cjs)，在创建服务时注册一次：

```javascript
const { grpcMetricsInterceptor } = require('./grpc-metrics.cjs');
const server = new grpc.Server({ interceptors: [grpcMetricsInterceptor] });
```

继续用 `node --require @opentelemetry/auto-instrumentations-node/register app.cjs` 启动，复用其 MeterProvider 和 OTLP exporter。拦截器记录每次调用的终态和秒级耗时，覆盖 unary、流式、取消和超时；不读取 Span，也不补偿采样比例。此拦截器是示例接入代码，不是官方自动埋点自带能力。仅配置环境变量不会注册插件或拦截器；不要对同一请求重复安装指标生产者。

本地检查：先安装该目录的 npm 依赖，再运行 `node --test grpc-metrics.test.cjs`。隔离端到端验收仍使用上面的 `run-languages.sh`。回滚时移除插件/拦截器注册并回退 Collector 配置，已有 HTTP 和 Trace 接入保持原有方式。

## 持续运行的 Edge 演示

`compose.yaml` 持续运行 Go、Java、Python 三种语言，每种两个实例，共六个实例和一个低频请求生成器。其他语言源码及隔离验收保留。用于已有 Edge 的 Linux 主机，OTLP HTTP 接收器需监听 `127.0.0.1:4318`；业务端口仅监听本机，使用官方 SDK/Agent。

```sh
sudo install -d -o 65532 -g 65532 /var/log/ongrid-apm-demo
docker compose -f examples/apm-languages/compose.yaml up -d --build --remove-orphans
```

| 服务 | 实例 | 版本 | HTTP / gRPC 端口 |
| --- | --- | --- | --- |
| apm-demo-go | ubuntu-go-1 | 1.0.0 | 18080 / 18081 |
| apm-demo-go | ubuntu-go-2 | 1.1.0-demo | 18180 / 18181 |
| apm-demo-java | ubuntu-java-1 | 1.0.0 | 18082 / 18083 |
| apm-demo-java | ubuntu-java-2 | 1.1.0-demo | 18182 / 18183 |
| apm-demo-python | ubuntu-python-1 | 1.0.0 | 18086 / 18087 |
| apm-demo-python | ubuntu-python-2 | 1.1.0-demo | 18186 / 18187 |

标准 OTel Resource 使用 `service.name`、`service.namespace=apm-demo`、`deployment.environment.name=development`、`service.version` 和 `service.instance.id`。部署到其他主机时需使用唯一实例名。两个版本使用同一示例构建，版本标签用于演示：请求生成器分别每 20 / 5 组产生一组真实失败，预期错误率约 5% / 20%，不代表代码版本回归。每组后等待 2 秒；包含 HTTP 慢请求，Go gRPC Health 仅模拟成功与失败。

CPU 与内存均为实际测量。Go 复用官方 Prometheus Go/Process collector 经 OTel bridge 导出；Python 使用官方 system metrics instrumentation；Java 使用官方 Agent 的 JVM 指标，并由官方 Collector `hostmetrics/process` 补充进程 RSS。页面 CPU 是占用核数，三种语言均展示进程 RSS；Java 额外展示 JVM 堆与非堆已用内存，两者不能等同。Go 堆与 goroutine、Java 线程数也在运行时图中展示。

`java-rss-v1/v2` 分别共享对应 Java 容器的 PID 命名空间，仅采集名为 `java` 的进程 RSS；不需要宿主机 PID、Docker socket 或特权权限。通过标准资源属性绑定服务、环境、命名空间、实例和版本，沿用 Edge 的 OTLP HTTP 接收器。`java-rss.yaml` 必须与部署的 compose 文件放在同一目录。Compose 更新 Java 容器时会重启其采集器；外部工具单独删除重建 Java 容器后，需同时重建对应 RSS 采集器以重新加入 PID 命名空间。采集间隔 5 秒，已有 JVM CPU 指标不会重复采集。

服务详情可按版本、实例筛选 RED、接口、链路和资源曲线。概览聚焦服务请求表现；“实例与资源”集中展示 CPU、内存及语言运行时趋势图，点击实例继续留在资源页查看该实例。依赖图保持服务级聚合，需要清除版本和实例筛选查看；日志链接明确查看服务全部实例。指标每 5 秒导出，后端仍有短暂延迟。选择最近 15 分钟可查看当前实例，历史服务仍保留在此前时间窗口。

设备 logs 插件需要三个来源，分别使用 `id`/`service_name: apm-demo-<language>`、`include: ["/var/log/ongrid-apm-demo/<language>.log"]`、`parser: json`、`start_at: beginning`，语言为 `go/java/python`。同语言两个实例共用日志文件，日志带版本、实例、Trace ID 和 Span ID。保留其他业务来源。主机需配置 logrotate，例如每日轮转、`maxsize 5M`、`rotate 3`、`compress`、`copytruncate`、`missingok`、`notifempty`、`su 65532 65532`。

检查六个实例的成功、慢请求及预期失败（非预期结果返回非零）：

```sh
docker compose -f examples/apm-languages/compose.yaml exec -T traffic python /app/traffic.py --check
```

停止并保留历史观测数据：

```sh
docker compose -f examples/apm-languages/compose.yaml down
```

当前本地 Ubuntu Edge 部署文件为 `/opt/ongrid-apm-demo/compose.yaml`。停止后可移除对应演示日志来源，保留其他来源。

## 新增语言的能力与验收

| 语言 | HTTP 请求指标 | Trace | 日志关联 | 集成方式 |
| --- | --- | --- | --- | --- |
| C# / .NET | 有 | 有 | 有 | 官方 ASP.NET Core instrumentation 与 OTLP exporter |
| PHP | 有 | 有 | 有 | 官方 Slim 自动 Trace；官方 Metrics API 测量真实请求 |
| C++ | 有 | 有 | 有 | 官方 SDK 与 OTLP exporter，在请求处理处记录 Trace/Histogram |
| Rust | 有 | 有 | 有 | 官方 SDK 与 OTLP exporter，在请求处理处记录 Trace/Histogram；SDK 为 Beta |
| Ruby | 无 | 有 | 有 | 官方 Sinatra 自动 Trace；指标 SDK 尚未稳定 |

C++ SDK 要求 `OTEL_METRIC_EXPORT_TIMEOUT` 小于 `OTEL_METRIC_EXPORT_INTERVAL`，否则会回退到默认 60 秒导出。持续演示使用 1 秒超时、5 秒间隔；验收使用 0.5 秒超时、1 秒间隔。

这里没有自研 SDK。PHP 示例使用 ReactPHP 长驻 worker 保留累计计数，不代表普通 PHP-FPM 每请求新建 SDK 能直接生成连续累计指标。C++、Rust 需要在应用代码初始化 Provider 并埋点；只配环境变量不会自动观测业务请求。新增五种语言当前示例只验证 HTTP，不承诺未测过的 RPC 自动指标。

Ruby 的 HTTP RPS、错误率和延迟从服务端 Trace 样本计算，并标注“Trace 样本”；这不是全量请求指标，也不补偿采样比例。概览、趋势和接口使用相同来源。发现依赖 Tempo span-metrics 处理器，以及 `http.request.method` / `http.method` 和 `telemetry.sdk.language` 维度；新配置只对后续 Span 生效。没有该处理器时仍可在链路页面查询 Trace。请求级告警需另行接入真实请求指标。

先构建新增五个镜像，再运行隔离验收：

```sh
for language in dotnet php cpp rust ruby; do
  docker build -f examples/apm-languages/Dockerfile --target "$language" -t "ongrid-apm-demo-$language:local" .
done
scripts/apm-test/run-more-languages.sh
```

脚本复用现有 Edge Collector 配置生成器、Tempo、Prometheus，测试每种语言的开启/关闭采样实例。每实例发送 40 个真实 HTTP 请求，包含 10 个 500 和 10 个慢请求，断言原生 Histogram 计数不受 Trace 采样开关影响，并验证采样开启时 Trace ID 与 JSON 日志一致。Ruby 明确断言无原生请求指标，同时验证 HTTP Trace 样本的 RED、概览和接口回退；关闭采样时所有语言均无 Trace。输出为 `output/apm-acceptance/more-languages.json`，退出自动清理测试容器和卷。需要空闲端口 13200、19090、14319。

## Go 双版本源码定位演示

2026-09-09 的 Ubuntu 现场通过 `examples/apm-go/compose.versions.yaml` 覆盖部署：
`ubuntu-go-1` 使用 `2.0.0-demo`，`ubuntu-go-2` 使用 `2.1.0-demo`，两个 Tag 对应不同提交和独立编译的二进制。Java/Python 仍保持上表的原版本。

Go 流量改为结算链路 `/checkout/42`：包含订单读取、优惠计算、模拟支付授权与配送报价的子 Span。成功请求使用 `?region=US`，失败请求使用 `?coupon=SAVE20&region=eu&quantity=2`；不会产生真实订单或扣款。

完整版本映射、按 Tag 的验证结果和复现方法见 [Go 双版本验证](../../docs/test/apm-go-version-analysis-20260909.md)。仅运行基础 compose 文件仍是同一源码构建，不可把它当作双版本源码验证。

### Java RPC 延迟桶

Agent 2.31.1 的稳定 `rpc.server.call.duration` 使用秒，但默认通用桶会把 80ms 请求放入 0–5s 区间，导致 P95 插值约 4.75s。`java/src/main/java/RpcMetricsConfiguration.java` 通过官方 `AutoConfigurationCustomizerProvider` 注册 SDK View，仅覆盖 `rpc.*.call.duration` 的桶为 5ms–10s。保留官方自动埋点和现有 OTLP、Resource、采样环境变量；不是另一个 SDK，也不修改服务端查询倍率。

```sh
cd examples/apm-languages/java
mvn package
# 将生成的 JAR 作为官方 Java Agent 扩展加载；业务应用仍用自己的 app.jar。
java -Dotel.javaagent.extensions=/path/to/apm-java-1.0.jar \
  -javaagent:/path/to/opentelemetry-javaagent.jar -jar app.jar
```

示例 Dockerfile 与隔离验收驱动已自动加载扩展，SPI 编译依赖为 `provided`，运行时使用 Agent 自带 SDK。此配置需随 Java 应用重启生效，已有部署不会自动改变。每次升级 Agent 后重跑 `scripts/apm-test/run-languages.sh`，核实桶、计数和关闭采样场景；官方默认桶修复后可移除扩展环境变量及该 View。只移除扩展可回滚，保留其他埋点配置。
