# RFC-001: Kubernetes Full-node 接入适配方案

## 元信息

- 状态：草稿
- 日期：2026-06-29
- 作者：Codex
- 范围：ongrid manager、ongrid-edge、Helm/Kubernetes 部署资产、前端设备与集群视图

## 背景

Ongrid 现有 edge 以主机为中心运行：edge 主动外连 manager/frontier tunnel，首次连接后通过 `register_edge` 同步主机信息，manager 创建或更新 `Device`，后续指标、日志、trace、告警和 AI 工具统一围绕 `device_id` 关联。

Kubernetes 接入不能让所有 Pod 复用同一组 edge 凭证。一个 edge identity 对应一个 tunnel session 和一个 host device，如果 DaemonSet Pod 复用身份，多节点会互相覆盖在线状态、心跳、插件健康和 `device_id` 标签。

因此本方案采用“集群注册 + 节点级 edge identity 自动签发”：manager 创建 Kubernetes 集群和 bootstrap token，Helm 安装后由 controller 和每个 node agent 分别 enroll，换取独立 edge credentials，再复用现有 tunnel、插件和设备链路。

无节点权限接入不在本 RFC 当前交付范围内，后续单独设计。

## 目标

- 一个标准 Kubernetes 集群可以通过 Helm 一次接入 Ongrid。
- 每个 Kubernetes Node 都能作为一个 `Device` 出现在现有 Devices / Monitor / Alerts / AI 工具链路里。
- controller 负责 Kubernetes API watch、资源快照、事件采集和 K8s 写动作。
- node agent 复用现有 edge 主机能力，负责节点主机指标、日志、trace、插件健康和受控诊断。
- K8s 对象查询优先使用 manager DB 当前快照；需要实时细节时通过 controller 访问 Kubernetes API。
- 写动作默认 dry-run / 审批 / 审计，不绕过 ReviewGate。
- 不破坏现有 systemd/binary edge 安装路径。

## 非目标

- 不做跨集群调度、发布系统或自动修复闭环。
- 不接管用户已有 Prometheus Operator、Fluent Bit 或 OTel Operator。
- 不把高 churn 的 Pod/Event 历史长期完整落 MySQL。
- 不在本阶段提供无节点权限集群接入体验。

## 架构

```mermaid
flowchart LR
  UI["Manager UI / API"] --> Cluster["k8s_clusters"]
  Cluster --> Token["Bootstrap token"]
  Token --> Helm["helm upgrade --install ongrid-edge"]
  Helm --> Controller["ongrid-edge-controller Deployment"]
  Helm --> NodeAgent["ongrid-edge-node DaemonSet"]
  Controller --> K8sAPI["Kubernetes API"]
  Controller --> Snapshot["k8s_nodes / workloads / pods / events"]
  Controller --> ReviewGate["ReviewGate / action audit"]
  NodeAgent --> Device["Device / Edge identity per Node"]
  NodeAgent --> Metrics["metrics / logs / traces"]
```

### 组件职责

| 组件 | 代表对象 | 主要数据源 | 动作边界 |
| --- | --- | --- | --- |
| `ongrid-edge-node` | 单个 Node / host Device | 节点 OS、文件系统、进程、网络、节点日志、node_exporter/process_exporter | 与原生服务器 Edge 一致的节点级诊断和受控动作能力 |
| `ongrid-edge-controller` | 整个 K8s 集群 | Kubernetes API、Events、kube-state-metrics、应用 metrics | K8s API 写动作 |
| manager | 集群与资源快照 | controller push、DB、Prometheus/Loki/Tempo | 查询、审批、审计、安装命令生成 |

### 数据关系

