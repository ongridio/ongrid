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

## A · 根因 RCA 诊断（HLD-013 Phase 3）

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
- **A.6** `□` RCA 置信度校准
  - 每 incident 置信度 vs 运维标注的正确率，长期回流
  - 调 prompt / 选择模型
- **A.7** `□` 根因图可视化
  - 因果链以节点-边图渲染在报告旁边
  - hover 节点看证据（PromQL / 日志行 / trace span）

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
- **C.3** `□` Schema 感知的自动补全
  - 手写查询时的内嵌编辑器
  - 从实时 `/api/v1/labels` 提示 label + function

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
- **D.4** `□` 提案 & 确认中介层
  - **作用**：所有 agent 触发的 mutating 动作 —— chat、SOP 执行（K）、
    守望任务（L.3）一律走同一条管道。
  - **D.4.1** 提案生命周期
    - `pending` → `approved` / `rejected` / `expired` / `executed` / `rolled-back`
    - 落 `mutating_proposal` 表（签名信封）
  - **D.4.2** `review_gate` sub-agent
    - reviewer worker 起草提案、点名 SOP 步骤、暴露 blast radius、附 dry-run 输出
  - **D.4.3** 审批策略
    - 级别：`safe` 自动 / `mutating` 单人审 / `dangerous` 双签
    - 角色：viewer 永远不行 / user 限 scope / admin 全局
    - 支持 per-edge / per-resource 覆盖
  - **D.4.4** IM 侧审批
    - Slack / 飞书 / Telegram 消息带 `Approve` / `Reject` / `Defer` 按钮
    - 短期 token 绑定到具体提案
  - **D.4.5** 提案预览（dry-run diff）
    - 签字前看到将要发生的变更
    - 文件类显 unified diff；服务类显前后状态探测
  - **D.4.6** 批量化
    - 一份提案、N 个 edge，all-or-nothing 或滚动应用
    - 每个 edge 可单独 opt out
  - **D.4.7** 过期 + 自动 decline
    - 默认 24h，按 policy 可调
    - 过期提案绝不静默执行
  - **D.4.8** 委派 + on-call 路由
    - 非工作时间的提案路由到当班 on-call（接 G.10 on-call schedule）
    - "忙了就交给下一档"的 fallback 链
  - **D.4.9** 提案审计链
    - hash-chain 条目（接 I.4）
    - 提案 URL 可分享，事后复盘用
- **D.5** `□` Token / 成本预算
  - per-org / per-user 月度上限
  - 单次硬超时 + token 上限
  - 触上限前降级（小模型 / 少步数）而不是粗暴 abort

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
- **E.6** `□` 审批 UI
  - 一键 approve / reject D.4 提案
  - 移动端友好卡片
  - on-call 班次的 swipe 收件箱

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
- **G.1.8** `□` 离线 / 私有网络安装包
  - 一个 tarball 不带任何外网调用
  - 镜像内置、镜像仓配置、license 校验

### G.2 边端生命周期

- **G.2.1** `✓` ADR-024 一键 bundle 升级 + `.previous` 回滚
- **G.2.2** `□` 升级 channel + canary
  - stable / beta / canary，按 edge tag 灰度
- **G.2.3** `□` 健康感知 rollout
  - 升完 30s 心跳不绿自动回滚
- **G.2.4** `□` 离线升级包
  - 内网客户场景
  - manager 当 mirror
- **G.2.5** `□` 批量重注册
  - 一次性轮换 edge 凭证 / 跨 fleet 重新登记
- **G.2.6** `□` Edge fleet 标签 + selector
  - `env=prod`、`region=cn-east`、`role=db`
  - selector 同时被升级、SOP 目标、告警路由消费

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

### G.5 备份 / 恢复 / 容灾

- **G.5.1** `□` Manager 全量快照
  - MySQL + Qdrant + 对象存储 一并打包
  - 可配置 retention
- **G.5.2** `□` 一键 restore
  - 新 manager 指向 snapshot tarball
  - 校验 schema 版本 + edge 重握手计划
- **G.5.3** `□` 异地复制
  - rsync / S3 sync 到 cold standby region
- **G.5.4** `□` 演练模式
  - 定时从最新备份做 restore 演练，回报 MTTR
- **G.5.5** `□` Edge 状态 checkpoint
  - 升级前快照插件工作目录（`.previous` 已经做了一半）

### G.6 HA & failover

- **G.6.1** `◯` Active-standby manager 对
  - MySQL 共享（DRBD / 托管 cluster）
  - VRRP / 浮动 IP
  - 触发条件：客户问 SLA 或 > 100 edges
- **G.6.2** `◯` 多区域只读副本
- **G.6.3** `□` Manager 深层 `/healthz`
  - 探每一个依赖
- **G.6.4** `□` Tunnel session 迁移
  - geminio 会话在 manager 副本之间漂移，edge 不重连

### G.7 告警生命周期

- **G.7.1** `□` 去重 + 分组
  - 按 `alertname + labels` 哈希窗口
  - 一组只发一条通知，详情追加
