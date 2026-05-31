# Ongrid e2e 用例全集

> **状态**: 实际 e2e 覆盖正在按本清单一条条落地;catalog 本身是"目标
> 全集",不是"已完成清单"。已经实现的条目会在 `tests/e2e/` 里有同名
> 文件,catalog 这边的"实现"列同步勾上。

## 优先级
- **P0** — 出厂、告警贯通、通知、RCA、Slack、边端核心。挂了产品不能交付。
- **P1** — 用户体验明显劣化的回归。强烈建议覆盖。
- **P2** — 边缘流程、polish。有余力再补。

## 写在动手前
- 涉及**外部 LLM / IM / git** 的用例默认走 `tests/e2e/testenv` 里的
  fakes,**不需要任何 secret 也能跑通**。
- 想跟真实服务对账的"live mode" e2e:`E2E_LIVE_<X>=1` 环境变量打开,
  缺失的 secret 用 `testenv.RequireSecret(t, "X")` 自动 `t.Skip`。CI
  不带 secret 自动跳过,本地拿到 secret 才跑。详见 `tests/e2e/README.md`。

---

## 域 A — 出厂安装 / 升级 / 自观测

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| A1 | 首次 purge install | shell: `uninstall.sh --purge --purge-edge` → `ONGRID_PUBLIC_URL=… install.sh` | `healthz=200`、容器全 up、`.env` 有 ADMIN_PASSWORD、`/v1/auth/login` 通 | **P0** | |
| A2 | upgrade.sh 增量升级 | install 后改 VERSION 再 upgrade.sh | 旧 .env 保留、新 VERSION 上线、healthz 200 | **P0** | |
| A3 | install.sh 30s 倒计时 prompt | `bash install.sh` 在 tty 下 | 30s 内无输入 → 默认值落 .env;输入则用输入值 | P1 | |
| A4 | install.sh 非交互 env 预置 | `ONGRID_PUBLIC_URL=… curl \| bash` | 不阻塞、写入对的 PUBLIC_URL | P1 | |
| A5 | install-edge curl-pipe 完整安装 | edge: `curl /install.sh \| bash -- --access-key=…` | 拉到全 4 个 plugin 二进制、加 adm/systemd-journal 组、self-check 全通、连 cloud 成功 | **P0** | |
| A6 | edge 整包(bundle)升级 | manager → `POST /v1/edges/{id}/upgrade-package` | tunnel 发 fetch+apply、ExecStartPre 切包、新 agent 起来、health 回报 | **P0** | |
| A7 | manager 自观测指标 | 容器跑着 | Prom 抓到 `ongrid_llm_*`、`ongrid_alert_events_total`、`ongrid_im_inbound_total` | P2 | |
| A8 | manager 审计日志 | 任意 admin 操作 | `audit_logs` 表新增一行 action/resource_id/user | P1 | |

## 域 B — 认证 / 用户 / 组织

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| B1 | 登录 → JWT → 受保护接口 | `POST /v1/auth/login` | 返回 access+refresh, Bearer 调 `/v1/self` 200 | **P0** | ✅ `tests/e2e/auth_login_test.go` |
| B2 | JWT 过期 + refresh | access 过期后 `POST /v1/auth/refresh` | 新 access 可用 | P1 | ✅ `tests/e2e/auth_refresh_test.go` |
| B3 | 三角色 RBAC | admin/user/viewer 各登 | admin 通 / user 白名单 / viewer 403 | **P0** | |
| B4 | 用户 CRUD + 改密 + 改角色 | `/v1/users` 系列 | DB 行对得上、casbin 权限同步 | P1 | |
| B5 | 组织 + 成员 CRUD | `/v1/orgs` 系列 | 增删成员 | P2 | |

## 域 C — 边端注册 / 心跳 / 状态

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| C1 | 边端 register_edge | edge 启动 → tunnel handshake | `edges` 表新增、status=online、device row 创建 | **P0** | |
| C2 | 心跳 + last_seen_at | edge 每 30s heartbeat | `last_seen_at` 推进、plugin health 字段更新 | **P0** | |
| C3 | 边端离线检测 | 杀掉 edge agent | `device_offline` rule fire → incident 创建 | **P0** | |
| C4 | /v1/edges 列表 + 详情 | SPA `/devices` | 列表带 plugin health, agent_version, host_info | P1 | |
| C5 | 设备 role 改 → Sidebar 即时刷 | `PATCH /v1/devices/{id}/roles` | SPA 同 session sidebar 立刻显新 role | P1 | |
| C6 | 设备 role 过滤(Edges/Monitor/Logs) | URL `?roles=server` | 只列匹配 role 的;Loki 查询 `device_id=~"…"` | P1 | |

