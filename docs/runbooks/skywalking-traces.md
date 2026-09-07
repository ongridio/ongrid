# SkyWalking agent 接入 Trace

开启 edge 现有的 `traces` 插件后，OTLP 和 SkyWalking gRPC 同时接收，经过同一条资源补充、批处理和 OTLP 转发链路进入平台。无需额外的 SkyWalking 开关，也不需要另部署 Collector。

## 接入

1. 升级包含本功能的 edge，确认 `traces` 插件处于运行状态。只升级 manager 不会改变旧 edge 的接收能力。
2. SkyWalking gRPC 默认使用 OTLP gRPC 的监听 IP，端口为 `11800`。普通 edge 默认 `127.0.0.1:11800`；Kubernetes telemetry gateway 为 `0.0.0.0:11800`，Chart 同时提供 Service 端口。
3. 将 SkyWalking Java agent 的 `collector.backend_service`（环境变量 `SW_AGENT_COLLECTOR_BACKEND_SERVICES`）设置为应用可达的 `host:11800`，按应用的发布流程重启以加载配置。
4. 发起一笔业务请求，在平台 Trace 页面按服务名和时间范围查询。

支持边界以 Collector 0.157.0 的 [SkyWalking receiver 文档](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.157.0/receiver/skywalkingreceiver/README.md) 为准：Java agent 8.9.0+，traces 为 beta。此接入只启用 SkyWalking traces，不提供 OAP 的完整管理、日志或 JVM 指标能力，也不会自动双写原 OAP。

## 高级配置和排错

- 默认 localhost 只允许本机应用接入。容器或远程应用需使用可达的监听地址，并按现有网络策略限制来源。
- 如端口被占用，可在已有插件 JSON 配置中设置 `skywalking_grpc_endpoint`，例如 `"127.0.0.1:21800"`。应用上报地址需同步修改；Kubernetes 自定义 Service 对外端口用 `telemetryGateway.service.skywalkingGrpcPort`，其容器目标端口仍为 `11800`。
- SkyWalking 和 OTLP 不能监听同一 IP、同一 TCP 端口。无效配置在渲染/校验阶段拒绝；其他进程占用端口时，Collector 启动失败，具体绑定错误见插件工作目录的 `traces/traces.log`。
- 不要手改生成的 `otelcol.yaml`：配置下发会重新生成它。
- SkyWalking trace ID 会经上游转换器映射为 OTLP trace ID，不能假设原始 ID 可直接用于平台的 trace ID 精确查询；先按服务名定位。

## 回滚

回滚 edge 到升级前版本即可恢复原来的 OTLP-only 配置；如已调整 agent 上报目标，同步恢复到原 OAP。关闭 `traces` 插件会同时关闭 OTLP 与 SkyWalking 接收。

## 开发验证

```sh
ONGRID_TEST_OTELCOL_BINARY=/absolute/path/to/otelcol-contrib go test -race ./internal/edgeagent/plugins/traces
make test-k8s-chart
```

真实 Collector 测试使用 SkyWalking v3 gRPC 报文，验证转换后的 span、父子关系、服务名、设备归属和原有 OTLP 接收。它不替代实际 Java agent 与已部署平台的联调。
