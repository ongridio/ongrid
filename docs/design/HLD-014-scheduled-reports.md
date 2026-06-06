# HLD-014 — 定时报告（Scheduled Reports）

**Status**: draft
**Date**: 2026-06-06
**Author**: singchia + Claude
**Related**: HLD-011 (RCA investigator — 复用 worker + report artifact 范式), HLD-010 (audit log — 报告的"动作"数据源), ADR-021 (IM bridge — 投递复用 notification_channels), ROADMAP L.2 (周报/月报), ROADMAP L.1 (巡检 — 未来共用调度底座)

> 注：HLD-001~013 历史上落在 ongrid-cloud/docs/design；开源拆分后 ongrid 为 feature 主仓库，本 HLD 起设计文档落在 ongrid/docs/design。

## 目标

让用户**自己排程**生成运维报告（日报 / 周报 / 月报 / 自定义 cron），生成后：
- 站内一级菜单 `Reports` 里可回看（带数据的叙事 + 可钻取的实体 chip）；
- 同时通过已配置的 channel（Slack / 飞书 / Telegram / 企业微信）推送摘要 + 站内链接。

报告**不是** chat 会话，也不是 incident 绑定的 RCA——是一份**跨 incident / 跨 edge / 跨时间窗的周期合成 artifact**。它对标 HLD-011 的 investigation_reports（也是"一份 closed artifact、可回看、可投递"），但触发源是 cron 而非 alert fire，输出是"给人看的综述"而非"溯源到 0 号病人"。

**本期范围**：只做报告。统一任务调度（TODO / watch / 巡检 / 周期 SOP 共用一个 scheduler）是更大的事，本 HLD 设计时**预留接口**但不实现——见 §10。

## 非目标

- 不做统一 `scheduled_tasks` 表（预留迁移路径，§10）。
- 不做邮件投递（v2，先站内 + IM）。
- 不做租户隔离（私有化 MVP，沿用 feedback_skip_tenant_logic：无 org_id 列，owner 用 `created_by`）。
- 不做"任意小时粒度"排程（防 spam，只开 日/周/月预设 + 高级 cron）。

## 菜单落位（已与 singchia 对齐 2026-06-06）

放**一级菜单**，跟 `首页 / 仪表盘` 同档（L1 顶级、不折叠）：

```
顶级（不归组）
├ 首页       /            ← 即时对话
├ 仪表盘     /dashboard   ← 实时状态
└ 报告       /reports     ← 周期合成（新增）
```

理由：仪表盘是"此刻怎么样"，报告是"上一段时间怎么样"——同档聚合视图，区别只在窗口（实时 vs 周期）。不放进 `[监控告警]` 折叠段，因为那段是**被动响应**（出事点进去看），报告是**主动回顾**（每周一早上自己打开），频率与习惯都匹配 L1 常驻。

子路由：

```
/reports                  → 默认页：已生成报告列表（卡片倒序）
/reports/:id              → 单份报告详情（Hero + 叙事 + 钻取）
/reports/schedules        → 排程管理（列表 + 启停 + 编辑）
/reports/schedules/new    → 新建排程表单（也可由 chat 创建后跳进来确认）
```

## 总览

