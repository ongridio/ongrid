# Python / Node.js gRPC 全量指标验收

2026-09-10，本地隔离环境通过。范围为 Python 和 Node.js 的 gRPC 服务端请求指标、Collector 归一化、APM 查询及接入提示；未更新现有部署。

## 实现

- Python：`grpcio-observability==1.83.1` 官方插件；官方 SDK View 配置 5ms–10s 的秒级直方图桶。自动埋点已初始化的 Provider 无法追加 View，因此示例另建一个仅供插件使用的官方 Provider，保持相同 Resource 和 OTLP HTTP 环境配置。
- Node.js：`@grpc/grpc-js==1.14.4` 服务端拦截器调用官方 Metrics API；每次调用记录终态一次，与 Trace 采样无关。无需新增 npm 依赖。
- Collector 0.157.0：仅将 `grpc.server.call.duration` 及其方法、状态属性映射到已有 RPC 查询模型，保留单位、桶和资源身份；无需修改 APM 生产查询实现。
- [接入说明](../../examples/apm-languages/README.md#python--nodejs-grpc-指标接入) 和中英文接入提示已更新。

## 结果

每种语言启动 `sampled` / `unsampled` 两个实例，每实例分别发送 12 次 HTTP 与 12 次 gRPC 请求，包含各 3 次失败、3 次 80ms 慢请求。

| 语言 | 每实例 HTTP / gRPC 计数 | 每协议错误数 | 开采样 Trace | 关采样 Trace |
| --- | --- | --- | --- | --- |
| Java 回归 | 12 / 12 | 3 | 24 | 0 |
| Node.js | 12 / 12 | 3 | 24 | 0 |
| Python | 12 / 12 | 3 | 24 | 0 |

六个实例各输出 24 条请求日志；开启采样时日志 Trace ID 与 Tempo 一致。生产 APM 查询适配器验证三种语言 25% 错误率、准确的 `grpc.health.v1.Health/Check` 方法名，以及该方法下的 sampled/unsampled 两个实例。Node.js/Python gRPC P95 通过 50–1000ms 区间断言；隔离 Prometheus 原始直方图查询均约 90ms。

Node.js 额外真实回环测试通过：unary 成功、NOT_FOUND、取消、超时、两条消息的 server streaming，共 5 次调用、5 条观测；取消后的迟到回调不重复计数。

检查通过：

- `go test -race ./internal/edgeagent/plugins/traces ./internal/manager/biz/apm`
- 独立 Collector / Tempo / Prometheus + `scripts/apm-test/languages.py` + `TestAPMLanguageMetricsIntegration -race`；测试容器、卷和网络已自动清理。
- `node --test examples/apm-languages/grpc-metrics.test.cjs`（使用锁定的 npm 依赖）。
- `Onboarding.test.tsx` 与 `Apm.test.tsx` 共 21 项；最终文案追加复跑 Onboarding 2 项。
- `make build-web`、修改组件的 ESLint、`git diff --check`。
- 独立浏览器预览：Python 中文深色、Node.js 英文浅色，实际查看截图与键盘选择结果；验证空间已关闭。

本次原始日志、计数 JSON 与截图位于 `/tmp/ongrid-grpc-native-results/`。应用测试端口使用 `HTTP_PORT=28080 RPC_PORT=28081`，避免已运行演示占用的默认端口；Java 使用已有锁定 Agent/构建缓存。

## 边界与后续

现有服务需部署更新后的 Collector 并重新生成配置，应用需实际注册 Python 插件或 Node.js 拦截器；只复制环境变量不会启用 gRPC 指标。外部 Collector 路径需同等映射。本次未做现场部署、镜像构建、容量、开销或告警生命周期复验；回滚方式见接入说明。

另测得 Java 既有默认 gRPC 桶较粗：同一 80ms 慢请求用原始桶估算的 P95 约 4750ms。Java 本次仅回归计数、错误率、方法与实例查询，不能据此宣称其毫秒级 gRPC P95 已验收；该既有配置问题需单独处理。
