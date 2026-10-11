# OBI Trace 连续性补丁

完整的 SDK/OBI 混合链路修复依赖本目录的 OBI 源码补丁。仅部署 Ongrid 侧修改和官方 OBI v0.14.0，不能得到本补丁的完整行为。补丁随 Ongrid 代码一起审查、保存和推送，构建为独立版本 `0.14.0-ongrid.1`。Make 和 Edge 镜像的默认依赖均使用这个补丁版本。

- 上游：[OpenTelemetry eBPF Instrumentation](https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation)，`v0.14.0`，commit `13d9b0c3f600a060bd78820a63ebec882b10757e`。
- 补丁：[v0.14.0-trace-continuity.patch](v0.14.0-trace-continuity.patch)，包含 31 个源码和测试文件。
- 版本、哈希、工具链和验收计数：[manifest.json](manifest.json)。补丁 SHA256 与实际验收版本一致。
- 上游 [LICENSE](LICENSE) 和 [NOTICE](NOTICE) 原样保留。差异文件明确列出 Ongrid 对上游的修改；这不是上游官方发布。

## 变更

Go 探针读取 SDK recording span 上下文，识别 SDK 已覆盖的操作，保留 SDK 未覆盖的子操作及自定义 Span。未覆盖操作携带 `obi.sdk.context=true`，由 Ongrid 的入口 Collector 保留，避免按实例删除全部 OBI Span。

补丁也修复 HTTP/1 大请求头的传播交接、HTTP/2 协商帧大小及 h2c 服务端时间戳、嵌套 PID 命名空间内的探针身份和 Auto SDK 激活，以及 parent-based 采样的父上下文。不增加用户配置项。

## 构建、下载与安装

在 Ongrid 仓库中执行：

```sh
bash scripts/build-patched-obi.sh /tmp/obi-release-output
```

输出目录必须为空。脚本需要 Docker、Git、jq 和 sha256sum；读取 `manifest.json`，校验上游 commit、补丁和内嵌 Java 代理的哈希，重新生成全部 BPF，再构建 Linux amd64/arm64。生成镜像按 digest 固定。Java 代理及双架构本地库复用上游 tag 的原始产物，不重新构建无关的 Java 代码。

产物包含双架构压缩包、带生成对象的完整修改源码、补丁、来源清单和 `SHA256SUMS`。二进制携带补丁版本和补丁 SHA；分发包保留上游及依赖许可证，并在 NOTICE 标明 Ongrid 修改。

发布使用 Ongrid 仓库独立的 `obi-v0.14.0-ongrid.1` 标签，触发 `.github/workflows/release-obi.yml`。该流程构建并上传双架构 OBI，再复用现有 CNB 发布器创建公共 Edge 依赖附件并验证公开下载。OBI Release 初始标记为预发布，完成运行验收后再转为正式依赖；不设置为 Ongrid 最新应用版本，不覆盖官方 OBI Release。同一标签的既有资产不得覆盖，重跑先校验已发布资产，修改补丁必须递增 `-ongrid.N`。

```sh
make fetch-obi EDGE_PLUGIN_ARCHES='linux-amd64 linux-arm64'
make build-edge-deps-attachments
```

`scripts/fetch-obi.sh` 对 `-ongrid.N` 版本使用 Ongrid Release，纯上游版本仍使用 OpenTelemetry Release。两条路径均下载 `SHA256SUMS` 并校验后安装。Make、Edge Dockerfile 与本目录清单必须保持版本一致，CI 检查这三处。依赖标签包含完整 `OBI_VERSION`，因此不会复用之前不带补丁的 CNB 依赖包。

后续 Ongrid 应用发布沿用现有 CNB 依赖附件、宿主机安装/升级包及 Kubernetes Edge 镜像流程。发布 OBI 依赖本身不等于 Ongrid PR 已合并，也不等于已发布新的 Ongrid 应用版本。回滚时恢复上一版 Edge 镜像或依赖附件；回到官方 `0.14.0` 会失去本次混合链路修复。