```
┌──────────────────────────────────────────────────────────────┐
│ 触发 (三条路径)                                               │
│  ① cron evaluator (1-min tick) 扫 report_schedules            │
│      WHERE enabled AND next_fire_at <= now                    │
│  ② POST /v1/reports  (手动"立即生成"，schedule_id=NULL)       │
│  ③ chat BaseTool create_report_schedule (自然语言排程)        │
└───────────────────┬──────────────────────────────────────────┘
                    ↓
┌──────────────────────────────────────────────────────────────┐
│ biz/report.Usecase                                           │
│   ① 计算 period [start,end] (按 kind + tz)                   │
│   ② dedup: (schedule_id, period_start) UNIQUE 防重复生成     │
│   ③ INSERT reports row (status=pending)                      │
│   ④ goroutine run(reportID)                                  │
└───────────────────┬──────────────────────────────────────────┘
                    ↓
┌──────────────────────────────────────────────────────────────┐
│ 数据收集 (纯 SQL，不调 LLM) → ReportFacts                    │
│   incidents:  alert_incidents WHERE first_fired_at ∈ period  │
│   actions:    audit_log + mutating_proposals ∈ period        │
│   alerts:     alert_events ∈ period (count by severity)      │
│   edges:      edges online ratio / new / churned             │
│   上一周期同结构 → 算环比 delta_pct + sparkline 序列         │
└───────────────────┬──────────────────────────────────────────┘
                    ↓
┌──────────────────────────────────────────────────────────────┐
│ Pass-1: chatruntime.SpawnWorker (persona=reporter)           │
│   输入: ReportFacts(JSON) + 用户 prompt_override             │
│   输出: ContentJSON (hero/narrative/key_incidents/...)       │
│   max_turns 低 (≤5)——report 主要是"把结构化事实写成叙事"，   │
│   不需要长 tool loop；可选给只读 tool 让它补查细节           │
│   locale 跟 schedule.owner 的 UI locale (feedback_ai_output_locale)│
└───────────────────┬──────────────────────────────────────────┘
                    ↓
┌──────────────────────────────────────────────────────────────┐
│ 落库 + 渲染 markdown fallback                                │
│   reports.content_json = ContentJSON                         │
│   reports.content_md   = renderMarkdown(ContentJSON)         │
│   reports.status = ready, generated_at = now                 │
└───────────────────┬──────────────────────────────────────────┘
                    ↓
┌──────────────────────────────────────────────────────────────┐
│ 投递 (复用现有 channel router)                               │
│   for ch in schedule.channel_ids:                            │
│     buildSenderFromChannel(ch).Send(summary + 站内链接)      │
│   delivery_json 记录每个 channel 的 sent_at / status / error │
│   (复用 notify_channel_dispatch 修过的 SendVia seam)         │
└──────────────────────────────────────────────────────────────┘
```

## 数据模型

两张表。沿用 manager 现有约定（对照 `alert.InvestigationReport` / `alert.Rule`）：
- 配置行用 `uint64 autoIncrement`（同 `rules`）；artifact 行用 `char(36)` UUID（同 `investigation_reports`）。
- TEXT / longtext 列一律 `not null`（MySQL 禁 TEXT DEFAULT，Error 1101），biz 层始终供空串。
- 无 `org_id`（单租户 MVP）；owner = `created_by`。

### `report_schedules` — 谁、何时、发去哪

```go
// ReportSchedule 是一条用户排程：定义报告的周期、范围、投递渠道、
// 生成它的 agent persona。cron evaluator 按 next_fire_at 烧它。
type ReportSchedule struct {
    ID          uint64 `gorm:"primaryKey;autoIncrement;column:id"`
    CreatedBy   uint64 `gorm:"column:created_by;not null;index:idx_rsched_owner"`

    Name        string `gorm:"column:name;size:128;not null"`
    Description string `gorm:"column:description;size:255;not null;default:''"`

    // Kind 是 UI 预设 + period 推导依据；CronSpec 是真正的触发计算源。
    // daily/weekly/monthly 三档由前端快捷生成对应 cron；custom 用户直接给。
    Kind     string `gorm:"column:kind;size:16;not null"`              // daily|weekly|monthly|custom
    CronSpec string `gorm:"column:cron_spec;size:64;not null"`         // 5-field cron, evaluator 真正用这个
    Timezone string `gorm:"column:timezone;size:64;not null;default:'UTC'"`

    // ScopeJSON 是数据筛选条件 (JSON 让加字段不挪 schema)。
    // v1 字段: {fleet_tags:[], edge_ids:[], severity_min:"warning"}
    // 空 = 全覆盖。
    ScopeJSON string `gorm:"column:scope_json;type:text;not null"`

    // 投递
    ChannelIDsJSON string `gorm:"column:channel_ids_json;type:text;not null"` // [12,7] → notification_channels.id
    InAppVisible   bool   `gorm:"column:in_app_visible;not null;default:true"`

    // agent 配置
    AgentPersona   string  `gorm:"column:agent_persona;size:64;not null;default:'reporter'"`
    PromptOverride *string `gorm:"column:prompt_override;type:text"` // 用户自定义补充 prompt，NULL = 用 persona 默认

    // 调度状态
    Enabled    bool       `gorm:"column:enabled;not null;default:true;index:idx_rsched_enabled_next,priority:1"`
    NextFireAt *time.Time `gorm:"column:next_fire_at;index:idx_rsched_enabled_next,priority:2"`
    LastFireAt *time.Time `gorm:"column:last_fire_at"`
    LastReportID *string  `gorm:"column:last_report_id;size:36"`

    CreatedAt time.Time      `gorm:"column:created_at;autoCreateTime"`
    UpdatedAt time.Time      `gorm:"column:updated_at;autoUpdateTime"`
    DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (ReportSchedule) TableName() string { return "report_schedules" }
```