- `k8s_clusters` 保存集群注册、接入模式、controller edge、bootstrap token hash、最近同步水位和 watch lag。
- `k8s_nodes` 保存当前 Node 快照，并通过 `edge_id` / `device_id` 关联节点 edge。
- `k8s_workloads`、`k8s_pods` 保存当前资源快照，用于列表、详情、AI 快照查询和排障入口。
- `k8s_events` 保存短期事件窗口，用于异常队列和 AI 诊断。
- `k8s_installations` 保存 Helm 安装实例元数据，full-node 安装按 cluster scope 记录。

## 接入流程

1. 用户在 `/kubernetes` 创建集群，manager 生成集群记录和 bootstrap token。
2. UI 生成远程 Helm 命令，固定从 CNB OCI 制品库 `oci://helm.cnb.cool/ongridio/ongrid-edge` 拉取与当前 Ongrid 版本一致的 chart；manager 不再分发本地 chart。
3. Chart 默认从 `docker.cnb.cool/ongridio/ongrid-edge:<appVersion>` 拉取 amd64/arm64 多架构镜像，也允许通过 `image.repository` 和 `image.tag` 覆盖。
4. 用户在目标集群执行 Helm 命令，传入：
   - `manager.publicURL`
   - `manager.tunnelAddr`
   - `manager.tlsInsecure`
   - `enrollment.clusterID`
   - `enrollment.controllerBootstrapToken`
   - `enrollment.nodeBootstrapToken`
   - `mode=full-node`
5. controller Deployment 启动后读取 `kube-system` Namespace UID，manager 首次 enroll 时将其原子绑定为真实集群身份，再建立控制面 tunnel。
6. node DaemonSet 在每个 Node 上启动，每个 Pod 使用只允许读取 `kube-system` Namespace 的 ServiceAccount 权限校验集群 UID，再用 Node Name enroll，换取独立 edge credentials；controller 快照到达后再合并 Kubernetes Node UID。
   - 启动阶段将 Edge 和插件安装到宿主机 `/var/lib/ongrid-edge/k8s-runtime`，进入宿主机 mount/PID/network 环境后降为非 root UID，仅保留网络诊断所需的 `CAP_NET_ADMIN` 和读取 root-only Pod 日志所需的 `CAP_DAC_READ_SEARCH`。
   - 节点凭据以 `0600` 文件保存在该节点宿主机 `/var/lib/ongrid-edge/k8s-state/credentials`，Pod 滚动重建时复用，不使用所有节点共享的 Kubernetes Secret。
   - Node Edge 的文件、进程、网络、日志和 WebSSH 均作用于宿主机；controller 始终保持非 root 且不注册主机能力。
   - controller 和 node bootstrap token 长期有效，仅管理员手动轮换 token 或删除集群时失效。已注册 controller 禁止重复 bootstrap 轮换最终凭据；普通 Pod 重建复用 Secret，凭据丢失时由管理员轮换 token 显式开放一次恢复接入。
6. manager 将 Node edge 关联为普通设备，并在设备列表展示 `K8s Node`、所属集群和可选 `K8s Controller` 标签。

## 数据同步

- controller 默认开启 inventory watch。
- 首次启动使用 full list seed 当前快照。
- watch 事件触发 delta payload，只更新 upsert/delete 的对象。
- watch burst 在 Edge 内按对象合并最终操作，通知 channel 只负责唤醒，不丢弃资源变更。
- 普通周期快照用于兜底收敛。
- full snapshot 按不超过 tunnel 单包上限的块顺序上传，manager 只在最后一块成功后执行 prune 和提交同步水位。
- full sync 按集群范围清理本轮未出现的 Pod / Workload / Event。
- delta sync 只应用显式 upsert/delete，不做范围 prune。
- manager 记录 `inventory_resource_version`、`inventory_resource_versions_json`、`inventory_synced_at`、`inventory_sync_duration_ms`、`inventory_watch_lag_seconds`。

### Event 保留策略

Kubernetes Event 是高 churn 数据，只保留短期诊断窗口：

