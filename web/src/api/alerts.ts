import { request } from './client';

export type IncidentStatus = 'open' | 'acknowledged' | 'silenced' | 'resolved';
export type IncidentSeverity = 'info' | 'warning' | 'critical' | string;

export type Incident = {
  id: number;
  rule_key: string;
  rule_name: string;
  severity: IncidentSeverity;
  status: IncidentStatus;
  summary: string;
  target_type?: string;
  target_id?: string;
  target_name?: string;
  runbook_url?: string;
  dedupe_key?: string;
  labels?: Record<string, string>;
  event_count: number;
  value?: number;
  threshold?: number;
  fired_at: string;
  last_fired_at: string;
  updated_at: string;
  acknowledged_at?: string | null;
  resolved_at?: string | null;
};

export type IncidentListResp = { items: Incident[]; total: number };

export function listIncidents(params: { status?: string; severity?: string; page?: number; pageSize?: number }) {
  const qs = new URLSearchParams();
  if (params.status) qs.set('status', params.status);
  if (params.severity) qs.set('severity', params.severity);
  if (params.page) qs.set('page', String(params.page));
  if (params.pageSize) qs.set('page_size', String(params.pageSize));
  const suffix = qs.toString() ? `?${qs.toString()}` : '';
  return request<IncidentListResp>('GET', `/alerts/incidents${suffix}`);
}

export function getIncident(id: number) {
  return request<Incident>('GET', `/alerts/incidents/${id}`);
}

export function ackIncident(id: number, note: string) {
  return request<Incident>('POST', `/alerts/incidents/${id}/ack`, { note });
}

export function resolveIncident(id: number, note: string) {
  return request<Incident>('POST', `/alerts/incidents/${id}/resolve`, { note });
}

export function silenceIncident(id: number, until: string, reason: string) {
  return request<Incident>('POST', `/alerts/incidents/${id}/silence`, { until, reason });
}

export type IncidentEventActor = 'system' | 'user';

export type IncidentEvent = {
  id: number;
  incident_id: number;
  event_type: string;
  status_after?: string;
  severity?: string;
  title?: string;
  message?: string;
  actor_type: IncidentEventActor;
  operator_user_id?: number;
  reason?: string;
  occurred_at: string;
  created_at: string;
};

export type IncidentEventListResp = { items: IncidentEvent[]; total: number };

export function listIncidentEvents(id: number, limit = 200) {
  const qs = limit ? `?limit=${limit}` : '';
  return request<IncidentEventListResp>('GET', `/alerts/incidents/${id}/events${qs}`);
}

// ---- Investigation report ----------------------------------------------

export type InvestigationStatus =
  | 'pending'
  | 'running'
  | 'ready'
  | 'failed'
  | 'skipped'
  // Backend-only sentinels for the not-yet-spawned / feature-off cases.
  // Same endpoint returns these as a stub object instead of 404 so the
  // SPA can render a meaningful badge.
  | 'not_started'
  | 'feature_disabled';

export type InvestigationEvidence = {
  step?: number;
  tool?: string;
  summary?: string;
  tool_call_id?: string;
};

export type InvestigationSuggestion = {
  label: string;
  category?: 'mutate' | 'capacity' | 'observe' | string;
  danger?: 'high' | 'medium' | 'low' | 'none' | string;
  command?: string;
  deeplink?: string;
};

export type InvestigationReport = {
  id?: string;
  incident_id: number;
  status: InvestigationStatus;
  status_reason?: string;
  root_cause?: string;
  affected_window?: string;
  pinpointed_target?: Record<string, unknown>;
  related_alerts?: Array<{
    incident_id: number;
    rule: string;
    rule_name: string;
    severity: string;
    status: string;
    fired_at: string;
    last_fired_at: string;
  }>;
  evidence?: InvestigationEvidence[];
  suggested_actions?: InvestigationSuggestion[];
  findings_md?: string;
  confidence?: number;
  confidence_factors?: Record<string, unknown>;
  audit_session_id?: string;
  worker_id?: string;
  tool_call_count?: number;
  created_at?: string;
  ready_at?: string;
};

export function getIncidentInvestigation(id: number) {
  return request<InvestigationReport>('GET', `/alerts/incidents/${id}/investigation`);
}

// Manually enqueue an investigation for an incident that didn't
// auto-spawn one (e.g. fired before the investigator feature was
// enabled, or operator wants a re-run). Returns 202 Accepted with
// the current report shape.
export function triggerIncidentInvestigation(id: number) {
  return request<InvestigationReport>('POST', `/alerts/incidents/${id}/investigation`);
}