- **G.7.2** `□` 静默（silence）
  - 运维设置带 reason 的限时静默
  - 涉及 dangerous 类别的静默走 D.4
- **G.7.3** `□` 关联
  - 图遍历找关联告警（同 edge / 同服务 / 同 incident）
  - 合并到一个 incident artefact
- **G.7.4** `□` 路由策略 DSL
  - 按 team / severity / 时段 路由
  - N 分钟无 ack 自动升级
- **G.7.5** `□` 通知偏好
  - 单用户配渠道、勿扰时段

### G.8 On-call 与维护窗

- **G.8.1** `□` On-call 排班
  - 团队 + 轮值日历
  - 升级链
  - 接 G.7.4 路由
- **G.8.2** `□` 维护窗
  - per-edge / per-fleet
  - 同时抑制告警 + 暂停 SOP 执行提案
- **G.8.3** `□` 交班摘要
  - 班次切换时的摘要：开着的 incident、挂起的提案、最近的变更

### G.9 批量操作与配置漂移

- **G.9.1** `□` 多 edge SOP 执行
  - 一份 playbook 应用到 selector（G.2.6）
  - 每 edge dry-run 结果，滚动应用
- **G.9.2** `□` Config-as-code（GitOps）
  - 告警 / SOP / 渠道存 Git 仓库
  - manager 周期 reconcile
- **G.9.3** `□` 配置漂移检测
  - 实际配置 vs desired 的 diff
  - 报为低优 incident
- **G.9.4** `□` 资产清单 / CMDB
  - 一张表：edge → 主机 → 已装包 → 暴露端口 → owner
  - 喂回 RCA 和拓扑

### G.10 NOC 视图与运营 dashboard

- **G.10.1** `□` 单页全局状态板
  - 所有 edge、所有 incident、所有挂起提案
  - 按 severity 着色
- **G.10.2** `□` Kiosk 模式
  - 大屏友好
  - 多 fleet 轮播
- **G.10.3** `□` 按角色保存的视图
  - "L1 triage"、"DBA on-call"、"网络 on-call"

### G.11 补丁与漏洞

- **G.11.1** `□` OS / package CVE 扫描
  - 每 edge 的 inventory + CVE feed
  - 通过 D.4 提案派生 patch SOP
- **G.11.2** `□` Edge 自身二进制 CVE 感知
  - manager 警告"装着的 edge bin 有已知 CVE"

### G.12 复盘与变更管理

- **G.12.1** `□` 事故复盘模板
  - 从 incident 时间线自动填
  - LLM 起草叙述，运维改
- **G.12.2** `□` 变更日历
  - 轻量"什么时候部署什么"
  - 与 SOP 执行交叉引用

### G.13 凭证 / 知识

- **G.13.1** `✓` ADR-023 SSH key 表 + git 凭证双轨
- **G.13.2** `□` ADR-018 RepoFetcher
  - per-repo auth；不能把上一版被 park 的 token 泄漏问题带回来
- **G.13.3** `□` 离线 vault 包
  - 无外网客户场景
  - 内置 vault + 离线快照 tarball

---

## H · 沙箱与执行隔离

"agent 想试一下危险操作"的公共底座 —— 覆盖 dry-run、capability 控制、
内核隔离、skill 测试。今天靠 `cmdpolicy` + Tool.Class 撑着，这段是
未来怎么长大的草图。

- **H.1** `◯` microsandbox runtime（升级路径）
  - rootless + OCI 单 binary
  - 第一档插件式沙箱；客户主动要求 kernel 隔离才启
  - cmdpolicy + bash 还是默认通道
- **H.2** `◯` Python 执行通道（script_python tool）
  - 等 microsandbox 落地后再开（N+16 memo）
  - seccomp + import 白名单 + env 清洗
- **H.3** `□` SOP playbook 的 dry-run 沙箱
  - 用 `--dry-run` shim 跑 playbook
  - 回返预期 diff，零真实副作用
  - `dangerous` 类提案必须先通过 dry-run 才能进 D.4 签字
- **H.4** `□` web-fetch 工具的浏览器沙箱
  - 容器里的临时 headless chromium
  - 无持久 cookie，per-call profile
- **H.5** `□` Skill / playbook 创作沙箱
  - skill 作者编辑 + 用合成 edge fixture 测试
  - 沙箱绿了才能 publish
- **H.6** `□` 每租户 compute 预算
  - agent + tool 子进程 CPU / mem 上限
  - per-user-per-minute 速率限制
- **H.7** `□` seccomp + capability profile 库
  - 每 tool 一份 profile，进仓
  - 从 `cmdpolicy` 规则自动生成
- **H.8** `□` Dangerous 类的临时容器包装
  - I/O 破坏类命令塞进一次性容器
  - bind-mount 目标目录
  - 失败回滚 = 删容器

---

## I · 安全与合规

- **I.1** `□` SSO / SAML / OIDC
  - Okta / Azure AD / Google Workspace
  - JIT 用户开通 + role mapping
