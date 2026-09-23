# Edge 9101 端口自动规避方案

状态：已实现，完成本地 Linux 验证，尚未发布。日期：2026-09-23。
分支：`fix/edge-port-conflicts`；基线：`origin/main` 的 `50ad3db9`。
客户反馈疑似 9101 冲突，尚未取得现场报错。

## 目标和范围

普通主机 Edge 和 Kubernetes Node Edge 默认无需用户配置，9101 被占用时仍能启动、建立隧道和采集数据。
仅处理 Edge 自身 metrics/health HTTP 监听，不修改 Exporter、pprof、Logs、OBI 或 OTLP 端口；不承诺解决多实例共享目录或 BPF 资源问题。

## 已验证代码依据

- 原实现 `cmd/ongrid-edge/main.go` 把 `:9101` 直接传给 HTTP server；监听错误向 errgroup 返回，导致 Edge 退出。
- 普通 Edge 的该端口提供 `/metrics` 和 `/healthz`，不是 Manager/Frontier 隧道地址，也不是 host/proc Exporter 地址。
- Node DaemonSet 为 hostNetwork，共享宿主端口，模板当前未声明 HTTP 健康探针。
- 独立 Gateway/Scraper 已读取 `ONGRID_EDGE_METRICS_ADDR`，其 Pod 探针固定引用 diagnostics/9101。它们不纳入本次动态切换，保持现有行为。
- 公共 HTTP server 当前只支持 ListenAndServe，且在实际 bind 前记录 listening；实现需支持已绑定 listener 并在成功后记录实际地址。

## 选择规则

| 情况 | 行为 |
| --- | --- |
| 未指定地址，9101 空闲 | 绑定并使用 `:9101` |
| 未指定地址，9101 返回 address already in use | 用相同监听主机绑定 `:0`，由操作系统分配空闲端口 |
| 9101 失败原因为权限、资源耗尽等 | 明确报错，不当作端口冲突处理 |
| 自动分配也失败 | 明确报错并保留现有退出行为，不伪报健康 |
| 显式设置 `ONGRID_EDGE_METRICS_ADDR` | 固定模式，严格使用指定地址，冲突时报错，不自动替换 |

显式地址的端口限制为 1..65535；空白按未设置处理。固定模式用于外部 Prometheus 抓取或固定防火墙规则。默认监听范围保持现状，不另行扩大暴露范围。

实现使用 `net.Listen`，按 `errors.Is(err, syscall.EADDRINUSE)` 等目标平台可验证的错误判定识别占用；直接把成功返回的 listener 交给 `http.Server.Serve`，不先扫描或释放后重绑，不解析错误字符串。

## 生命周期和部署行为

- 每次启动优先 9101；本次分配的端口在进程生命周期内不再变化。重启后允许变化，不持久化随机端口。
- 绑定后再启动服务并记录监听成功；在后续初始化失败、取消或正常退出时关闭 listener，保留现有优雅退出逻辑。
- 不需页面先配置，也不需等待 Manager 下发即可启动；内部健康请求使用实际端口。
- Node DaemonSet 当前无固定 HTTP 探针，因此无需为本方案改成 exec 探针。Gateway/Scraper 的固定端口及 probes 保持不变。
- Docker host 网络适用自动模式；Docker 显式发布端口和外部固定抓取使用固定模式。自动端口不保证外部防火墙自动放行。
- 安装重装保留用户显式配置的该变量；不新增配置页面或全套安装端口表单。

## 可观测性

第一版通过启动日志提供 `component=diagnostics`、`requested_addr`、`actual_addr`、`mode=auto|fixed` 和 `fallback_reason=address_in_use`。成功规避只记录一次，不持续告警；最终失败提供两次绑定的错误上下文。

日志示例：默认 9101 已占用，诊断服务已改用 43827，Edge 继续运行。

页面只读展示实际端口可作为后续小改：需要通过隧道上报运行时状态并同步 Manager/API，不将此链路作为端口修复的前置条件。第一版不新增数据库字段或独立状态文件。

## 改动位置

1. `cmd/ongrid-edge/`：诊断监听的自动/固定选择、启动接入与测试；不改独立数据面模式。
2. `internal/pkg/httpserver/`：复用已有生命周期，补充接收已绑定 listener 的入口，保留 Start 调用方行为。
3. `deploy/install/edge/` 与 `docs/install/edge.md`：显式覆盖的保存/重装保留和操作说明。

## 验收和回滚

- 默认空闲时仍使用 9101。
- 测试先持有默认端口，Edge 获得其他端口；实际请求 `/healthz`、`/metrics` 成功，默认端口的原服务不受影响。
- 固定模式遇占用明确失败；非法地址和非占用错误不触发回退。
- 并发启动监听测试、取消与关闭测试通过，Go 测试带 `-race`；公共 HTTP server 的原 Start 路径回归通过。
- Linux/systemd 与真实 hostNetwork Node 验证 Edge 在线及主机指标持续上报；Gateway/Scraper Helm 渲染和探针保持原样。
- 重装后固定地址不丢失；不配置时不引入新增部署要求。
- 回滚到旧版前确保 9101 空闲；旧版普通 Edge 不支持本覆盖变量。回滚不涉及数据迁移。

## 本次验证结果

- Linux（隔离 golang:1.25-bookworm 容器）：`go test -race ./internal/pkg/httpserver ./cmd/ongrid-edge -count=1`、对应 `go vet` 和 `make build-ongrid-edge` 通过。
- 安装检查：`make test-edge-metrics-env` 通过，覆盖两个入口的新装、保留、显式覆盖、清空、IPv6 及拒绝换行输入。
- 真实二进制：默认 `[::]:9101`；预占 9101 后改为 `[::]:34423`，两种情况下 `/healthz`、`/metrics` 均返回 200，正常退出并释放端口；显式固定 9101 遇占用明确失败。测试使用隔离容器，没有连接客户或本地 Manager。
- 额外检查 IPv4/IPv6、多个并发回退 listener、取消和端口释放；独立数据面固定端口冲突仍失败，不自动回退。
- 未做真实 systemd 安装、Kubernetes 集群部署、Manager 联网采集或 Windows 运行验证。macOS 上 cmd/ongrid-edge 原有平台函数缺失，改在 Linux 验证，未扩展平台支持。