// ---- Rules ----

export type RuleCondition = {
  metric: string;
  operator: '>' | '>=' | '<' | '<=' | '==' | '!=';
  threshold: number;
  window?: string;
  for?: string;
  aggregator?: string;
};

// 二维分类法：信号源 × 触发模式 — 每个 RuleKind 是这两个维度
// 的笛卡尔积之一。Phase-3 collapse: 删掉了 edge_absence / health_ingest
// / event_internal 三个特殊 kind，对应能力收敛为 metric_raw 跑 PromQL
// 查询新暴露的自观测指标（edge_last_seen_seconds_ago / prom_write_total
// / alert_events_total），见 rule_presets.ts。
//
// Phase-3-final collapse: metric_threshold 退化为「UI 友好表单」入口。
// 用户在编辑器里仍按"主机指标 + 阈值"填写，但保存时后端会把它编译
// 成单条 PromQL 表达式存为 kind=metric_raw，存储 / 评估器都只看到
// metric_raw 这一种形态。RULE_KINDS 数组里仍保留 metric_threshold
// 作为「表单选择器」存在；后端 buildRuleRow 会在落库前把 kind 改写
// 成 metric_raw。
export type RuleKind =
  | 'metric_threshold'
  | 'metric_anomaly'
  | 'metric_forecast'
  | 'metric_burn_rate'
  | 'metric_raw'
  | 'log_search'
  | 'log_match'
  | 'log_volume'
  | 'trace_latency'
  | 'trace_error_rate';

// SignalSource — three engines fan out to the nine surviving kinds.
// "event" was a fourth source pre-Phase-3 but every trigger collapsed
// to a metric_raw query against newly-exposed manager metrics
// (edge_last_seen_seconds_ago / alert_events_total / prom_write_total),
// so the picker no longer needs the dimension.
export type SignalSource = 'metric' | 'log' | 'trace';
export type TriggerMode =
  | 'threshold'
  | 'anomaly'
  | 'forecast'
  | 'burn_rate'
  | 'match'
  | 'absence'
  | 'raw';

export type RuleKindMeta = {
  kind: RuleKind;
  source: SignalSource;
  trigger: TriggerMode;
  /** UI label */
  label: string;
  /** UI helper text shown under the trigger-mode option */
  hint: string;
  /** 该 kind 允许的 scope 集合。第一个元素是默认值。
   *  长度为 1 表示 scope 锁死（UI 只显示 readonly 徽章，不给选）；
   *  长度 > 1 表示用户在这些值之间二选一/三选一。 */
  scopes: ('host' | 'global' | 'monitoring_pipeline')[];
};

import { tr as trInline } from '@/i18n/locale';

const KIND_LABEL_EN: Record<RuleKind, { label: string; hint: string }> = {
  metric_threshold: { label: 'Metric · Threshold', hint: 'Friendly host-metric + threshold form. Compiled to PromQL on save (same storage as metric_raw).' },
  metric_anomaly: { label: 'Metric · Anomaly', hint: 'Deviation from a historical baseline (z-score / MAD).' },
  metric_forecast: { label: 'Metric · Forecast', hint: 'Predict when the metric crosses the threshold in the next N minutes.' },
  metric_burn_rate: { label: 'Metric · Burn rate', hint: 'SLO error-budget multi-window multi-rate.' },
  metric_raw: { label: 'Metric · Raw PromQL', hint: 'Write your own PromQL expression.' },
  log_search: { label: 'Log · Structured search', hint: 'Backend-neutral keyword and field filters; works with Loki or Elasticsearch.' },
  log_match: { label: 'Log · Pattern match (Loki legacy)', hint: 'Legacy LogQL rule; migrate it before switching to Elasticsearch.' },
  log_volume: { label: 'Log · Volume change (Loki legacy)', hint: 'Legacy LogQL rule; migrate it before switching to Elasticsearch.' },
  trace_latency: { label: 'Trace · Latency', hint: 'p50 / p95 / p99 latency over threshold.' },
  trace_error_rate: { label: 'Trace · Error rate', hint: 'span.status=error ratio over threshold.' },
};

