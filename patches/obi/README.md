# OBI Trace 连续性补丁

完整的 SDK/OBI 混合链路修复依赖本目录的 OBI 源码补丁。仅部署 Ongrid 侧修改和官方 OBI v0.14.0，不能得到本补丁的完整行为。补丁随 Ongrid 代码一起审查、保存和推送，构建为独立版本 `0.14.0-ongrid.4`。Make 和 Edge 镜像的默认依赖均使用这个补丁版本。

- 上游：[OpenTelemetry eBPF Instrumentation](https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation)，`v0.14.0`，commit `13d9b0c3f600a060bd78820a63ebec882b10757e`。
- 补丁：[v0.14.0-trace-continuity.patch](v0.14.0-trace-continuity.patch)，包含 32 个源码和测试文件。
- 版本、哈希、工具链和验收计数：[manifest.json](manifest.json)。补丁 SHA256 与实际验收版本一致。
- 上游 [LICENSE](LICENSE) 和 [NOTICE](NOTICE) 原样保留。差异文件明确列出 Ongrid 对上游的修改；这不是上游官方发布。

## 变更

Go 探针读取 SDK recording span 上下文，识别 SDK 已覆盖的操作，保留 SDK 未覆盖的子操作及自定义 Span。未覆盖操作携带 `obi.sdk.context=true`，由 Ongrid 的入口 Collector 保留，避免按实例删除全部 OBI Span。

补丁也修复 HTTP/1 大请求头的传播交接、HTTP/2 协商帧大小及 h2c 服务端时间戳、嵌套 PID 命名空间内的探针身份和 Auto SDK 激活，以及 parent-based 采样的父上下文。不增加用户配置项。Ongrid 的 Trace 导出配置同时启用 HTTP、gRPC、SQL、Redis、Kafka、MongoDB，避免补丁采集到的非 HTTP 子操作在出口被关闭；真实服务端验收范围仍以下表和发布边界为准。

`0.14.0-ongrid.3` 还将 gRPC 服务端的 stream/transport 缓存按进程隔离，并在删除缓存项前复制父上下文，避免并发插入复用 LRU 槽位。回归测试模拟删除时立即覆盖槽位，并验证相同指针和 stream ID 在不同进程之间不会串联。Ongrid 在 `sample_ratio=0` 时使用 `always_off`，保留仅采集指标的行为；其他比例继续尊重父级采样标志。

`0.14.0-ongrid.4` 修复自定义 SDK CLIENT Span 吞掉内部未插桩 SQL、HTTP 等子操作的问题。子调用先创建自己的 Span；HTTP/gRPC 仅在具体请求与 SDK 上下文关联后才去重。HTTP 的传播头匹配还会更新同连接的备用上下文，确保大请求头下的服务端仍连接到真实 SDK Span。自定义 CLIENT 包装与内部原始 SQL 调用保留为两个有父子关系的 Span，不能仅凭 SpanKind 相同合并。 gRPC 服务端和 SQL 入口保留同一调用的首次上下文，防止 Go 扩栈后重入探针覆盖已生成的 Span；SQL 使用相对栈位置区分重入与真正的嵌套调用。

Manager 的接入地址查询同时修复串行探测耗尽请求时间的问题。每阶段按去重后的目标并发探测，上限受 100 个实例限制；阶段预算最多 8 秒，且不超过剩余请求时间的一半。探测超时只影响对应目标，可选 Pod 元数据超时保留已确认的接收地址；调用者取消仍返回取消错误。

## 构建、下载与安装

在 Ongrid 仓库中执行：

```sh
bash scripts/build-patched-obi.sh /tmp/obi-release-output
```

输出目录必须为空。脚本需要 Docker、Git、jq 和 sha256sum；读取 `manifest.json`，校验上游 commit、补丁和内嵌 Java 代理的哈希，重新生成全部 BPF，再构建 Linux amd64/arm64。生成镜像按 digest 固定。Java 代理及双架构本地库复用上游 tag 的原始产物，不重新构建无关的 Java 代码。

产物包含双架构压缩包、带生成对象的完整修改源码、补丁、来源清单和 `SHA256SUMS`。二进制携带补丁版本和补丁 SHA；分发包保留上游及依赖许可证，并在 NOTICE 标明 Ongrid 修改。