## 域 D — 边端 plugin runtime

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| D1 | plugin 默认开关下发 | edge 启动 `get_plugin_configs` | metrics/logs/traces/hostmetrics/procmetrics 全 enabled | **P0** | |
| D2 | logs plugin → Loki | edge journald 写日志 | Loki `{device_id="N",ongrid_source="journald"}` 有行 | **P0** | |
| D3 | hostmetrics → Prom | 等 1 个 scrape | `node_uname_info{device_id="N"}` 有 | **P0** | |
| D4 | procmetrics → Prom | 同上 | `namedprocess_namegroup_*{device_id="N"}` 有 | **P0** | |
| D5 | traces plugin → Tempo | edge 跑产生 span 的进程 | Tempo `/api/search` 能查到 | P1 | |
| D6 | plugin health 心跳上报 → UI | 杀掉 promtail | EdgeDetail 显 logs=crashed + last_error | **P0** | |
| D7 | plugin config 改 → edge SIGHUP 重载 | `PUT /v1/edges/{id}/plugins/logs` | subprocess 拿到新 yaml,不重启 agent | P1 | |
| D8 | plugin self-check 失败上报 | 删 plugin 二进制 | install-edge.sh 红字、agent 上报 crashed | P1 | |

## 域 E — 告警(规则 / 触发 / 通知)

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| E1 | metric_raw 规则 fire(PromQL) | 推 fake metric cpu>90 | `alert_incidents` 行 status=open | **P0** | ✅ `tests/e2e/alert_evaluator_test.go` |
| E2 | device_offline 规则 fire | 停 edge 5min | incident 创建、target_type=edge | **P0** | |
| E3 | incident → notification 投递 | E1 之后 | `notification_deliveries` status=ok | **P0** | |
| E4 | 通知 cooldown | E1 紧接第二次 fire | 第二次不发(or throttled) | P1 | |
| E5 | incident ack/resolve/silence | `POST /v1/alerts/incidents/{id}/ack` | status 切换、event 写一行 | P1 | |
| E6 | 规则 enabled toggle | `POST /v1/alert-rules/{id}/enabled` | 切 false 后 evaluator 不再 fire | P1 | |
| E7 | 规则 preview | `POST /v1/alert-rules/preview` | 返 Prom 查询数据 | P2 | |
| E8 | rule 通道 fallback (0 勾 ≠ 0 通知) | 不 pin channel + incident fire | 所有 enabled 渠道收到 | **P0** | |
| E9 | rule 通道 pin | rule 只 pin slack | 只 slack 收、log 不收 | P1 | |
| E10 | resolved 自动通知 | 条件转 false N min | incident → resolved, 通知一次"已恢复" | P1 | |
| E11 | 运行时 cadence chip | `GET /v1/alerts/runtime-info` | 返 evaluator_interval / cooldown | P2 | |

## 域 F — RCA Investigator

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| F1 | 自动 RCA on incident fire | E1 之后 ~10s | `investigation_reports` pending → running → ready | **P0** | |
| F2 | RCA 拿出 root_cause + tool_calls | F1 终态 | root_cause 非空、tool_call_count > 0、evidence | **P0** | |
| F3 | 手动 ForceEnqueue | `POST /v1/alerts/incidents/{id}/investigation` Accept-Language: en | 旧 report 删、新 report 创建、root_cause 英文 | **P0** | |
| F4 | Accept-Language locale 跟随 | 同上分 en/zh | 报告 root_cause 跟 Accept-Language 走 | **P0** | |
| F5 | 后台自动 fire 用 ONGRID_DEFAULT_LOCALE | 设 env=zh fire incident | report 中文 | P1 | |
| F6 | boot 时 backfill not_started | 5 个 incident 没 report 后重启 | 启动 30s 内全 backfill | P1 | |
| F7 | MaxConcurrent 限流 | 同时 fire 10 incident | 前 5 running 后 5 skipped | P2 | |
| F8 | MaxSteps 超步保护 | worker 拉满 tool call | salvage 路径写 partial report | P2 | |
| F9 | LLM 余额耗尽兜底 | 关 LLM | report fail、status_reason 有 err | P1 | |

