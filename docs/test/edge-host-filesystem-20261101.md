# Edge systemd 主机文件系统视图修复

## 范围

仅修改 `deploy/install/edge/ongrid-edge.service` 及安装器内嵌 unit：
- ProtectSystem=false、ProtectHome=false、PrivateTmp=false。
- 移除 ReadWritePaths，保留 StateDirectory 与 NoNewPrivileges=true。
- 不修改 Manager、Edge 的生产 Go 逻辑，不修改 cmd policy、后台写开关或审批行为。
- 新增模板一致性检查与真实文件写入的 handler 回归测试。

原因：原 ProtectSystem=strict 使 Edge 命令看到宿主机文件系统的只读挂载视图，导致审批写入失败及挂载状态误判。

## 测试环境变更

2026-11-01 15:46（北京时间），test.example.invalid，仅更新现有 Edge unit 后 daemon-reload / restart。未更新二进制，未重启 Manager，未执行会话中原有备份删除任务。

原配置保存在 `<BACKUP_PATH>`。

回滚：将上述备份复制回 `/etc/systemd/system/ongrid-edge.service`，执行 `systemctl daemon-reload` 和 `systemctl restart ongrid-edge`。

验收：
- systemctl: active；ProtectSystem/ProtectHome/PrivateTmp=no，NoNewPrivileges=yes。
- 从实际 Edge 挂载命名空间检查 `/opt`：`/dev/vda2 / ext4 rw,relatime`。
- 同一命名空间在 `/opt` 创建随机测试目录，写入/读取文件并清理成功。
- 日志确认 tunnel connected / registered with cloud，edge_id=14。
- 模板一致性脚本、安装器 bash 语法检查、升级 hook guard 通过。

注意：旧安装仅升级二进制不保证重写 unit；需要更新 unit 并重启服务。此次测试环境已显式完成该步骤。其余测试结果见本次执行记录；命名空间检查和 handler 测试不等同于真实 Web/IM 审批 E2E。

Linux/amd64 交叉编译的 `-race` 测试二进制已在测试机执行通过：Edge bash 全部测试（包含 31 秒命令、未批准写入被拒绝、批准路径真实落盘），Manager tools 的 `Test.*Bash` 测试（命令分类、审批前不下发、读取请求保持只读策略等）。测试二进制及临时文件均已清理。

## 产品升级自举：v0.18.2-edge.20261101.2

### 实现

- 沿用 root oneshot / apply-pending-upgrade.sh，不增加远程执行接口，不变更 cmd policy、写权限和审批规则。
- 新 hook 使用 `/etc/systemd/system/ongrid-edge.service.d/90-ongrid-host-filesystem.conf` 管理四个文件系统配置项；保留用户原 unit 的其他设置。仅内容改变时写入并执行 daemon-reload。
- 配置变更前记录 drop-in 原有内容或不存在状态；升级未健康时，恢复配置并重载，再恢复旧程序。恢复失败保留回滚元数据。
- 兼容旧 hook：旧 hook 只更新已有允许目录下的程序和脚本。新 Edge 在注册前检查待升级版本和配置就绪标记，原子写一次自举请求后退出；下一次 pre-start 使用新 hook 完成迁移。只接受匹配当前待验证版本且尚未 ready 的请求。
- 新 Edge 在注册前检查生效的 ProtectSystem / ProtectHome / PrivateTmp；未符合预期就不注册、不写健康标记。因此无需修改 Manager API/状态机，现有版本重连确认即可等待完整迁移。
- 同一升级任务可能经历两次启动；只有用户第一次升级旧 hook 需要额外启动。后续已更新 hook 的升级直接在 pre-start 完成迁移。
- Windows、非 systemd 进程和不属于 ongrid-edge.service 的手工进程不运行该自举逻辑。

### 验证记录

测试环境 test.example.invalid，Edge 14 / host device 1。
先恢复原来的 strict / ProtectHome / PrivateTmp 配置作为迁移前置条件，保留旧 hook（此前仅 SSH 替换了 binary/unit，没有替换 hook）。
升级包仅含本次变化的 Edge 程序与 hook，两者均有 manifest SHA256；采集器保持现状。通过产品 REST 接口 `/api/v1/edge-upgrade-jobs` 提交任务，不通过 SSH 替换运行程序。

- 任务 #2：v0.18.2-edge.20261101.1 → v0.18.2-edge.20261101.2，succeeded。
  - 17:37:21 旧 hook 完成二进制/脚本替换。
  - 17:37:22 新 Edge 在注册前请求 bootstrap 并退出。
  - 17:37:27 新 hook 更新配置并 reload，随后新 Edge 注册。
  - 17:37:30 Manager 完成任务，未在中间启动误报成功。
- 任务 #3：同版本 force_reinstall，succeeded，17:38:08 正常启动、17:38:10 完成任务。日志没有配置修改和 reload；三个属性均 no，NoNewPrivileges=yes。
- `scripts/test-apply-pending-upgrade.sh`：旧 hook 自举、正常迁移、配置不变不 reload、恢复原 unit 之外的用户配置、失败回滚、reload 及恢复 reload 失败重试全部通过。
- `cmd/ongrid-edge/systemd_upgrade_linux_test.go`：Linux amd64 `-race` 测试通过，覆盖自举请求、ready 放行、配置未生效拒绝注册。

### 回滚

自动回滚沿用程序 `.previous` 备份，并增加 drop-in 恢复；新 hook 判断升级健康前保留这些备份。
手工应急回滚：停止 Edge，恢复备份程序和原 unit，移除或恢复本次新增的 `90-ongrid-host-filesystem.conf`，daemon-reload 后启动。只恢复主 unit 不够，因为 drop-in 的优先级更高。
保留 SSH 阶段备份 `<BACKUP_PATH>` 及产品验收前 unit `<BACKUP_PATH>`。不要在回滚时自动恢复/删除业务数据。

### 两台 Edge 产品升级复验（2026-11-01 18:11–18:14 北京时间）

- 任务 #4：Edge 14 / Host 1，同版本强制重装 v0.18.2-edge.20261101.2，8 秒完成。重启后重新注册；本次日志无配置迁移或 reload，systemd 三项文件系统隔离均 no，NoNewPrivileges=yes。
- 任务 #5：Edge 2 / Host 2，v0.15.0 → v0.18.2-edge.20261101.2，约 16 秒完成；产品任务等待新版本重新注册后成功。
- 最终两台在线，Edge ID 与 Host ID 关联均不变；两台 processes 产品接口正常返回。
- Prometheus node_cpu_seconds_total 两台均有新样本，检查时样本分别约 7 秒、8 秒前，证明升级后指标仍在采集。
- Edge 2 私网 SSH 不可达，本轮通过产品任务、新版本注册、进程查询和指标验证，未直接读取其 systemd 配置及启动日志。
- 插件接口返回 metrics/logs 的 stopped 状态，不能单凭该字段断言实际采集停止；上述实时指标验证正常。未在本轮验证完整日志采集链路或真实故障回滚。
