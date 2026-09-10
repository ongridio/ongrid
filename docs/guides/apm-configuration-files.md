# 使用配置文件接入 APM

Ongrid 接收标准 OTLP 数据，应用可以使用环境变量、Agent 配置文件或应用自己的配置系统。页面生成的环境变量是快速接入示例，不是唯一接入方式。先确定启动的是哪个 Agent / SDK，再选择对应格式；把 `OTEL_*` 原样放进任意 YAML 不会自动生效。

## 选择配置入口

| 接入方式 | 文件与读取者 | 接入要求 |
| --- | --- | --- |
| Java `-javaagent` | Java Agent 读取独立 `otel.properties` | 用 JVM 参数指定文件，不依赖 Spring |
| Spring Boot + OTel Starter | Starter 读取 `application.yaml` / `.properties` | 应用必须安装官方 Starter；单独挂 Java Agent 不会因此读取 Spring 配置 |
| .NET SDK | 应用通过 `IConfiguration` 读取 `appsettings.json` | 绑定 exporter options，并注册 Resource、Trace、Metrics 和框架埋点 |
| .NET Framework 自动埋点 | 自动埋点读取 `App.config` / `Web.config` 中支持的 `OTEL_*` | 仅限对应 Framework 接入方式，不等于现代 .NET SDK 的 JSON 绑定 |
| Go、Node.js、Python、PHP、C++、Rust、Ruby SDK | 应用已有的 JSON / YAML / TOML 配置加载器 | 将配置值传入官方 SDK；加载与优先级由应用负责 |
| Collector / Edge / K8s Gateway | Collector 读取自己的管线 YAML | 配置接收、处理、导出；不能代替应用安装埋点或初始化 SDK |

以下例子使用 OTLP HTTP/protobuf，接收端基础地址为 `http://127.0.0.1:4318`。容器内需换成可达的主机或 Gateway 地址。不要把 4317 的 gRPC 地址直接代入 HTTP 示例。

## Java Agent：otel.properties

适用于仓库示例锁定的官方 Agent 2.31.1。保存为 `/etc/myapp/otel.properties`，值不加 shell 引号，也不写 `export`：

```properties
otel.service.name=order-api
otel.resource.attributes=service.namespace=trade,deployment.environment.name=production,service.version=1.2.3,service.instance.id=order-api-01
otel.exporter.otlp.protocol=http/protobuf
otel.exporter.otlp.endpoint=http://127.0.0.1:4318
otel.traces.exporter=otlp
otel.metrics.exporter=otlp
otel.logs.exporter=none
otel.propagators=tracecontext,baggage
otel.semconv-stability.opt-in=http,rpc
otel.exporter.otlp.metrics.temporality.preference=cumulative
otel.exporter.otlp.metrics.default.histogram.aggregation=explicit_bucket_histogram
```

加载文件并启动原来的业务 JAR：

```sh
java -javaagent:/opt/otel/opentelemetry-javaagent.jar \
  -Dotel.javaagent.configuration-file=/etc/myapp/otel.properties \
  -jar /opt/myapp/app.jar
```

每个实例必须有独立的 `service.instance.id`；不要把同一个固定 ID 随镜像复制到全部副本。共享配置文件时可给每个进程单独提供完整的 `otel.resource.attributes`；覆盖该属性时，要保留环境、命名空间、版本等其他条目。