## 域 G — 通知渠道(单向)

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| G1 | Channel CRUD + reveal | `/v1/notification-channels` + reveal | DB 行对、secret 加密 at-rest | **P0** | |
| G2 | 测试通道按钮 | `POST /v1/notification-channels/{id}/test` | 真发一条到目标 | **P0** | |
| G3 | Slack attachments 富格式 | G2 测 slack | text=`[CRITICAL]…`、attachment color/fields/footer 全对 | **P0** | ✅ `tests/e2e/notify_slack_test.go` |
| G4 | Feishu/DingTalk 签名 | G2 测 feishu/dingtalk | timestamp+sign 字段在 payload/URL | P1 | |
| G5 | Telegram chatID 在 secret | G2 测 telegram | sendMessage 带 chat_id | P1 | |
| G6 | WeCom 凭证在 URL | G2 测 wecom | URL 含 key,不发 secret 头 | P2 | |
| G7 | delivery retry worker | mock channel 返 500 | 5 次指数退避后 status=failed | P1 | |

## 域 H — Channels(双向 IM bot)

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| H1 | ImApp CRUD + secret JSON 校验 | `POST /v1/im/apps` slack | xoxb-/xapp- 前缀校验,reveal 回弹双 token | **P0** | |
| H2 | allow_from 校验 | telegram 数字 / slack `U…` | 错格式 400 + 提示 | **P0** | |
| H3 | StreamSupervisor reconcile + dial | 新建 enabled=true app | 30s 内 stream client 拉起、收 hello | **P0** | |
| H4 | Slack Socket Mode 入站 → agent run | 真 @bot 一条 | envelope → bridge → agent → chat.postMessage 出 | **P0** | |
| H5 | Telegram getUpdates → agent run | 同样 | 同链路 | P1 | |
| H6 | DefaultLocale=en 强制英文回复 | im_apps.default_locale=en + 中文输入 | agent 回英文 | P1 | |
| H7 | /new 命令切新 session | 发 /new | `im_threads.ongrid_session_id` 换、新 session | P2 | |
| H8 | Slack 频道 invite 才收 @ | 不 invite → @ → 收不到 | (人工动作) | P2 | |
| H9 | ImApp disable 后 stream 拆 | 改 enabled=false | 30s 内 close stream | P1 | |

## 域 I — Chat / Agent / AIOps

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| I1 | 创建 session + 发消息 + 回复 | `POST /chat/sessions` + `POST /messages` | chat_messages 表 user+assistant | **P0** | |
| I2 | 流式回复 | `POST /messages/stream` SSE | event 流持续、最终 done | P1 | |
| I3 | LLM provider 切 zhipu 后路由生效 | 改 default_provider | 新对话用 glm 模型 | **P0** | |
| I4 | per-message model 覆盖 | 消息体带 model | 用指定模型 | P1 | |
| I5 | tool call (promql/logql/knowledge) | agent 触发 | tool result 进 chat_messages role=tool | P1 | |
| I6 | graph kernel salvage(超步) | 小 max_step | 部分回复 + 警告 | P2 | |
| I7 | @-mention 搜索 | `/aiops/mentions/search?q=` | 返 device/incident/log 三类 | P2 | |
| I8 | query-translate(NL→PromQL/LogQL) | `/aiops/query-translate` | 返合理表达式 | P2 | |

## 域 J — 监控 / 指标 / 日志 / Trace

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| J1 | Loki query_range | `GET /v1/logs/query_range` | 返 streams | **P0** | |
| J2 | Loki label/values | `/v1/logs/labels` | device_id label 有 1..N | P1 | |
| J3 | Prom range query | `/v1/prometheus/query_range` | 返时序 | **P0** | |
| J4 | Monitor 面板(per-device) | SPA `/monitor?device=X` | 9 个核心 panel | P1 | |
| J5 | Trace 搜索 + 详情 | `/v1/traces/search`、`/v1/traces/{id}` | 列表+完整 trace | P1 | |
| J6 | Logs role → device_id 正则 | UI 选 role=server | LogQL 用 `device_id=~"id\|id"` | P1 | |
| J7 | Grafana root URL 通 | `/v1/observability/dashboards/{uid}` | 返 iframe URL | P2 | |

