# 官方自动埋点验收应用

Java、Node.js、Python 各提供 HTTP `/orders/42` 和 gRPC Health Check，包含成功、失败和 80ms 慢请求。使用官方 Agent/自动埋点；不实现自研 SDK。JSON 标准输出日志继续交给现有文件采集通道。

从仓库根目录执行 `scripts/apm-test/run-languages.sh`。需要 Docker、Go、Node/npm、Python 3、Java 17+，以及下载锁定依赖的网络权限。脚本将依赖与 Java 构建缓存放在 `/tmp/ongrid-apm-languages`，Python gRPC 代码也在该临时目录生成，不写回源码。

测试使用独立 Collector、Tempo 和 Prometheus，退出自动删除这些测试容器与卷。端口 13200、19090、14319、18080、18081 必须空闲。默认依次检查开/关 Trace 采样的实例、真实原生指标、链路和告警生命周期。结果写入 `output/apm-acceptance`。

- `APM_TEST_LOAD=1`：增加 100/500/1000 服务的合成指标查询测试。
- `APM_TEST_OVERHEAD=1`：增加无埋点、仅指标、指标加全采样的 HTTP 开销对照，各三轮、每轮十秒。
- `make test-apm-acceptance`：再验证真实请求日志经 Collector 写入 ES 并精确查回，以及 Go HTTP/gRPC、按需 pprof 和相关前端回归。

开销已独立验收时，可用 `APM_TEST_OVERHEAD=0 make test-apm-acceptance` 只重跑功能与容量；开销单独执行 `APM_OVERHEAD_ONLY=1 APM_TEST_OVERHEAD=1 scripts/apm-test/run-languages.sh`。报告必须区分这两个执行结果。

当前锁定版本中 Java 提供 HTTP/gRPC 原生请求指标；Node/Python 的 gRPC 自动埋点提供 Trace，未提供原生请求指标。测试明确检查这个边界，不用已采样 Span 冒充全量请求指标。完整记录见 [验收报告](../../docs/test/apm-acceptance-20260908.md)。
