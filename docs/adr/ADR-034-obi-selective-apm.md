# ADR-034：独立控制 OBI 自动发现与按选择采集

Status: Accepted — 用户确认 selective/off 并授权实现，2026-09-14。

## 决策

新增 autoapm 插件，复用现有 Manager 插件配置、鉴权 tunnel、心跳及 subprocess 生命周期。enabled 表示整个自动 APM 开关；targets 为空只运行监听进程发现。候选最多 200 组，目标最多 100 组，只上报路径/端口/PID，不采集进程参数或环境变量。目标必须为确定的绝对路径和非零端口；不接受 glob、正则配置或环境替换符。

选择进程后启动固定版本 OBI v0.12.1 与私有 Collector 0.157.0。OBI 使用 v1 services 的转义完整路径正则与端口 AND 匹配，保留逐目标服务名称；其 v2 迁移不支持逐目标名称，独立 `config validate` 命令也只接受 v2。因此 v1 由 OBI 启动时校验，复用 subprocess readiness 检测启动错误并回滚。所需系统能力设为强制检查，禁止缺权限仍显示成功启动。

OBI 仅向 loopback 的 OTLP 14317/14318 导出。Collector 健康端口 14333、自监控 18888、应用指标 exporter 9465，避免影响已有 SDK Collector。Trace 经现有 Manager URL 与鉴权写入 Tempo；指标复用 custommetrics scraper，经 push_prom_samples 上报，source=obi。环境、device_id、可用的 cluster_id 和 ongrid.instrumentation.source 在 Collector 设置。未添加新的公开接收端口、存储或 spanmetrics 管线。自动 APM 默认验证 Manager 证书，自签名部署可显式勾选跳过验证。

请求指标直接使用 OBI 的 HTTP/RPC 秒制直方图，沿用现有 APM 查询，不从已采样 Trace 计算全量 RED。OBI 开启现有 OTLP 导出进程排除以减少重复采集，但这不是任意 SDK/协议的可靠去重器；已接 SDK 的服务应由用户保持单一采集路径。APM 目前不提供 OBI/SDK 按信号自动择优合并。

## 部署及边界

Linux amd64/arm64，要求内核 BTF 和相应 eBPF 能力。原生服务通常以 root 运行；受限部署需要 CAP_DAC_READ_SEARCH、CAP_SYS_PTRACE、CAP_PERFMON、CAP_BPF、CAP_CHECKPOINT_RESTORE、CAP_NET_ADMIN、CAP_NET_RAW。Kubernetes 需先设置 Helm `node.autoAPM.allowBPF=true` 授予节点权限，再在设备插件中开启。默认 false 不扩展现有节点权限。更新策略禁止 surge，避免同节点两个探针。Kubernetes 元数据自动探测关闭，当前使用用户指定服务身份，不新增 API Server RBAC。

按节点选择进程是本期范围；不提供独立 OBI DaemonSet、工作负载策略或跨节点规则同步。多容器同路径同端口会一起匹配；只采部分工作负载时应等待工作负载选择能力。HTTP/gRPC 支持范围受 OBI、语言、加密方式和内核限制，不承诺所有监听进程均可采集。应用内原有 Trace 上下文和第三方中间件的端到端传播需要环境验收。

启用示例：设备详情 → 插件 → 自动 APM → 开启 → 从候选添加目标 → 设置名称/命名空间/环境 → 保存。检查插件错误及应用性能页面的指标与 Trace。无数据时先检查应用是否有请求、是否已被 SDK 排除、路径/端口是否匹配，再检查 plugins/autoapm/autoapm.log 和 plugins/autoapm/traces/traces.log。关闭不会删除历史数据，服务在旧查询时间范围内继续出现是预期行为。

回滚：关闭自动 APM；保存目标仍在，可重新开启。移除 Helm BPF 授权前先关闭节点插件；如需回滚 Agent，安装旧版本即可，Manager 中额外 autoapm 配置不会被旧 Agent 识别。

## 验证

相关 Go 测试包含 race、严格输入验证、默认关闭/配置保留、空目标不启动采集、关闭清理和恢复；前端测试覆盖显式保存与失败草稿；安装附件测试覆盖校验和、缓存和双架构文件集。

`internal/edgeagent/plugins/autoapm/integration_test.go` 是可选 Linux 原生验收。设置 ONGRID_TEST_AUTOAPM_BIN_DIR 为包含 obi/otelcol-contrib 的目录，在独立 PID/网络命名空间运行 TestOBISelectiveIntegration；需要 BPF 权限。测试启动两个未接 SDK 的 HTTP 进程（选中进程另含 gRPC 服务），选择一个，经真实 OBI/Collector 验证身份、指标、Trace、鉴权，以及关闭/恢复和零采样指标。临时进程、Collector 与探针在结束时清理。

已在 ARM64 Linux 6.19 上通过原生 HTTP/gRPC 验收。最低 Linux 5.15 的实际 BPF 加载、x86 内核运行、多语言应用以及 Kubernetes 现场能力仍需发布前环境验收；编译通过不代替这些验证。

全仓 macOS race 测试除两项既有平台问题外通过：`internal/manager/biz/aiops/tools` 的测试引用 Linux-only cmdpolicy；`internal/edgeagent/upgrademachine` 的路径大小写测试在 macOS /var→/private/var 路径上失败。这两处未修改。`cmd/ongrid-edge` 在 Linux 单独通过全部测试。Manager 所需 ONNX 依赖需要 CGO，不能用 CGO_ENABLED=0 进行整仓交叉构建；本次 Edge 双架构交叉构建与其余包原生构建分别验证。


## 全局发现入口（2026-09-14 修订）

服务页统一管理发现与选择采集。system_settings/platform/auto_apm_enabled
是唯一有效开关（默认 false），PluginConfigUC 在 UI、数据接收 gate、下发快照
三个路径使用同一策略。旧设备 enabled 字段不再改变行为；目标 spec 保留。
关闭不会删目标，新设备无需创建配置行就能发现；设备 60 秒轮询自动生效。
服务页按设备分页读取既有插件发现心跳并编辑目标，复用现有设置鉴权与
edge:plugin 写权限。不新增表、广播任务或全量采集模式。
回滚到上一版前先将全局开关置 false；上一版恢复设备级 enabled 语义，需核对各设备旧值。