普通 Agent 配置的优先级为 **JVM `-D` > 环境变量 > properties 文件 > SPI 提供的默认属性**。因此，原来的 `OTEL_SERVICE_NAME` 或 `OTEL_RESOURCE_ATTRIBUTES` 仍可能覆盖文件。排查时检查启动参数、容器注入的环境变量和文件路径，不要只检查文件内容。[官方 Agent 配置](https://opentelemetry.io/docs/zero-code/java/agent/configuration/)

需要精确 Java RPC 延迟桶时，另按 [Java RPC View 说明](../../examples/apm-languages/README.md#java-rpc-延迟桶) 构建扩展，并在上述命令中、`-jar` 前追加：

```text
-Dotel.javaagent.extensions=/opt/otel/apm-java-1.0.jar
```

配置文件只改变配置来源，不会自动补上该 View。已有其他扩展时保留原扩展列表。

## Spring Boot Starter：application.yaml

适用于已经通过依赖引入官方 `opentelemetry-spring-boot-starter` 的应用，使用其普通属性配置模式：

```yaml
otel:
  service:
    name: order-api
  resource:
    attributes:
      service.namespace: trade
      deployment.environment.name: production
      service.version: 1.2.3
      service.instance.id: order-api-01
  exporter:
    otlp:
      protocol: http/protobuf
      endpoint: http://127.0.0.1:4318
  traces:
    exporter: otlp
  metrics:
    exporter: otlp
  logs:
    exporter: none
  propagators: tracecontext,baggage
```

由 Spring Boot 按现有方式加载 `application.yaml`，例如外置文件：

```sh
java -jar app.jar --spring.config.additional-location=file:/etc/myapp/application.yaml
```

这是 Starter 路径，不能只把 YAML 加到未安装 Starter 的应用中，也不要为同一应用重复初始化 Starter 和 Agent 的 Provider。资源属性可被环境变量覆盖，其他配置沿用应用的 Spring 配置源规则。Starter 与 Agent 的框架覆盖、RPC 指标和 View 配置需分别核实，不能把 Agent 的扩展 JAR 参数当作 Starter 的配置。[官方 Starter 配置](https://opentelemetry.io/docs/zero-code/java/spring-boot-starter/sdk-configuration/)

## .NET SDK：appsettings.json

在已有 ASP.NET Core SDK 接入中使用应用配置，分别配置两个 HTTP 完整路径：

```json
{
  "OpenTelemetry": {
    "Tracing": { "Endpoint": "http://127.0.0.1:4318/v1/traces", "Protocol": "HttpProtobuf" },
    "Metrics": { "Endpoint": "http://127.0.0.1:4318/v1/metrics", "Protocol": "HttpProtobuf" },
    "Resource": {
      "ServiceName": "order-api",
      "ServiceNamespace": "trade",
      "Environment": "production",
      "Version": "1.2.3",
      "InstanceId": "order-api-01"
    }
  }
}
```

`Program.cs` 中替换已有 SDK 注册块；保留应用路由及其他需要的埋点，不额外注册第二套 Provider。需要官方 Hosting、OTLP exporter、ASP.NET Core instrumentation 包，参见 [现有 .NET 示例](../../examples/apm-languages/dotnet/Program.cs)。以下 `Resource` 字段为应用自定义键，必须显式映射：

```csharp
using OpenTelemetry.Exporter;
using OpenTelemetry.Metrics;
using OpenTelemetry.Resources;
using OpenTelemetry.Trace;

var builder = WebApplication.CreateBuilder(args);
var cfg = builder.Configuration.GetRequiredSection("OpenTelemetry");
var res = cfg.GetRequiredSection("Resource");
builder.Services.Configure<OtlpExporterOptions>("tracing", cfg.GetSection("Tracing"));
builder.Services.Configure<OtlpExporterOptions>("metrics", cfg.GetSection("Metrics"));
builder.Services.AddOpenTelemetry()
    .ConfigureResource(r => r.AddService(res["ServiceName"]!,
        serviceVersion: res["Version"], serviceInstanceId: res["InstanceId"])
        .AddAttributes(new Dictionary<string, object> {
            ["service.namespace"] = res["ServiceNamespace"]!,
            ["deployment.environment.name"] = res["Environment"]!
        }))
    .WithTracing(t => t.AddAspNetCoreInstrumentation().AddOtlpExporter("tracing", _ => { }))
    .WithMetrics(m => m.AddAspNetCoreInstrumentation().AddOtlpExporter("metrics", _ => { }));
```

按应用现有的配置校验机制检查必填身份字段和 URL。`IConfiguration` 的文件、环境变量、命令行覆盖由应用的配置源顺序决定；**显式设置的 exporter options 优先于 SDK 的 OTEL 环境变量默认值**，不能套用 Java Agent 的优先级。使用完整 `/v1/traces` 和 `/v1/metrics` 路径可避免程序化 Endpoint 设置时漏掉信号路径。[官方 exporter 配置](https://github.com/open-telemetry/opentelemetry-dotnet/blob/main/src/OpenTelemetry.Exporter.OpenTelemetryProtocol/README.md)

这段代码没有注册日志 OTLP exporter；现有文件日志采集仍需从相同配置对象写出服务身份、版本、实例及当前 Trace/Span ID。原来只从环境变量读取日志身份的代码，也要改为使用同一个配置对象，否则指标和日志无法按同一身份关联。

## 其他语言：复用业务配置加载器

无需为 Ongrid 新建专用配置格式。可以把如下字段加入应用原来的配置文件；**此 JSON 是业务配置示意，不是 SDK 自动识别的标准文件**：

```json
{
  "telemetry": {
    "endpoint": "http://127.0.0.1:4318",
    "service_name": "order-api",
    "service_namespace": "trade",
    "environment": "production",
    "service_version": "1.2.3",
    "instance_id": "order-api-01"
  }
}
```

在应用启动、载入被埋点框架之前读取并校验配置，然后：

1. 将身份字段映射为标准 Resource 属性：`service.name`、`service.namespace`、`deployment.environment.name`、`service.version`、`service.instance.id`。Trace 和 Metrics 使用相同 Resource，日志也取同一份身份数据。
2. 将 endpoint 和协议传给官方 Trace / Metrics exporter。若 exporter 接口接收完整 HTTP URL，分别使用 `/v1/traces` 与 `/v1/metrics`，不要混用基础地址与信号地址。
3. 注册框架埋点、上下文传播、指标 Reader 和需要的 histogram View，再启动业务服务；退出时 flush/shutdown。配置读取必须发生在 Provider 初始化前，修改文件通常需要重启。

| 语言 | 对接现有 SDK 的位置 |
| --- | --- |
| Go | 配置传给 `otlptracehttp` / `otlpmetrichttp` exporter options 和 `resource.WithAttributes`；沿用 [Go 示例](../../examples/apm-go/main.go) 的初始化及退出处理 |
| Node.js | 在业务模块加载前读取配置，传给官方 `NodeSDK` 的 Resource、trace exporter、metric reader；选择程序化启动后，不再重复加载自动初始化入口 |
| Python | 配置传给官方 Resource、Trace/MeterProvider 和 OTLP exporters；选择手动初始化时，不再由 `opentelemetry-instrument` 先创建全局 Provider |
| PHP / C++ / Rust / Ruby | 复用项目已有配置读取代码，在官方 SDK 初始化处传入资源和 exporter 配置；对应接入示例见 [语言目录](../../examples/apm-languages/) |

程序化初始化方式见 [JavaScript](https://opentelemetry.io/docs/languages/js/instrumentation/)、[Python](https://opentelemetry.io/docs/languages/python/instrumentation/)、[Go](https://opentelemetry.io/docs/languages/go/instrumentation/)。各语言支持的信号和指标边界不因配置文件而改变；Python 的 gRPC 插件、Node.js 的 gRPC 拦截器和 Ruby 示例的 Trace 样本限制仍按 [现有说明](../../examples/apm-languages/README.md) 处理。

## OTEL_CONFIG_FILE 与 Collector YAML

`OTEL_CONFIG_FILE` / Java `-Dotel.config.file=...` 是另一套 **SDK 声明式配置**，不能用它加载 `otel.properties`、Spring 普通 `otel:` 属性树或 Collector 的 `receivers/processors/exporters/service` 管线。Java Agent 从 2.26.0 起支持这条路径，官方仍将 Agent 的支持标为实验性；具体语言及版本必须核实。

启用 SDK 声明式配置后，SDK 的其他环境变量通常不会自动合并，只有在文件中明确引用的变量才用于替换；Agent 专有选项和框架集成可能有额外规则。迁移时要一起迁移资源、采样、exporter、propagator、Reader 与 RPC View，不能只把地址搬进 YAML。本指南的 Java 验证路径是普通 properties 配置，不把声明式 YAML 作为所有语言的通用开关。[SDK 声明式配置](https://opentelemetry.io/docs/languages/sdk-configuration/declarative-configuration/)、[Java Agent 声明式配置](https://opentelemetry.io/docs/zero-code/java/agent/declarative-configuration/)

## 部署与验收

- Docker/Kubernetes：将配置文件只读挂载到应用进程可见路径，并在该进程的启动参数中指定。ConfigMap 挂载本身不会使 SDK 自动读取文件。实例 ID 按副本生成；可由编排系统注入，或使用 SDK 的实例标识机制，不能所有副本共用示例值。
- 修改配置后重启应用，发送真实成功、失败和慢请求，在「服务 → 接入管理」核对身份、实例、指标和链路；按版本/实例检索日志。指标应独立于 Trace 采样，关闭采样不会使请求数归零。
- 配置文件加载失败、权限错误、环境变量覆盖、HTTP 路径错误和重复 SDK 初始化应先排查。恢复上一版文件和启动参数并重启即可回滚。

验证范围：Agent 2.31.1 已通过本地隔离 OTLP 接收器验证 properties 中的资源身份，以及文件、环境变量、JVM 参数三种覆盖结果。运行 `/tmp/ongrid-apm-languages/python/bin/python scripts/apm-test/check-java-properties.py` 可复验（先按语言示例准备 Agent、Java 与 Python 缓存）；测试仅发送探测 Span，不代表完整应用接入。Spring/.NET 配置依据上述官方文档，尚未在本仓库针对文件方式做运行验收，需在应用实际依赖版本上验证。配置文件不会补回历史日志资源属性，也不会自动补齐尚未安装的埋点。