- controller 上报当前 Event 快照。
- manager 每轮同步清理当前集群已消失的 Event。
- 后端提供 Event TTL 与每集群上限清理，避免 `k8s_events` 长期膨胀。
- UI 异常队列只展示当前仍有意义的 Warning/异常资源，已恢复信号不再作为健康问题展示。

## AIOps 能力

### 只读查询

- `query_k8s_snapshot`：查询 manager DB 当前快照，支持 clusters / nodes / workloads / pods / events 和 namespace/kind/phase/reason 筛选。
- `describe_k8s_resource`：通过 controller edge 实时访问 Kubernetes API，支持 Pod/Node/Namespace/Service/Deployment/StatefulSet/DaemonSet/ReplicaSet/Job/CronJob/Event describe。
- `query_k8s_logs`：通过 controller edge 调用 `pods/log` 读取有界日志片段，用于 Loki 不可用或需要实时日志的场景。

### 写动作

`execute_k8s_action` 作为写动作工具暴露，必须经过 ReviewGate：

- `scale`
- `rollout_restart`
- `delete_pod`
- `evict_pod`
- `cordon`
- `uncordon`
- `drain`

所有动作需要展示 dry-run、审批状态、执行记录和失败原因。`drain` 默认跳过 DaemonSet、mirror/static、terminal、unmanaged 和 `emptyDir` Pod，并支持 PDB 429 重试与超时参数。

## 前端体验

### 集群列表

- 展示集群名称、模式、状态、controller edge 和最近同步。
- 提供接入命令、轮换 token、卸载命令和删除入口。
- 删除集群前提示先执行卸载命令；删除只移除 manager 侧记录和该集群关联设备/拓扑数据，不负责远端 Helm 卸载。

### 集群详情

页面定位为 AIOps-oriented K8s Cluster Detail，不做完整 K8s 控制台：

- 顶部展示健康结论、Controller、资源规模、同步状态、快照版本。
- 异常线索按影响面排序，展示异常 Workload、Pod、Node、Warning Event 和同步风险。
- 资源页签包括 Nodes / Workloads / Pods / Events / Namespaces / Actions。
- Telemetry 入口展示 Prometheus / Loki / Tempo 是否可查询，并提供“打开图表”入口，查询语句作为详情信息而非主视觉。
- 写动作独立区域展示 dry-run、审批、建议数和执行记录。
- 拓扑作为辅助入口，不在详情页内强塞大图。

### 设备与拓扑

- 设备列表只展示实际 host/node edge，不单独展示 controller edge。
- K8s Node 设备显示短标签：`K8s Node`、所属集群、可选 `K8s Controller`。
- 删除集群时级联清理该集群关联的节点设备关系和拓扑节点/关系，避免删除主对象后拓扑残留。

## 安装与卸载

安装命令示例：

```bash
helm upgrade --install ongrid-edge 'oci://helm.cnb.cool/ongridio/ongrid-edge' \
  --version '<chart_version>' \
  --namespace ongrid-system \
  --create-namespace \
  --set-string manager.publicURL='https://<manager>' \
  --set-string manager.tunnelAddr='<manager>:40012' \
  --set-string manager.tlsInsecure=true \
  --set-string enrollment.clusterID='<cluster_id>' \
  --set-string enrollment.controllerBootstrapToken='<controller_bootstrap_token>' \
  --set-string enrollment.nodeBootstrapToken='<node_bootstrap_token>' \
  --set-string mode='full-node'
```

卸载命令：

```bash
helm uninstall ongrid-edge --namespace ongrid-system
kubectl delete namespace ongrid-system --ignore-not-found
```

## 验证清单

