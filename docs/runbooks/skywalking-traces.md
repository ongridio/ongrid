# SkyWalking agent 接入 Trace

开启 edge 现有的 `traces` 插件后，OTLP 和 SkyWalking gRPC/HTTP 默认同时接收，经过同一条资源补充、批处理和 OTLP 转发链路进入平台。两种协议可分别关闭，不需要另部署 Collector。

## 接入

1. 升级包含本功能的 edge，确认 `traces` 插件处于运行状态。只升级 manager 不会改变旧 edge 的接收能力。
2. SkyWalking gRPC 配置默认继承 OTLP gRPC 的监听 IP，端口为 `11800`；HTTP 继承 OTLP HTTP 的监听 IP，端口为 `12800`。发行包使用 `0.157.0-ongrid.1`，修正上游 gRPC 丢弃监听 IP 的问题；gRPC 和 HTTP 均遵守监听 IP。Kubernetes Chart 提供两个 Service 端口。
3. 将 SkyWalking Java agent 的 `collector.backend_service`（环境变量 `SW_AGENT_COLLECTOR_BACKEND_SERVICES`）设置为应用可达的 `host:11800`，按应用的发布流程重启以加载配置。
4. 发起一笔业务请求，在平台 Trace 页面按服务名和时间范围查询。

支持边界以 Collector 0.157.0 的 [SkyWalking receiver 文档](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.157.0/receiver/skywalkingreceiver/README.md) 为准：Java agent 8.9.0+，traces 为 beta。此接入只启用 SkyWalking traces，不提供 OAP 的完整管理、日志或 JVM 指标能力，也不会自动双写原 OAP。

## 高级配置和排错

- 两种协议默认 localhost，只允许本机应用接入；远程应用需配置可达的监听地址，并按网络策略限制来源。
- 如端口被占用，可在已有插件 JSON 配置中设置 `skywalking_grpc_endpoint`，例如 `"127.0.0.1:21800"`。应用上报地址需同步修改；Kubernetes 自定义 Service 对外端口用 `telemetryGateway.service.skywalkingGrpcPort`，其容器目标端口仍为 `11800`。
- SkyWalking 和 OTLP 不能监听同一 IP、同一 TCP 端口。无效配置在渲染/校验阶段拒绝；其他进程占用端口时，Collector 启动失败，具体绑定错误见插件工作目录的 `traces/traces.log`。
- 不要手改生成的 `otelcol.yaml`：配置下发会重新生成它。
- 平台的 trace ID 输入框可直接粘贴日志中的原始 SkyWalking ID，无需先按服务名查找。查询端自动按 Collector 的规则转换并直查 Tempo，不受列表筛选时间窗限制（数据仍须在存储保留期内）。原始 ID 同时保存在 `sw8.trace_id`，详情页支持查看和复制；已有 OTLP ID 仍可照常使用。

## 回滚

回滚 edge 到升级前版本即可恢复原来的 OTLP-only 配置；如已调整 agent 上报目标，同步恢复到原 OAP。关闭 `traces` 插件会同时关闭 OTLP 与 SkyWalking 接收。

## 开发验证

```sh
ONGRID_TEST_OTELCOL_BINARY=/absolute/path/to/otelcol-contrib go test -race ./internal/edgeagent/plugins/traces
make test-k8s-chart
```

真实 Collector 测试使用 SkyWalking v3 gRPC 报文和 HTTP JSON 数组，验证转换后的 span、父子关系、服务名、设备归属、原有 OTLP 接收，以及原始 SkyWalking ID 查询与实际转换结果的一致性。它不替代实际 Java agent 与已部署平台的联调。

## 协议开关

SkyWalking 接收器分别支持 gRPC `11800` 和 HTTP `12800`（`POST /v3/segments`，SkyWalking JSON）。插件面板与 OTLP 一样提供两个开关，默认均开启。`skywalking_receivers.grpc/http` 与 `receivers.grpc/http` 设置为 false 可关闭对应协议；至少保留一个 Trace 接收协议，日志/指标管道需要保留 OTLP。
HTTP 地址通过 `skywalking_http_endpoint` 设置，默认继承 OTLP HTTP 的监听 IP。Kubernetes Service 端口通过 `telemetryGateway.service.skywalkingHttpPort` 设置。

## Collector 监听修复与升级

上游 0.157.0 的 SkyWalking gRPC 接收器忽略 endpoint 的主机部分。`make fetch-otelcol` 从固定且经 SHA256 校验的官方发行版源码构建完整 contrib 组件清单，应用 `dist/patches/skywalking-grpc-bind.patch`，得到 `0.157.0-ongrid.1`。HTTP 实现未修改。

gRPC 配置包含 `require_grpc_bind_host: true` 能力标记。旧上游二进制会在配置校验阶段拒绝此字段，避免只升级 Edge 后意外打开全网卡监听。升级时应同时安装新版 Collector 依赖附件；新版依赖标签包含 `0.157.0-ongrid.1`，旧二进制缓存不会被复用。Kubernetes 镜像和完整升级包使用同一构建入口。

真实 Collector 测试同时验证本机 gRPC/HTTP 上报成功，以及非 loopback 地址无法连接两种协议端口。CI 对相关改动构建修补版并运行该测试。待上游正式修复后，应先通过此测试再移除补丁及能力标记。
