# Host 与 Edge 双向关联

Host/device_id 是指纹确定的持久业务身份。Edge 是安装实例和长连接身份。
执行路径：Host → current_edge_id → 校验 host 关联 → Edge 隧道。
采集路径：认证 Edge → host 关联 → Host 数据。禁止同号 ID 回退。

## 当前绑定

Device.current_edge_id 保存当前安装实例；edge_devices 保留历史归属。注册事务串行更新 Host 行和关联，已创建的旧实例不能抢占更新的安装实例。使用 Edge 单调自增 ID 判定安装代际；这是实例代际比较，不是把 Edge ID 当作 Host ID。
删除 Edge 不删除 Host；Host 删除仍是独立业务操作。离线与心跳只有当前 Edge 可以更新 Host 状态。

## 升级与回滚

删除旧 backfillFromEdges。新增可空 current_edge_id；仅从已经注册、与 edge.device_id 一致的 host 关联初始化，不创建 Host 或补猜测关联。没有可信关联时要求重新注册。
老版本不认识当前绑定，回滚二进制会恢复旧的选择行为；回滚前停止写入并保留数据库备份，不能把旧启动回填重新运行在已修复数据上。

## 验证

覆盖不同号与同号 ID、注册重试、同指纹重装、旧实例重连、旧实例下线、删除最后一个 Edge、升级重复运行、无连接历史查询、审批期间换实例和采集反向归属。所有主机入口复用相同解析。

## 执行入口和兼容

Agent 工具统一为 `get_host_summary`、`rank_hosts`、`find_outlier_hosts`。旧名称仅在已保存配置的分发和权限匹配时归一化；禁止把旧 `edge_id` 参数当作 Host ID。技能 HTTP 接口新增 `device_id`，旧客户端的真实 `edge_id` 必须反查 Host 且仍是当前安装实例。技能审计同时保存 Host 身份与执行 Edge 实例。

插件配置 wire 新增 `device_id`。为支持滚动升级，旧 `edge_id` wire 字段暂时继续承载其历史约定的 Host ID；新 Agent 优先读取明确的 `device_id`，禁止使用隧道 Edge ID 补值。此兼容字段不能用于选择执行通道。

移除启动时“无存活 Edge 就删除 Host”的清理逻辑。Kubernetes controller 错误关联可解除，但解除关联不删除 Host。告警旧表不再用数值复制将 Edge ID 改写为 Host ID。

## 环境验收边界

代码测试不能证明线上已经被历史回填污染的关联自动恢复。升级前应先备份并只读核对 `devices.current_edge_id`、`edges.device_id`、`edge_devices` 和注册记录；不能仅依据相同 ID 修复。真实重装、历史指标连续性和旧实例迟到事件需要在测试环境完成端到端验收。本分支不包含对线上数据的自动纠正。

## 后续连接加固

[Issue #527](https://github.com/ongridio/ongrid/issues/527) 跟踪两个尚未修复的边界：连接映射缺失时底层仍会将 Edge ID 回退为 transport ID；Host 当前绑定的解析与发送之间尚无代次 fencing。当前绑定校验不代表端到端原子执行保证。后续需明确重装切换及在途请求语义，禁止自动重放有副作用的命令。


## Host 长期插件配置

`host_plugin_configs` 以 `(device_id, plugin_name)` 唯一保存长期配置。注册及升级只接纳明确绑定的安装配置；已有 Host 配置始终优先，新安装的默认值不能覆盖用户意图。删除使用 tombstone，避免重新注册时复活旧配置。

设备接口 `GET /v1/devices/{id}/plugins` 和 `PUT /v1/devices/{id}/plugins/{name}` 使用 Host ID。旧 Edge 配置接口必须验证当前双向绑定，过期安装不能修改 Host 配置。无安装时仍可编辑长期配置，实际通知仅发送给当前安装。APM 新链接使用 `/apm/capture/devices/{device_id}`；旧 `/hosts/{edge_id}` 链接必须经 Edge 显式关联解析，不做数值回退。

Profiles 是安装级采样会话，不随重装重放。数据库采集配置虽长期保存，但凭据文件仍在 Edge 本地；重装不保证恢复本地密钥，必须重新下发或配置凭据。无当前安装时拒绝数据库采集配置写入。Kubernetes 的集群级策略仍由原集群配置入口管理。

升级前备份数据库，执行 `docs/migrations/20261019_host_plugin_configs.up.sql`，然后启动新 Manager 接纳明确绑定的旧配置。不要混用新旧 Manager 接受配置写入：旧版本不认识 Host 配置表。首次启动失败且没有新写入时，可恢复旧二进制与数据库备份；已有 Host 写入后必须停写并导出配置、核对当前绑定后再制定回退，不能直接删表。Edge 二进制无需随此配置存储变更更新。
