# Ongrid 路线图

_最近更新：2026-06-06 · English version: [`ROADMAP.md`](ROADMAP.md)_

开源仓 Ongrid 的工作路线图。把 2026-06-06 规划讨论的新方向与散落在
ADR / HLD / PRD / 历史规划备忘录里的 backlog 合到一起。

## 图例

- `✓` 已落地 —— 仅作为后续工作的背景列出
- `◐` 进行中
- `□` 已规划，尚未开始
- `◯` Park —— 明确暂缓，等达到触发条件再启动

## 指导原则

Ongrid 的护城河是 `geminio` 双向通道 + AI agent 能在客户机器上**真动手**。
三件套（指标 / 日志 / 链路）是入场券，不是差异化。每一条 roadmap 项都
回答一个问题：**这事是不是让 agent 多一种动手能力、或者让 incident 时
间线多一个动作？** 纯展示型功能让位。

每个段落内的顺序是段内优先级，不跨段比较。

---

## A · 因果 RCA 深化（HLD-013 Phase 3）

- **A.1** `✓` Phase 1 —— investigator prompt 改写为因果回溯循环
  - `max_turns` 25 → 40
  - 结构化输出 `根因 / 因果链 / 现象 / 置信度`
- **A.2** `✓` Phase 2 —— `query_change_events` BaseTool
  - 读 HLD-010 audit log 作为 "0 号病人" 候选
  - 经 `Registry.SetAuditLister` 连入
- **A.3** `□` 边端变更事件源
  - 当前 audit log 只看得到走 Ongrid 的变更
  - 订阅 `journald` + `dockerd events` + `apt` / `dnf` history
  - 让外部 SSH、带外部署、容器 churn 也能进 RCA 候选集
- **A.4** `□` 有向依赖边 + 基线
  - 拓扑图当前无向 —— 分不清上下游
  - 加方向 + 每指标历史基线
  - 两者一起喂进 RCA prompt
- **A.5** `□` 跨会话相似 incident 检索
  - `incident_resolutions` 表 + embedding 库
  - `query_similar_incidents` BaseTool
  - 详情页 resolve modal 闭环 "见过这种情况"

---

## B · 远程诊断动作工具（Roadmap Step 3）

- **B.1** `□` 远程抓包 → 对象存储 + 产品端渲染 ★
  - `capture_pcap(iface, bpf, duration)` BaseTool
  - tarball 上传到内置 MinIO / S3
  - incident 时间线挂内嵌 pcap 查看器（web wireshark 或简化流表 UI）
  - SaaS 同行不敢做 —— 没有安全的双向通道
- **B.2** `□` 网络探测一类工具升一级公民
  - `probe_tcp` / `probe_http` / `probe_dns`
  - `traceroute` / `mtr`
  - 各有自己的结构化输出 schema，时间线渲染干净
- **B.3** `□` 文件 / 日志 / 内核诊断
  - `tail_file`、`grep_file`
  - `strace`、`lsof`、`dmesg`、`sosreport`
- **B.4** `◐` 网络 Layer-1 —— cmdpolicy 扩展（已落地）
  - 9 个 binary：OVS / nft / conntrack / ipset / ethtool / bpftool / `ip netns`
  - 只开 read，write 留给 SOP 走双签
  - 源码：`internal/edgeagent/cmdpolicy/policy.go`
- **B.5** `□` 网络 Layer-2/3 skill
  - `host_ovs_show`、`host_netfilter_dump`、`host_conntrack_summary`
  - eBPF preset 库 —— 只放 preset id，永远不允许 `bpftrace -e <body>` 这种裸传

---

## C · 自然语言查询

- **C.1** `□` LLM 生成 PromQL / LogQL / TraceQL
  - `chat_to_query` BaseTool
  - 上下文喂 label 集合 + 指标 metadata
  - 执行前先 dry-run preview
  - 高频 pattern 自动存模板
  - 把不会写查询语言的运维门槛降下来
- **C.2** `□` Chat → Grafana panel
  - 自然语言描述 → 选 panel + range + 变量
  - 直接 deep-link 或在 chat 里内嵌

---

## D · Agent 内核质量

- **D.1** `□` 专家 sub-agent 分工
  - persona：`specialist-network` / `specialist-disk` / `specialist-process`
  - coordinator 按 incident 类型派单，并约束 tool bag
  - 把现在闲置的 sub-agent 框架真用上
- **D.2** `□` Critic loop（自我批判）
  - 主 ReAct 跑完 → critic LLM 审
    - 没证据的断言
    - 应调没调的工具
    - 断掉的因果链
  - 最多 2 轮反馈
  - 业界报 +10–30% 准确率
  - token 翻倍 —— gate 在 `severity ≥ critical`
- **D.3** `□` Eval / replay 框架
  - 5–10 个 golden incident，标好期望 tool 集 + 关键词
  - `cmd/ongrid-eval` CLI
  - CI 钩子：改 prompt / persona / 工具描述时强跑

---

## E · 用户面 Agent 助理体验

