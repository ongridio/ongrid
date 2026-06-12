# HLD-015 — 网络设备发现（Network Device Discovery）

**Status**: draft
**Date**: 2026-06-12
**Author**: singchia + Claude
**Related**: ADR-015 (edge plugin 框架 — 复用插件生命周期/配置下发), HLD topology (nodes/relations 图模型 — 渲染底座), ADR-025 (topology BaseTools — expand_topology 复用), device 实体拆分 (EdgeDevice.Type=Discovered 预留常量), ADR-012/013 (edge 遥测管线 — P3 SNMP 指标复用)

> 注：device model 里 `EdgeDeviceRelationDiscovered = 2` 自 device 实体拆分起已**预留但未实现**，本 HLD 是它的落地。HLD-001~013 历史落在 ongrid-cloud/docs/design；开源拆分后 ongrid 为 feature 主仓库，设计文档落在 ongrid/docs/design。

## 目标

让 ongrid 自动发现 LAN 上**装不了 agent 的网络设备**（交换机 / 路由器 / 防火墙 / AP 等），并渲染进架构拓扑图：

- edge agent 代为扫描其所在网段，发现存活设备 + 识别身份（厂商 / 型号 / 类型 / 主机名）；
- 发现的设备进入 `devices` 表（`source=discovered`），镜像成拓扑 `node`；
- 通过 **LLDP/CDP 邻接**关系，**自动画出物理连线**（谁连到哪台交换机）；
- operator 在 UI 审阅发现结果，一键纳入主拓扑图。

**本期范围（MVP = P1 + P2）**：存活发现 + SNMP 身份识别 + LLDP 邻接连线。SNMP 接口指标采集（P3）只预留接口、不实现。

## 非目标

- 不做 SNMP 接口流量 / 状态指标采集与告警（P3 预留，§9）。
- 不做主动漏洞扫描 / 全端口指纹（不是 nmap，避免被 IDS 当攻击）。
- 不做跨路由发现（只发现 edge 直连可达网段 + operator 显式配置的子网）。
- 不做租户隔离（私有化 MVP，沿用 feedback_skip_tenant_logic：owner 用 `created_by`）。
- 不做网络设备 agent 化（它们永远 agentless，只被被动探测，绝不写配置）。

## 现状盘点（为什么现在能做）

2026-06-12 四路代码调研结论：

| 维度 | 现状 | 对本 HLD 的意义 |
|---|---|---|
| 拓扑模型 | `nodes.type` 自由字符串、无枚举；关系类型可自定义（声明 direction + semantics_tag）；前端 React Flow + Dagre 按 `tier` 分层 | **零改造**容纳 switch/router/firewall；加新设备类型只需注册 node_type |
| Discovered 关系 | `EdgeDevice.Type=Discovered(2)` 常量已定义、**无创建路径** | 本 HLD 实现它 |
| edge 插件框架 | Plugin 接口（Configure/Start/Stop/HealthSnapshot）成熟；in-process(metrics) + subprocess(logs) 两型；plugin_config `spec_json` 下发 | 发现插件 = 新 in-process 插件，最像 `metrics` |
| 网络命令白名单 | cmdpolicy 已放行只读 `ip neigh`(ARP) / `ping`(非 flood) / `traceroute` / `nc -z`；受 NetworkHostAllowlist gate | ARP/ping 存活发现有现成权限基座 |
| SNMP | **零基础**（无 gosnmp、无任何发现代码） | 需引入 `gosnmp` + 全新实现 |
| device 创建路径 | **只有** edge register（FindOrCreateByFingerprint）；无手动 / 发现入口 | 需新增 discovered 设备 ingester |
| 凭证处理范式 | databasemetrics 已确立 write-only secrets 模式（manager 不存明文 / 0600 / 不回显） | SNMP 凭证直接复用 |

## 架构总览

发现引擎放在 **edge 侧（agentless via edge）**：网络设备装不了 agent，但 edge 就在 LAN 里，让它代扫。

```
┌─ edge agent ─────────────────────────────┐
│  networkdiscovery 插件 (in-process)         │
│   每 scan_interval:                          │
│   1. 存活: ip neigh(ARP) + ICMP ping sweep   │
│   2. 身份: 对存活 IP 做 SNMP GET             │
│   3. 邻接: SNMP LLDP-MIB lldpRemTable         │
│   → push_network_discovery RPC (批量)         │
└──────────────┬────────────────────────────┘
               │ tunnel
┌──────────────▼─ manager ───────────────────┐
│  NetworkDiscoveryIngester                     │
│   - FindOrCreate device(source=discovered)    │
│   - EdgeDevice(Type=Discovered)               │
│   - mirror → topology node(type 按 SNMP 推断)  │
│   - LLDP 邻接 → relation(connects_to)          │
└──────────────┬────────────────────────────┘
               │ HTTP
┌──────────────▼─ 前端 ──────────────────────┐
│  发现待确认池 → operator 一键入图             │
│  React Flow + Dagre: switch/router/firewall   │
│  node + connects_to 物理连线                   │
└─────────────────────────────────────────────┘
```

