# ADR-034：独立控制 OBI 自动发现与按选择采集

Status: Accepted — 原始决策 2026-09-14；以下早期设计记录中的开关和 Kubernetes 限制已由 2026-09-20 修订替代，当前行为以 [PRD-008](../requirements/PRD-008-obi-selective-apm.md) 为准。

## Pod 注解指标互通（2026-10-09）

默认的 Kubernetes Metrics Scraper 使用已随 Edge 分发的官方 OpenTelemetry
Collector Prometheus Receiver 采集 `prometheus.io/scrape` Pod；端口、路径和协议
沿用 `prometheus.io/port`、`prometheus.io/path`、`prometheus.io/scheme` 约定。
Kubernetes 服务发现、Watch、指标解析、目标去重与重标签由官方组件处理。
Collector 复用现有 subprocess 监督、配置校验、凭据轮换及 remote_write 出口。
Scraper 保持单副本和 Recreate 更新；扩容前必须增加目标分片，不能复制相同配置。
KSM 保持原有采集路径；旧 controller 兼容模式继续使用已有抓取实现。

新安装默认开启 Pod 注解发现，显式设置的旧版或新版 `appDiscovery.enabled=false`
仍有效。Pod 注解是每个应用的采集声明，采集范围与 Auto APM 共用同一组
Namespace/工作负载规则；未选择任何规则时不采集应用指标。Namespace 规则
自动包含该 Namespace 的新 Pod，工作负载规则复用库存的真实 owner 链解析
Pod UID，不使用 Pod 名称前缀猜测归属。KSM 不受应用范围限制。
Manager 通过现有 telemetry-config 下发范围，Controller 同步到现有 Secret，
Scraper 在抓取前按 Namespace/Pod UID 过滤。Controller 默认每分钟同步一次，
随后还需等待 Kubernetes Secret 投影与 Scraper 的 10 秒配置检查；新增工作负载
Pod 还需先完成库存同步。取消选择不是瞬时生效，历史指标不会删除。
缺少范围的旧 Manager/Controller 不会使新版 Scraper 回退为全量采集；应先升级
Manager 和 Controller，再升级 Scraper。已有配置读取失败时沿用最近有效配置。
Scraper 只增加 Pod 的 list/watch 权限，范围通过卷投影读取，不增加 Secret API 读取权限。

应用指标保留业务标签。Prometheus 的 `honor_labels=false` 将与目标身份冲突的
应用标签保存在 `exported_*` 标签中；平台控制 `cluster_id`、`ongrid_source`，
不再统一删除业务 `id`、`instance`、`url` 等维度。应用抓取来源统一为
`ongrid_source="k8s:app-metrics"`。目标状态使用官方 `up` 指标；有 KSM 时
Scraper readiness 继续反映核心采集状态，只有 Pod 发现时反映 Collector 进程健康。
readiness 不代表所有应用端点或 remote_write 出口都成功。
Receiver 将应用的 `target_info` 转为资源属性，remote_write 保持默认的
`target_info` 导出，以保留服务、版本等元数据及 `job`/`instance` 关联。

APM 请求、错误率、延迟和运行时查询排除此原始抓取来源，保留现有 APM 统计口径。
原始指标仍可独立查询。此来源选择是 Ongrid 的产品策略，不是官方通用语义去重：
同名指标的请求范围、单位、桶边界或运行时含义可能不同，不能自动相加。
OBI 保持 `exclude_otel_instrumented_services=true`；其 OTel 导出检测不等同于
识别任意 Prometheus `/metrics` 与 OBI 的指标重叠。