### `reports` — 生成后的产物

```go
// Report 是一份生成好的报告 artifact。对照 InvestigationReport——
// 同样 status 机 + JSON content + worker 回填 + 站内回看。
type Report struct {
    ID string `gorm:"primaryKey;type:char(36);column:id"`

    // ScheduleID: 定时触发填排程 id；手动"立即生成"为 NULL。
    // (schedule_id, period_start) UNIQUE 防同一时段重复生成。
    ScheduleID  *uint64 `gorm:"column:schedule_id;uniqueIndex:uniq_report_sched_period,priority:1"`
    CreatedBy   uint64  `gorm:"column:created_by;not null"` // 排程 owner 或手动触发者

    Title       string    `gorm:"column:title;size:255;not null"` // "周报 · 2026 W23 (6/2–6/8)"
    Kind        string    `gorm:"column:kind;size:16;not null"`   // 冗余自 schedule，手动生成也有
    PeriodStart time.Time `gorm:"column:period_start;not null;uniqueIndex:uniq_report_sched_period,priority:2;index:idx_report_period"`
    PeriodEnd   time.Time `gorm:"column:period_end;not null"`
    Timezone    string    `gorm:"column:timezone;size:64;not null"`

    // status: pending → generating → ready / failed
    Status   string  `gorm:"column:status;size:16;not null;default:'pending';index:idx_report_status_created,priority:1"`
    ErrorMsg string  `gorm:"column:error_msg;type:text;not null"`

    // 内容: JSON 是 source of truth (结构化卡片渲染), MD 是导出/IM/搜索 fallback
    ContentJSON string `gorm:"column:content_json;type:longtext;not null"`
    ContentMD   string `gorm:"column:content_md;type:longtext;not null"`
    SummaryText string `gorm:"column:summary_text;size:512;not null;default:''"` // IM 预览 / 短链描述 / 列表副标题

    // 生成元信息
    GeneratedAt      *time.Time `gorm:"column:generated_at"`
    GeneratedByModel string     `gorm:"column:generated_by_model;size:64;not null;default:''"`
    PromptTokens     uint64     `gorm:"column:prompt_tokens;not null;default:0"`
    CompletionTokens uint64     `gorm:"column:completion_tokens;not null;default:0"`
    AuditSessionID   *string    `gorm:"column:audit_session_id;size:36;index"` // reporter worker 的 transcript
    WorkerID         *string    `gorm:"column:worker_id;size:64"`              // cancel / re-attach on manager restart

    // 分享: 短 token 外部只读, 30 天 TTL
    ShareToken     *string    `gorm:"column:share_token;size:32;uniqueIndex:idx_report_share"`
    ShareExpiresAt *time.Time `gorm:"column:share_expires_at"`

    // 投递结果: [{channel_id, channel_type, status, sent_at, error}]
    DeliveryJSON string `gorm:"column:delivery_json;type:text;not null"`

    CreatedAt time.Time      `gorm:"column:created_at;autoCreateTime"`
    UpdatedAt time.Time      `gorm:"column:updated_at;autoUpdateTime"`
    DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (Report) TableName() string { return "reports" }
```

### 索引说明

- `report_schedules`: `(enabled, next_fire_at)` 复合给 evaluator 一句 `WHERE enabled AND next_fire_at <= now` 扫；`created_by` 给"我的排程"列表。
- `reports`: `(schedule_id, period_start) UNIQUE` 防同一排程同一时段重复生成（evaluator 重入 / manager 重启补偿都靠它幂等）；`period_start` 给列表页倒序分页；`(status, created_at)` 给"还在生成的"查询；`share_token` 给外链 lookup。

