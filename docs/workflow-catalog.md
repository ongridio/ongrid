# ongrid 工作流能力目录

> 面向官网的工作流（Workflow）能力说明。所有条目均在测试环境（v0.8.6-flow75）经
> 端到端实跑验证：建流程 → 触发运行 → 校验每个节点 `succeeded`。最后更新
> 2026-06-25。

ongrid 工作流把「触发器 → 节点」连成可视化自动化：定时 / 告警 / 手动触发后，
按图依次执行工具、Agent、条件、通知等节点，节点间用 `{{nodes.<id>.output.<path>}}`
传数据。下面按**技能**分组列出可作为节点的工具，并给出已就绪的示例工作流。

---

## 节点类型

| 节点 | 作用 |
| --- | --- |
| 触发器（手动 / 定时 / 告警） | 启动一条流程；告警触发会带上事件上下文 |
| 工具（Tool） | 调用一个原子能力（见下方目录），结构化入参/出参 |
| Agent | 派一个子 Agent 自主多轮推理 + 调工具，回写结论 |
| 条件（Condition） | 按表达式分流 true / false |
| 转换（Transform） | 字段胶水：把上游输出整形成下游入参 |
| 通知（Notify） | 推送到飞书 / 钉钉 / 企业微信 / Slack / Telegram / Webhook |

---

## 工具目录（按技能）

> ✅ = 已 e2e 实跑通过，可直接作为工作流节点。

### 观测 Observability
| 工具 | 说明 | 状态 |
| --- | --- | --- |
| `query_promql` | 用 PromQL 查询指标时序 | ✅ |
| `query_logql` | 用 LogQL 查询 Loki 日志 | ✅ |
| `query_traceql` | 用 TraceQL 查询 Tempo 链路 | ✅ |
| `list_metric_catalog` | 列出当前在采的指标名 + 代表性标签 | ✅ |
| `list_database_sources` | 列出已发现的数据库指标采集源 | ✅ |
| `analyze_database_status` | 数据库指标源健康巡检 | ✅ |

### 设备管理 Devices
| 工具 | 说明 | 状态 |
| --- | --- | --- |
| `get_host_load` | 主机 CPU / 内存 / 负载快照 | ✅ |
| `get_host_processes` | 主机 Top 进程 | ✅ |
| `host_bash` | 边端主机上执行白名单内只读命令 | ✅ |
| `host_restart_service` | 重启白名单内 systemd 服务（写操作，走人审） | ⚠️ 变更类，不自动跑 |

### 集群与拓扑 Fleet & topology
| 工具 | 说明 | 状态 |
| --- | --- | --- |
| `query_devices` | 查询设备 / 边端清单 | ✅ |
| `get_edge_summary` | 按边端聚合健康概览 | ✅ |
| `rank_edges` | 按 cpu/mem/disk 给边端排名 | ✅ |
| `find_outlier_edges` | 按 cpu/mem/disk 找离群边端 | ✅ |
| `get_topology` | 拉取业务拓扑概览 | ✅ |
| `expand_topology` | 从某节点 BFS 扩散，算故障爆炸半径 | ✅ |
| `find_topology_node` | 按名称搜索拓扑节点 | ✅ |

### 告警与事件 Incidents & alerts
| 工具 | 说明 | 状态 |
| --- | --- | --- |
| `query_incidents` | 查询事件列表 | ✅ |
| `correlate_incident` | 围绕事件融合 指标 + 日志 + 链路 证据 | ✅ |
| `query_alert_rules` | 查询告警规则 | ✅ |
| `query_change_events` | 查询某时刻附近的变更事件（审计） | ✅ |
| `get_incident_detail` | 单条事件详情 | ⚠️ 见「已知问题」 |

### 知识 Knowledge
| 工具 | 说明 | 状态 |
| --- | --- | --- |
| `query_knowledge` | 语义检索内置知识库 / playbook | ✅ |
| `list_repo_sources` / `grep_source` / `read_source` | 浏览 / 检索 / 读取已接入的代码仓库 | ⚠️ 需先接入一个 Git 代码仓库 |