## 协议分层选型

务实地逐层叠加，每层独立可用：

| 层 | 协议 | 拿到什么 | 阶段 |
|---|---|---|---|
| 存活 | ARP 表（`ip neigh`）+ ICMP ping sweep | IP / MAC | **P1** |
| 身份 | **SNMP** v2c/v3：`sysName` / `sysDescr` / `sysObjectID` / `sysServices` / `entPhysicalSerialNum` | 主机名 / 厂商 / 型号 / 设备类型 / 序列号 | **P1** |
| 厂商 | MAC OUI 本地查表（内置 OUI 库） | vendor | **P1** |
| 邻接 | SNMP **LLDP-MIB** `lldpRemTable`（退回 CDP `cdpCacheTable`） | 谁连谁（chassisId / portId）→ 物理连线 | **P2** |
| 指标 | SNMP **IF-MIB** `ifTable`/`ifXTable`（接口流量/状态/错误） | 监控 + 告警 | P3（预留） |

**为什么这么选**：
- ARP+ping 是**最低成本存活探测**，且 cmdpolicy 已放行，无需新权限；先圈出"网段里有哪些 IP/MAC 活着"。
- SNMP 是网络设备的**事实标准管理协议**，`sysObjectID` + `sysServices` 足以区分交换机/路由器/防火墙/主机，且只读 GET 安全。引入成熟的 `gosnmp`。
- LLDP 是**链路层邻居发现的标准**（IEEE 802.1AB），交换机普遍开启，通过 SNMP LLDP-MIB 读邻接表即可还原物理拓扑——这是"自动画连线"的关键，不需要我们自己推断。
- IF-MIB 指标留到 P3，因为它走的是"持续采集"而非"周期发现"，复用现有 metrics 管线即可，与发现解耦。

## 数据模型改动

### Device 表（新增 5 字段）

| 字段 | 类型 | 说明 |
|---|---|---|
| `IPAddress` | string(45) nullable | 设备 IP（IPv4/IPv6） |
| `MACAddress` | string(17) nullable | 主 MAC（L2 标识 + OUI 厂商查表源） |
| `Source` | string(16) NOT NULL DEFAULT `agent` | `agent` / `discovered` / `manual` |
| `DiscoveredAt` | *time.Time nullable | 首次被发现时间（区别于 `CreatedAt` 系统插入时间） |
| `SNMPCredRef` | string(64) nullable | 加密凭证引用（write-only，见 §8） |

`Roles` 字段已有 `RoleBitNetwork(0b0100)`，发现时按推断的设备类型自动置位。

### agentless 设备的 fingerprint

网络设备没有 `product_uuid`/`machine-id`（issue #72 / HLD-fingerprint 的那套不适用）。按稳定性降级取：

1. SNMP `entPhysicalSerialNum` 或 LLDP `lldpLocChassisId`（最稳）；
2. 退回主 MAC；
3. 退回 IP（最弱，IP 会变 → 标 `low_confidence`，UI 提示 operator 核对）。

fingerprint = `disc_` + `sha256(seed)[:16]`，用 `disc_` 前缀与 agent 设备的 `fp_` 区分，避免两套混淆。

### EdgeDevice.Type=Discovered 落地

- 新增 `LinkDiscovered(edgeID, deviceID)` 路径（区别于现有 `Link(...Host)`）；
- 一个 edge 可发现多台设备（多条 Discovered 行）；
- 同一设备被多个 edge 看到（跨网段同一交换机）→ 靠 device fingerprint 去重折叠成**一个** device，junction 各记各的 edge；渲染去重（§7）。

### 新 tunnel RPC：`push_network_discovery`

```go
type PushNetworkDiscoveryRequest struct {
    EdgeID  uint64             `json:"edge_id,omitempty"`
    Devices []DiscoveredDevice `json:"devices"`
}
type DiscoveredDevice struct {
    MAC, IP, Hostname, Vendor string
    SysDescr, SysObjectID     string   // SNMP 身份
    DeviceType                string   // 推断: switch/router/firewall/host/...
    Serial                    string   // entPhysicalSerialNum
    Neighbors                 []LLDPNeighbor // P2: 远端 chassisId/portId + 本端口
    Confidence                string   // high/low
    LastSeen                  int64
}
```

