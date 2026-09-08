# 官方自动埋点验收应用

Java、Node.js、Python 各提供 HTTP `/orders/42` 和 gRPC Health Check，包含成功、失败和 80ms 慢请求。使用官方 Agent/自动埋点；不实现自研 SDK。JSON 标准输出日志继续交给现有文件采集通道。

从仓库根目录执行 `scripts/apm-test/run-languages.sh`。需要 Docker、Go、Node/npm、Python 3、Java 17+，以及下载锁定依赖的网络权限。脚本将依赖与 Java 构建缓存放在 `/tmp/ongrid-apm-languages`，Python gRPC 代码也在该临时目录生成，不写回源码。

测试使用独立 Collector、Tempo 和 Prometheus，退出自动删除这些测试容器与卷。端口 13200、19090、14319、18080、18081 必须空闲。默认依次检查开/关 Trace 采样的实例、真实原生指标、链路和告警生命周期。结果写入 `output/apm-acceptance`。

- `APM_TEST_LOAD=1`：增加 100/500/1000 服务的合成指标查询测试。
- `APM_TEST_OVERHEAD=1`：增加无埋点、仅指标、指标加全采样的 HTTP 开销对照，各三轮、每轮十秒。
- `make test-apm-acceptance`：再验证真实请求日志经 Collector 写入 ES 并精确查回，以及 Go HTTP/gRPC、按需 pprof 和相关前端回归。

开销已独立验收时，可用 `APM_TEST_OVERHEAD=0 make test-apm-acceptance` 只重跑功能与容量；开销单独执行 `APM_OVERHEAD_ONLY=1 APM_TEST_OVERHEAD=1 scripts/apm-test/run-languages.sh`。报告必须区分这两个执行结果。

当前锁定版本中 Java 提供 HTTP/gRPC 原生请求指标；Node/Python 的 gRPC 自动埋点提供 Trace，未提供原生请求指标。测试明确检查这个边界，不用已采样 Span 冒充全量请求指标。完整记录见 [验收报告](../../docs/test/apm-acceptance-20260908.md)。

## 持续运行的 Edge 演示

`compose.yaml` 运行 Go、Java、Node.js、Python 四个服务和低频请求生成器。用于已有 Edge 的 Linux 主机，OTLP HTTP 接收器需监听 `127.0.0.1:4318`。镜像使用官方 SDK/Agent，所有业务端口仅监听本机；`network_mode: host` 让请求和 OTLP 直接进入同机 Edge。

```sh
sudo install -d -o 65532 -g 65532 /var/log/ongrid-apm-demo
docker compose -f examples/apm-languages/compose.yaml up -d --build
```

| 服务 | HTTP / gRPC 端口 | 语言 |
| --- | --- | --- |
| apm-demo-go | 18080 / 18081 | Go |
| apm-demo-java | 18082 / 18083 | Java |
| apm-demo-node | 18084 / 18085 | Node.js |
| apm-demo-python | 18086 / 18087 | Python |

环境为 `development`，业务命名空间为 `apm-demo`。每种语言在完成一组 HTTP/gRPC 请求后等待 2 秒；每 10 组包含 1 组失败和 1 组慢请求。Go 标准 gRPC Health 服务只模拟成功/失败，慢请求在 HTTP 产生。实例以 `ubuntu-<language>-1` 标识，部署到其他主机时应改为唯一实例名。进程自动重启，单服务内存限额 256 MiB（Java 为 512 MiB），CPU 上限 0.5 核。

在设备的 logs 插件配置中追加四个 `sources`，各自使用 `id`/`service_name: apm-demo-<language>`、`include: ["/var/log/ongrid-apm-demo/<language>.log"]`、`parser: json`、`start_at: beginning`。语言文件名为 `go/java/node/python`。保留原有来源与现有日志后端配置。请求日志自带服务身份、Trace ID 和 Span ID；启动诊断可能是普通文本。文件需配置主机 logrotate，例如每日轮转、`maxsize 5M`、`rotate 3`、`compress`、`copytruncate`、`missingok`、`notifempty`、`su 65532 65532`；按主机 timer 周期检查大小。

从应用性能页面查看四个服务，进入服务可跳转链路和日志。指标每 5 秒导出；Edge 采集及后端索引还会带来短暂延迟。刚启动时“最近 1 小时”的平均 RPS 会偏低，可以改用较短时间范围。Node/Python 的 gRPC 原生请求指标边界见上文。

停止（保留历史观测数据）：

```sh
docker compose -f examples/apm-languages/compose.yaml down
```

停止后可从设备 logs 配置移除这四个演示来源；不要删除其他来源。当前本地部署位于 Ubuntu Edge 的 `/opt/ongrid-apm-demo/compose.yaml`，可用 `orb -m ubuntu -u root docker compose -f /opt/ongrid-apm-demo/compose.yaml down` 停止。

运行状态稳定后，可用以下命令检查四种语言各 10 组 HTTP/gRPC 请求（包含正常、慢请求和预期失败），非预期结果返回非零退出码：

```sh
docker compose -f examples/apm-languages/compose.yaml exec -T traffic python /app/traffic.py --check
```
