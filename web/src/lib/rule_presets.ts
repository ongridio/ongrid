// Preset rule library for the AlertRules editor's "📋 从预设挑" button.
// Each preset is a recipe mapped to a complete RuleInput on confirm —
// the user tweaks rule_key + threshold and saves. Grouped by operational
// concern so an operator can scan ~18 entries instead of scrolling a flat
// list. name/hint are bilingual via inline tr() reads.

import type { RuleKind, RuleInput } from '@/api/alerts';
import { tr as trInline } from '@/i18n/locale';

export type RulePresetGroup =
  | '平台健康'
  | '主机'
  | '网络'
  | '应用'
  | '日志';

const GROUP_LABEL_EN: Record<RulePresetGroup, string> = {
  平台健康: 'Platform health',
  主机: 'Host',
  网络: 'Network',
  应用: 'Application',
  日志: 'Logs',
};

export function localizedPresetGroup(g: RulePresetGroup): string {
  return trInline(g, GROUP_LABEL_EN[g]);
}

export type RulePreset = {
  id: string;
  suggestedKey: string;
  /** Live-resolved display name (re-reads locale each access). */
  name: string;
  /** Live-resolved rationale. */
  hint: string;
  exprPreview: string;
  group: RulePresetGroup;
  draft: Partial<RuleInput> & { kind: RuleKind };
};

type RulePresetDef = {
  id: string;
  suggestedKey: string;
  nameZh: string;
  nameEn: string;
  hintZh: string;
  hintEn: string;
  exprPreview: string;
  group: RulePresetGroup;
  draftBase: Partial<RuleInput> & { kind: RuleKind };
  /** Optional EN override for `draft.name` so the seeded form shows English when locale=en. */
  draftNameEn?: string;
};