- **E.1** `□` 全局 Side Panel 助理
  - 浮动按钮 + `Cmd+K`，任意页面唤起
  - 自动拿当前页 context 当 seed prompt
  - 体感提升最大；后面 E.2–E.5 都挂在这里
- **E.2** `□` 多助理 / 用户自定义助理
  - 每个助理自己的 prompt + 工具子集 + 默认 scope
  - Side Panel 里切换
- **E.3** `□` Quick Action 卡片
  - chat 输入框上方动态插槽
  - 按 page context 推荐 "一键问" 模板
- **E.4** `□` Knowledge 收藏
  - 有用的 agent 结论 📌 钉到 `agent_knowledge`
  - 详情页 "相关知识" 面板
  - 列表页搜索
- **E.5** `□` 推理时间线可视化
  - chat toggle 把
    `user → thought → tool_calls → tool_results → final` 渲染成树

---

## F · 数据与资源接入

### F.1 Kubernetes

- **F.1.1** `□` K8s edge 插件
  - 节点二进制 / 单 pod in-cluster 两种装法
  - service-account 驱动 kubectl
  - 出厂带 RBAC manifest
- **F.1.2** `□` K8s BaseTool 套
  - `kube_get_pods` / `kube_describe`
  - `kube_logs`（streaming）/ `kube_events` / `kube_top`
  - `kube_exec` 走 dangerous 等级
- **F.1.3** `□` Operator / CRD 部署
  - `OngridEdge` CRD
  - DaemonSet 模式
  - 跟 ADR-024 bundle upgrade 对齐
- **F.1.4** `□` K8s 拓扑
  - Pod / Deployment / Service / Ingress 作为拓扑节点
  - blast radius BFS 跨 K8s 资源

### F.2 云资源

- **F.2.1** `□` 只读 inventory 工具
  - AWS / GCP / 阿里云 / 腾讯云
  - EC2 或 CVM 列表、VPC、安全组、RDS、LB、对象存储
- **F.2.2** `□` 云监控 read-through
  - CloudWatch / Stackdriver / 阿里云 CMS 作为 RCA 证据源
  - 不替换 Prom —— 补云原生资源的盲区
- **F.2.3** `□` 云成本
  - `cloud_cost_breakdown` BaseTool
  - 跟 incident 关联：贵的 incident 优先看
- **F.2.4** `□` 多 org 云凭证
  - credential vault + rotate
  - 对齐 ADR-023（git 凭证双轨）

### F.3 LLM provider

- **F.3.1** `□` Ollama 适配
  - Custom（OpenAI 兼容）卡片 hint 预填 `http://localhost:11434/v1`
  - 自动 list 模型
  - 卖点：零外发数据
- **F.3.2** `□` vLLM / SGLang / LMDeploy preset
  - 自有 GPU 的私有化客户
- **F.3.3** `□` Bedrock / Vertex
  - 企业云客户，等问到再做

### F.4 IM / 协作

- **F.4.1** `✓` Slack / Telegram / 飞书 双向（ADR-021 / ADR-031）
- **F.4.2** `□` 钉钉 —— 把国内三件套补齐
- **F.4.3** `□` Microsoft Teams —— 海外企业必备
- **F.4.4** `□` 企业微信双向
  - 当前只有 webhook outbound
  - 想用 chat 下指令必须做 bot 模式

### F.5 日志 / 链路（Roadmap Step 5）

- **F.5.1** `✓` Loki 路径（ADR-012）
- **F.5.2** `□` Tempo Traces
  - 边端 OTel collector 反代到 manager ingress
  - 跟 metrics 路径同 pattern
  - 第一个客户问需求前不开 —— 存储 + UX 是个黑洞
- **F.5.3** `□` eBPF 自动 trace（Pixie 启发）
  - 必须先把 F.5.2 跑稳

---

## G · 工程化 & 运维

### G.1 安装 / 首次开机

- **G.1.1** `□` 端口冲突预检
  - 先 `ss -tlnp | grep :443`
  - 撞了自动 bump 到 `8443` / `8080`
  - 同步传到 `ONGRID_PUBLIC_URL`
- **G.1.2** `□` 共存 vs 独占问询
  - preflight 问："这台机是不是只跑 ongrid"
  - 答案决定端口和 public URL
- **G.1.3** `□` 内网 vs 公网 IP 确认
  - 单机自玩用户其实想要内网 IP
  - 云 metadata 默认拿公网 IP 并不总对
- **G.1.4** `□` mirror 健康检查
  - 写完 `daemon.json` + restart docker 后 `docker pull hello-world`
  - 失败就回滚或换备用 mirror 列表
- **G.1.5** `□` `read -s` 隐藏管理员密码
  - 当前会进 `.bash_history`
- **G.1.6** `□` 首次开机引导 wizard
  - admin 改密 → LLM provider → IM → 第一台 edge 安装 一条龙
  - 对齐 PRD-001
- **G.1.7** `✓` 卸载脚本
  - 整目录 purge 插件二进制 + 工作目录
  - 无条件 stop unit + `pkill -9` 兜底
  - PR #46 / PR #47