### 云端管理 Cloud
| 工具 | 说明 | 状态 |
| --- | --- | --- |
| `cloud_bash` | 云端沙箱执行（提案-确认-运行） | ⚠️ 见「已知问题」 |
| `draft_config_change` / `apply_config_change` | 起草 / 应用配置变更 | ⚠️ 变更类，不自动跑 |

### MCP · k8s（外接 Kubernetes MCP Server）
| 工具 | 说明 | 状态 |
| --- | --- | --- |
| `namespaces_list` | 列出命名空间 | ✅ |
| `pods_list` / `pods_list_in_namespace` | 列出 Pod | ✅ |
| `pods_get` | 取单个 Pod 详情 | ✅ |
| `events_list` | 列出集群事件 | ✅ |
| `configuration_view` | 查看 kubeconfig | ✅ |
| `resources_get` / `resources_list` | 通用资源读取 / 列举 | ✅ |
| `nodes_log` | 节点日志 | ✅ |
| `nodes_stats_summary` | 节点 kubelet stats | ✅ |
| `pods_log` | Pod 日志 | ⚠️ 需 Pod 处于 Running |
| `pods_top` / `nodes_top` | Pod / 节点资源用量 | ⚠️ 需集群安装 metrics-server |

---

## 示例工作流（已就绪，可直接运行）

测试环境保留了 5 条 `[就绪]` 多步工作流，皆为只读巡检，触发即出结果：

| 工作流 | 节点链 | 用途 |
| --- | --- | --- |
| **平台快照** | `query_promql(up)` → `query_devices` → `get_topology` | 一屏看平台全貌 |
| **k8s 集群巡检** | `namespaces_list` → `pods_list` → `events_list` | 快速看集群状态 |
| **主机健康巡检** | `get_host_load` → `get_host_processes` → `get_edge_summary` | 单机健康三连查 |
| **告警与变更审计** | `query_alert_rules` → `query_incidents` → `query_change_events` | 巡检告警面 + 变更 |
| **事件关联诊断** | `query_incidents` → `correlate_incident` | 对事件融合多维证据 |

此外测试环境另有 31 条 `[e2e] <工具>` 单工具流程，作为每个工具的最小可跑样例。

---

## 已知问题（待修，不影响上述就绪项）

1. **Agent 节点调工具时可能崩** —— flow 的 Agent 子 worker 编译出的 eino
   `toolsNode` 缺少部分 inventory 桥接工具（如 `get_edge_summary`），但仍把它们
   暴露给 LLM；模型一旦调到缺失工具就 `tool ... not found in toolsNode indexes`。
   纯推理 Agent 正常；调工具的 Agent 节点暂不稳。建议：worker 的 toolsNode 用与
   flow 工具节点一致的全量 toolbag 编译。
2. **`cloud_bash` 在工作流里报 "unknown tool"** —— flow 工具 invoker 在
   `SetCloudBashProposer` 之前就快照了 `BuildBaseTools`，导致 `cloud_bash` 进了
   目录却不在 invoker map 里。建议：invoker 在所有 proposer 注册后再建，或改为
   像 MCP 那样运行时解析。
3. **`get_incident_detail` 挂起** —— 单工具流程跑它会停在 `running`（>90s 不返回）。
   待排查。其余事件类工具（`query_incidents` / `correlate_incident` /
   `query_alert_rules` / `query_change_events`）均正常。
4. **依赖外部条件的工具** —— 代码仓库类（需接入 Git 仓库）、`pods_log`（需 Pod
   Running）、`pods_top`/`nodes_top`（需 metrics-server）：能力本身就绪，测试环境
   暂不具备数据/组件，故未纳入就绪示例。
5. **变更类工具** —— `host_restart_service` / `apply_config_change` /
   `draft_config_change` 为写/危险类，按设计走人审、不做自动 e2e。