const PRESET_DEFS: RulePresetDef[] = [
  {
    id: 'device_offline_metric',
    suggestedKey: 'device_offline',
    nameZh: '设备离线（90s）',
    nameEn: 'Device offline (90s)',
    hintZh: '心跳停跳超过 90 秒判离线 — 基于 heartbeat 实时刷新的 device_last_seen_timestamp_seconds 计算。',
    hintEn: 'Heartbeat stale > 90s = offline — computed from device_last_seen_timestamp_seconds refreshed by heartbeats.',
    exprPreview: 'time() - max by (device_id) (device_last_seen_timestamp_seconds) > 90',
    group: '平台健康',
    draftBase: {
      kind: 'metric_raw',
      severity: 'critical',
      scope_type: 'global',
      enabled: true,
      spec: { expr: 'time() - max by (device_id) (device_last_seen_timestamp_seconds) > 90' },
    },
    draftNameEn: 'Device offline',
  },
  {
    id: 'manager_down',
    suggestedKey: 'manager_down',
    nameZh: 'Manager 失联',
    nameEn: 'Manager unreachable',
    hintZh: 'Prom 直接 scrape Ongrid manager 失败 ⇒ manager 进程挂了 / 网络断了。比 prom_write_total 可靠（不需要 remote_write 通畅）。',
    hintEn: "Prom scrape of the Ongrid manager fails — manager process down or network broken. More reliable than prom_write_total (doesn't depend on remote_write).",
    exprPreview: 'up{job="ongrid-manager"} == 0',
    group: '平台健康',
    draftBase: {
      kind: 'metric_raw',
      severity: 'critical',
      scope_type: 'global',
      enabled: true,
      spec: { expr: 'up{job="ongrid-manager"} == 0' },
    },
  },
  {
    id: 'edge_agent_down',
    suggestedKey: 'edge_agent_down',
    nameZh: 'Edge Agent 失联',
    nameEn: 'Edge agent unreachable',
    hintZh: 'Prom scrape 边缘 agent /metrics 失败。和 device_last_seen_timestamp_seconds 互补——前者基于 Prom scrape，后者基于 manager-side last_seen 状态。',
    hintEn: 'Prom scrape of edge agent /metrics fails. Complements device_last_seen_timestamp_seconds: scrape vs. manager-side last_seen.',
    exprPreview: 'up{job="ongrid-edge"} == 0',
    group: '平台健康',
    draftBase: {
      kind: 'metric_raw',
      severity: 'critical',
      scope_type: 'global',
      enabled: true,
      spec: { expr: 'up{job="ongrid-edge"} == 0' },
    },
  },
  {
    id: 'notify_failed_5m',
    suggestedKey: 'notify_failed_5m',
    nameZh: '通知投递失败（5min 内）',
    nameEn: 'Notification delivery failure (5m)',
    hintZh: '5 分钟内出现任何 notification_failed 事件即告警。',
    hintEn: 'Fires when any notification_failed event appears in the last 5 minutes.',
    exprPreview: 'increase(alert_events_total{event_type="notification_failed"}[5m]) >= 1',
    group: '平台健康',
    draftBase: {
      kind: 'metric_raw',
      severity: 'critical',
      scope_type: 'global',
      enabled: true,
      spec: { expr: 'increase(alert_events_total{event_type="notification_failed"}[5m]) >= 1' },
    },
    draftNameEn: 'Notification delivery failure',
  },
  {
    id: 'alert_storm_30m',
    suggestedKey: 'alert_storm_30m',
    nameZh: '告警风暴（30min 内 silenced ≥ 5 次）',
    nameEn: 'Alert storm (silenced ≥ 5 in 30m)',
    hintZh: '30 分钟内人工 silence 操作 ≥ 5 次提示存在告警噪音，需要复盘规则阈值。',
    hintEn: "≥ 5 manual silences in 30 min suggests alert noise — revisit rule thresholds.",
    exprPreview: 'increase(alert_events_total{event_type="silenced"}[30m]) >= 5',
    group: '平台健康',
    draftBase: {
      kind: 'metric_raw',
      severity: 'warning',
      scope_type: 'global',
      enabled: true,
      spec: { expr: 'increase(alert_events_total{event_type="silenced"}[30m]) >= 5' },
    },
    draftNameEn: 'Alert storm',
  },
  {
    id: 'edge_reconnect_storm',
    suggestedKey: 'edge_reconnect_storm',
    nameZh: '边缘 Agent 重连频繁',
    nameEn: 'Edge agent reconnect storm',
    hintZh: '10 分钟内 geminio 重连次数 > 3 ⇒ 网络抖动 / 凭证错误 — 需要 manager 暴露 geminio_reconnects_total。',
    hintEn: '> 3 geminio reconnects in 10 min — network flap or credential error. Requires manager to expose geminio_reconnects_total.',
    exprPreview: 'increase(geminio_reconnects_total[10m]) > 3',
    group: '平台健康',
    draftBase: {
      kind: 'metric_raw',
      severity: 'warning',
      scope_type: 'global',
      enabled: false,
      spec: { expr: 'increase(geminio_reconnects_total[10m]) > 3' },
    },
    draftNameEn: 'Edge reconnect storm',
  },

  {
    id: 'cpu_high_promql',
    suggestedKey: 'cpu_high_promql',
    nameZh: 'CPU 持续高负载（>90%）',
    nameEn: 'CPU sustained high (>90%)',
    hintZh: '5min 平均 CPU ≥ 90%，by(device_id) 拆分按主机。',
    hintEn: '5-minute average CPU ≥ 90%, split by(device_id) per host.',
    exprPreview: '100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m]))) > 90',
    group: '主机',
    draftBase: {
      kind: 'metric_raw',
      severity: 'warning',
      scope_type: 'host',
      enabled: true,
      spec: { expr: '100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m]))) > 90' },
    },
    draftNameEn: 'CPU high',
  },
  {
    id: 'mem_high_90',
    suggestedKey: 'mem_high_90',
    nameZh: '内存使用率 > 90%',
    nameEn: 'Memory > 90%',
    hintZh: 'node_memory_MemAvailable / MemTotal 反推 — Linux 标准 node_exporter 指标。',
    hintEn: 'Derived from node_memory_MemAvailable / MemTotal — standard Linux node_exporter metric.',
    exprPreview: '100 * (1 - node_memory_MemAvailable_bytes/node_memory_MemTotal_bytes) > 90',
    group: '主机',
    draftBase: {
      kind: 'metric_raw',
      severity: 'warning',
      scope_type: 'host',
      enabled: true,
      spec: { expr: '100 * (1 - node_memory_MemAvailable_bytes/node_memory_MemTotal_bytes) > 90' },
    },
    draftNameEn: 'Memory high',
  },
  {
    id: 'disk_high_85',
    suggestedKey: 'disk_high_85',
    nameZh: '磁盘使用率 > 85%',
    nameEn: 'Disk > 85%',
    hintZh: '根分区 used%。多 mount 场景请改 mountpoint label 或拆 by(mountpoint)。',
    hintEn: 'Root-partition used%. For multi-mount setups, adjust the mountpoint label or split by(mountpoint).',
    exprPreview: '100 * (1 - node_filesystem_avail_bytes{mountpoint="/"}/node_filesystem_size_bytes{mountpoint="/"}) > 85',
    group: '主机',
    draftBase: {
      kind: 'metric_raw',
      severity: 'warning',
      scope_type: 'host',
      enabled: true,
      spec: { expr: '100 * (1 - node_filesystem_avail_bytes{mountpoint="/"}/node_filesystem_size_bytes{mountpoint="/"}) > 85' },
    },
    draftNameEn: 'Disk high',
  },
  {
    id: 'disk_fill_6h',
    suggestedKey: 'disk_fill_6h',
    nameZh: '磁盘 6h 内耗尽（预测）',
    nameEn: 'Disk will fill in 6h (forecast)',
    hintZh: 'predict_linear 推测 6 小时后 disk_avail_bytes ≤ 0 — 提早通知 on-call 扩容。',
    hintEn: 'predict_linear estimates disk_avail_bytes ≤ 0 in 6h — give on-call time to expand.',
    exprPreview: 'predict_linear(disk_avail_bytes[1h], 21600) <= 0',
    group: '主机',
    draftBase: {
      kind: 'metric_forecast',
      severity: 'critical',
      scope_type: 'host',
      enabled: true,
      spec: {
        metric: 'disk_avail_bytes',
        fit_window: '1h',
        predict_seconds: 21600,
        operator: '<=',
        threshold: 0,
        for_seconds: 0,
      },
    },
    draftNameEn: 'Disk fill forecast',
  },
  {
    id: 'load1_over_cpu',
    suggestedKey: 'load1_over_cpu',
    nameZh: 'Load1 高于 CPU 数 1.5 倍',
    nameEn: 'Load1 over 1.5× CPU count',
    hintZh: 'Load1 反映可运行进程数；超过 1.5×核数说明系统过载。',
    hintEn: 'Load1 reflects runnable processes; > 1.5× core count means the system is overloaded.',
    exprPreview: 'node_load1 > count by (device_id)(node_cpu_seconds_total{mode="idle"}) * 1.5',
    group: '主机',
    draftBase: {
      kind: 'metric_raw',
      severity: 'warning',
      scope_type: 'host',
      enabled: true,
      spec: { expr: 'node_load1 > count by (device_id)(node_cpu_seconds_total{mode="idle"}) * 1.5' },
    },
    draftNameEn: 'Load1 high',
  },
  {
    id: 'node_exporter_down',
    suggestedKey: 'node_exporter_down',
    nameZh: 'node_exporter 失联',
    nameEn: 'node_exporter unreachable',
    hintZh: 'Prom up 指标 == 0 持续到下一次 scrape — 区别于 edge 整机离线，是 exporter 进程 crash。',
    hintEn: "Prom up == 0 — distinct from a full edge offline; the exporter process itself has crashed.",
    exprPreview: 'up{job="node-exporter"} == 0',
    group: '主机',
    draftBase: {
      kind: 'metric_raw',
      severity: 'warning',
      scope_type: 'host',
      enabled: true,
      spec: { expr: 'up{job="node-exporter"} == 0' },
    },
  },
  {
    id: 'clock_skew',
    suggestedKey: 'clock_skew',
    nameZh: '时钟漂移 > 1s',
    nameEn: 'Clock skew > 1s',
    hintZh: 'node_timex_offset_seconds — NTP 同步异常会让所有时序数据时间错位。',
    hintEn: 'node_timex_offset_seconds — NTP issues misalign every time-series in the system.',
    exprPreview: 'abs(node_timex_offset_seconds) > 1',
    group: '主机',
    draftBase: {
      kind: 'metric_raw',
      severity: 'warning',
      scope_type: 'host',
      enabled: true,
      spec: { expr: 'abs(node_timex_offset_seconds) > 1' },
    },
    draftNameEn: 'Clock skew',
  },

  {
    id: 'net_rx_burst',
    suggestedKey: 'net_rx_burst',
    nameZh: '网卡接收速率突增 > 100MB/s',
    nameEn: 'NIC RX burst > 100MB/s',
    hintZh: '5min 平均 rx_bps > 100MB/s — 异常下载 / 扫描 / 攻击。',
    hintEn: '5-minute average rx_bps > 100 MB/s — anomalous downloads / scans / attacks.',
    exprPreview: 'rate(node_network_receive_bytes_total[5m]) > 100*1024*1024',
    group: '网络',
    draftBase: {
      kind: 'metric_raw',
      severity: 'warning',
      scope_type: 'host',
      enabled: false,
      spec: { expr: `rate(node_network_receive_bytes_total[5m]) > ${100 * 1024 * 1024}` },
    },
    draftNameEn: 'NIC RX burst',
  },
  {
    id: 'net_drop',
    suggestedKey: 'net_drop',
    nameZh: '网卡丢包',
    nameEn: 'NIC packet drops',
    hintZh: '任何持续的 receive_drop 都不正常 — 排查驱动 / 缓冲区 / 物理层。',
    hintEn: 'Any sustained receive_drop is abnormal — investigate driver / buffers / physical layer.',
    exprPreview: 'rate(node_network_receive_drop_total[5m]) > 0',
    group: '网络',
    draftBase: {
      kind: 'metric_raw',
      severity: 'warning',
      scope_type: 'host',
      enabled: false,
      spec: { expr: 'rate(node_network_receive_drop_total[5m]) > 0' },
    },
    draftNameEn: 'NIC packet drops',
  },

  {
    id: 'http_5xx_rate',
    suggestedKey: 'http_5xx_rate',
    nameZh: 'HTTP 5xx 错误率 > 1%',
    nameEn: 'HTTP 5xx error rate > 1%',
    hintZh: '5min 5xx 占比超过 1% — 取决于业务暴露的 http_requests_total{status} 直方图。',
    hintEn: '5xx ratio over 5 min > 1% — depends on the service exposing http_requests_total{status}.',
    exprPreview: '(sum(rate(http_requests_total{status=~"5.."}[5m])) / sum(rate(http_requests_total[5m]))) * 100 > 1',
    group: '应用',
    draftBase: {
      kind: 'metric_raw',
      severity: 'critical',
      scope_type: 'global',
      enabled: false,
      spec: { expr: '(sum(rate(http_requests_total{status=~"5.."}[5m])) / sum(rate(http_requests_total[5m]))) * 100 > 1' },
    },
    draftNameEn: 'HTTP 5xx error rate',
  },
  {
    id: 'http_p95_latency',
    suggestedKey: 'http_p95_latency',
    nameZh: 'HTTP p95 延迟 > 500ms',
    nameEn: 'HTTP p95 latency > 500ms',
    hintZh: 'histogram_quantile 直方图 p95 越线 — 业务需要暴露 *_bucket 指标。',
    hintEn: 'histogram_quantile p95 over threshold — service must expose *_bucket metrics.',
    exprPreview: 'histogram_quantile(0.95, sum by (le) (rate(http_request_duration_seconds_bucket[5m]))) * 1000 > 500',
    group: '应用',
    draftBase: {
      kind: 'metric_raw',
      severity: 'warning',
      scope_type: 'global',
      enabled: false,
      spec: { expr: 'histogram_quantile(0.95, sum by (le) (rate(http_request_duration_seconds_bucket[5m]))) * 1000 > 500' },
    },
    draftNameEn: 'HTTP p95 latency high',
  },

  {
    id: 'log_panic_oom_fatal',
    suggestedKey: 'log_panic_oom_fatal',
    nameZh: 'panic / OOM / fatal 日志',
    nameEn: 'panic / OOM / fatal logs',
    hintZh: 'LogQL 正则匹配 5min 内任意命中 — Loki 接入后启用。',
    hintEn: 'LogQL regex match in the last 5 min — enable once Loki is wired up.',
    exprPreview: 'count_over_time({ongrid_source=~"journald:.+"} |~ "(?i)panic|oom|fatal" [5m]) >= 1',
    group: '日志',
    draftBase: {
      kind: 'log_match',
      severity: 'critical',
      scope_type: 'global',
      enabled: false,
      spec: {
        stream_selector: '{ongrid_source=~"journald:.+"}',
        line_filter: '(?i)panic|oom|fatal',
        window: '5m',
        operator: '>=',
        threshold: 1,
      },
    },
  },
  {
    id: 'log_volume_2x',
    suggestedKey: 'log_volume_2x',
    nameZh: '错误日志量翻倍',
    nameEn: 'Error log volume doubled',
    hintZh: '当前 5min 日志条数相对上一窗口 ratio ≥ 2 — 突发故障 / 死循环。',
    hintEn: 'Current 5-min log count ratio ≥ 2 vs. prior window — incident or hot loop.',
    exprPreview: 'count_over_time({level="error"} [5m]) / offset(5m) >= 2',
    group: '日志',
    draftBase: {
      kind: 'log_volume',
      severity: 'warning',
      scope_type: 'global',
      enabled: false,
      spec: {
        stream_selector: '{level="error"}',
        window: '5m',
        ratio_op: '>=',
        ratio_threshold: 2,
      },
    },
    draftNameEn: 'Error log doubled',
  },
];