- **I.2** `□` MFA 强制
  - 最低 TOTP
  - per-org 策略：必须 / 可选 / 关闭
- **I.3** `□` 会话管理
  - 单用户活跃会话列表
  - admin UI 撤销
  - per-org IP 白名单
- **I.4** `□` Tamper-evident audit log
  - hash-chain 记录（D.4 提案信封会引用 chain hash）
  - 每日 root hash 锚到 Git，供外部验证
- **I.5** `□` 凭证轮换策略
  - LLM key、IM token、git deploy key
  - 运维设置轮换周期；UI 显示当前 key 的年龄
- **I.6** `□` TLS 证书自动轮换
  - manager 走 Let's Encrypt + 自签兜底
  - edge agent 心跳时拉新 manager cert
- **I.7** `□` 边端二进制漏洞跟踪
  - 已装的 bundled bin 版本通过 `/edge/inventory` 暴露
  - CVE feed → 动作卡
- **I.8** `□` LLM prompt 的 PII 过滤
  - 可配置脱敏（IP / 邮箱 / 主机名 可选）
  - on-prem 模式把所有 LLM 流量绑在自托管
- **I.9** `□` Prompt injection 防御 + 输出内容过滤
  - 工具描述带信任级 tag
  - 输出渲染前扫漏的凭证
- **I.10** `□` 速率限制 per-user / per-org
  - chat msg / min、tool call / hour、IM webhook / sec
- **I.11** `□` 静态加密
  - MySQL + Qdrant + 对象存储
  - 企业版支持 per-org KMS
- **I.12** `□` 合规证据包
  - 一键导出：audit log、访问列表、变更历史、incident 时间线
  - 适配 SOC2 / ISO 27001 / 等保
- **I.13** `□` Per-org secrets vault
  - 静态加密、scope 到 org
  - SOP runtime 通过 env 注入
- **I.14** `□` 异常使用检测
  - 新地理位置登录、dangerous 工具调用激增
  - 通知 + 可选自动隔离

---

## J · 生态 / 后置

- **J.1** `◯` Skill marketplace 公开化（ADR-017）
  - skill 数量到 ~30 再 unpark
- **J.2** `□` HLD-009 coordinator e2e evaluation 上线
  - 当前只是设计
  - D.3 eval 框架落地后才能跑
- **J.3** `□` HLD-012 code-aware 分析
  - 代码仓库结合 incident
  - PR diff → 影响面分析
- **J.4** `◯` 开源生态
  - 插件 SDK 文档 + 第三方 BaseTool 注册
  - 等开源拆分（ADR-030）孵化出贡献者再启

---

## K · SOP / Runbook 闭环

整张 roadmap 里最大的工程。**故意放靠后**：A–I 要么是 SOP 的前置依
赖，要么是 SOP 安全上线的前提。**前面 B（诊断工具）、D（agent 内核
含 D.4 提案中介）、H（沙箱）、I（安全）四块没跑稳前不开 SOP** ——
否则可执行路径会变成"产事故的引擎"而不是护城河。

- **K.1** `□` Tool.Class 三级
  - `safe`（只读）/ `mutating`（改状态）/ `dangerous`（不可回滚）
  - 替换今天的二值 read / write 分流
- **K.2** `□` SOP DSL
  - YAML runbook：`triggers` / `steps` / `approvals` / `rollback`
- **K.3** `□` 双签执行链路
  - manager RSA 签
  - edge 校验
  - 两端 audit log 都打齐
  - 运维一侧的确认走 D.4
- **K.4** `□` 单步回滚
  - 每个 mutating step 声明逆操作（不可逆则声明 `noop`）
  - 中止的执行按反序 unwind
- **K.5** `□` 起手 playbook 库
  - `host_restart_service`
  - `disk_cleanup`
  - `log_rotate`
  - `certificate_renew`
- **K.6** `□` Playbook 市场（内部）
  - org 之间 share + import playbook
  - 由作者 org 签名
- **K.7** `□` SOP 执行时间线
  - 单次执行视图：每步 logs / exit code / 前后 diff
  - 可回放用于复盘

---

## L · 周期性 Agent 任务

共用一个调度器原语；面向"agent 长时间盯着"这种工作量，不是同步 chat。

- **L.1** `□` 巡检
  - 每天 / 每周扫所有 edge
  - 跑轻量 RCA：`top_load_anomaly` / `dep_health_check` / `cert_expiry_check`
  - 命中风险自动建 incident
- **L.2** `□` 周报 / 月报
  - 跨 edge 汇总告警、incident、执行过的动作
  - LLM 写摘要
  - IM 推送
- **L.3** `□` 守望任务 / 主动推送
  - `create_watch(condition, expire_at)` BaseTool
  - 命中条件后 SSE 推回原 session
- **L.4** `□` 容量预测
  - 磁盘 / 内存 / 基数 趋势外推
  - 阈值预测在 N 天内会撞时通过 D.4 起提案
- **L.5** `□` 成本汇总
  - LLM token + 存储 + 带宽 按 org 归因
  - 月度邮件