## ContentJSON schema（关键设计决策）

reporter worker 输出的**不是 markdown，是结构化 JSON**，前端按 schema 渲染。这样 hero 数字能加 sparkline / count-up / 环比箭头，叙事里的实体能渲染成可点击 chip。markdown 是从 JSON 模板二次渲染的导出 fallback。

```jsonc
{
  "version": "1",
  "hero": [                         // 4–6 张大数字卡
    {"key":"incidents_resolved","label":"Incidents resolved","value":23,
     "delta_pct":-12,"sparkline":[4,7,3,5,2,1,1]},
    {"key":"mttr_minutes","label":"MTTR","value":47,"unit":"min","delta_pct":8,"sparkline":[...]}
  ],
  "narrative": {                    // LLM 叙事段
    "headline":"本周整体平稳，主要风险集中在 db-prod-3 的 IO 饱和",
    "paragraphs":[
      {"text":"{{entity:edge:7|db-prod-3}} 周内三次突破 30% iowait，最严重一次与 6/4 backup 重合，触发 {{entity:incident:1234|I-1234}}。",
       "entities":[{"key":"edge:7","name":"db-prod-3"},{"key":"incident:1234","name":"I-1234"}]}
    ]
  },
  "key_incidents":[
    {"id":1234,"title":"db-prod-3 IO 饱和","severity":"warning","duration_min":47,
     "status":"resolved","root_cause_snippet":"backup 与业务高峰重叠"}
  ],
  "actions_summary":{
    "mutating_total":11,"mutating_approved":11,"safe_total":47,
    "by_tool":[{"tool":"restart_service","count":4},{"tool":"disk_cleanup","count":2}]
  },
  "advice":[
    {"text":"{{entity:edge:7|db-prod-3}} 的 backup 窗口与业务高峰冲突，建议挪到 03:00–05:00"}
  ],
  "metadata":{
    "period_start":"2026-06-02T00:00:00+08:00","period_end":"2026-06-08T23:59:59+08:00",
    "data_sources":["incidents","audit_log","proposals","alerts"]
  }
}
```

`{{entity:kind:id|display_name}}` 是个轻量实体语法。前端 parse → 渲染成带色 chip，hover 浮出该实体过去 7 天的 sparkline tooltip，click 跳详情（edge → `/devices/:id`，incident → `/alerts/incidents/:id`）。reporter persona 的 system prompt 里给 schema + 几个 few-shot，LLM 学这个语法不难（参考 feedback_chat_persistence_bugs 里 i18n directive 同样靠 prompt 约束生效）。

**为什么 JSON 而非纯 markdown**：markdown 渲染不出 count-up / sparkline / 可 hover chip，只能堆静态文字 + 截图。结构化 JSON 是"炫"的前提。hero 数字 + 环比 + sparkline 全部来自数据收集层（纯 SQL 算的），LLM 只负责 narrative / advice / headline——**数字不经过 LLM，杜绝幻觉**。

## 数据收集层（ReportFacts）

关键反幻觉设计：**所有数字在喂给 LLM 之前用纯 SQL 算好**，LLM 只把事实写成叙事，不发明数字。

```go
type ReportFacts struct {
    Period       Period            // start, end, tz
    PrevPeriod   Period            // 上一周期，算环比
    Incidents    []IncidentFact    // alert_incidents ∈ period (+ RCA root_cause 若有)
    Actions      ActionFacts       // audit_log + mutating_proposals 计数
    AlertCounts  map[string]int    // by severity
    EdgeStats    EdgeFact          // online ratio / new / churned
    Hero         []HeroStat        // 预算好的大数字 + delta_pct + sparkline
}
```

`Hero[].sparkline` 用 period 内按天（周报）/ 按小时（日报）分桶的 SQL aggregate 直接出序列。`delta_pct` = `(this - prev) / prev * 100`，prev=0 时显示"new"而非除零。

## 调度器（cron evaluator）

v1 用**最小实现**，不引入新依赖：

