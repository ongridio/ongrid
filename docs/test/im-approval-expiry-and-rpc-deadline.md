# IM 审批残留与 RPC 30 秒截断回归

## 修复范围

- 同步会话审批 `host_bash`、`cloud_bash`、`mcp_call`、`agent_tool` 遵守现有 30 分钟等待上限。
  Reconciler 处理重启后遗留的 pending 记录；数据库条件更新同时阻止过期记录被批准，消除清理间隔内的竞态。
- 与会话等待共用超时常量。工作流、没有 session 的审批、异步安装审批不按这个上限清理。
- 已批准或执行中的记录不会被清理线程伪造为失败，也不会自动重放。
- 过期记录进入 failed 后，由持久化通知更新原审批卡片、去掉操作按钮并结束刷新。
  保留卡片作为审计结果，不删除平台消息。正常批准后的卡片更新仍走原有投递重试机制。
- Frontier 修复和独立镜像构建见 `deploy/frontier-patches/README.md`。

## 自动验证

```sh
go test -race ./internal/manager/biz/approval ./internal/manager/biz/imbridge/... ./internal/manager/service/aiops -run 'Test.*(Approval|HumanApproval|ExecutionContext)' -count=1
```

新增审批测试覆盖历史无租约 pending、近期合法审批、工作流、异步安装、没有 session、
已批准与已执行记录。通知测试覆盖飞书、Slack、Telegram、钉钉：过期只编辑原消息，
不新发卡片、无批准/拒绝 token、终态不再刷新。

## 环境验收（部署后执行）

1. 原有旧 pending 审批在清理后变为失效，原飞书卡片按钮消失，不跑到新会话重新发消息。
2. 新审批批准/拒绝后原卡片进入对应状态；重复点击不重复执行。
3. 观察短命令正常，长磁盘扫描超过 30 秒仍返回数据；按原业务预算及时取消。
4. 验证重启时尚未批准的同步审批到期清理，正在执行的操作不被改写终态。

本轮不修改数据库结构。回滚 Manager 可恢复旧判定逻辑，但不恢复已经合法过期的审批；
用户应重新发起这些操作。Frontier 镜像回滚见补丁说明。

## 本轮结果

审批相关上述 `-race` 回归通过；补丁对干净 Frontier v1.2.4 的应用校验通过。
Frontier 双向 31 秒调用、短预算与默认预算功能断言符合预期，但完整 `-race` 测试失败：
原版依赖 geminio 的 hijackRPC 注册/读取存在并发竞争，已用未修改的上游测试复现。
因此 Frontier 保留为独立候选构建目标，尚未部署。

Manager 审批修复已部署为 `v0.18.2-im.20261102.1`：健康与就绪检查通过，
两条遗留审批自动失效，过期 pending 清零；飞书原卡片的终态投递完成且无投递错误。
两台 Edge 在线，飞书、Slack、钉钉长连接恢复 running。尚未进行新的真人审批点击验收。