### G.2 边端生命周期

- **G.2.1** `✓` ADR-024 一键 bundle 升级 + `.previous` 回滚
- **G.2.2** `□` 升级 channel + canary
  - stable / beta / canary，按 edge tag 灰度
- **G.2.3** `□` 健康感知 rollout
  - 升完 30s 心跳不绿自动回滚
- **G.2.4** `□` 离线升级包
  - 内网客户场景
  - manager 当 mirror

### G.3 Prometheus 生产硬化（已 park）

- **G.3.1** `◯` nginx `/prometheus/` 加 `auth_request`
  - 把 remote_write 接口从公网关上
- **G.3.2** `◯` `promwrite.Ingester` ring buffer + worker pool + bbolt DLQ
- **G.3.3** `◯` VictoriaMetrics drop-in compose profile
- **G.3.4** `◯` `install.sh` TSDB 选型
  - 内建 Prom / VictoriaMetrics / 外部 TSDB 三选一
- **G.3.5** `◯` label whitelist 控基数
- **触发条件（满足任一即 unpark）**
  - 单客户 > 100 edges
  - 客户主动问 HA / SLA
  - manager 日志出现 `promwrite` 写超时累积

### G.4 自观测（ADR-026）

- **G.4.1** `✓` `/metrics`、6 条自身告警、自身 dashboard
- **G.4.2** `□` SLO 板
  - 可用率、工具成功率、RCA 准确率（数据来自 D.3）
- **G.4.3** `□` 自诊断 agent
  - 周期跑 self-RCA 对自家 manager

### G.5 安全 / 沙箱

- **G.5.1** `◯` microsandbox 升级路径
  - 客户明确要求 kernel 级隔离时再启
  - `host_bash` + `cmdpolicy` 当前覆盖 90% 诊断面
- **G.5.2** `□` WebSSH（ADR-019）
  - geminio stream + xterm.js
  - 当 SOP 执行的兜底通道
- **G.5.3** `□` Per-tool RBAC + audit replay
  - ADR-022 当前到 viewer role 这层
  - 颗粒度还要下到单个 tool

### G.6 凭证 / 知识

- **G.6.1** `✓` ADR-023 SSH key 表 + git 凭证双轨
- **G.6.2** `□` ADR-018 RepoFetcher
  - per-repo auth；不能把上一版被 park 的 token 泄漏问题带回来
- **G.6.3** `□` 离线 vault 包
  - 无外网客户场景
  - 内置 vault + 离线快照 tarball

---

## H · 生态 / 后置

- **H.1** `◯` Skill marketplace 公开化（ADR-017）
  - skill 数量到 ~30 再 unpark
- **H.2** `□` HLD-009 coordinator e2e evaluation 上线
  - 当前只是设计
  - D.3 eval 框架落地后才能跑
- **H.3** `□` HLD-012 code-aware 分析
  - 代码仓库结合 incident
  - PR diff → 影响面分析
- **H.4** `◯` 开源生态
  - 插件 SDK 文档 + 第三方 BaseTool 注册
  - 等开源拆分（ADR-030）孵化出贡献者再启

---

## I · SOP / Runbook 闭环

整张 roadmap 里最大的工程。**故意放最后**：A–H 要么是 SOP 的前置依
赖，要么是 SOP 安全上线的前提。**前面 B 和 D 没跑稳前不开 SOP** ——
否则可执行路径会变成"产事故的引擎"而不是护城河。

- **I.1** `□` Tool.Class 三级
  - `safe`（只读）/ `mutating`（改状态）/ `dangerous`（不可回滚）
  - 替换今天的二值 read / write 分流
- **I.2** `□` SOP DSL
  - YAML runbook：`triggers` / `steps` / `approvals` / `rollback`
- **I.3** `□` 双签执行链路
  - manager RSA 签
  - edge 校验
  - 两端 audit log 都打齐
- **I.4** `□` `review_gate` sub-agent
  - mutating step spawn reviewer worker
  - 写 `mutating_proposal` 行
  - UI 弹动作卡
  - 操作者签 → edge 执行
  - 时间线打 `mutating_action_executed`
- **I.5** `□` 起手 playbook 库
  - `host_restart_service`
  - `disk_cleanup`
  - `log_rotate`
  - `certificate_renew`

---

## J · 周期性 Agent 任务

共用一个调度器原语；面向"agent 长时间盯着"这种工作量，不是同步 chat。

- **J.1** `□` 巡检
  - 每天 / 每周扫所有 edge
  - 跑轻量 RCA：`top_load_anomaly` / `dep_health_check` / `cert_expiry_check`
  - 命中风险自动建 incident
- **J.2** `□` 周报 / 月报
  - 跨 edge 汇总告警、incident、执行过的动作
  - LLM 写摘要
  - IM 推送
- **J.3** `□` 守望任务 / 主动推送
  - `create_watch(condition, expire_at)` BaseTool
  - 命中条件后 SSE 推回原 session