发布沿用 CNB 的完整 Edge 依赖附件：`edge-deps-linux-amd64.tar.xz` 和 `edge-deps-linux-arm64.tar.xz`。OBI 与 Collector、各 exporter 一起打包，不单独发布 OBI 二进制 Release。构建和上传在本地执行，不使用新增 CI 发布流程。

本地构建输出的 OBI 压缩包仅作为打包输入。将两个架构的 `obi` 和汇总后的 `obi.NOTICES` 放入 `bin/linux-<arch>/`，然后执行：

```sh
# 本地 OBI 已完成源码/补丁/构建校验；其余依赖仍由现有下载步骤准备。
make -o fetch-obi build-edge-deps-attachments
make -o fetch-obi publish-edge-deps-attachments
make verify-edge-deps-release
```

上传复用现有 CNB 发布器和 `CNB_TOKEN`，以新依赖标签发布，不覆盖已有资产。源码补丁和来源清单随 Ongrid 仓库保存。修改补丁或交付产物时必须递增 `-ongrid.N`。

`scripts/fetch-obi.sh` 对 `-ongrid.N` 版本从对应 CNB 整包提取 OBI；先验证外层校验和，再验证包内清单、目标架构、依赖标签和 OBI 版本。纯上游版本仍使用 OpenTelemetry Release 及其 `SHA256SUMS`。Make、Edge Dockerfile 与本目录清单必须保持版本一致。依赖标签包含完整 `OBI_VERSION`，因此不会复用之前不带补丁的 CNB 依赖包。

后续 Ongrid 应用发布沿用现有 CNB 依赖附件、宿主机安装/升级包及 Kubernetes Edge 镜像流程。发布 OBI 依赖本身不等于 Ongrid PR 已合并，也不等于已发布新的 Ongrid 应用版本。回滚时恢复上一版 Edge 镜像或依赖附件；回到官方 `0.14.0` 会失去本次混合链路修复。

`manifest.json` 中 `historical_candidate_acceptance` 标识此前完成矩阵的本地候选；发布产物以 CNB 整包的 `.sha256` 和包内 `MANIFEST.sha256` 为准，不承诺不同环境重建后逐字节相同。

## 验证

### 0.14.0-ongrid.4 审查修复验收

完整交付结果见 [delivery-20261011-review-fixes.json](delivery-20261011-review-fixes.json)。公开 CNB 整包、Make 默认安装、双架构 Edge 镜像和 Kubernetes 运行中 OBI 的哈希均与验收产物一致。

| 环境 | HTTP/gRPC 矩阵 | 边界请求 | 合计 |
| --- | ---: | ---: | ---: |
| Linux 6.8 ARM64 原生进程 | 420/420 | 190/190 | 610/610 |
| OrbStack 6.19 ARM64 Docker bridge | 420/420 | 190/190 | 610/610 |
| Linux 6.8 ARM64 Kubernetes（最终 Edge 镜像） | 420/420 | 190/190 | 610/610 |
| Linux 6.8 AMD64 原生进程（QEMU x86_64 内核） | 420/420 | 190/190 | 610/610 |

新增自定义 SDK CLIENT 包装场景，确认内部未覆盖的 HTTP、gRPC、SQL 操作保留独立 Span 和正确父引用，同时保持已有 HTTP/gRPC SDK 操作去重。四组共 2440/2440 条请求；2380 条采样 Trace 与 60 条未采样空结果均已保存并完成离线复验。真实双架构插件生命周期、三次应用重启、身份、认证导出、RED/运行时指标、零采样与恢复通过。

Manager 回归覆盖 5/100 个实例、健康目标排在最后、集群去重、可选元数据超时、目标优先级与主动取消；相关包 race、Linux 非 root 全量 Go 测试和安装/发布检查通过。五个相关 BPF 回归包含 CLIENT 包装和 gRPC/SQL 扩栈重入保护。原始证据保存在忽略目录 `output/obi-runtime/edge-review-fixes-20261011/`。

### 0.14.0-ongrid.3 历史验收

`0.14.0-ongrid.3` 的历史完整交付结果见 [delivery-20261011.json](delivery-20261011.json)。下表保留该版本的验收记录；公开校验和、Make 默认下载、双架构 Edge 镜像内 OBI 均与已测二进制一致。测试夹具为移除符号表的 Go 1.25.11，使用 OTel SDK 1.43.0、Auto SDK 1.2.1 和 otelhttp/otelgrpc 0.68.0。

