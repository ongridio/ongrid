# APM 双实例与资源指标本地验收（2026-09-09）

Ubuntu Edge（设备 650）持续运行 Go、Java、Python 各两个实例；其他语言演示容器已停止，历史观测数据保留。版本为 `1.0.0` / `1.1.0-demo`，实例为 `ubuntu-<language>-1` / `ubuntu-<language>-2`。两个版本来自同一示例构建，分别施加约 5% / 20% 失败的真实请求流量。

## 已验证

- 六个实例均为 running；请求指标和资源指标具备服务、环境、命名空间、版本、实例身份。
- 实时 Prometheus 集成测试验证三个服务各两条 CPU 和内存曲线、正值、历史点，以及按版本和实例隔离后的接口和错误率。首次测试错误率：Go 4.72% / 19.42%，Java 5.26% / 20.59%，Python 5.11% / 19.42%。
- CPU 使用核数。Go/Python 使用进程 RSS，Java 使用 JVM 堆与非堆已用内存，不冒充 RSS。Go 堆、goroutine、Java 线程数同时展示。
- `go test -race ./internal/manager/biz/apm ./internal/manager/server/apm` 通过。
- 在 `web/` 执行 `npx vitest run src/pages/Apm.test.tsx src/api/apm.test.ts`，22 项通过；相关 ESLint 和生产构建通过。
- 本地部署 manager / nginx，Tempo 增加 span-metrics 的版本和实例维度并重启；`/readyz` 返回 ready。维度只对后续 Span 生效。
- 浏览器验证版本与实例下拉联动、资源曲线过滤、TraceQL 链接保留两项过滤；浅色/深色截图实看。实看后修复实例列表未跟随筛选和深色轴标签颜色，加入实例列表回归断言。

## 重跑实时验证

```sh
APM_REPLICAS_PROMETHEUS=http://127.0.0.1:9090/prometheus \
HTTP_PROXY= HTTPS_PROXY= ALL_PROXY= \
go test -race ./internal/manager/biz/apm -run TestAPMReplicaMetricsIntegration -v
```

截图与本地证据位于忽略的 `output/apm-demo/replicas-*.jpg`；构建和测试日志在 `/tmp/apm-more-languages/replicas-*.log`。本报告是本地真实数据验收，不代表生产容量或开销验收。

## 边界与回滚

依赖拓扑仍为服务级聚合，带版本/实例筛选时提示清除筛选；日志入口明确查看全部实例。历史实例和已停服务可能在涵盖其观测时间的窗口出现，不能据此判断容器仍运行。CPU 使用至少 5 分钟滚动窗口，内存最近值最多回看 5 分钟。

部署前镜像保留为 `ongrid:rollback-before-replicas`、`ongrid-web:rollback-before-replicas`，需要时重新标记为 dev 并重建对应容器。Ubuntu 旧演示配置备份为 `/opt/ongrid-apm-demo/compose.before-replicas.yaml`；恢复前先停止当前六实例以释放端口。

## 服务与资源布局调整

概览保留 HTTP/RPC 请求指标、重点接口与依赖；现有实例 tab 改为“实例与资源”，集中展示全部已支持的运行时趋势图。删除最近值明细表，最近值保留在各图图例，窄屏单列、宽屏双列。点击实例保持在资源页。共享版本、实例与时间筛选不变。

参考 [Datadog Service Page](https://docs.datadoghq.com/tracing/services/service_page/) 对服务、基础设施与运行时视图的区分，以及 [ARMS 应用监控](https://www.alibabacloud.com/help/en/arms/application-monitoring/user-guide/application/) 对应用概览与实例/JVM 监控的区分；这里复用现有 tab，不增加嵌套导航。

布局回归：22 项前端测试通过（机器负载下原有九语言切换测试超过默认 5 秒，使用命令行 `--testTimeout=15000` 重跑通过）；随后将实例页链路诊断改为接入管理按需执行，对应两项定向回归通过，ESLint 与构建通过。Ego 浏览器实看 1440px 宽屏双列和 780px 窄屏单列，无整页横向溢出；资源区无表格。截图为 `output/apm-demo/layout-*.png`。

## 依赖与操作区整理

- 依赖 API 过滤 `connection_type=virtual_node`、调用方为 `user` 且环境/命名空间均为空的占位关系，概览和依赖图共用。保留同名真实服务、具备身份的调用方、具名外部服务及数据库；底层遥测保留。无可识别关系时显示空状态，不展示空表。
- 接入管理删除 HTTP/RPC 诊断区，不再自动请求诊断或运行时；诊断 API 保留。概览操作使用统一小尺寸次级按钮外观及链接图标，链接保留原有筛选条件。
- 后端 race 测试与 22 项前端测试通过；相关 ESLint 和构建通过。

## Java 进程 RSS 补充

Java 两个实例增加官方 Collector 0.157.0 `host_metrics/process` 采集器；分别加入对应 Java 容器 PID 命名空间，普通用户、只读文件系统、无 capabilities。只采集 `process.memory.usage`，通过标准资源属性绑定原有服务身份，交给现有 Edge OTLP 链路。JVM 内存保留，Manager 和前端无需修改。

实际验证：两个实例上报 RSS 为 270.76 / 267.98 MiB，随即读取 `/proc/1/status` 为 268.98 / 266.16 MiB，异步采样差异均小于 1%。每个采集器内存约 34–43 MiB（单次快照）。配置校验通过，重启后无采集错误；只静默最小镜像缺少 passwd 数据库导致的无关用户名解析错误。真实数据 race 验收覆盖三种语言的两条 RSS 曲线、Java 两条 JVM 曲线及按版本/实例筛选，全部通过。Ego 浏览器实看 RSS 与 JVM 内存并列显示，截图 `output/apm-demo/java-rss.png`。RSS 历史从此次启用时开始。

回滚：停止 `java-rss-v1`、`java-rss-v2`，恢复 `/opt/ongrid-apm-demo/compose.before-java-rss.yaml` 为 `compose.yaml`；Java 应用及其他采集链路不受影响。异步采样不要求与 `/proc` 瞬时值完全相等。
