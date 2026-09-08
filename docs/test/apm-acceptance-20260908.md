# APM 补齐验收：2026-09-08

范围：功能分支 `feat/application-performance-monitoring`，补齐多语言真实请求、实例发现、ES 日志关联、告警生命周期和本地容量/SDK 开销验证。保留官方 OpenTelemetry 接入和现有日志后端，不增加存储或自研探针。

## 可重复执行

```bash
make test-apm-acceptance
```

最终执行结果：`APM_OVERHEAD_ONLY=1 APM_TEST_OVERHEAD=1 scripts/apm-test/run-languages.sh` 的 27 轮开销对照通过；随后 `APM_TEST_OVERHEAD=0 make test-apm-acceptance` 全部通过，包括真实语言指标查询、告警、九档容量检查、五组日志样本、Go OTLP/SDK、pprof、TypeScript 和 52 项前端测试。开销与功能分开执行以避免无意义重复；默认命令仍同时包含两者。原始执行日志为本地 `output/apm-acceptance/overhead.log` 和 `acceptance.log`。

后端变更另通过五个相关包的 `-race` 测试、`go vet`、Proto 生成检查；三个修改的前端文件通过 ESLint。Manager/前端 ARM64 生产镜像构建通过。前端测试仍输出已有 React act、Router future 和故障用例日志，不影响 52 项通过结果。

环境要求与独立运行选项见 [语言示例](../../examples/apm-languages/README.md)。测试容器与卷退出清理，构建依赖缓存留在 `/tmp/ongrid-apm-languages`，报告数据留在 `output/apm-acceptance`。不要把集成测试的 Prometheus 地址指向业务环境。

## 验收内容

| 路径 | 验收与边界 |
|---|---|
| Java HTTP/gRPC | 官方 Java Agent 2.31.1，真实成功、失败、慢请求；每实例每协议 12 次、3 次失败；路由模板稳定。 |
| Node/Python HTTP/gRPC | 官方自动埋点的 HTTP 原生指标精确记录请求；gRPC Trace 可关联日志，但这两个锁定版本未提供原生 gRPC 指标。 |
| Trace 采样 | 三语言各启动 sampled/unsampled 两个实例；开启采样收到 24 个服务端 Trace，关闭采样收到 0 个，原生请求计数保持一致。 |
| 实例发现 | 生产 APM 查询用例对真实 Prometheus 查询两个实例，即使禁用 Trace 后端仍返回 sampled/unsampled；环境和 namespace 同时约束查询。 |
| Elasticsearch 日志 | 从三语言真实请求输出提取 JSON 日志，重放到生产生成的 filelog 配置；经 Collector 0.157.0 写 ES 8.16.3，再使用生产 LogqueryAdapter 按完整身份与 Trace ID 查回。另保留 Loki/ES 固定样本隔离检查。 |
| 告警闭环 | 生成错误率模板，经已有用例保存并重新加载规则、解析持久化渠道，真实 PromQL 触发事件，既有通知路由发给本地 HTTP 接收器，记录投递，健康样本使事件恢复。使用测试 SQLite 和可控评估时间，未替代部署环境 MySQL/外部通知验收。 |
| Go 与按需 Profile | 复跑既有真实 OTLP 与官方 Go HTTP/gRPC SDK 集成，以及真实 pprof → Collector → Profiles Gateway → Pyroscope 关联查询。 |
| 前端 | API、APM、Trace、日志、日常工具定向回归与 TypeScript 检查；语言支持边界中英文一致。 |

SDK 和 Collector 并非随意升级：Node 自动埋点 0.80.0、Python SDK 1.44.0 / instrumentation 0.65b0；确切依赖在 package-lock、requirements 和 pom 中。测试服务使用 Javalin/Express/Flask，不能据此宣称每个业务框架均已兼容。

## 本次修复

- 实例列表以前只来自最多三条 Trace 样本；现在合并请求指标的实例身份，采样关闭不会隐藏已有指标的实例。完全未接入或未上报身份的实例仍不能凭空发现，须与实际部署清单核对。
- 共享告警发送路径保留白名单内的应用身份标签；系统生成的事件、规则、设备标签不接受指标覆盖。
- 接入页明确 Node/Python 原生 gRPC 指标的支持边界；不会用采样 Span 替代全量请求指标。

## 容量与开销

方法、数字和适用范围见 [本地压测记录](../ops/loadtest-20260908.md)。这是一台开发机上的有限规模检查；未确定业务 SLO，也未建立生产容量保证。

## 业务试点仍需现场确认

1. 指定业务服务、环境、namespace、实例清单及实际 HTTP/RPC 框架版本；逐实例核对 Metrics 导出和身份。
2. 在业务流量窗口从服务指标进入 Trace，再进入当前 ES 日志，按绝对时间查到同一次请求，并核对部署实例。
3. 指定测试通知渠道，使用低流量试点规则确认部署中的规则保存、触发、实际到达、恢复；验收后删除测试规则。
4. 给出预期服务数、活跃时序、查询并发和可接受的延迟/应用开销，在目标拓扑做持续负载与资源观测，预留至少 30% 容量。

未指定业务仓库/运行服务或外部通知渠道，本轮不对这些状态作完成声明。不包含 RUM、持续 Profiling、移动端或自动根因判定。

## 本地更新与回滚

已在原 `ongrid-native-deps` Compose 项目中将 Manager/前端更新为 `dev-apm-acceptance`，只重建 `ongrid`、`nginx`。无本轮数据库变更；原数据库、ES、Edge、Kubernetes 和遥测后端保留。`/healthz`、`/readyz` 通过，`https://localhost:8443` 返回 200。

浏览器使用同一历史服务 `apm-go-example-native`、`development/trade` 和 2026-09-07 05:58:33–06:58:33 UTC 复查：旧页面实例为空，新页面从原生指标发现设备 `650`，HTTP/RPC 均显示实例指标已观测、零 Trace 样本。原始指标未上报 service.instance.id，所以只显示设备关联，不能据此断言应用副本数。已实看实例页浅/深主题、Node 中文提示和 Python 英文浅色提示；随后恢复中文和跟随系统主题。截图位于本地 `../apm-design-review/26-instances-before.png` 至 `30-python-support-english-light.png`。

回滚在仓库根目录执行：

```bash
docker tag ongrid:rollback-before-apm-acceptance ongrid:dev
docker tag ongrid-web:rollback-before-apm-acceptance ongrid-web:dev
VERSION=dev docker compose -p ongrid-native-deps \
  -f deploy/docker-compose.yml -f output/native/deps.override.yml \
  up -d --no-deps --no-build ongrid nginx
```

回滚后重新检查健康接口与 HTTPS 页面。这里只更新本地开发环境，未发布生产版本。