| 环境 | HTTP/gRPC 矩阵 | 边界请求 | 合计 |
| --- | ---: | ---: | ---: |
| Linux 6.8 ARM64 原生进程 | 360/360 | 152/152 | 512/512 |
| OrbStack 6.19 ARM64 Docker bridge | 360/360 | 152/152 | 512/512 |
| Linux 6.8 ARM64 Kubernetes（最终 Edge 镜像） | 360/360 | 152/152 | 512/512 |
| Linux 6.8 AMD64 原生进程（QEMU x86_64 内核） | 360/360 | 152/152 | 512/512 |

矩阵包含 SDK→OBI、OBI→SDK、SDK→SDK、OBI→OBI、Auto SDK→OBI、部分 SDK→OBI。入口为 HTTP/1，下游为 HTTP/1、HTTP/2 h2c、gRPC，覆盖首次业务请求、连接复用、并发及重启后的新实例。边界包含 SQLite、20 秒请求、8/32/64 KiB 填充头和未采样传播。

逐请求检查 TraceID、父引用、SDK SpanID、自定义 Span 的直接父子关系、操作数量、唯一传播头和有效时间戳。48 个未采样请求的 flags 保持 `00`，出口等待窗口结束后无后端 Span。原始 Trace、传播日志及离线重放结果保留于本地 `output/obi-runtime/edge-delivery-20261011/`；本目录上传源码与验收摘要，不上传环境日志。四组共 2048/2048 条请求，离线重放也全部通过。

双架构真实 Linux 插件测试通过，覆盖 `Configure/Start/Stop`、选择性采集、三次应用重启后重新附加、认证导出、服务身份、RED 与运行时指标、零采样和恢复。Linux 非 root `go test ./...`、Auto APM race 检查、完整安装/发布脚本测试、7 个 Trace 校验器自检及 5 个相关 BPF 回归通过。

应用补丁并生成 BPF 后，可在具备 C 编译器的 Linux 环境运行已有回归测试：

```sh
CGO_ENABLED=1 go test -race ./pkg/ebpf ./pkg/ebpf/common \
  ./pkg/export/otel/tracesgen ./pkg/internal/ebpf/gotracer \
  -skip TestLockdownParsing -count=1
go test ./pkg/internal/goexec \
  -run 'TestSDKRecordingContextOffsets|TestHTTPHeaderSortedValuesABI' -count=1
make -C bpf/tests CC=clang bpf_go_client_trace_parent bpf_go_observer_pid \
  bpf_go_h2_owned_stream bpf_go_h2_write_transaction bpf_go_grpc_server_trace_parent
for check in bpf_go_client_trace_parent bpf_go_observer_pid \
  bpf_go_h2_owned_stream bpf_go_h2_write_transaction bpf_go_grpc_server_trace_parent; do
  "bpf/tests/$check"
done
```

上述针对性测试、静态检查及真实 Collector 入口回归已通过。root 测试容器不满足 `TestLockdownParsing` 的不可读文件假设，因此跳过该测试；非 root 环境可去掉 `-skip`。BPF 全套测试在原有 `test_failed_connect_event` 的测试头文件缺少 `__always_inline` 定义处编译失败，不能称为全套通过。

## 发布边界

未完成其他语言、TLS、Linux 5.15、故障/容量压力及其他 SDK/Go ABI 的现场验收。Redis、MongoDB、Kafka 仅通过上下文转换单元测试。最终候选未重跑移除 CAP_SYS_ADMIN 的矩阵；Docker 夹具缺少 tracefs，验收覆盖 Go 库级传播，未覆盖网络注入回退。

HTTP/1 提前检查最多 512 个头名称；HTTP/2 保留现有 65535 字节帧缓冲边界。本次验收未覆盖更大边界、SDK 导出故障和异步跨 goroutine 上下文。

按 2026-10-11 的交付要求，Ongrid 使用可追溯的补丁版 OBI；此前“只能使用官方原版”的限制不再适用。该版本属于 Ongrid 维护的派生构建，不能将其测试结果归于官方原版。
