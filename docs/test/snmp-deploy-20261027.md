# SNMP 配置测试环境部署（2026-10-27）

- 用户授权目标：`https://test.example.invalid/`。
- 来源：`feat/network-snmp-config` 工作区，基于 `5eb7ab1b`；未推送或合并。
- 版本：`v0.18.2-snmp.20261027.1`，只更新 Manager/Web，不升级 Edge。
- 变更级别：L2，单实例短暂重建；新增扫描源 Host ID 列及旧记录关联回填。
- 备份：`<DEPLOY_DIR>/`，数据库快照与原 Compose/镜像引用保留，目录仅 root 可访问。
- 构建：使用测试机已有 `golang:1.25-bookworm`，与当前运行镜像保持 glibc 兼容，CGO 启用；资源限制 2 CPU。
- 回滚：该目录 `python3 deploy.py rollback` 恢复原 Manager/Web；不自动覆盖数据库。使用自定义扫描源的采集配置需先停用，新增列和 Secrets 保留。
- 验证：部署后检查 `/healthz`、`/readyz`、首页静态文件一致性、两台 Host 在线、浏览器真实 API 配置弹窗及明暗主题。无真实 SNMP 凭证，不宣称交换机采集通过。

## 实际结果

- Manager/Web 已切换到 `v0.18.2-snmp.20261027.1`，首页文件与上传产物一致，`healthz` / `readyz` 均 200。
- 13 个原有容器均运行，两台 Host 均在线；Edge 未升级。剩余磁盘约 5.1 GB。
- 真实管理员会话进入“网络发现 → 配置 SNMP”，默认扫描源正确，两台在线 Host 均可选择；页面无 JS 异常，深浅主题截图已实看。
- 新增测试/保存接口均拒绝不存在的凭证（400），候选状态和设备归属不变；未创建测试凭证或设备。
- 环境没有正式网络设备及提供的 SNMP 凭证，未执行实际交换机查询、加入设备或定时采集。
- 镜像构建前本机磁盘不足导致 Docker 构建失败，清理本机 Go 编译缓存后释放约 7 GB；最终使用测试机现有 Debian 构建镜像成功完成，测试服务切换前完成运行库兼容检查。

## 2026-10-28 手动添加入口修正

用户指出原需求是网络设备页直接填写 SNMP 地址，而非复用发现候选入口。本轮补充右上角及空状态“添加 SNMP 设备”，新增独立测试/创建 API，不依赖候选、不伪造发现或拓扑记录。正式设备、配置和接口快照事务写入；重复指纹拒绝覆盖；测试失败不产生库存。

- Linux 容器中设备业务、HTTP、存储包 `go test -race` 全部通过，新增手动添加、无候选轮询、重复身份和失败回滚覆盖。
- 前端 26 条相关测试通过；Mock API 浏览器流程 10 条通过，新增空网络设备列表手动添加全流程，明暗主题截图已实看；生产构建通过。
- 更新版本 `v0.18.2-snmp.20261028.1`；部署目录 `<DEPLOY_DIR>/`，原版本及数据库快照保留。回滚用该目录 `python3 deploy.py rollback`。

修正版部署与真实页面验收完成：健康检查通过，两台 Host 在线；网络设备为空时右上角和空状态均有“添加 SNMP 设备”。弹窗地址初始为空，可以直接输入，两台扫描源可选，页面无 JS 异常。新增无候选测试/创建接口对不存在的凭证返回 400，网络库存保持不变。真实交换机认证和采集仍待有效设备凭证验证。

### 2026-10-30：空列表横向滚动修复

- 设备列表无数据时使用共享 `EmptyStateCard`，不渲染固定最小宽度的表格；加载期间不显示创建操作，有数据时保留原表格滚动。
- 验证：前端构建成功；Edges 单测 16 项、SNMP Playwright E2E 10 项通过。空列表在 1280px / 640px、深浅主题下无页面横向溢出，新增后恢复表格。
- 测试环境仅更新 Web 镜像 `ongrid-web:v0.18.2-snmp.20261030.1`。真实登录检查两种主题及宽度通过，截图位于本地 `output/snmp-empty-deploy/`；healthz / readyz 均为 200。
- 回滚：测试机 `<DEPLOY_DIR>/rollback.json` 保存部署前 Compose 参数，执行该参数数组即可恢复之前的 Web 服务。未更改数据库、Manager 或 Edge。

