# APM 接入完整性、Java RPC 桶与日志范围验收

2026-09-10，本地隔离验收。范围为 Java RPC 延迟桶、接入诊断，以及服务日志版本/实例精确筛选。

## 实现与结果

- Java Agent 2.31.1 继续负责自动埋点，通过标准 `AutoConfigurationCustomizerProvider` 为 `rpc.*.call.duration` 注册官方 SDK View；不修改 Collector 桶值或查询倍率。原始默认桶对 80ms 慢请求的 P95 约 4750ms；当前验收为 **95ms**。Node.js 与 Python 回归均为 90ms。三种语言均纳入 50–1000ms 的真实慢请求 P95 断言。
- 每种语言的 sampled / unsampled 实例分别收到 12 次 HTTP 和 12 次 gRPC 请求，每协议 3 次失败；采样开关不改变请求与错误计数。开启采样 24 条服务端 Trace，关闭时 0 条，每实例 24 条请求日志。APM 查询核实 25% 错误率、方法名和两个实例，告警触发、webhook 与恢复回归通过。
- 接入诊断在没有 Trace 后端时仍返回服务身份、原生指标实例和身份缺失结果。重复实例 ID 的多位置记录标为待核实，覆盖率与采样率保持未知；接口返回所选窗口结束时最新 Prometheus 样本时间。页面独立查询 HTTP/RPC，单协议失败不抹掉另一协议结果，支持重试并保留范围。
- 真实 Collector 0.157.0 分别写入隔离 Loki / Elasticsearch；加入其他版本、其他实例及字段缺失的干扰记录，生产日志查询仅返回目标。另用本轮 Java、Node.js、Python 输出日志验证 Elasticsearch 关联。测试容器、卷和网络均由脚本清理。

## 验证

- `HTTP_PORT=28080 RPC_PORT=28081 scripts/apm-test/run-languages.sh`。
- `APM_TEST_LANGUAGE_LOG_DIR=output/apm-acceptance scripts/apm-test/run-logs.sh`。
- `go test -race ./internal/manager/biz/apm ./internal/manager/server/apm ./internal/pkg/logquery ./internal/edgeagent/plugins/logs`。
- 前端全量 61 个文件、326 项测试通过；修改组件 ESLint、`make build-web` 和 Linux ARM64 `make build-ongrid` 通过。
- 本地 Manager 与 Web 已更新为 `dev-apm-completeness`，真实 Go 服务的两个实例、最新指标样本和 Trace/日志诊断可见；浅色中文、深色中文和 768px 英文窄屏均实际查看，窄屏文档宽度为 768px，无页面横向溢出。
- 本地回滚标签为 `ongrid:dev-before-apm-completeness-20260910` / `ongrid-web:dev-before-apm-completeness-20260910`。

## 适用边界

样本时间描述指标存储，不证明所有实例持续接入或应用存活。没有部署实例基准，不计算接入覆盖率；Trace 检查最多三条，不能据此推断整体上下文传播率。此次没有扩展错误分组、版本对比、SLO 或服务图语义，也没有复跑容量与开销测试。

Java 修复已进入示例镜像和验收驱动，部署中的 Java 应用需重新构建/重启后生效。日志新字段需要 Collector 配置更新，历史数据不回填。本地 Manager/页面部署与隔离验收不代表现场生产验收。