`manifest.json` 中 `candidate_version` 和 `accepted_linux_arm64_binary_sha256` 标识此前完成矩阵的本地候选；发布产物以该 Release 的 `SHA256SUMS` 为准，不承诺不同环境重建后逐字节相同。

## 验证

同一候选二进制的现场结果如下。测试夹具为移除符号表的 Go 1.25.11，使用 OTel SDK 1.43.0、Auto SDK 1.2.1 和 otelhttp/otelgrpc 0.68.0。

| 环境 | HTTP/gRPC 矩阵 | 边界请求 | 合计 |
| --- | ---: | ---: | ---: |
| Linux 6.8 ARM64 原生进程 | 360/360 | 152/152 | 512/512 |
| OrbStack 6.19 ARM64 Docker bridge | 360/360 | 152/152 | 512/512 |
| Linux 6.8 ARM64 Kubernetes | 360/360 | 152/152 | 512/512 |

矩阵包含 SDK→OBI、OBI→SDK、SDK→SDK、OBI→OBI、Auto SDK→OBI、部分 SDK→OBI。入口为 HTTP/1，下游为 HTTP/1、HTTP/2 h2c、gRPC，覆盖首次业务请求、连接复用、并发及重启后的新实例。边界包含 SQLite、20 秒请求、8/32/64 KiB 填充头和未采样传播。

逐请求检查 TraceID、父引用、SDK SpanID、自定义 Span 的直接父子关系、操作数量、唯一传播头和有效时间戳。36 个未采样请求的 flags 保持 `00`，出口等待窗口结束后无后端 Span。原始 Trace、传播日志及离线重放结果保留于本地 `output/obi-runtime/trace-continuity-20261010/`；本目录上传源码与验收摘要，不上传环境日志。

应用补丁并生成 BPF 后，可在具备 C 编译器的 Linux 环境运行已有回归测试：

```sh
CGO_ENABLED=1 go test -race ./pkg/ebpf ./pkg/ebpf/common \
  ./pkg/export/otel/tracesgen ./pkg/internal/ebpf/gotracer \
  -skip TestLockdownParsing -count=1
go test ./pkg/internal/goexec \
  -run 'TestSDKRecordingContextOffsets|TestHTTPHeaderSortedValuesABI' -count=1
make -C bpf/tests CC=clang bpf_go_client_trace_parent bpf_go_observer_pid \
  bpf_go_h2_owned_stream bpf_go_h2_write_transaction
for check in bpf_go_client_trace_parent bpf_go_observer_pid \
  bpf_go_h2_owned_stream bpf_go_h2_write_transaction; do
  "bpf/tests/$check"
done
```

上述针对性测试、静态检查及真实 Collector 入口回归已通过。root 测试容器不满足 `TestLockdownParsing` 的不可读文件假设，因此跳过该测试；非 root 环境可去掉 `-skip`。BPF 全套测试在原有 `test_failed_connect_event` 的测试头文件缺少 `__always_inline` 定义处编译失败，不能称为全套通过。

## 发布边界

未完成其他语言、TLS、amd64 实际运行、Linux 5.15、故障/容量压力及其他 SDK/Go ABI 的现场验收。Redis、MongoDB、Kafka 仅通过上下文转换单元测试。最终候选未重跑移除 CAP_SYS_ADMIN 的矩阵；Docker 夹具缺少 tracefs，验收覆盖 Go 库级传播，未覆盖网络注入回退。

HTTP/1 提前检查最多 512 个头名称；HTTP/2 保留现有 65535 字节帧缓冲边界。本次验收未覆盖更大边界、SDK 导出故障和异步跨 goroutine 上下文。

按 2026-10-11 的交付要求，Ongrid 使用可追溯的补丁版 OBI；此前“只能使用官方原版”的限制不再适用。该版本属于 Ongrid 维护的派生构建，不能将其测试结果归于官方原版。