const RULE_KIND_DEFS: Array<RuleKindMeta & { _zh: { label: string; hint: string } }> = [
  { kind: 'metric_threshold',  source: 'metric', trigger: 'threshold',  scopes: ['host'],
    label: '', hint: '', _zh: { label: '指标 · 阈值', hint: '友好的主机指标 + 阈值表单。保存时编译为 PromQL（与 metric_raw 同存储格式）。' } },
  { kind: 'metric_anomaly',    source: 'metric', trigger: 'anomaly',    scopes: ['host', 'global'],
    label: '', hint: '', _zh: { label: '指标 · 异常', hint: '偏离历史基线（z-score / MAD）' } },
  { kind: 'metric_forecast',   source: 'metric', trigger: 'forecast',   scopes: ['host', 'global'],
    label: '', hint: '', _zh: { label: '指标 · 预测', hint: '推算未来 N 分钟内何时跨过门槛' } },
  { kind: 'metric_burn_rate',  source: 'metric', trigger: 'burn_rate',  scopes: ['global'],
    label: '', hint: '', _zh: { label: '指标 · 燃烧率', hint: 'SLO error budget 多窗多倍率' } },
  { kind: 'metric_raw',        source: 'metric', trigger: 'raw',        scopes: ['global', 'host'],
    label: '', hint: '', _zh: { label: '指标 · 原生 PromQL', hint: '自己写 PromQL 表达式' } },
  { kind: 'log_search',        source: 'log',    trigger: 'match',      scopes: ['global'],
    label: '', hint: '', _zh: { label: '日志 · 结构化搜索', hint: '关键词 + 字段过滤，Loki / Elasticsearch 共用' } },
  { kind: 'log_match',         source: 'log',    trigger: 'match',      scopes: ['global', 'host'],
    label: '', hint: '', _zh: { label: '日志 · 模式匹配（Loki 兼容）', hint: '旧 LogQL 正则规则，切换 Elasticsearch 前需迁移' } },
  { kind: 'log_volume',        source: 'log',    trigger: 'threshold',  scopes: ['global', 'host'],
    label: '', hint: '', _zh: { label: '日志 · 总量异变（Loki 兼容）', hint: '旧 LogQL 规则，切换 Elasticsearch 前需迁移' } },
  { kind: 'trace_latency',     source: 'trace',  trigger: 'threshold',  scopes: ['global'],
    label: '', hint: '', _zh: { label: '链路 · 延迟阈值', hint: 'p50 / p95 / p99 延迟跨阈值' } },
  { kind: 'trace_error_rate',  source: 'trace',  trigger: 'threshold',  scopes: ['global'],
    label: '', hint: '', _zh: { label: '链路 · 错误率', hint: 'span.status=error 占比过阈值' } },
];

function localizedKindMeta(d: typeof RULE_KIND_DEFS[number]): RuleKindMeta {
  const obj = { kind: d.kind, source: d.source, trigger: d.trigger, scopes: d.scopes, label: '', hint: '' } as RuleKindMeta;
  Object.defineProperty(obj, 'label', {
    get: () => trInline(d._zh.label, KIND_LABEL_EN[d.kind].label),
    enumerable: true,
  });
  Object.defineProperty(obj, 'hint', {
    get: () => trInline(d._zh.hint, KIND_LABEL_EN[d.kind].hint),
    enumerable: true,
  });
  return obj;
}

export const RULE_KINDS: RuleKindMeta[] = RULE_KIND_DEFS.map(localizedKindMeta);

const SCOPE_ZH = { host: '按主机', global: '全局', monitoring_pipeline: '平台自身' } as const;
const SCOPE_EN = { host: 'Per host', global: 'Global', monitoring_pipeline: 'Platform itself' } as const;
export const SCOPE_LABEL = new Proxy({} as Record<'host' | 'global' | 'monitoring_pipeline', string>, {
  get: (_t, k: string) => trInline(SCOPE_ZH[k as keyof typeof SCOPE_ZH] ?? k, SCOPE_EN[k as keyof typeof SCOPE_EN] ?? k),
});

const SCOPE_HINT_ZH = {
  host: '每台机器一条 incident，evaluator 按设备分组',
  global: '整条查询一条 incident，跨主机聚合',
  monitoring_pipeline: 'Ongrid 自我观测（manager / 通知投递 / Prom write 健康）',
} as const;
const SCOPE_HINT_EN = {
  host: 'One incident per host; evaluator groups by device',
  global: 'One incident per query; aggregated across hosts',
  monitoring_pipeline: 'Ongrid self-observability (manager / notify delivery / Prom write health)',
} as const;
export const SCOPE_HINT = new Proxy({} as Record<'host' | 'global' | 'monitoring_pipeline', string>, {
  get: (_t, k: string) => trInline(SCOPE_HINT_ZH[k as keyof typeof SCOPE_HINT_ZH] ?? k, SCOPE_HINT_EN[k as keyof typeof SCOPE_HINT_EN] ?? k),
});