- Helm chart 渲染包含 controller Deployment、node DaemonSet、ServiceAccount、ClusterRole、ClusterRoleBinding、Controller Secret/ConfigMap；Node 不具备 Secret 读写权限。
- controller ServiceAccount 可 list/watch Nodes/Workloads/Pods/Events，并可执行受支持的 K8s 写动作。
- 每个 Node 都能 enroll 为独立 edge，设备列表自动出现 Node 设备。
- Node Edge 最终进程以非 root UID 运行且只保留 `CAP_NET_ADMIN`、`CAP_DAC_READ_SEARCH`；启动阶段 capability 在进入宿主机后全部丢弃。
- Node Edge 的主机文件、进程、网络和 WebSSH 结果与在节点直接安装 Edge 一致。
- 新增 Node 后 DaemonSet 自动启动并 enroll，新设备自动上报，不依赖一次性快照。
- 删除 Node 后 inventory delta 能清理 `k8s_nodes` 中的旧节点。
- 删除集群时清理关联设备关系和拓扑数据。
- 卸载 Helm 后集群在 90 秒左右进入离线判定。
- Event 表按 TTL 和上限清理，避免长期膨胀。
- `query_k8s_snapshot`、`describe_k8s_resource`、`query_k8s_logs` 能覆盖常见异常定位路径。
- 写动作必须进入 ReviewGate，审批和执行记录可查。

## 风险与缓解

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| DaemonSet 复用同一 edge 凭证 | 多节点互相覆盖在线状态和 `device_id` | 使用 bootstrap token 换取 per-node edge credentials |
| Node Pod 读取共享凭据 Secret | 单节点失陷后可读取或覆盖其他节点密钥 | 每个节点只在宿主机本地持久化自己的 `0600` 凭据文件；ServiceAccount 仅允许读取 `kube-system` Namespace 以校验集群 UID |
| Node 启动阶段需要进入宿主机 mount namespace | 启动进程短暂持有 `SYS_ADMIN`、`SYS_PTRACE` 等高权限 | 仅固定的内置 launcher 获得最小 capability；进入宿主机后立即降 UID、清空补充组和 capability bounding set，只保留 `NET_ADMIN`、`DAC_READ_SEARCH`，不使用 `privileged: true` |
| Node Edge 需要与主机安装版一致地读取和修改宿主机文件 | launcher 通过 hostPID 的 `/proc/1/root` 进入节点真实根目录 | Controller 保持隔离；launcher 进入主机后立即以 UID/GID 65532 运行，写操作继续受 Edge 审批与审计链路约束 |
| CNB 公共镜像仓库不可达 | Controller 和 Node Edge 出现 ImagePullBackOff | release 先发布并校验 amd64/arm64 多架构 manifest；受限环境通过 `image.repository` 覆盖为集群可达镜像仓库 |
| 可选 kube-state-metrics 访问公网 | 离线环境启用后出现 ImagePullBackOff | 默认关闭；启用时必须显式提供集群可达的离线镜像仓库地址 |
| Event 高 churn | MySQL 表膨胀 | 当前快照 prune + TTL + per-cluster cap |
| controller edge 被当成设备 | 设备列表出现非主机对象 | controller 不创建 host Device，UI 不单独展示 controller edge |
| 长期 bootstrap token 泄露 | 未授权 controller 恢复或 node 接入 | 数据库仅保存 hash，安装命令按敏感信息管理；已注册 controller 拒绝重复 bootstrap，泄露后由管理员手动轮换，旧 token 立即失效 |
| 同一安装命令用于不同物理集群 | 多个集群争用同一 controller 和快照 | 首次 enroll 绑定 `kube-system` Namespace UID，后续不一致的 enroll/register 直接拒绝 |
| 大集群 full snapshot 超过 tunnel 单包限制 | 首次接入和周期收敛持续失败 | Edge 按大小分块上传，manager 校验 snapshot 顺序并只在最后一块执行 prune |
| 未卸载直接删除集群 | 远端仍继续上报 | UI 提示卸载命令，删除后 token/cluster 记录失效，上报应被拒绝或无法关联 |
| 自签名证书 | 目标集群无法拉 chart 或连 manager | 安装命令默认提供 Helm `--insecure-skip-tls-verify` 和 `manager.tlsInsecure=true` |
