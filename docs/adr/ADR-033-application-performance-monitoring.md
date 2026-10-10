# ADR-033: 复用现有遥测后端建设应用性能监控

Status: Accepted — 用户确认方案并授权实现，2026-09-07。

## 决策

复用应用侧 OpenTelemetry SDK/Agent，经现有 Collector 写入 Tempo、Prometheus 和当前日志后端。APM 以 `(environment, service.namespace, service.name)` 为服务视图身份，实例单独关联。Manager 聚合查询，前端新增「监控告警 → 应用性能」。

2026-09-07 修订：用户确认将请求监控与 Trace 采样解耦，并要求支持 RPC。APM 默认查询应用原生 HTTP/RPC 请求指标，服务关系继续使用 Tempo service-graphs。Trace 派生指标保留为明确选择的样本视图，禁止静默回退或与应用指标相加，请求级告警只使用应用指标。

应用复用官方 OTel SDK/Agent，HTTP 采用 `http.server.request.duration`（秒），RPC 采用 `rpc.server.call.duration`（秒）。兼容模式分别查询旧 `http.server.duration` / `rpc.server.duration`（毫秒；旧 RPC 错误口径限 gRPC），整次查询只选一个指标版本，避免双发重复计数。切换兼容模式是显式操作，不对不同桶边界或单位的直方图混合求分位数。错误使用 error.type，兼容 HTTP 5xx 和 gRPC 非 OK 状态码，原始序列先并集去重再聚合。要求资源属性进入 Prometheus 标签，接口使用路由模板或 RPC 完整方法名。

主机复用 traces 插件的 OTLP 接收器和本机 Prometheus exporter，再由现有 metrics 插件通过已鉴权 tunnel 上报；Kubernetes 复用现有 Telemetry Gateway 的 Metrics remote_write。2026-10-09 修订：普通 Linux 设备默认监听 loopback 和本机 Docker bridge 地址，显式监听地址配置优先；不默认绑定全网卡，也不向业务应用下发管理凭据。

2026-10-10 修订：服务列表的「接入配置」按完整服务身份和当前资源范围自动生成环境变量，不要求用户填写接收地址。Manager 通过既有实例查询发现部署位置：Kubernetes 使用注册集群 ID 和 Controller namespace，通过现有资源描述 RPC 读取 Gateway Service 的实际 HTTP 端口；主机通过设备关联的 Edge，以新增只读 `get_application_receiver` RPC 读取 traces 插件的渲染配置和目标进程网络。容器只使用与实际接收器匹配的 IPv4 默认网关，不返回不可达的容器 loopback。无法关联资源、进程退出或接收器不可用时显示原因，不猜测地址。

Trace 来源自动判断：未检测到 SDK Trace 导出时由 OBI 提供 Trace；OBI 检测到该进程成功导出 OTLP Trace 后停止自己的 Trace 输出，SDK 与 OBI 指标仍按信号独立处理。不使用固定 SDK 模式，也不根据环境变量或业务指标推断 SDK Trace 已启用。当前检测标记保持至进程退出，不保证同一进程停用 SDK 后自动恢复 OBI Trace。内置入口另通过现有官方 Collector 缓冲 OBI Trace 30 秒，在同一 Trace、服务与实例中优先保留 SDK，覆盖首次导出前的重叠窗口；详见 [ADR-034](ADR-034-obi-selective-apm.md#sdk-与-obi-首批-trace-去重2026-10-10)。

普通设备将采集目标与运行实例分开：已保存目标具有设备范围内的稳定 UUID；旧配置按可执行路径和端口生成确定的 UUID，保存后保留。`service.instance.id` 是不透明标识，不从中解析 PID。Edge 在已选路径与端口范围内读取当前进程的 SDK 实例资源属性，与 OBI 身份核对；仅使用该资源属性，不返回或记录应用环境。无 SDK 的主机使用 OBI 的进程定位元数据。每次采集更新实例到实际 PID、启动时间和主机启动 ID 的关联，接收端查询复用此关联并校验进程启动时间，拒绝过期、复用或有歧义的进程。Docker 继续以实际 `container.name` 查询本地运行时，并核对当前实例身份，避免缓存的旧实例关联到新容器进程。资源计数器保留进程与启动维度，重启后的服务归属仍按完整服务身份聚合。

主机与普通容器输出启动前执行的环境变量 shell 脚本，Kubernetes 仅输出 env，不提供格式切换；多实例按各自接收端和部署位置切换。主机脚本每次从 Linux 的随机 UUID 接口生成实例 ID，SDK 与 OBI 读取同一启动环境；`ongrid.target.id` 保持为已保存目标的 ID。脚本放在应用启动命令之前或容器 entrypoint 中执行，不能作为静态 env 文件导入，独立 worker 各自执行。Kubernetes env 通过 Downward API 注入每个 Pod 的 UID。Manager 通过现有只读 Pod 描述 RPC 确认 `app.kubernetes.io/version` 标签存在后，生成标签的 `fieldRef`，由各 Pod 启动时注入自身版本；不复制已观测的版本值。此标签必须设置在 Pod 模板上，发布时随应用版本更新。Kubernetes 缺少标签时不生成版本属性，合并配置时须保留应用已有的版本属性。现有 Pod 描述 RPC 会删除注解，因此这次动态配置只采用标准版本标签；OBI 自动发现仍可从环境变量或标准 OTel 注解读取版本。生成配置仅包含服务身份、OTLP 接收地址和匹配的 HTTP/protobuf 协议，不覆盖 SDK 的信号 exporter、propagator、指标 temporality、直方图聚合或 semantic convention 设置，也不关闭日志导出。应用自行启用 SDK exporter；接收端未启用指标时明确提示另配指标接收端。当前 Prometheus remote write 路径仍要求累计计数器和直方图，APM 分位数查询依赖经典 HTTP/RPC 请求直方图；移除 SDK 策略变量不代表这些服务端格式限制已解决。接入配置 API 不返回应用环境或 exporter 凭据，也不修改应用或采集器配置。主机自动解析依赖支持该 RPC 的 Edge；旧 Edge 不提供此能力。更新时先部署 Edge，再部署 Manager 和前端。回滚到不识别 `target_id` 的旧版本前，移除已保存目标中的对应可选字段；路径、端口和服务身份保持不变，无数据库表迁移。

## 理由与影响

现有存储和标准 SDK 已覆盖数据采集，新增自研探针、图数据库或另一套 spanmetrics 生成器会增加重复维护。请求级告警用同源 PromQL 模板进入既有 metric_raw 规则流程，不更改旧 Trace 告警含义。

原始数据不写入 APM 数据库，时段内发现的服务不等同永久服务目录。缺失 Span、采样和外部服务识别只能提示，不能恢复未上报内容。保持现有平台级访问范围，不宣称多租户隔离。持续 Profiling 不进入本次范围。

详见 [PRD-006](../requirements/PRD-006-application-performance-monitoring.md) 与 [HLD-002](../design/HLD-002-application-performance-monitoring.md)。