const SIGNAL_SOURCES_DEF: Array<{ code: SignalSource; zh: string; en: string }> = [
  { code: 'metric', zh: '指标 (Prometheus)', en: 'Metric (Prometheus)' },
  { code: 'log',    zh: '日志 (Loki)',       en: 'Log (Loki)' },
  { code: 'trace',  zh: '链路 (Tempo)',      en: 'Trace (Tempo)' },
];
export const SIGNAL_SOURCES: { code: SignalSource; label: string }[] = SIGNAL_SOURCES_DEF.map((s) => {
  const obj = { code: s.code, label: '' } as { code: SignalSource; label: string };
  Object.defineProperty(obj, 'label', {
    get: () => trInline(s.zh, s.en),
    enumerable: true,
  });
  return obj;
});

const TRIGGER_MODES_DEF: Array<{ code: TriggerMode; zh: string; en: string }> = [
  { code: 'threshold', zh: '阈值',         en: 'Threshold' },
  { code: 'anomaly',   zh: '异常',         en: 'Anomaly' },
  { code: 'forecast',  zh: '预测',         en: 'Forecast' },
  { code: 'burn_rate', zh: '燃烧率',       en: 'Burn rate' },
  { code: 'match',     zh: '匹配',         en: 'Match' },
  { code: 'absence',   zh: '缺失',         en: 'Absence' },
  { code: 'raw',       zh: '原生表达式',   en: 'Raw expression' },
];
export const TRIGGER_MODES: { code: TriggerMode; label: string }[] = TRIGGER_MODES_DEF.map((m) => {
  const obj = { code: m.code, label: '' } as { code: TriggerMode; label: string };
  Object.defineProperty(obj, 'label', {
    get: () => trInline(m.zh, m.en),
    enumerable: true,
  });
  return obj;
});

// BUILTIN_RULE_NAMES maps seeded rule_key → bilingual display name. Server
// stores the seed names in Chinese (see internal/manager/data/alert/store/
// seed_rules.go); we localize at render time without touching the DB.
// Unknown rule_keys (user-created rules) fall through to rule.name verbatim.
const BUILTIN_RULE_NAMES: Record<string, { zh: string; en: string }> = {
  cpu_high: { zh: 'CPU 高负载', en: 'CPU high load' },
  mem_high: { zh: '内存高占用', en: 'Memory high' },
  disk_high: { zh: '磁盘高占用', en: 'Disk high' },
  load1_high: { zh: 'Load1 高', en: 'Load1 high' },
  device_offline: { zh: '设备离线', en: 'Device offline' },
  scrape_down: { zh: 'Scrape Down', en: 'Scrape down' },
  prom_ingest_fail: { zh: 'Prom 写入失败', en: 'Prom write failure' },
  disk_full_warning: { zh: '磁盘使用率 > 85%', en: 'Disk usage > 85%' },
  cpu_high_default: { zh: 'CPU 高负载（PromQL）', en: 'CPU high load (PromQL)' },
  swap_high: { zh: 'Swap 使用率 > 50%', en: 'Swap usage > 50%' },
  fd_exhaustion: { zh: '文件描述符接近耗尽（>85%）', en: 'File descriptors near exhaustion (>85%)' },
};

/**
 * Returns the localized display name for a rule. For built-in seeded rules
 * (cpu_high / device_offline / etc.) we substitute a bilingual variant;
 * user-created rules (or any rule_key we don't recognize) return their
 * stored `name` verbatim.
 */
export function localizedRuleName(rule_key: string, name: string): string {
  const m = BUILTIN_RULE_NAMES[rule_key];
  if (!m) return name;
  return trInline(m.zh, m.en);
}

export type Rule = {
  id: number;
  rule_key: string;
  kind: RuleKind;
  name: string;
  source_type: string;
  scope_type: string;
  join_mode: 'all' | 'any';
  severity: string;
  enabled: boolean;
  conditions?: RuleCondition[];
  spec?: Record<string, unknown>;
  labels?: Record<string, string>;
  runbook_url?: string;
  /** Channels this rule pins notifications to. Empty / undefined means
   *  the engine falls back to global severity / scope filters. */
  notify_channel_ids?: number[];
  /** 发送策略 (send-policy) dampening config. Both zero / undefined
   *  means dampening is disabled (every firing notifies, subject only
   *  to cooldown). */
  notify_window_seconds?: number;
  notify_min_fires?: number;
  created_at: string;
  updated_at: string;
};

