# 官方 OpenTelemetry 验收应用

Java、Node.js、Python 各提供 HTTP `/orders/42` 和 gRPC Health Check，包含成功、失败和 80ms 慢请求。使用官方 Agent/自动埋点；不实现自研 SDK。JSON 标准输出日志继续交给现有文件采集通道。

从仓库根目录执行 `scripts/apm-test/run-languages.sh`。需要 Docker、Go、Node/npm、Python 3、Java 17+，以及下载锁定依赖的网络权限。脚本将依赖与 Java 构建缓存放在 `/tmp/ongrid-apm-languages`，Python gRPC 代码也在该临时目录生成，不写回源码。

测试使用独立 Collector、Tempo 和 Prometheus，退出自动删除这些测试容器与卷。端口 13200、19090、14319、18080、18081 必须空闲。默认依次检查开/关 Trace 采样的实例、真实原生指标、链路和告警生命周期。结果写入 `output/apm-acceptance`。

- `APM_TEST_LOAD=1`：增加 100/500/1000 服务的合成指标查询测试。
- `APM_TEST_OVERHEAD=1`：增加无埋点、仅指标、指标加全采样的 HTTP 开销对照，各三轮、每轮十秒。
- `make test-apm-acceptance`：再验证真实请求日志经 Collector 写入 ES 并精确查回，以及 Go HTTP/gRPC、按需 pprof 和相关前端回归。

开销已独立验收时，可用 `APM_TEST_OVERHEAD=0 make test-apm-acceptance` 只重跑功能与容量；开销单独执行 `APM_OVERHEAD_ONLY=1 APM_TEST_OVERHEAD=1 scripts/apm-test/run-languages.sh`。报告必须区分这两个执行结果。

当前锁定版本中 Java 提供 HTTP/gRPC 原生请求指标；Node/Python 的 gRPC 自动埋点提供 Trace，未提供原生请求指标。测试明确检查这个边界，不用已采样 Span 冒充全量请求指标。完整记录见 [验收报告](../../docs/test/apm-acceptance-20260908.md)。

## 持续运行的 Edge 演示

`compose.yaml` 运行 Go、Java、Node.js、Python、C# / .NET、PHP、C++、Rust、Ruby 九个服务和低频请求生成器。用于已有 Edge 的 Linux 主机，OTLP HTTP 接收器需监听 `127.0.0.1:4318`。镜像使用官方 SDK/Agent，所有业务端口仅监听本机；`network_mode: host` 让请求和 OTLP 直接进入同机 Edge。

```sh
sudo install -d -o 65532 -g 65532 /var/log/ongrid-apm-demo
docker compose -f examples/apm-languages/compose.yaml up -d --build
```

| 服务 | HTTP / gRPC 端口 | 语言 |
| --- | --- | --- |
| apm-demo-go | 18080 / 18081 | Go |
| apm-demo-java | 18082 / 18083 | Java |
| apm-demo-node | 18084 / 18085 | Node.js |
| apm-demo-python | 18086 / 18087 | Python |
| apm-demo-dotnet | 18088 / — | C# / .NET |
| apm-demo-php | 18090 / — | PHP |
| apm-demo-cpp | 18092 / — | C++ |
| apm-demo-rust | 18094 / — | Rust |
| apm-demo-ruby | 18096 / — | Ruby |

环境为 `development`，业务命名空间为 `apm-demo`。每种语言在完成一组请求后等待 2 秒；每 10 组包含 1 组失败和 1 组慢请求。Go 标准 gRPC Health 服务只模拟成功/失败，慢请求在 HTTP 产生。实例以 `ubuntu-<language>-1` 标识，部署到其他主机时应改为唯一实例名。进程自动重启，单服务内存限额 256 MiB（Java 为 512 MiB），CPU 上限 0.5 核。

在设备的 logs 插件配置中追加九个 `sources`，各自使用 `id`/`service_name: apm-demo-<language>`、`include: ["/var/log/ongrid-apm-demo/<language>.log"]`、`parser: json`、`start_at: beginning`。语言文件名为 `go/java/node/python/dotnet/php/cpp/rust/ruby`。保留原有来源与现有日志后端配置。请求日志自带服务身份、Trace ID 和 Span ID；启动诊断可能是普通文本。文件需配置主机 logrotate，例如每日轮转、`maxsize 5M`、`rotate 3`、`compress`、`copytruncate`、`missingok`、`notifempty`、`su 65532 65532`；按主机 timer 周期检查大小。

从应用性能页面查看九个服务，进入服务可跳转链路和日志。指标每 5 秒导出；Edge 采集及后端索引还会带来短暂延迟。刚启动时“最近 1 小时”的平均 RPS 会偏低，可以改用较短时间范围。Node/Python 的 gRPC 原生请求指标边界见上文。

停止（保留历史观测数据）：

```sh
docker compose -f examples/apm-languages/compose.yaml down
```

停止后可从设备 logs 配置移除这九个演示来源；不要删除其他来源。当前本地部署位于 Ubuntu Edge 的 `/opt/ongrid-apm-demo/compose.yaml`，可用 `orb -m ubuntu -u root docker compose -f /opt/ongrid-apm-demo/compose.yaml down` 停止。

运行状态稳定后，可用以下命令检查九种语言各 10 组 HTTP 请求及原有四种语言的 gRPC 请求（包含正常、慢请求和预期失败），非预期结果返回非零退出码：

```sh
docker compose -f examples/apm-languages/compose.yaml exec -T traffic python /app/traffic.py --check
```

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

Ruby 在应用性能列表显示“仅 Trace · 无请求指标”，RPS、错误率和延迟保持空值。发现依赖 Tempo span-metrics 处理器；升级 Tempo 配置后新增 `telemetry.sdk.language` 维度才可显示语言图标。没有该处理器时仍可在链路页面查询 Trace。请求级告警需另行接入真实请求指标。

先构建新增五个镜像，再运行隔离验收：

```sh
docker compose -f examples/apm-languages/compose.yaml build dotnet php cpp rust ruby
scripts/apm-test/run-more-languages.sh
```

脚本复用现有 Edge Collector 配置生成器、Tempo、Prometheus，测试每种语言的开启/关闭采样实例。每实例发送 40 个真实 HTTP 请求，包含 10 个 500 和 10 个慢请求，断言原生 Histogram 计数不受 Trace 采样开关影响，并验证采样开启时 Trace ID 与 JSON 日志一致。Ruby 明确断言无请求指标；关闭采样时所有语言均无 Trace。输出为 `output/apm-acceptance/more-languages.json`，退出自动清理测试容器和卷。需要空闲端口 13200、19090、14319。
