# 真实 Edge 三轮卸载 / 重装验收

## 范围与方法

- 用户明确指定 `https://101.34.63.91/` 并要求反复卸载安装。
- 仅操作测试机 `VM-4-17-ubuntu` 的真实 systemd Edge；`VM-0-10-ubuntu` 保留在线作为对照。
- Manager 为 `v0.18.2-host.20261018.2`，重复安装相同 Edge 二进制 `v0.18.2-host.20261018.1`。
- 每轮执行仓库 `deploy/install/edge/uninstall.sh`，确认服务二进制、配置目录、插件运行状态目录均删除；用 `install-edge.sh` 离线安装包重新安装。不是进程重启，也不是模拟隧道。
- 每轮新建 Edge 获取新凭据；保留测试环境非身份配置，清空显式数字身份。指纹由实际机器重新采集，未人为指定 Host ID。
- 本机磁盘紧张，安装资源通过已有二进制构建离线包，备份使用硬链接；没有重复下载安装资源。未测试公网下载渠道。

## 结果

| 轮次 | 旧 Edge | 新 Edge | Host | 端到端耗时 | 结果 |
|---|---:|---:|---:|---:|---|
| 1 | 1 | 12 | 1 | 约 103 秒 | 通过 |
| 2 | 12 | 13 | 1 | 约 86 秒 | 通过 |
| 3 | 13 | 14 | 1 | 约 89 秒 | 通过 |

每轮验证：

- 卸载后 Host 保留、转离线、执行不可用；重新安装后 Host ID 不变，current_edge_id 指向新安装。
- 新 Edge 的 device_id 反向关联同一 Host；设备总数始终为 2，没有重复 Host。
- 当前插件 enabled/spec 与原有配置一致，没有进行配置补写。本环境的现有配置不能代表所有自定义配置迁移场景。
- 新内存指标样本时间晚于本轮开始时间，device_id=1；原有指标历史仍能查询。
- 每轮 logger 写入唯一标记，日志通过 Host 查询可见；之后各轮仍能查到之前标记。
- 按 Host ID 执行真实 localhost ping 成功，实时 host_load 恢复。
- 原告警 3、4、5 仍与 Host 1 关联；旧 Edge 保持离线。
- 对照设备持续保留原 Host / Edge 身份，各轮结束均在线。

额外验证：

- 第二轮期间，用旧 Edge 12 执行技能返回 HTTP 400 `superseded edge installation`，没有复用当前连接。
- 第三轮后经本机真实 OTLP HTTP 接收器发送一个 span；请求故意携带 device_id=999999，读取 Trace 详情后标签为 Host 1，当前 Edge 是 14，证明真实采集链路按 Host 标注并覆盖错误输入。未模拟 Collector。
- 最终 Prometheus 查询仅发现 device_id 1、2，没有 12、13、14 的 Host 指标分叉；新 Edge 14 的指标 API 能读取 Host 历史（本次返回 121 个时间点）。
- 最终 logs / traces / hostmetrics / procmetrics / metrics / autoapm 均报告 running。autoapm 的 running 只代表发现进程运行，本轮未验证 eBPF 自动插桩。

证据：

- [三轮记录](artifacts/edge-reinstall-20261018/rounds.json)
- [最终身份、指标和插件状态](artifacts/edge-reinstall-20261018/final-check.json)
- [旧 Edge 执行拒绝](artifacts/edge-reinstall-20261018/old-edge-rejection.json)
- [真实 Trace 标签校验](artifacts/edge-reinstall-20261018/trace.json)

## 发现的问题与未覆盖项

1. **上线早于采集就绪**：冷安装先注册在线，首次插件配置请求发生在连接就绪之前时走空配置回退，约等 60 秒定时轮询后才启动插件。身份正确，但在线状态不代表遥测立即可用。本轮没有修改这个启动行为。
2. **metrics 历史错误不清除**：插件启动过程中 9464 暂未监听，曾记录 connection refused。后续直接检查该端点已 HTTP 200，但插件健康仍保留旧 last_error；`metrics/plugin.go` 成功 scrape/push 路径没有清除该字段。这属于健康状态表达问题，不能据旧错误判定当前采集失败，尚未修复。
3. 数据库、Profiles 在本环境未启用，没有验证其业务数据连续性。Trace 验证了重装后的真实链路和 Host 归属；未对三轮分别注入 Trace 并验证跨轮历史。
4. 仅覆盖此 Linux 实机及现有配置，未覆盖 Kubernetes 重装、macOS/Windows、所有自定义插件配置继承。
5. 脚本最后尝试调用不存在的 PATCH Edge 重命名接口，得到 405；三轮核心验证此前均已完成，旧 Edge 已清理。已补独立最终检查，保留最终 Edge 名称的 reinstall 后缀，Host 用户可见名称保持不变。这是验收脚本错误，不是身份绑定失败。

## 最终环境与清理

- Host 1（VM-4-17-ubuntu）→ Edge 14，在线。
- Host 2（VM-0-10-ubuntu）→ Edge 2，在线。
- 旧 Edge 1、12、13 已删除，Host 与观测历史保留。
- 临时工作流均已删除；三条验收日志和一条 Trace 作为验证证据随正常遥测保留策略过期，没有删除共享遥测数据。
- 临时安装资源、插件备份、运行状态备份已清理，测试目录约 27 MB；保留权限受限的恢复二进制、配置、最终凭据、脚本和报告。磁盘可用空间约 1.8 GB。
- 首页 / healthz / readyz 均 HTTP 200，systemd Edge active。
- 现场目录 `/opt/ongrid/test-edge-reinstall-20261018/`。如需恢复二进制，使用 backup/ongrid-edge，保留当前 Edge 14 凭据；不要恢复旧 Edge 1 凭据，旧安装已注销。
- 本轮没有修改或发布业务代码，没有推送、合并。