### 2026-10-30：保留一个可体验的虚拟 SNMP 设备

- 用户要求在 Edge 所在机器启用演示设备，保留供后续体验，不属于测试后删除的临时 fixture。
- 主机 / 扫描源：`example-host`，Host ID `1`。独立非 root systemd 服务 `ongrid-snmp-demo`，监听 `127.0.0.1:1161/UDP`，随系统启动；本机 Edge 通过 SNMP v2c 只读访问。
- 平台设备：`ongrid-virtual-snmp-demo`，Device ID `158`；凭据引用 `virtual-snmp-demo-local`，随机 community 仅保存于服务器受限文件和平台凭据库，不写入仓库。
- 演示使用 Net-SNMP 暴露该 Linux 主机的真实接口，首次读取 21 个接口，设备描述明确注明虚拟演示，不模拟交换机型号或 LLDP 拓扑。
- 配置每 60 秒轮询。服务配置 `/etc/ongrid-snmp-demo/snmpd.conf`；验证记录 `/opt/ongrid/snmp-demo/verification.json`。
- 停用顺序：先在平台关闭设备 158 的 SNMP 轮询，再执行 `systemctl disable --now ongrid-snmp-demo`。彻底清理时删除平台演示设备及专用凭据，然后移除该服务及专属配置、状态目录；不影响原有 `snmpd` 服务或 Edge。

### 2026-10-31：Host → SNMP 设备采集关系

- 新增有向 `collects_from`（采集自），持久化 Host ID，不指定网卡，不代表物理直连，不传播故障。创建、配置、后台分页核对都维护该关系；停用保留配置，改源移除旧自动关联。
- 后台修复既有设备节点，仅清理 `source=network_discovery` 的旧自动 `connected_to`；保留手工及其他来源的物理关系。Agent 工具单独返回 `collection_sources`，不混入物理 `neighbors`。
- 本地及 Linux `-race`：device / topology 业务、两者 store、device handler 均通过；Linux Agent 网络工具专项通过。前端 26 项单测、12 项 Playwright E2E 通过；深浅主题截图已检查。
- 部署版本 `v0.18.2-snmp.20261031.1`，备份及回滚脚本 `<DEPLOY_DIR>/`。回滚命令 `python3 deploy.py rollback`，恢复部署前 Manager/Web 镜像，不自动覆盖数据库；数据库快照保留供必要时单独恢复。未升级 Edge，未引入 LLDP 或命名空间改动。
- 真实环境验收：设备 158 自动获得 node 12，Host 1 → node 12 只有一条自动采集关系；禁用、改到 Host 2、恢复 Host 1 成功。注入的旧自动连接被修复，手工连接被保留；验收临时关系已删除，原配置已恢复，SNMP 接口数据正常。
- 线上详情入口：`https://test.example.invalid/devices/158?tab=topology`。验收报告在部署目录 `acceptance.json`；本地页面截图 `output/snmp-collection-deploy/live-collection-{dark,light}.png`。

### 2026-10-31：Agent 网络设备状态修复

- `query_network_devices` 不再返回 Host 心跳 `online`；`query_devices` 仅对 Host 返回该字段，SNMP 设备使用可达性及采集状态。在线筛选、最近观测筛选分页执行，不会漏掉后续页的可达设备。
- 明确扫描源离线/不可用、停用、采集异常、过期及未验证状态；这些情况不把目标直接判断为离线。新鲜度窗口为 `2 × poll_interval + 30 秒`，保留最近成功时间和错误。
- 专用工具接口数改为优先读取当前接口快照，修复手动添加设备被误报为 0 个接口。
- Linux 完整 Agent tools 包 `go test -race` 通过，覆盖状态分支、Host network 角色、SNMP 排在后续分页、接口快照等回归场景。
- Manager 部署版本 `v0.18.2-snmp.20261031.4`，Web 和 Edge 保持原版本。备份与回滚目录 `<DEPLOY_DIR>/`，执行 `python3 deploy.py rollback` 可恢复部署前 Manager；不自动覆盖数据库。
- 真实 Agent 验收使用临时会话询问网络设备是否在线及按在线状态列举；调用 `query_network_devices` / `query_devices` 均正确返回可达，临时会话完成后删除。验收记录保存在部署目录 `chat-check.json`。