- manager 启动时拉起一个 `report.Scheduler` goroutine，1-min ticker。
- 每 tick：`SELECT * FROM report_schedules WHERE enabled AND next_fire_at <= NOW()`，逐个 `Usecase.Generate(schedule)` + 重算 `next_fire_at`（用 cron 库，如 `robfig/cron/v3` 的 parser 算下一次）。
- **重入防护**：`(schedule_id, period_start) UNIQUE` 兜底——即使两个 tick 撞上同一 schedule，第二个 INSERT 撞唯一键直接跳过。
- **manager 重启补偿**：错过的 fire 默认只补最近一次（next_fire_at 落在过去 → 立即烧一次 + 重算到未来），不回放整段历史。
- cron 计算用 schedule.timezone（"周一 09:00" 是 owner/org 时区，存 UTC 算，渲染本地）。

未来统一 scheduler 落地时，这个 goroutine 被 `scheduled_tasks` evaluator 取代，`report.Usecase.Generate` 变成 `kind=report` 的 worker handler（§10）。

## 投递（复用 channel router）

**不重做投递**。`schedule.channel_ids_json` 引用现有 `notification_channels` 表，拿到 channel 后调 project_notify_channel_dispatch 修好的 `buildSenderFromChannel` + `SendVia` seam——Slack / 飞书 / Telegram / 企业微信都通。

IM 消息形态：rich text 摘要（hero 4 数字 + headline）+ "查看完整报告 →" 站内深链。完整的"炫"渲染只在站内 `/reports/:id`。

`delivery_json` 记录每 channel 的 `{status, sent_at, error}`，详情页"投递状态"面板展示（哪个发成功、哪个失败可重试）。

## API surface

```
GET    /v1/reports                      列表 (分页, 倒序, ?status= ?kind= 过滤)
POST   /v1/reports                      手动立即生成 (body: {scope, channel_ids?, period?})
GET    /v1/reports/:id                  单份详情 (含 content_json)
DELETE /v1/reports/:id                  删除
POST   /v1/reports/:id/share            生成/刷新分享短链
POST   /v1/reports/:id/redeliver        重投递到指定 channel
GET    /r/:share_token                  外部只读 (无需登录, 30d TTL)

GET    /v1/report-schedules             排程列表
POST   /v1/report-schedules             新建排程
PUT    /v1/report-schedules/:id         编辑
DELETE /v1/report-schedules/:id         删除
POST   /v1/report-schedules/:id/toggle  启停
POST   /v1/report-schedules/:id/run-now 立即跑一次 (不等 cron)
```

RBAC（ADR-022 三角色）：admin/user 可建排程；viewer 只读已生成报告。分享短链 `/r/:token` 走独立无鉴权路由（类比公开 vault），token 32 字符随机 + 30d 过期。

## 前端（"炫"的落点）

技术栈沿用现有（React + 现有 motion 依赖）。几个能让人想截图的细节：

| 细节 | 实现 |
|---|---|
| Hero 数字 count-up | 入场 0→目标 800ms（Framer Motion，已在依赖里）|
| 数字旁 inline sparkline | 60×16 inline SVG polyline，零图表库依赖 |
| 环比箭头 + 色编码 | ↑23% 红 / ↓12% 绿 / →灰，一眼读方向 |
| 叙事实体 chip | parse `{{entity}}` → 带色徽章，hover 出 7 天 sparkline，click 跳详情 |
| 报告头部渐变 | CSS gradient + 1 张 SVG mesh，"Linear 周报"质感（暗黑模式适配）|
| 一键导出 PDF | `@media print` 调样式 + html2pdf.js，发管理层 |
| 分享短链 | 详情页"分享"按钮 → `/r/:token` |

**反例（不做）**：满屏粒子、自动播放图表过渡、轮播——那是 demo，不是每周扫一眼的产品。

## reporter persona