function makePreset(d: RulePresetDef): RulePreset {
  const draftName = trInline(d.nameZh, d.draftNameEn ?? d.nameEn);
  const obj = {
    id: d.id,
    suggestedKey: d.suggestedKey,
    exprPreview: d.exprPreview,
    group: d.group,
    draft: { ...d.draftBase, name: draftName },
  } as RulePreset;
  // Live-resolved name/hint via getters so locale swap is reflected on next read.
  Object.defineProperty(obj, 'name', {
    get: () => trInline(d.nameZh, d.nameEn),
    enumerable: true,
  });
  Object.defineProperty(obj, 'hint', {
    get: () => trInline(d.hintZh, d.hintEn),
    enumerable: true,
  });
  return obj;
}

export const RULE_PRESETS: RulePreset[] = PRESET_DEFS.map(makePreset);

export const RULE_PRESET_GROUPS: { group: RulePresetGroup; presets: RulePreset[] }[] = (() => {
  const order: RulePresetGroup[] = ['平台健康', '主机', '网络', '应用', '日志'];
  const byGroup = new Map<RulePresetGroup, RulePreset[]>();
  for (const g of order) byGroup.set(g, []);
  for (const p of RULE_PRESETS) {
    byGroup.get(p.group)?.push(p);
  }
  return order.map((group) => ({ group, presets: byGroup.get(group) ?? [] }));
})();
