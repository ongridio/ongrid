# PRD-002：Edge 上联网络设备发现与统一身份

## 元信息

- 状态：开发中
- 作者：Ongrid
- 日期：2026-07-29
- 关联 issue：#199
- 关联 ADR：ADR-030-network-device-identity
- 关联 HLD：不适用（复用现有 Edge tunnel、Device 和 Topology）

## 背景

当前 `devices` 主要表示安装了 Edge 的主机。运维人员还需要在同一设备列表和拓扑中看到交换机、路由器、防火墙等上联网络设备，并能够在多个 Edge、LLDP 和 SNMP 结果之间保持同一个设备身份。

## 目标

- 开启网络发现后，Edge 能上报默认网关、邻居和 LLDP 发现结果；这些结果先进入候选区，不直接创建正式设备。
- 管理员可在设备页的“网络发现”标签配置 SNMP v2c/v3；只有只读 SNMP 身份校验成功后，候选才进入全部设备和正式拓扑。
- 同一网络设备经多个来源发现后只保留一个 `devices.id`。
- 管理 IP 或 hostname 变化不会制造重复设备。
- 自动发现只追加 `network` 角色，不覆盖人工角色或人工取消记录。
- 设备详情可查看发现来源、身份标识、置信度、接口和链路。

## 功能需求

### 第一阶段

- Edge 配置提供 `network_discovery.enabled` 开关，默认开启；管理员可在 Edge 环境文件中显式关闭。
- 默认发现默认网关、ARP/Neighbor、LLDP 邻居；不做 CIDR 全网扫描。
- SNMP 使用管理员配置的只读凭据，仅查询 sysName、sysDescr 和 sysObjectID；一期凭据仅在单次扫描内存中使用，不落库、不回显，接口和 LLDP MIB enrichment 后续补齐。
- SNMP 校验同时读取 IF-MIB 接口状态与 IPv4 地址归属；SNMPv3 支持 SHA-1/SHA-2 认证及 AES-128/192/256 隐私协议，配置项使用受控选项而非任意字符串。
- 未通过 SNMP 校验的候选不能调用 promote 接口绕过准入。
- 发现结果幂等写入 `devices`、`device_network`、`device_identifiers`、`network_interfaces`、`network_links`。
- 自动发现设备与发现 Edge 建立 `edge_devices(type=discovered)` 关系。
- 自动网络角色支持人工 override；人工移除后不自动加回。

### 身份归并

身份优先级和冲突规则由 ADR-030 定义。只有管理 IP 或 sysName 不得永久合并。

### 暂不包含

- CIDR 主动扫描。
- 自动写入网络设备配置。
- 将堆叠成员拆成多个顶层设备。

## 非功能需求

- SNMP 凭据不进入日志、API 响应或发现结果快照；当前一期不持久化凭据，后续如需复用应接入 Secret vault 加密存储。
- 单次发现批次有界：设备、接口、链路和标识数量均有上限，避免 Edge 将异常响应放大到 Manager。
- 发现上报失败可重试且幂等，不阻塞 Edge 心跳和主机指标采集。
- 所有写入使用短事务；不在事务内调用网络设备。

## 数据库变更

新增 `device_network`、`device_identifiers`、`network_interfaces`、`network_links`。详情见 ADR-030 与实现中的 GORM 模型。现有 `devices` 和 `edge_devices` 保持兼容。

## 验收标准

- [ ] 开关关闭时 Edge 不执行网络发现。
- [ ] 开关开启后能上报默认网关和 LLDP 邻居。
- [ ] LLDP 与 SNMP 命中强身份时归并为同一个 `devices.id`。
- [ ] 只有 IP/sysName 相同不会自动永久合并。
- [ ] 自动发现设备获得 `network` 角色，人工角色不被覆盖。
- [ ] 设备详情能列出来源、标识、置信度、接口和链路。
- [ ] SNMP 接口快照能展示 ifIndex 对应的 IPv4 地址。
- [x] SNMP 凭据不会出现在日志和 API 响应中。
- [x] 候选设备单独展示在设备页“网络发现”标签，不污染“全部设备”。
- [x] 只有 SNMP 身份校验成功的候选才会创建正式 Device。
- [ ] 重复批次不会产生重复设备、标识、接口或链路。

## 任务拆分

- [x] Task 1：补齐 PRD 与身份归并 ADR。
- [ ] Task 2：新增四张网络设备专属表和迁移。
- [ ] Task 3：实现发现结果身份解析与幂等 upsert。
- [ ] Task 4：增加 tunnel 上报契约和 Edge 默认网关/LLDP 采集。
- [x] Task 5：增加一次性 SNMP 只读探测与准入校验；凭据持久化仍待接入 Secret vault。
- [ ] Task 6：设备详情和拓扑消费网络接口/链路。
- [x] Task 7：补齐接口 IPv4 地址归属及 SNMPv3 SHA-2/AES-192/256 配置。
- [x] Task 8：为 Agent、Skills 和 Workflow 增加网络设备资产与主机邻接关系的只读查询能力。
