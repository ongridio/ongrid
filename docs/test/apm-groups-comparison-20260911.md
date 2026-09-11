# APM 查询减负、错误聚合与版本对比验收

日期：2026-09-11。范围：本地 Manager/Web 与现有 Go 演示数据；不代表生产容量验收。

## 实现与边界

- `/instances` 只做实例发现；运行时指标及曲线仅在实例页查询。`/summary` 只查窗口汇总，版本对比不拉曲线。
- `/error-groups` 每种协议搜索最近最多 50 条已采样错误链路，详情并发 4，每条限制 2 MiB，匹配 Span 限制 10,000。完整服务身份、版本、实例、设备/集群、协议、接口及时间均参与过滤；失败详情和截断会明确提示。
- 错误组计数为匹配的错误 Span 数，时间为样本内首次/最近，不能当作全部失败请求。Go 堆栈归组忽略 goroutine ID、参数地址和 PC 偏移，保留符号与源码行；其他语言暂按堆栈文本匹配。页面手动刷新，避免轮询反复读取详情。
- 版本对比使用相同窗口和资源范围，清除单实例限制，分别请求两个版本的 HTTP/RPC 汇总。不同来源/格式/采样口径不计算差值，缺失值显示破折号。错误率差值单位为百分点。

## 验证

- Go：`go test -race ./internal/manager/biz/apm ./internal/manager/server/apm` 通过。覆盖单次实例发现/汇总查询、跨服务排除、筛选、重复 Span、读取上限、部分失败和 Go 堆栈归一化。
- 前端：APM、ErrorTraces、ErrorGroups、VersionComparison、ServiceMapDependencies 共 5 个定向测试文件、36 项通过；修改文件 ESLint 通过。
- Protobuf 生成、Manager Linux ARM64 镜像、Web TypeScript/Vite 生产构建通过；`git diff --check` 通过。
- 真实浏览器：错误页调用 `/instances`，未调用 `/runtime`；对比页调用 4 次 `/summary`。实测 Go HTTP 最近 50 条错误链路归为 1 组，RPC 当前样本为 0 组；代表链路打开真实详情，显示 `GET /checkout/{id}`、6 个 Span、3 个错误。
- 真实两个版本错误率约 5% 和 20%，交换基准/对比版本后差值由约 +15 变为 -15 个百分点，选择值写入 URL。浅色、深色及 768px 窄屏截图已检查；窄屏表格使用内部横向滚动。
- AI 入口复用已有源码固定与只读分析流程，本轮未运行新的在线模型分析。当前 RPC 错误聚合无真实错误 Trace 样本，RPC 分组逻辑由单测覆盖。

## 本地部署与回滚

使用现有 `ongrid-native-deps` Compose 项目，仅更新 Manager/Web，无数据库迁移。构建版本为 `dev-apm-groups-compare`，部署使用本地 `dev` 标签。

回滚：将 `ongrid:dev-before-apm-groups-compare-20260911` 与 `ongrid-web:dev-before-apm-groups-compare-20260911` 分别重新标记为各自的 `dev` 标签，再使用原 `deploy/docker-compose.yml` 与 `output/native/deps.override.yml` 对 Manager/nginx 执行 `up -d --no-deps --force-recreate`。