## 域 K — 知识库 / RAG

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| K1 | vault sync(github → embedding → qdrant) | `POST /knowledge/vault/sync` | qdrant points > 0、search 命中 | **P0** | |
| K2 | knowledge search | `POST /knowledge/search {q}` | 返 top-K | **P0** | |
| K3 | upload md/txt/pdf/docx | `POST /knowledge/docs` multipart | 提取+嵌入+入 qdrant | P1 | |
| K4 | org repo clone + sync | `/knowledge/repos` | git clone 成、sync 后 docs 计数 | P1 | |
| K5 | SSH key 给私库 | 加 key + clone 私库 | GIT_SSH_COMMAND 生效 | P2 | |

## 域 L — Skill / Marketplace

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| L1 | 加载内置 skill registry | `GET /v1/skills` | 列出 ~18 内置 | P1 | |
| L2 | skill execute(read-only) | `POST /skills/{key}/execute` | edge 跑 + 返结果 | P1 | |
| L3 | marketplace install | `POST /marketplace/install` | pack 下载、registry 行 | P2 | |

## 域 M — Webshell

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| M1 | 开 ws → 发命令 → 回显 | WebSocket + `whoami` | 回显 ongrid-edge | P1 | |
| M2 | 审计日志 | M1 结束 | `webshell_sessions` 行 + 字节计数 | P2 | |
| M3 | 跨用户隔离 | viewer 看不到 admin 的活跃 session | 列表过滤 | P1 | |

## 域 N — 拓扑 / 设备

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| N1 | node-types/relation-types CRUD | `/topology/node-types` | 增删改查 | P2 | |
| N2 | node+relation 上图 | `/topology/nodes` `/relations` | SPA `/topology` 渲染 | P2 | |
| N3 | expand_topology tool | agent 调 | 返 BFS 节点 | P2 | |

## 域 O — 系统设置 / Integration

| # | 用例 | 触发 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|---|
| O1 | system-settings CRUD + reveal sensitive | `PUT /system-settings/llm/anthropic_api_key` | sensitive reveal 需权限 | **P0** | ✅ `tests/e2e/settings_reveal_test.go` |
| O2 | LLM 模型列表 / 默认实时刷 | 改 default_provider | `/aiops/models` default 字段变 | P1 | |
| O3 | Grafana/Prom/Loki/Tempo integration test 按钮 | `/integrations/{x}/test` | 真探一次,返 ok+latency | P2 | |
| O4 | invalidate LLM router | `POST /integrations/llm/invalidate` | 下一次 chat 走新配置 | P2 | |

## 域 P — SPA 关键页面 (Playwright 候选)

| # | 用例 | 关键断言点 | 优 | 实现 |
|---|---|---|---|---|
| P1 | 登录 → Dashboard | sidebar 出现, edge 数 > 0 | **P0** | |
| P2 | /alerts → 点 incident → 详情 | summary/target/RCA panel 都渲染 | P1 | |
| P3 | /edges → EdgeDetail plugin health tab | 每条 plugin 一行 state | P1 | |
| P4 | /alert-rules → 创建 → 保存 → 列表 | "默认" 主勾 + Window chip | P1 | |
| P5 | /settings/notifications → 新建 Slack → Test | 实际打 hook + 回显 | P1 | |
| P6 | /settings/channels → 新建 Slack IM → connected | 状态变 connected | P1 | |
| P7 | 中/EN 切 locale | SPA 文案切, Accept-Language 头变 | P1 | |
| P8 | 告警 Summary truncate + hover | title 有全文 | P2 | |

---

## 总计

- **P0**: 32 条
- **P1**: 31 条
- **P2**: 18 条
- 共 **~80 条**

## 实现进度

> 已实现条目在 `tests/e2e/` 下有对应 `*_test.go`,catalog 这边的"实现"列写 ✅。
>
> 当前实现:**3 条**
> - B1 登录链路 → `tests/e2e/auth_login_test.go`
> - G3 Slack 通知富格式 → `tests/e2e/notify_slack_test.go`
> - O1 sensitive reveal 权限 → `tests/e2e/settings_reveal_test.go`

详见 `tests/e2e/README.md`。
