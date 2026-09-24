# ADR-034：独立控制 OBI 自动发现与按选择采集

Status: Accepted — 原始决策 2026-09-14；以下早期设计记录中的开关和 Kubernetes 限制已由 2026-09-20 修订替代，当前行为以 [PRD-008](../requirements/PRD-008-obi-selective-apm.md) 为准。

## 当前修订（2026-09-20）

发现常开，空目标只发现。取消全局设置依赖，旧 false 值不阻止发现；停止采集通过清空目标或规则完成。普通设备使用进程路径与端口，日志路径随目标交给现有 logs 插件；Kubernetes 按集群 Namespace 和工作负载选择，范围内全部容器的链路与日志使用同一规则；旧容器限制在共享配置解析时清除。服务身份和环境沿用 PRD 的继承规则。已有设备日志配置保留，运行时投影服务文件源，避免保存两份日志配置。

## 决策

新增 autoapm 插件，复用现有 Manager 插件配置、鉴权 tunnel、心跳及 subprocess 生命周期。enabled 表示整个自动 APM 开关；targets 为空只运行监听进程发现。心跳及界面候选最多 200 组，已选目标的资源采集不受候选展示上限限制；目标最多 100 组，只上报路径/端口/PID，不采集进程参数或环境变量。目标必须为确定的绝对路径和非零端口；不接受 glob、正则配置或环境替换符。

选择进程后启动固定版本 OBI v0.12.1 与私有 Collector 0.157.0。OBI 使用 v1 services 的转义完整路径正则与端口 AND 匹配，保留逐目标服务名称；其 v2 迁移不支持逐目标名称，独立 `config validate` 命令也只接受 v2。因此 v1 由 OBI 启动时校验，复用 subprocess readiness 检测启动错误并回滚。所需系统能力设为强制检查，禁止缺权限仍显示成功启动。

OBI 仅向 loopback 的 OTLP 14317/14318 导出。Collector 健康端口 14333、自监控 18888、应用指标 exporter 9465，避免影响已有 SDK Collector。Trace 经现有 Manager URL 与鉴权写入 Tempo；指标复用 custommetrics scraper，经 push_prom_samples 上报，source=obi。环境、device_id、可用的 cluster_id 和 ongrid.instrumentation.source 在 Collector 设置。未添加新的公开接收端口、存储或 spanmetrics 管线。自动 APM 采集统一使用 Trace 采样比例 1，并允许 Manager 自签名证书（跳过证书验证）。由 Manager 在下发时覆盖历史采样和证书选项，普通设备与 Kubernetes 一致；旧 API 字段保留以兼容旧配置。

请求指标直接使用 OBI 的 HTTP/RPC 秒制直方图，沿用现有 APM 查询，不从已采样 Trace 计算全量 RED。OBI 开启现有 OTLP 导出进程排除以减少重复采集，但这不是任意 SDK/协议的可靠去重器；已接 SDK 的服务应由用户保持单一采集路径。APM 目前不提供 OBI/SDK 按信号自动择优合并。

## 部署及边界

Linux amd64/arm64，要求内核 BTF 和相应 eBPF 能力。原生服务通常以 root 运行；受限部署需要 CAP_DAC_READ_SEARCH、CAP_SYS_PTRACE、CAP_PERFMON、CAP_BPF、CAP_CHECKPOINT_RESTORE、CAP_NET_ADMIN、CAP_NET_RAW。Kubernetes 节点部署自动准备采集权限，用户保存目标后启动 OBI（节点部署开关已于 2026-09-24 取消，见下文）。更新策略禁止 surge，避免同节点两个探针。Kubernetes 元数据自动探测关闭，当前使用用户指定服务身份，不新增 API Server RBAC。

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

## 设备环境归属（2026-09-20）

设备默认环境提升为 `devices.environment` 可空列，服务端统一解析设备覆盖与拓扑集群继承，再投影到主机 APM 及服务文件日志配置。Kubernetes 使用原有集群配置路径。两处 UI 编辑共用设备 GET/PUT environment 接口，写操作仅管理员可用；清空恢复继承。NULL 标识旧设备尚未导入，空字符串标识已迁移或已清空，避免重启恢复旧采集器默认值。升级保留原 Edge 配置以便回滚；回滚前导出新增设备环境，先回滚 Manager，再执行 down migration。

## 运行时指标（2026-09-20）

普通设备与 Kubernetes 的共用 OBI 渲染启用 `application_runtime`，复用私有 Collector、现有 Prometheus 和实例页，不新增 exporter 或 SDK 注入。查询兼容 OBI 的 OTel 指标命名与既有 SDK 命名；计数器先按实例计算 rate，JVM 按 heap/non_heap 合并内存池，Go CPU 排除 idle 并与 OS 进程 CPU 分开展示。Go GC 暂停与调度直方图均值为桶下界估算，不替代 SDK 的真实时长和分位数。

OBI v0.12.1 的本地 OrbStack 验收发现嵌套 PID 命名空间会影响 Go 运行时目标匹配，以及请求与 JVM 运行时的实例标识对应。该环境需要单独验证兼容修复；启用配置和图表适配本身不能视为所有内核、语言或 Kubernetes 节点已通过运行验收。

### 官方发布与本地测试边界（2026-09-20）

正式发布使用 OpenTelemetry 官方 OBI Release 原版二进制，不修改 OBI 源码、不应用兼容补丁、不发布自维护 fork。版本由 `OBI_VERSION` 固定（当前 0.12.1），通过 `scripts/fetch-obi.sh` 从官方 Release 下载并校验 `SHA256SUMS`；Edge 镜像及依赖附件构建均沿用该入口。

OrbStack PID 命名空间补丁仅用于本地测试，补丁、源码副本和测试产物保留在已被 Git 与 Docker 构建上下文忽略的 `output/obi-runtime/`，不得复制到发布依赖目录或复用为发布镜像。补丁环境的验收结果不能作为官方原版的兼容性证明；正式支持范围以原版实测为准，遇到兼容问题优先跟进官方修复或升级官方版本。Ongrid 的进程基础资源采集及页面适配继续保留，它们不修改 OBI 源码。

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
