# Frontier RPC 截止时间修复

适用基线：Frontier v1.2.4。其双向 RPC 转发原来固定增加 30 秒超时，
会提前截断 Manager 已设置较长截止时间的 `host_files.du_summary` 等调用。

补丁保留调用方的 context deadline；没有 deadline 的调用仍保留 30 秒默认值。
不修改业务工具的超时预算，不放宽权限，不重试执行命令。

## 构建与验证

使用干净的 v1.2.4 源码目录：

```sh
make docker-build-broker-rpc-fix FRONTIER_SRC=/path/to/frontier-v1.2.4
```

默认产出 `singchia/frontier:v1.2.4-ongrid-rpc.1`，与原版标签分开。
Docker BuildKit 从本目录加载补丁；上游源码不匹配时构建失败，不会静默忽略。
现有 release 镜像镜像同步流程不变；部署时需要明确选择上述修复镜像。
仅部署 Manager 不会修复 Frontier 的 30 秒截断。

也可以在源码副本中应用补丁，运行真实 SDK → Frontier → SDK 链路测试：

```sh
patch -p1 < /path/to/ongrid/deploy/frontier-patches/rpc-deadline.patch
go test -race ./pkg/frontier/exchange -run TestOngridForwardedRPCDeadline -count=1 -timeout=4m
```

测试覆盖双向转发：40 秒预算下的 31 秒调用成功，100 毫秒预算仍及时超时，
没有 deadline 的 35 秒调用仍被默认 30 秒限制终止。测试约需两分钟。

## 上线和回滚

先在测试环境替换 Frontier 镜像，等待 Edge 和 Manager 自动重连，验证设备在线、
短调用以及耗时超过 30 秒的磁盘扫描。替换 Frontier 会短暂断开长连接，避免在写操作执行中重启。
镜像替换不涉及数据库。回滚时恢复原 Frontier 镜像；原版的 30 秒限制也会恢复。
上游发布等效修复后，升级上游并删除本补丁。

## 当前验收状态

RPC 功能断言覆盖了上述双向调用行为，但 `-race` 检测到 geminio v1.3.0-rc.2
的 `application.stream.Hijack` 写入与 `handleInRequestPacket` 读取 `hijackRPC` 的并发竞争。
因此此补丁仍是待验收候选，不能宣称完整回归通过；暂未构建或部署修复镜像。
常规 `docker-build-broker` 和 release 镜像选择不受影响。

未应用补丁的 v1.2.4 原有 `TestExchangeForwardRPCToEdge` 也复现相同竞争，确认不是 deadline 补丁引入。
