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