export type RuleListResp = { items: Rule[]; total: number };

export type RuleInput = {
  rule_key: string;
  kind: RuleKind;
  name: string;
  scope_type: string;
  join_mode: 'all' | 'any';
  severity: string;
  enabled: boolean;
  conditions?: RuleCondition[];
  spec?: Record<string, unknown>;
  labels?: Record<string, string>;
  runbook_url?: string;
  notify_channel_ids?: number[];
  /** 发送策略 dampening — both zero / unset = disabled. */
  notify_window_seconds?: number;
  notify_min_fires?: number;
};

export function listRules(scope?: string) {
  const qs = scope ? `?scope=${encodeURIComponent(scope)}` : '';
  return request<RuleListResp>('GET', `/alert-rules${qs}`);
}

export function getRule(id: number) {
  return request<Rule>('GET', `/alert-rules/${id}`);
}

export function createRule(input: RuleInput) {
  return request<Rule>('POST', '/alert-rules', input);
}

export function updateRule(id: number, input: RuleInput) {
  return request<Rule>('PUT', `/alert-rules/${id}`, input);
}

export function setRuleEnabled(id: number, enabled: boolean) {
  return request<Rule>('POST', `/alert-rules/${id}/enabled`, { enabled });
}

export function deleteRule(id: number) {
  return request<void>('DELETE', `/alert-rules/${id}`);
}

// ---- Rule preview (试算) ----
//
// Backend: POST /v1/alert-rules/preview, body = RuleInput + lookback_seconds.
// Pure read-only side-channel — never persists, never RecordFiring's.

export type RulePreviewSample = {
  ts: string;
  labels?: Record<string, string>;
  value: number;
  summary: string;
};

export type RulePreviewSeriesPoint = {
  ts: string;
  value: number;
};

export type RulePreviewResp = {
  fire_count: number;
  first_fire_at?: string;
  last_fire_at?: string;
  samples?: RulePreviewSample[];
  /** Time-ordered metric points for the inline chart preview. May be
   *  empty for kinds whose previewer doesn't yield a clean line. */
  series?: RulePreviewSeriesPoint[];
  /** Single horizontal threshold to overlay as a dashed line. nil for
   *  kinds where the threshold is per-series (anomaly z-score etc.). */
  threshold?: number;
  /** Y-axis unit hint ("%", "ms", "bps", ...). May be empty. */
  unit?: string;
  skipped_reason?: string;
};

export function previewRule(input: RuleInput, lookbackSeconds = 86400) {
  return request<RulePreviewResp>('POST', '/alert-rules/preview', {
    ...input,
    lookback_seconds: lookbackSeconds,
  });
}

// ---- Channels ----

export type Channel = {
  id: number;
  name: string;
  type: string;
  enabled: boolean;
  endpoint?: string;
  created_at: string;
  updated_at: string;
};

export type ChannelListResp = { items: Channel[]; total: number };

export type ChannelInput = {
  name: string;
  type: string;
  endpoint: string;
  /** Optional secret. Empty string means "preserve existing"; "-" clears. */
  secret?: string;
  enabled: boolean;
};

export type ChannelTestResult = {
  accepted: boolean;
  message?: string;
};

export function listChannels() {
  return request<ChannelListResp>('GET', '/notification-channels');
}

export function createChannel(input: ChannelInput) {
  return request<Channel>('POST', '/notification-channels', input);
}

export function updateChannel(id: number, input: ChannelInput) {
  return request<Channel>('PUT', `/notification-channels/${id}`, input);
}

export function deleteChannel(id: number) {
  return request<void>('DELETE', `/notification-channels/${id}`);
}

export function testChannel(id: number) {
  return request<ChannelTestResult>('POST', `/notification-channels/${id}/test`);
}

// Runtime info exposed by the manager so the SPA can show "evaluator
// runs every N min" / "notify cooldown N min" without the operator
// having to read env vars. Per-deployment, not per-rule.
export type AlertRuntimeInfo = {
  evaluator_interval_seconds: number;
  notify_cooldown_seconds: number;
};

export function getAlertRuntimeInfo() {
  return request<AlertRuntimeInfo>('GET', '/alerts/runtime-info');
}