`agents/reporter.md`（沿用 project_inventory_bridge persona 体系）。system prompt 要点：
- 输入是 `ReportFacts` JSON，**数字已算好，禁止改动或发明**；
- 输出严格 ContentJSON schema（few-shot 给样例 + entity 语法）；
- 语言跟 owner 的 UI locale（feedback_ai_output_locale）；
- narrative 要"点名具体实体 + 给因果"，advice 要"可执行、对应到具体 edge/incident"；
- 低 max_turns（≤5）——主要是写作不是 tool loop；可选放只读 tool（`query_incidents` / `get_host_processes`）让它补查一个叙事里需要的细节。

## §10 与未来统一调度器的兼容（预留）

`report_schedules` 现在独立。将来引入 `scheduled_tasks`（统一 TODO / watch / 巡检 / 周期 SOP）时迁移：
1. `scheduled_tasks` 表持有 `kind / cron_spec / timezone / enabled / next_fire_at / last_fire_at / payload_ref`；
2. `report_schedules` 加 `scheduled_task_id *uint64`，把调度字段迁过去，自己退化成 `kind=report` 的 payload-side 配置；
3. `report.Usecase.Generate` 注册成 `kind=report` 的 worker handler；
4. chat 入口 `create_report_schedule` 与未来 `create_todo` / `create_watch` 并列，各自细工具（语义清晰 > 一个万能工具）。

一次性迁移脚本 + AutoMigrate 加列，不破坏现有数据。本期**不做**，仅保证 schema 不挡路。

## v1 范围裁剪

| 项 | v1 | 后续 |
|---|---|---|
| 两张表 schema + AutoMigrate | ✅ | |
| cron evaluator (1-min tick) | ✅ | 统一 scheduler 取代 |
| reporter persona + ReportFacts 收集 | ✅ | |
| ContentJSON 渲染 (hero/叙事/incidents/actions/advice) | ✅ | |
| 站内列表 + 详情页 | ✅ | |
| 排程管理页 (日/周/月预设 + 高级 cron) | ✅ | |
| IM 投递 (复用 channel router) | ✅ | |
| count-up + sparkline + 渐变头 | ✅ | |
| 实体 chip hover tooltip | ✅ | |
| 手动"立即生成" | ✅ | |
| chat BaseTool create_report_schedule | ⏳ v1.5 | |
| PDF 导出 | ⏳ v1.5 | |
| 分享短链 | ⏳ v1.5 | |
| 邮件投递 | ⏳ v2 | |
| fleet selector scope | ⏳ v1.5 (接 G.2.6 标签) | |
| 用户自定义 reporter prompt | ✅ (prompt_override 列已留) | UI 高级位 v1.5 |

## 待决（开 ADR 或本 HLD 内拍）

1. **cron 库**：`robfig/cron/v3`（成熟、tz-aware parser）vs 自己写 next-fire 计算。倾向用库，只用它的 `Parser.Next()`，不用它的 in-process scheduler（我们自己 tick）。
2. **report id**：char(36) UUID（对齐 investigation_reports）已定。
3. **period 边界**：周报"上周"= 上周一 00:00 → 周日 23:59:59（owner tz）。日报 = 昨天整天。月报 = 上月。自定义 cron 的 period = 上次 fire → 本次 fire。需在 §数据收集 明确，实现时定。
4. **空报告**：周期内 0 incident / 0 action 时，仍生成一份"本周无异常"的报告（正向信号，运维想看到"平静"），还是跳过不发？倾向**仍生成**，narrative 写"平稳"，hero 全绿。
5. **生成失败的投递**：worker 失败 → status=failed，**不投递** IM（避免发半成品），站内详情页显示失败原因 + "重试"按钮。

## 实施顺序

1. PR-1：两张表 model + AutoMigrate + biz/report.Usecase 骨架（dedup + period 计算）+ 单测
2. PR-2：ReportFacts 数据收集层（纯 SQL）+ reporter persona + worker 接线，CLI 触发验证 ContentJSON
3. PR-3：cron evaluator goroutine + manager 启动接线 + 重入/补偿测试
4. PR-4：API surface + RBAC
5. PR-5：前端列表 + 详情页（hero/叙事/chip/sparkline/渐变）
6. PR-6：排程管理页 + 手动立即生成
7. PR-7：IM 投递接线（复用 channel router）+ delivery 状态面板

每个 PR 攒着，按 feedback_batch_pr_for_release 到 release 节点统一合。