### 新 plugin：`networkdiscovery`

`plugin_config.spec_json`：`{subnets:[], scan_interval, snmp:{version,community/v3creds(write-only)}, protocols:[arp,icmp,snmp,lldp], enabled}`。**默认不启用** —— operator 必须显式开 + 配子网（安全优先，§8）。

### 新关系类型：`connects_to`

`direction=bidirectional`，新增 `semantics_tag=link`（物理链路）。`propagates_failure` 可配（交换机挂了下游断 → 可设 true，让 expand_topology 把网络设备纳入爆炸半径）。

## 发现 → 拓扑渲染

### node type 推断

SNMP `sysServices`（bitmap：L2/L3/L4…）+ `sysObjectID`（厂商 enterprise OID 前缀）联合推断 → `switch` / `router` / `firewall` / `host` / `ap` / `printer`。推不出 → `network_device`（通用兜底）。

### 注册 node types（一次性 seed）

`firewall`(tier 2.5) / `router`(tier 3) / `switch`(tier 3.5) / `subnet`(tier 4)。Dagre 自动把它们分层落在 host 附近。前端 `NODE_COLORS` 补这几类配色（沿用 zinc+indigo 调性，见 AGENTS.md 共识）。

### `connects_to` 连线（P2）

SNMP `lldpRemTable` 给出"本设备某端口 ↔ 远端 chassisId/portId"。manager 把远端 chassisId 对齐到已发现 device 的 fingerprint（两边都用 chassisId/MAC，保证可匹配），生成 `device ↔ device` 的 `connects_to` 边。React Flow 直接渲染成物理连线。

### 待确认池（不污染主图）

发现的设备先进**待确认**状态（入 `devices` 表但 node 标 `pending` 或暂不建 relation）。operator 在「发现」页审阅 → 一键 promote 入主拓扑。避免一上来几百个未连节点糊屏；已有的 `hideOrphans` 开关兜底。

## 安全与边界

- **SNMP 凭证**：复用 databasemetrics secrets 模式 —— manager **不持久化明文**，write-only 下发到 edge，落盘 0600，GET 不回显（只回 `secret_set:true`）。强烈推荐 SNMPv3（认证+加密）；v2c community 是明文，UI 明确警示。
- **扫描范围**：双重 gate —— NetworkHostAllowlist（edge 侧）+ plugin config `subnets`（manager 侧），默认空 = **不扫**。operator 必须显式授权网段。
- **限速**：ping/SNMP 并发上限 + 速率限制，避免被 IDS 当扫描攻击；ping 非 flood（cmdpolicy 已禁 `-f`）。
- **只读**：纯被动探测 —— SNMP **只 GET 不 SET**，ARP/ping 只读。绝不修改网络设备任何配置。

## 分阶段

- **P1（本期）**：`networkdiscovery` 插件 + ARP/ping 存活 + SNMP 身份 → device(discovered) + node + UI 发现列表 / 一键入图。
- **P2（本期）**：LLDP/CDP 邻接 → `connects_to` 自动连线，架构图直接出物理拓扑。
- **P3（预留，不实现）**：SNMP IF-MIB 接口指标（流量/状态/错误）→ 复用现有 metrics + alert 管线，网络设备接口监控告警。预留：`DiscoveredDevice.Interfaces[]`（ifIndex/ifName/ifType）、plugin config `metrics_enabled` / `metric_oids`。

## 开放问题 / 风险

- **agentless fingerprint 稳定性**：MAC 可能因换网卡 / 堆叠虚拟 MAC 变化 → 可能产生重复 device（类似 issue #72 但更弱）。缓解：优先 `entPhysicalSerialNum`/`chassisId`，IP-only 标 low_confidence。
- **多 edge 发现同一设备**：跨网段同一交换机被两个 edge 看到 → fingerprint 去重折叠成一个 device + 两条 Discovered junction；需确认渲染不重复出节点。
- **SNMP v2c community 明文**：现网普遍但安全弱 → 文档警示 + 推 v3。
- **LLDP 远端标识对齐**：`lldpRemChassisId` 与我方发现的 device fingerprint 必须用同一标识体系（chassisId/MAC），否则连线对不上 → 实现时统一。
- **CDP（Cisco 私有）**：LLDP 不开的纯 Cisco 环境才退回 CDP；优先 LLDP。

## 预留接口（不实现）

- `DiscoveredDevice.Interfaces[]`（ifIndex/ifName/ifType）—— P3 指标采集用。
- plugin config `metrics_enabled` / `metric_oids` —— P3。
- 统一 discovery scheduler（与未来巡检 / 周期 SOP 共调度底座）—— 本期暂用 plugin 自身 ticker，预留迁移路径。