验证示例位于 [examples/prometheus-go](../../examples/prometheus-go/README.md)。
设置 `ONGRID_TEST_OTELCOL_BINARY` 为随 Edge 分发的 Collector 路径后，运行
`go test -race ./cmd/ongrid-edge -run TestPodMetricsPreservesTargetInfo`，
可验证真实抓取和 remote_write 转发后的元数据与关联标签。
官方参考：[Prometheus Receiver](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/receiver/prometheusreceiver)、
[Scraper 分片](https://opentelemetry.io/docs/collector/scaling/#scaling-the-scrapers)、
[OBI 已插桩服务排除](https://opentelemetry.io/docs/zero-code/obi/configure/service-discovery/#exclude-otel-instrumented-services)。

## 当前修订（2026-09-20）

发现常开，空目标只发现。取消全局设置依赖，旧 false 值不阻止发现；停止采集通过清空目标或规则完成。普通设备使用进程路径与端口，日志路径随目标交给现有 logs 插件；Kubernetes 按集群 Namespace 和工作负载选择，范围内全部容器的链路与日志使用同一规则；旧容器限制在共享配置解析时清除。服务身份和环境沿用 PRD 的继承规则。已有设备日志配置保留，运行时投影服务文件源，避免保存两份日志配置。

## 决策

新增 autoapm 插件，复用现有 Manager 插件配置、鉴权 tunnel、心跳及 subprocess 生命周期。enabled 表示整个自动 APM 开关；targets 为空只运行监听进程发现。心跳及界面候选最多 200 组，已选目标的资源采集不受候选展示上限限制；目标最多 100 组，只上报路径/端口/PID，不采集进程参数或环境变量。目标必须为确定的绝对路径和非零端口；不接受 glob、正则配置或环境替换符。

选择进程后启动固定版本 OBI v0.14.0 与私有 Collector 0.157.0。OBI 使用 v1 services 的转义完整路径正则与端口 AND 匹配，保留逐目标服务名称；其 v2 迁移不支持逐目标名称，独立 `config validate` 命令也只接受 v2。因此 v1 由 OBI 启动时校验，复用 subprocess readiness 检测启动错误并回滚。所需系统能力设为强制检查，禁止缺权限仍显示成功启动。

OBI 仅向 loopback 的 OTLP 14317/14318 导出。Collector 健康端口 14333、自监控 18888、应用指标 exporter 9465，避免影响已有 SDK Collector。Trace 经现有 Manager URL 与鉴权写入 Tempo；指标复用 custommetrics scraper，经 push_prom_samples 上报，source=obi。环境、device_id、可用的 cluster_id 和 ongrid.instrumentation.source 在 Collector 设置。未添加新的公开接收端口、存储或 spanmetrics 管线。自动 APM 采集统一使用 Trace 采样比例 1，并允许 Manager 自签名证书（跳过证书验证）。由 Manager 在下发时覆盖历史采样和证书选项，普通设备与 Kubernetes 一致；旧 API 字段保留以兼容旧配置。

请求指标直接使用 OBI 的 HTTP/RPC 秒制直方图，沿用现有 APM 查询，不从已采样 Trace 计算全量 RED。OBI 开启 `exclude_otel_instrumented_services`：没有 SDK Trace 时使用 OBI，观测到该进程成功导出 SDK Trace 后停止 OBI Trace。Trace 与指标独立检测，不因 SDK 仅发送指标而关闭 OBI Trace；运行时和进程资源采集保留。检测结果保持到进程退出，不能保证同进程 SDK 停用后自动回退。检测前已输出的重叠 Trace 另由下述入口缓冲处理；不合并或修改应用指标。

## SDK 与 OBI 首批 Trace 去重（2026-10-10）

用户确认等待窗口为 30 秒。内置 nginx `/v1/traces` 保留原认证、限流和请求上限，改为进入已有 `profiles-gateway` 的官方 Collector 0.157.0；复用其现有 OTLP 接收端及镜像，不新增服务、依赖、公开端口、OBI 源码补丁或 Manager Go 数据流。Profiles 管线保留。SDK Trace 直接转发；另一路同时观察 SDK 与 OBI，在首次收到每组 Trace 后等待 30 秒。组内出现 SDK 时丢弃该组的 OBI；否则输出 OBI。因此无 SDK 时仍有 Trace，延迟约 30 秒加出口批处理时间。

Tempo 指标生成的接收时间窗口设为 2 分钟，服务图的客户端/服务端 Span 配对等待设为 1 分钟，覆盖入口的 30 秒等待、1 秒批处理及后续导出延迟。Tempo 2.10.0 默认的 30 秒接收窗口会拒绝延迟 OBI Span 的指标生成，但仍保存 Trace；默认 10 秒配对等待也不能覆盖 SDK 直通与 OBI 缓冲的到达差。`TestGatewayTempoTiming` 检查这两个配置均大于入口等待加批处理时间。更新后重启 Tempo 生效；回滚这两个时间窗口前须先恢复 nginx 直写 Tempo，否则地图连线仍会缺失。

Collector 的 tail sampling 只能按 TraceID 分组。仅在私有候选管线中，将 TraceID 临时映射为原 TraceID 加身份的 SHA256 前 128 位；身份包含设备或统一集群、服务名称、命名空间、环境和实例。每个身份部分先分别散列，避免分隔符碰撞。Kubernetes 优先用实际 `k8s.pod.uid` 对齐 SDK Pod UID 和 OBI 容器实例名，并沿用服务名称区分同 Pod 的服务。输出 OBI 前恢复原 TraceID、删除临时属性；SDK 直通管线不修改原数据。SpanID、父 SpanID、事件、状态和业务属性保留。同一分布式 Trace 中其他实例或服务的 OBI 不会因上游 SDK 被删除。缺少实例及 Pod UID 的 SDK 不参与抑制。

内存限制 384 MiB、尖峰 64 MiB，候选上限 10000 组、单组 1 MiB，保留与丢弃决定缓存各 100000 项；超出容量可能拒收或淘汰候选，不能把此上限当作无损吞吐承诺。Collector 自监控由内网 Prometheus 抓取，应观察拒收、提前淘汰、采样丢弃和出口失败；在代表性负载下验证容量后才能扩容。共享 Collector 同时承载 Profiles，其内存也计入限制检查。

边界：SDK 和 OBI 必须带一致的身份、TraceID，并进入同一个入口 Collector。SDK 到达超过 30 秒、直接写外部后端、旧 nginx 绕过入口、多个入口未经 Trace 分片、同 Pod 同名服务未提供容器级身份，均不在此去重保证内。SDK 与 OBI 没有共同上下文时不按时间或 URL 猜测删除。该管线不处理 SDK 内部重复插桩，也不消除无 SDK 时 OBI 自己的多层服务端 Span。重启入口会丢弃尚未决策的候选，避免提前发出 OBI 而产生重复；出口仍使用官方有界队列和重试。回滚先恢复 nginx 直写 Tempo，再回滚 Collector 配置；历史重复 Span 不删除。

真实管线回归入口：设置 `ONGRID_TEST_OTELCOL_BINARY` 后执行 `go test -race ./internal/edgeagent/plugins/traces -run TestGatewaySDKPreference`。测试启动原版 Collector，检查延迟 SDK、无 SDK 回退、同 Trace 跨实例/服务/设备/环境隔离、Pod UID 对齐与输出 Span 原值；生产配置的 30 秒窗口另做现场冷启动验收。

本地 30 秒配置验收：普通 Linux 设备上的原生二进制和 Docker bridge 应用各重启三轮，启动脚本和环境配置不变；每轮 PID 与实例 UUID 更新，目标 UUID 保持。共 320 个冷启动请求（各 160 个）均只保留对应 SDK 服务端 Span，SpanID 和父 SpanID 正确，SDK 请求指标和当前进程资源归属通过。另用完全不含 OTel SDK 的 Go 标准库容器发送 20 个请求，15 秒时均未输出，窗口结束后全部由 OBI 输出且每次只有一个服务端 Span。真实 Collector 的 traces 包 race 检查、双架构安装包配置打包检查通过；入口拒收和提前淘汰计数为零。这不是容量压测或 Kubernetes 现场验收。

升级前的运行时指标单独记录：OBI v0.12.1 第一轮和第三轮当前实例的六类 Go 运行时指标通过；第二轮原生进程在新 GC 后等待 90 秒仍未收到 Go 指标，BPF 检查显示该进程的 Go 探针未附着，Docker 同轮正常。第三轮两个进程均恢复，期间未重启 OBI。该三轮记录只证明 Trace 去重，重新附着修复及后续验收见下文。

## Trace 连续性最终验收目标（2026-10-10）

SDK、OBI 及混合接入共用同一验收标准：同一次请求保持一个 TraceID，链内父子引用有效，每个预期操作恰好一份 Span，自定义内部 Span 和 SDK 未覆盖但 OBI 支持的操作不丢失。自动识别覆盖关系，不增加用户配置。客户端和服务端是两个不同操作，不能相互去重。仅有同名、相近时间或相同 URL 不足以证明重复。

验收在采集器就绪、各段已纳入采集且采样一致时进行。矩阵包含纯 SDK、纯 OBI、SDK→OBI、OBI→SDK、同进程混用和仅部分操作使用 SDK；覆盖普通二进制、Docker bridge、Kubernetes、首次请求、长连接及重启，并分别验证 HTTP/1、HTTP/2 和 gRPC。故障、采样丢弃和未支持协议必须单独记录，不能归入通过。检查真实传播头、原始 SpanID、父 SpanID、预期操作数和自定义 Span；不能以页面有数据或服务端 Span 数量减少代替完整链路验收。

官方 OBI v0.14.0 的本地单探针 HTTP/1 实测发现：Go 探针识别已有 SDK `traceparent` 后未清理网络探针待注入状态，网络探针再次写入不同父 SpanID，20/20 请求携带两个传播头。另一个无完整 SDK Provider、仅使用 Go OTel API 的样本同时产生 OBI 和 Auto SDK 服务端 Span。现有按实例丢弃全部 OBI 的入口策略及 OBI 自身按进程排除 SDK 的策略，均不能单独保证上述完整性目标。

用户已授权为此目标进行本地 OBI 补丁构建和验收。候选补丁与测试二进制不进入正式发布依赖；正式发布仍使用官方 Release。补丁构建成功或局部用例通过不代表最终验收通过。原始 Trace 的可重复检查入口为 `python3 scripts/apm-test/trace-continuity.py TRACE.json EXPECTATION.json`；用 `--self-test` 检查断链、重复操作、缺失内部 Span、循环父引用和传播头重复的判定。

第一阶段本地候选补丁已编译双架构 BPF 对象，并在 Linux 6.8 ARM64 实际加载。Kubernetes SDK Pod→原生 OBI 服务在正常权限及移除 CAP_SYS_ADMIN 两种情况下各 20/20 请求通过：传播头唯一、SDK 客户端 SpanID 与线上父 ID 一致、全部父引用有效且预期操作和自定义 Span 保留。移除权限时确认走网络传播回退路径。

第一阶段原生矩阵中，SDK→SDK、SDK→OBI、OBI→SDK、OBI→OBI 共 240/240 请求通过，Auto SDK 及部分 SDK 覆盖共 120/120 请求失败。前者重复服务端与客户端操作；后者丢失 OBI 客户端 Span 并产生下游孤立父引用。该结果作为修复前证据保留。

最终本地候选 `0.14.0-local-sdk-context9` 在已有 Go 探针内读取完整 SDK 的 recording span 上下文，区分 SDK 已覆盖的操作和 SDK 未覆盖的子操作；保留自定义内部 Span，取消对这些子操作的进程级排除。后者携带 `obi.sdk.context=true`，入口 Collector 直接保留，不再因同实例存在 SDK 而整组丢弃。此标记由采集器生成，不增加用户配置；未带标记的官方 OBI 仍使用现有 30 秒决策。回归测试覆盖该标记与 SDK 原始父引用。

候选同时修正 HTTP/1 在大请求头刷新前的传播交接、HTTP/2 协商帧大小和 h2c 服务端起始时间、嵌套 PID 命名空间下的 Go 探针身份与 Auto SDK 激活，以及 parent-based 采样的父上下文。完整 SDK 通过具体构造函数返回值识别 recording span，支持本次移除符号表的 Go 1.25.11 样本。

2026-10-10 完成同一二进制的最终矩阵：Linux 6.8 ARM64 原生进程、OrbStack 6.19 ARM64 Docker bridge、Linux 6.8 ARM64 Kubernetes 各 512/512，共 1536/1536 请求通过。每种环境包含上述四组及 Auto SDK→OBI、部分 SDK→OBI 六组，分别请求 HTTP/1、HTTP/2 h2c、gRPC 下游，各保留首次请求并复用连接、四路并发；另覆盖 SQLite 查询、20 秒请求、8/32/64 KiB 填充头和未采样传播。应用使用 SDK 1.43.0、Auto SDK 1.2.1，候选实际加载于两个 ARM64 内核，BPF 对象生成覆盖 amd64 和 arm64。

验收逐请求比对线上唯一传播头、导出的客户端 SpanID、自定义 Span 的直接父子关系、操作数量和有效时间戳；36 个未采样请求维持 flags=00 且后端无 Span。原生进程已重启，Docker 应用已重新启动，Kubernetes Pod 已重建。本轮误用旧原生配置导致未启用 SQL 的一次失败另行保留；修正测试配置后重跑全部 512 项通过，未通过修改候选代码消除该失败。最终临时探针与测试应用已停止，原五条采集规则已恢复。

2026-10-11 按用户要求将必要的 OBI 源码补丁纳入版本管理：[patches/obi](../../patches/obi/README.md) 包含完整补丁、上游 commit、哈希、构建步骤、许可证和验收摘要。仅部署 Ongrid 侧修改与官方 OBI v0.14.0，不能得到该候选的完整混合链路行为。全新官方 tag 应用补丁后，全部 31 个源码/测试文件与实际验收版本逐字节相同。候选二进制、夹具、原始 Trace、传播日志及离线检查器继续保存在 Git/Docker 构建上下文均忽略的 `output/obi-runtime/trace-continuity-20261010/`。受影响 Go 包 race、ABI 检查、四个针对性 BPF 测试、静态检查及真实 Collector 回归通过。BPF 全套测试在不相关的 `test_failed_connect_event` 上因测试头文件缺少 `__always_inline` 定义而编译失败；不能记为全套通过。

此结果只证明上述本地候选和样本。其他语言、TLS、amd64 实际运行、Linux 5.15、故障/容量压力及其他 SDK/Go ABI 未完成本轮验收；Redis、MongoDB、Kafka 仅验证上下文转换单元测试，未做真实服务端链路验收。最终候选未重跑移除 CAP_SYS_ADMIN 的矩阵；早期 20 请求回退结果不能替代最终候选验证。正式依赖仍是官方 OBI Release，不能将该本地补丁打入发布包或宣称官方版本已修复；发布前需上游修复进入官方 Release，并用该原版重跑矩阵。

## Go 探针重新附着（2026-10-10）

升级固定版本至官方 OBI v0.14.0。v0.12.1 的 `NewExecutable` 查找可复用探针后先释放锁，再提交新进程；并发的 `UnlinkExecutable` 可在两者之间关闭该探针，新进程随后复用已关闭的链接。该竞态与现场“新进程元数据已注册，但 Go uprobes 缺失”的故障一致。v0.14.0 对整个附着过程持有与卸载相同的锁，避免关闭后再提交；同时统一探针关闭的所有权。参见 [v0.12.1 实现](https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation/blob/v0.12.1/pkg/ebpf/tracer_linux.go)、[v0.14.0 实现](https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation/blob/v0.14.0/pkg/ebpf/tracer_linux.go)和 [上游修改](https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation/pull/3418)。

Make 与 Edge Docker 构建使用同一版本，依赖附件标签随 `OBI_VERSION` 更新；仍从官方 Release 下载、校验双架构发布包和分发许可证，不修改 OBI 源码。现有 v1 目标匹配、SDK 自动排除、Collector、服务身份和应用启动配置继续使用。应用重启由 OBI 自动处理，Ongrid 不增加按应用重启探针的逻辑。

官方原版在 Lima Linux 6.8 ARM64 上完成六轮现场验收：原生二进制和 Docker bridge 容器原地重启四轮、容器重建一轮，以及两种应用各连续重启八次后的最终检查一轮。连续重启间隔为 3 秒，遵守测试服务的 systemd 启动限频。OBI PID 全程为 27260；应用启动脚本和环境配置校验和不变，每轮实例 UUID 更新、目标 UUID 保持。每个当前实例均有六类 Go 指标（345 条原始运行时序列）和八条身份正确的进程资源序列；重建后的容器 ID 更新。共 480 个 SDK 请求均无重复服务端 Span，SpanID、父 SpanID、请求数、错误数和业务计数器正确。

新版另用完全不含 OTel SDK 的 Go 标准库容器复验 20 个请求：15 秒时全部仍被入口缓冲，窗口结束后全部由 OBI 输出，每次只有一个服务端 Span。临时容器及其镜像标签已清理，主测试应用及 OBI 进程保持运行。

受影响包的 Linux/macOS race 检查、双架构官方包校验及 Edge 依赖附件/升级包检查通过。保留宿主机 PID 的真实 OBI 集成测试通过 HTTP/gRPC、三次应用重启后的 Go 指标、关闭/恢复及零采样指标检查。另一次让 OBI 自身进入独立 PID 命名空间的测试，在新实例 Go 指标检查处超时；该拓扑仍有兼容问题，不能把宿主机验证推广到它。Docker 应用使用独立 PID/bridge 网络命名空间、由宿主机 OBI 采集的场景已按上述现场测试通过。

部署新版 Edge 依赖后由现有监督器启动新版 OBI。回滚恢复旧 Edge 依赖包并重启 Edge；采集目标和历史数据保留，但旧 OBI 的重新附着竞态会恢复。此升级不代表 Linux 5.15、amd64、多语言或 Kubernetes 的新增现场验收。

## 部署及边界

Linux amd64/arm64，要求内核 BTF 和相应 eBPF 能力。原生服务通常以 root 运行；受限部署需要 CAP_DAC_READ_SEARCH、CAP_SYS_PTRACE、CAP_PERFMON、CAP_BPF、CAP_CHECKPOINT_RESTORE、CAP_NET_ADMIN、CAP_NET_RAW。Kubernetes 节点部署自动准备采集权限，用户保存目标后启动 OBI（节点部署开关已于 2026-09-24 取消，见下文）。更新策略禁止 surge，避免同节点两个探针。Kubernetes 元数据自动探测关闭，当前使用用户指定服务身份，不新增 API Server RBAC。

按节点选择进程是本期范围；不提供独立 OBI DaemonSet、工作负载策略或跨节点规则同步。多容器同路径同端口会一起匹配；只采部分工作负载时应等待工作负载选择能力。HTTP/gRPC 支持范围受 OBI、语言、加密方式和内核限制，不承诺所有监听进程均可采集。应用内原有 Trace 上下文和第三方中间件的端到端传播需要环境验收。

启用示例：设备详情 → 插件 → 自动 APM → 开启 → 从候选添加目标 → 设置名称/命名空间/环境 → 保存。检查插件错误及应用性能页面的指标与 Trace。无数据时先检查应用是否有请求、是否已被 SDK 排除、路径/端口是否匹配，再检查 plugins/autoapm/autoapm.log 和 plugins/autoapm/traces/traces.log。关闭不会删除历史数据，服务在旧查询时间范围内继续出现是预期行为。

回滚：关闭自动 APM；保存目标仍在，可重新开启。移除 Helm BPF 授权前先关闭节点插件；如需回滚 Agent，安装旧版本即可，Manager 中额外 autoapm 配置不会被旧 Agent 识别。

## 验证

相关 Go 测试包含 race、严格输入验证、默认关闭/配置保留、空目标不启动采集、关闭清理和恢复；前端测试覆盖显式保存与失败草稿；安装附件测试覆盖校验和、缓存和双架构文件集。

2026-10-10 早期 SDK 共存验收发现启动窗口：OBI 在 SDK 首批 Trace 导出前也会输出 Trace。固定禁用 OBI Trace 虽能避免重复，但会丢失无 SDK 时的采集，因此撤回该方案；该启动窗口现由前述 30 秒入口决策处理。早期恢复自动模式后，两个 SDK 进程的稳定阶段共 40 个请求均只收到 SDK 服务端 Span；SDK exporter 关闭但仍链接 otelhttp 的进程，20 个请求全部由 OBI 输出，但每次同时含包装层和 HTTP 层两个服务端 Span，唯一性检查未通过。Docker 重建一轮还出现 OBI 未重新附着 Go 运行时探针，重启 OBI 后恢复；这是升级前的故障记录；后续重新附着验收单独覆盖容器重建，不能用容器原地重启替代。

`internal/edgeagent/plugins/autoapm/integration_test.go` 是可选 Linux 原生验收。设置 ONGRID_TEST_AUTOAPM_BIN_DIR 为包含 obi/otelcol-contrib 的目录，保留宿主机 PID，在独立网络/挂载命名空间运行 TestOBISelectiveIntegration；需要 BPF 权限。测试启动两个未接 SDK 的 HTTP 进程（选中进程另含 gRPC 服务），选择一个，经真实 OBI/Collector 验证身份、指标、Trace、鉴权、应用连续重启三次后的六类 Go 指标，以及关闭/恢复和零采样指标。重启检查只接受新实例身份，且要求 OBI PID 不变。临时进程、Collector 与探针在结束时清理。

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

## 设备环境归属（2026-09-20）

设备默认环境提升为 `devices.environment` 可空列，服务端统一解析设备覆盖与拓扑集群继承，再投影到主机 APM 及服务文件日志配置。Kubernetes 使用原有集群配置路径。两处 UI 编辑共用设备 GET/PUT environment 接口，写操作仅管理员可用；清空恢复继承。NULL 标识旧设备尚未导入，空字符串标识已迁移或已清空，避免重启恢复旧采集器默认值。升级保留原 Edge 配置以便回滚；回滚前导出新增设备环境，先回滚 Manager，再执行 down migration。

## 运行时指标（2026-09-20）

普通设备与 Kubernetes 的共用 OBI 渲染启用 `application_runtime`，复用私有 Collector、现有 Prometheus 和实例页，不新增 exporter 或 SDK 注入。查询兼容 OBI 的 OTel 指标命名与既有 SDK 命名；计数器先按实例计算 rate，JVM 按 heap/non_heap 合并内存池，Go CPU 排除 idle 并与 OS 进程 CPU 分开展示。Go GC 暂停与调度直方图均值为桶下界估算，不替代 SDK 的真实时长和分位数。

OBI v0.12.1 的本地 OrbStack 验收发现嵌套 PID 命名空间会影响 Go 运行时目标匹配，以及请求与 JVM 运行时的实例标识对应。该环境需要单独验证兼容修复；启用配置和图表适配本身不能视为所有内核、语言或 Kubernetes 节点已通过运行验收。

### 官方发布与本地测试边界（2026-09-20）

正式发布使用 OpenTelemetry 官方 OBI Release 原版二进制，不修改 OBI 源码、不应用兼容补丁、不发布自维护 fork。版本由 `OBI_VERSION` 固定（当前 0.14.0），通过 `scripts/fetch-obi.sh` 从官方 Release 下载并校验 `SHA256SUMS`；Edge 镜像及依赖附件构建均沿用该入口。

早期 OrbStack PID 命名空间补丁及测试产物保留在已被 Git 与 Docker 构建上下文忽略的 `output/obi-runtime/`。后续 Trace 连续性所需源码补丁按 2026-10-11 的要求保存于 [patches/obi](../../patches/obi/README.md)，用于审查、重建和上游修复；不会自动复制到发布依赖目录或复用为发布镜像。补丁环境的验收结果不能作为官方原版的兼容性证明；正式支持范围以原版实测为准。Ongrid 的进程基础资源采集及页面适配继续保留，它们不修改 OBI 源码。

## 实例基础资源（2026-09-20）

### Kubernetes 集群身份统一（2026-09-23）

Manager 复用 Kubernetes Cluster.NodeID 映射，在下发 OBI、SDK Collector 和日志配置时设置统一拓扑 `cluster_id`；内部注册和认证继续使用原 K8s ID。新 APM 数据另携带 `k8s_cluster_id` 作为内部身份校验，避免统一 ID 与历史内部 ID 数字碰撞。映射缺失时拒绝下发，不将注册 ID 当作统一 ID。用户保存采集设置不能覆盖这些 Manager 所有的字段。

按统一集群查询时，分别匹配新数据的 `(cluster_id=拓扑 ID, k8s_cluster_id=内部 ID)` 和历史数据的 `(cluster_id=内部 ID, k8s_cluster_id 缺失)`，指标在 rate 后、汇总前合并，链路使用同样的范围。日志回退先将新旧实例身份解析为同一个拓扑集群，仍限制设备、namespace 和 Pod。回滚优先恢复 Edge，保留新 Manager 查询新旧身份；若继续回滚 Manager 和 web，已写入统一身份的数据仍保留，但旧查询端不保证能按原集群筛选到这些数据。

独立遥测网关由控制器刷新 Secret 的 `telemetry-cluster-node-id` 获取统一 ID；旧 `telemetry-cluster-id` 仍供注册及 Kubernetes 基础设施指标使用，基础设施查询继续沿用已有映射。Tempo spanmetrics 需增加 `k8s_cluster_id` dimension（安装模板已更新），否则缺少该维度的新 spanmetrics 不会被归入选定集群。

### 混合版本兼容（2026-09-23）

每次 `get_plugin_configs` 请求携带可选的 `unified_cluster_identity` 能力；不依赖版本号、注册记录或持久缓存，因此滚动升级和回滚都会重新协商。旧 Edge 的空请求继续获得不含新增字段的 OBI 配置和原内部 ID 的 traces 配置；容器日志仍使用其已有的统一 ID。新 Edge 才获得统一 ID 与内部 ID 的配对。集群 ID 始终由 Manager 解析，Edge 只声明能力，不能指定映射。

新 Edge 连接旧 Manager 时，Kubernetes OBI 和 SDK traces 使用原来的内部 ID，且不添加表示统一身份的 `k8s_cluster_id`；新网关读取旧控制器的 Secret 时同样保留旧标签语义。Secret 新字段缺失或为空表示旧协议，非空但非法则拒绝应用。新控制器从旧 Manager 收不到映射时投影空值，避免把零值或旧的残留映射当作新身份。建议先升级 Manager 和 Tempo 配置，再滚动升级控制器、网关和节点 Edge；升级期间 APM 按既有映射兼容两种身份，旧数据不重写。链路查询用 `!(resource.k8s_cluster_id != nil)` 判断缺失，避开 Tempo 2.10 在 OR 内直接使用 `= nil` 时漏查旧链路的问题；独立 Tempo 验收覆盖新旧样本和数值碰撞。

在 autoapm 既有 15 秒指标 scrape / push 通道附加 `ongrid_apm_process_*`，复用 procfs 库读取 CPU 累计秒、RSS、虚拟内存、线程、FD 和磁盘字节计数，不新增 exporter、监听端口或 OBI 源码补丁。OBI `target_info` 提供服务身份；主机以实时 exe + port + PID 再验证，Kubernetes 复用现有只读 Pod API，限制当前节点，以 Pod UID / 容器名称取得当前 runtime container ID，再精确匹配 `/proc/<pid>/cgroup`。不按 Pod 总量、进程名称或工作负载前缀猜测。

主机发现通过 procfs 的监听 socket inode 关联全部所属进程，保留同路径、同端口的不同 PID，覆盖 `SO_REUSEPORT` 和继承共享监听 socket 的 worker；同一 PID 的重复监听记录仍合并。只调整 Ongrid 的发现逻辑，不修改 OBI 或依赖库源码。

服务日志回查保留设备、集群及实际 Kubernetes namespace，跳转日志检索时保留全部设备范围。Pod 名称只在同集群、同 namespace 内唯一；跨集群或 namespace 时要求先缩小到具体集群或实例，避免用多个独立列表的笛卡尔积匹配其他服务日志。实例缺少 Kubernetes namespace 时不猜测业务服务命名空间。

每次资源读取限时 5 秒、最多 1000 个匹配进程；原 HTTP/RPC/运行时样本在资源读取失败时仍继续上报。进程 PID 和 start ticks 区分重启后的计数器；读取结束再次检查 start ticks，跨进程生命周期的样本丢弃。部分不可读指标不填零；`ongrid_apm_process_scrape_success`、`ongrid_apm_resource_collection_success` 和插件心跳错误展示失败。基础 gauge 只合计最近 30 秒的进程样本，避免退出子进程继续计入；CPU、I/O 先 rate 再汇总，Edge CPU/RSS 优先于重复 SDK 数据。Kubernetes RSS 为容器内进程 RSS 合计，不能用于容器工作集或内存限制利用率。

回滚恢复之前的 Edge、Manager 和 web 产物；实例响应新增兼容字段 `namespace`，无数据库 schema 变更，历史基础资源指标保留在 Prometheus。无需调整 OBI 权限，服务发现、现有设备 process-exporter、日志和 SDK 接入继续沿用原路径。

## 官方 OBI 权限与启动预检（2026-09-21）

Kubernetes 节点容器及切换到非 root 的宿主机 Edge 都保留官方能力集合：`BPF`、`NET_RAW`、`NET_ADMIN`、`PERFMON`、`DAC_READ_SEARCH`、`CHECKPOINT_RESTORE`、`SYS_PTRACE`、`SYS_RESOURCE`、`SYS_ADMIN`。`SYS_RESOURCE` 主要用于 5.11 以下内核的锁定内存限制；统一清单仍保留此项。`SYS_ADMIN` 用于 Go 库级上下文传播、网络命名空间访问及发行版 perf 限制。该变更覆盖上文旧权限清单，但不启用 privileged；实际采集仍由用户保存的目标控制。

非 root 的 JVM attach 还需要保留启动器已有的 `SYS_CHROOT`：Linux `setns(CLONE_NEWNS)` 同时要求 `SYS_ADMIN` 和 `SYS_CHROOT`，实测仅官方九项时返回 EPERM，补充后成功。同时保留启动器已有的 `SETUID`、`SETGID`，供官方 JVM attach 匹配目标进程的 UID/GID；本地 Java 使用 UID 65532、GID 0，与 Edge GID 不同，缺少 SETGID 时已实际报凭据切换失败。以上权限仅在开启 BPF 时继续保留，参见 [setns(2)](https://man7.org/linux/man-pages/man2/setns.2.html)。

采集节点的 AppArmor 配置为 Unconfined，以允许宿主机进程检查及 bpffs 访问；这仅作用于节点容器，不关闭宿主机 AppArmor。Kubernetes 1.30 以前使用兼容 annotation，之后使用 securityContext.appArmorProfile。节点启动器仍切换到配置的非 root UID，保留只读容器根文件系统及 allowPrivilegeEscalation=false。宿主机已挂载 `/sys/fs/bpf` 时，安装器只创建、授权 `/sys/fs/bpf/ongrid`，不修改 bpffs 根目录或其他 Agent 的目录。未挂载时仅记录警告并跳过，不阻断节点启动；OBI 降级固定 map 相关功能，基础 APM 采集不依赖该挂载。OBI 通过官方 `ebpf.bpf_fs_path` 使用该目录；tracefs、cgroup 与 procfs 通过既有 host-root 挂载及 chroot 可见。Kubernetes RBAC 仍只读 Pods、Nodes、ReplicaSets。

启动采集前，Edge 用自身实际身份执行一次 disabled uprobe 的 `perf_event_open` 并立即关闭，不加载或执行额外 BPF 程序。失败通过现有插件 health.last_error 上报操作、内核 perf 设置和修正方向，随后由既有 Supervisor 重试；未选目标的发现流程不执行此检查。此检查确认基础探针权限，不等于所有目标、TLS 或语言版本都已完成采集验收。

本地 Lima Ubuntu 6.8 对照实验中，单独将 perf_event_paranoid 从 4 改为 2 未解决问题；相同非 root 用户加入 SYS_ADMIN 后 uprobe 打开成功。因此恢复原值 4，适配部署权限，禁止 Agent 自动修改宿主机 sysctl。官方 OBI 二进制及源码保持不变。

Lima Kubernetes 实测通过：非 root OBI 进程保留以上权限，Go HTTP/gRPC、Go 运行时指标、Java 堆/非堆内存指标与三个应用的容器日志可查询；Go → Python → Java 的同一 Trace ID 及客户端/服务端父子关系正确。相关 Linux race 测试、Edge 双架构编译和 Helm 兼容模板测试通过。此结果仅覆盖该 Ubuntu 6.8 ARM64 测试集群，不代替其他内核、架构或语言版本验收。

参考：https://opentelemetry.io/docs/zero-code/obi/security/ 。回滚须同时恢复 Edge 镜像与 Helm chart；若不再需要 OBI，清空采集范围即可停止探针。内核参数不随部署变更。

## 取消节点部署开关（2026-09-24）

节点统一准备上述 OBI 能力、AppArmor 与只读 RBAC，移除 `node.autoAPM.allowBPF` 配置及 Edge、启动器对 `ONGRID_AUTO_APM_ALLOW_BPF` 的判断，取代上文需要额外开启的部署约定。Chart 为兼容旧 Edge 镜像仍固定传入旧环境变量 `true`，旧 values 中的 `allowBPF=false` 不再控制渲染结果。

用户仍需保存采集目标才启动 OBI 或其 Collector；清空目标停止采集，平台、BTF 和实际 uprobe 权限校验保留。节点运行时安装在宿主机已挂载 bpffs 时准备专属目录，未挂载时警告并跳过，避免影响普通指标、日志和服务发现；Agent 不主动挂载 bpffs 或修改内核参数。OBI v0.12.1 的 `setupOtelBPFFSPath` 在目录不可用时关闭 map pinning 并继续采集，参见 [上游实现](https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation/blob/v0.12.1/pkg/ebpf/tracer_linux.go#L159-L195)。仍不启用 privileged。

安装和升级命令保持原有流程。合并后随新的 Chart 和 Edge 版本一同发布，已有集群执行针对新版本的升级命令；发布动作不会自动更新已安装集群。停止采集使用空目标配置；恢复旧部署权限需同时回滚 Chart 和 Edge 镜像。

## Kubernetes Node 名与 hostname 不一致（2026-09-28）

官方 OBI v0.12.1 用 `os.Hostname()` 推断 Node 名；无法匹配时仍以 hostname 设置 Pod 的 `spec.nodeName` 过滤，导致以 IP 等不同名称注册的节点没有 Pod 元数据，按工作负载选择的采集无法匹配。该版本不支持直接传入 Node 名。

Edge 仅在 hostname 与 Downward API 注入的 `ONGRID_K8S_NODE_NAME` 完全一致时启用 `meta_restrict_local_node`；不同、缺失或无法读取 hostname 时关闭该元数据优化，读取集群元数据。本机进程与容器 ID 关联以及既有 Namespace / 工作负载规则继续限制实际采集，不会采集远端节点进程或未选目标。代价是这类节点的元数据缓存与 API 开销随集群规模增长；上游提供显式 Node 名后可恢复精确过滤。节点名自动发现告警仍可能出现，不再因这层过滤阻断目标匹配。

无需新增配置、修改设备名称或重命名主机；随 Edge 镜像升级生效，普通主机配置保持原状。回滚 Edge 镜像恢复旧行为，原采集规则与历史数据保留。

## Kubernetes JVM attach 的 UID 切换（2026-09-28）

官方 OBI 的 JVM attach 会通过进程级 `Seteuid` / `Setegid` 匹配目标 Java 身份。节点 Edge 原本只用 `PR_SET_KEEPCAPS` 保留初次降权的能力，但该标志在 exec 时清除，且不能阻止有效 UID 从 root 切回非 root 时清空 effective capabilities。非 root OBI 附加 root Java 后可永久丢失 permitted / effective capabilities；单纯把 OBI 改成 root，附加非 root Java 时仍会让并发采集线程暂时丢失 effective capabilities。

宿主机启动器改用 Linux `SECBIT_NO_SETUID_FIXUP`，合并保留已有 securebits，使必要能力跨 exec 和后续 UID 切换保持稳定。该设置只进入 Kubernetes 节点 Edge 的启动链路，由节点 Edge 及其子进程继承；不修改主机全局设置、普通 systemd Edge 或其他 Kubernetes 组件。节点继续使用已配置的 UID/GID，既有状态文件无需迁移。现有 bounding / permitted / effective / inheritable / ambient 能力清单和 `no_new_privs` 保持不变，不保留额外的 `SETPCAP`，不启用 privileged。继承该标志的子进程若要主动放弃能力，须显式缩减 capability 集合，不能只靠切换 UID；现有 Collector、exporter 和命令执行器没有通过 UID 切换实施权限隔离的路径。

回归测试 `TestK8sHostCapabilitiesSurviveJVMAttach` 通过 `make test-k8s-capabilities` 在隔离 Linux 环境运行，需要 root / sudo 及节点启动器的 capability 清单；CI 的 Ubuntu runner 使用同一入口。测试在独立子进程调用真实启动器，经 exec 后反复切换 root / 非 root Java 身份，验证当前和并发线程的精确能力集合、网络命名空间系统调用及 `no_new_privs`。采集配置与目标匹配不变。随 Edge 镜像升级生效；回滚镜像并重建节点 Pod 恢复原权限语义，不需要迁移配置或数据。

Lima ARM64 对照使用未修改的官方 OBI v0.12.1、真实启动器降权函数和独立 Java Pod。旧启动器出现 `failed to enter target net namespace`，permitted / effective / ambient 能力归零，root Java 没有 HTTP 请求指标；新启动器仍以 UID 65532 运行并保留原能力清单，root / 非 root Java 均产生 HTTP 请求指标，未选中的第三个端口没有上报。该实测使用端口选择和 OBI 原生 Prometheus 导出，未覆盖 Kubernetes 元数据匹配及 Manager / APM 页面全链路。两轮均有 Java agent attach 超时，因此此修复只确认权限丢失及对应 HTTP 采集恢复，不能据此宣称 Java TLS 采集已恢复。测试命名空间、进程和 BPF 目录已清理。
