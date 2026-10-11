# OBI Trace 连续性补丁

完整的 SDK/OBI 混合链路修复依赖本目录的 OBI 源码补丁。仅部署 Ongrid 侧修改和官方 OBI v0.14.0，不能得到本补丁的完整行为。补丁随 Ongrid 代码一起审查、保存和推送；正式安装器仍下载官方 Release，没有自动应用本补丁。

- 上游：[OpenTelemetry eBPF Instrumentation](https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation)，`v0.14.0`，commit `13d9b0c3f600a060bd78820a63ebec882b10757e`。
- 补丁：[v0.14.0-trace-continuity.patch](v0.14.0-trace-continuity.patch)，包含 31 个源码和测试文件。
- 版本、哈希、工具链和验收计数：[manifest.json](manifest.json)。补丁 SHA256 与实际验收版本一致。
- 上游 [LICENSE](LICENSE) 和 [NOTICE](NOTICE) 原样保留。差异文件明确列出 Ongrid 对上游的修改；这不是上游官方发布。

## 变更

Go 探针读取 SDK recording span 上下文，识别 SDK 已覆盖的操作，保留 SDK 未覆盖的子操作及自定义 Span。未覆盖操作携带 `obi.sdk.context=true`，由 Ongrid 的入口 Collector 保留，避免按实例删除全部 OBI Span。

补丁也修复 HTTP/1 大请求头的传播交接、HTTP/2 协商帧大小及 h2c 服务端时间戳、嵌套 PID 命名空间内的探针身份和 Auto SDK 激活，以及 parent-based 采样的父上下文。不增加用户配置项。

## 获取和构建

在 Ongrid 仓库中执行，产物只写入新建的临时目录：

```sh
ongrid_root=$(git rev-parse --show-toplevel)
obi_work=$(mktemp -d)
git clone --depth 1 --branch v0.14.0 \
  https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation.git \
  "$obi_work/source"
test "$(git -C "$obi_work/source" rev-parse HEAD)" = \
  13d9b0c3f600a060bd78820a63ebec882b10757e
git -C "$obi_work/source" apply --check \
  "$ongrid_root/patches/obi/v0.14.0-trace-continuity.patch"
git -C "$obi_work/source" apply \
  "$ongrid_root/patches/obi/v0.14.0-trace-continuity.patch"

docker run --rm --platform linux/arm64 \
  -v "$obi_work/source:/src" -w /src --entrypoint /bin/sh \
  ghcr.io/open-telemetry/obi-generator@sha256:3a8959e5253f2445b782b4f720ed54f6396fce350082486442e0b71ac02ff106 \
  -ec '
    export PATH="/usr/lib/llvm22/bin:$PATH"
    export BPF2GO=/go/bin/bpf2go
    make generate/all
    BPF_CLANG=clang BPF_CFLAGS="-O2 -g -Wall -Werror" \
      go generate ./pkg/internal/ebpf/gotracer
    GOFLAGS=-buildvcs=false make compile GOOS=linux GOARCH=arm64 \
      RELEASE_VERSION=0.14.0-local-sdk-context9 \
      RELEASE_REVISION=local-continuity
  '
```

候选二进制位于 `$obi_work/source/bin/obi`。显式生成全部 BPF 产物，再重新生成修改过的 Go 探针，避免增量构建复用旧对象。`manifest.json` 中的二进制哈希标识实际接受验收的 ARM64 产物，不承诺不同环境重建后逐字节相同。

已从全新官方 tag 检出验证 `git apply --check`；应用补丁后的全部 31 个文件与实际验收源码逐字节相同。补丁未包含生成的 BPF 对象、二进制、诊断程序或本机配置。 上述命令已在干净源码上重新生成双架构 BPF 并成功编译 ARM64 二进制；重建产物哈希单独记录，本轮未对该重建产物重跑现场矩阵。

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

`OBI_VERSION`、`scripts/fetch-obi.sh` 和正式发布依赖保持不变。当前上传使修复可以审查和重建，不代表正式安装已包含补丁。按当前发布约束，仍需修复进入官方 OBI Release，并用该原版重新验收后才能随正式依赖交付。
