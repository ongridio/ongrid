import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Activity,
  AlertTriangle,
  Bot,
  ChevronDown,
  ChevronRight,
  CheckCircle2,
  Clock3,
  Database,
  GitBranch,
  Loader2,
  RefreshCw,
  Server,
  ShieldAlert,
  ShieldCheck,
  Wifi,
  XCircle,
} from 'lucide-react';
import { ApiError } from '@/api/client';
import {
  runSystemHealthCheck,
  type HealthCheck,
  type HealthReport,
  type HealthStatus,
} from '@/api/systemHealth';
import { Card } from '@/components/ui';
import { cn } from '@/lib/cn';
import type { IconType } from '@/lib/icon';
import { useI18n } from '@/i18n/locale';

type ChipTone = 'default' | 'success' | 'warning' | 'danger' | 'info' | 'accent';

type StatusMeta = {
  labelZh: string;
  labelEn: string;
  tone: ChipTone;
  icon: IconType;
  dot: string;
};

const STATUS_META: Record<HealthStatus, StatusMeta> = {
  ok: {
    labelZh: '正常',
    labelEn: 'OK',
    tone: 'success',
    icon: CheckCircle2,
    dot: 'bg-emerald-500',
  },
  degraded: {
    labelZh: '降级',
    labelEn: 'Degraded',
    tone: 'warning',
    icon: AlertTriangle,
    dot: 'bg-amber-500',
  },
  failed: {
    labelZh: '异常',
    labelEn: 'Failed',
    tone: 'danger',
    icon: XCircle,
    dot: 'bg-red-500',
  },
  unknown: {
    labelZh: '未知',
    labelEn: 'Unknown',
    tone: 'default',
    icon: ShieldAlert,
    dot: 'bg-zinc-500',
  },
};

const GROUP_META: Record<string, { labelZh: string; labelEn: string; icon: IconType }> = {
  core: { labelZh: '核心服务', labelEn: 'Core services', icon: Server },
  observability: { labelZh: '可观测性', labelEn: 'Observability', icon: Activity },
  data: { labelZh: '数据组件', labelEn: 'Data components', icon: Database },
  automation: { labelZh: '自动化', labelEn: 'Automation', icon: ShieldCheck },
  edge: { labelZh: '边缘接入', labelEn: 'Edge access', icon: GitBranch },
  ai: { labelZh: 'AI 能力', labelEn: 'AI capabilities', icon: Bot },
};

const GROUP_ORDER = ['core', 'observability', 'data', 'automation', 'edge', 'ai'];

const CHECK_LABELS: Record<string, { zh: string; en: string }> = {
  manager_api: { zh: 'Manager API', en: 'Manager API' },
  database: { zh: '数据库', en: 'Database' },
  prometheus: { zh: 'Prometheus', en: 'Prometheus' },
  grafana: { zh: 'Grafana', en: 'Grafana' },
  loki: { zh: 'Loki 日志', en: 'Loki logs' },
  tempo: { zh: 'Tempo 链路', en: 'Tempo traces' },
  qdrant: { zh: 'Qdrant 向量库', en: 'Qdrant vector DB' },
  frontier: { zh: 'Frontier 隧道', en: 'Frontier tunnel' },
  alert_engine: { zh: '告警引擎', en: 'Alert engine' },
  edges: { zh: '边缘接入状态', en: 'Edge access state' },
  llm: { zh: 'LLM 模型', en: 'LLM provider' },
  embedding: { zh: 'Embedding 模型', en: 'Embedding provider' },
};

const DETAIL_LABELS: Record<string, { zh: string; en: string }> = {
  addr: { zh: '地址', en: 'Address' },
  collection: { zh: '集合', en: 'Collection' },
  default_provider: { zh: '默认模型源', en: 'Default provider' },
  enabled: { zh: '已启用', en: 'Enabled' },
  enabled_rules: { zh: '启用规则', en: 'Enabled rules' },
  evaluator_interval_seconds: { zh: '评估间隔', en: 'Evaluation interval' },
  limit: { zh: '采样上限', en: 'Sample limit' },
  notify_cooldown_seconds: { zh: '通知冷却', en: 'Notify cooldown' },
  offline: { zh: '离线', en: 'Offline' },
  online: { zh: '在线', en: 'Online' },
  open_incidents: { zh: '未关闭事件', en: 'Open incidents' },
  providers: { zh: '模型源', en: 'Providers' },
  rules: { zh: '规则', en: 'Rules' },
  sampled: { zh: '采样数', en: 'Sampled' },
  version: { zh: '版本', en: 'Version' },
};

const MIN_MANUAL_REFRESH_MS = 650;

export default function SettingsHealth() {
  const { tr, locale } = useI18n();
  const [report, setReport] = useState<HealthReport | null>(null);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const run = useCallback(async (manual = false) => {
    setLoading(true);
    setErr(null);
    const started = Date.now();
    try {
      const next = await runSystemHealthCheck();
      setReport(next);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      if (manual) {
        const elapsed = Date.now() - started;
        if (elapsed < MIN_MANUAL_REFRESH_MS) {
          await wait(MIN_MANUAL_REFRESH_MS - elapsed);
        }
      }
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void run();
  }, [run]);

  const groups = useMemo(() => groupChecks(report?.checks ?? []), [report]);
  const checkedAt = report?.checked_at
    ? new Date(report.checked_at).toLocaleString(locale === 'zh-CN' ? 'zh-CN' : 'en-US')
    : tr('尚未检查', 'Not checked');

  return (
    <div className="space-y-3" aria-busy={loading}>
      <Card className="p-4">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <Activity size={15} className="text-zinc-400" />
              <h2 className="text-sm font-medium text-zinc-100">{tr('系统健康', 'System health')}</h2>
              {report && <StatusChip status={report.status} />}
            </div>
            <div className="mt-1 flex items-center gap-1.5 text-[11px] text-zinc-500">
              <Clock3 size={12} className="text-zinc-600" />
              <span>{checkedAt}</span>
            </div>
          </div>
          <button
            type="button"
            onClick={() => void run(true)}
            disabled={loading}
            className="inline-flex w-full items-center justify-center gap-1.5 rounded-md border border-indigo-600 bg-indigo-600/20 px-3 py-1.5 text-xs font-medium text-indigo-200 hover:bg-indigo-600/30 disabled:opacity-50 sm:w-auto"
          >
            {loading ? <Loader2 size={13} className="animate-spin" /> : <RefreshCw size={13} />}
            {loading ? tr('检查中', 'Checking') : tr('一键检查', 'Run check')}
          </button>
        </div>
        {err && (
          <div className="mt-3 rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-200">
            {err}
          </div>
        )}
      </Card>

      {report && (
        <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
          <SummaryTile status="ok" count={report.summary.ok} />
          <SummaryTile status="degraded" count={report.summary.degraded} />
          <SummaryTile status="failed" count={report.summary.failed} />
          <SummaryTile status="unknown" count={report.summary.unknown} />
        </div>
      )}

      {loading && !report ? (
        <Card className="flex h-32 items-center justify-center text-sm text-zinc-500">
          <Loader2 size={15} className="mr-2 animate-spin" /> {tr('检查中…', 'Checking…')}
        </Card>
      ) : (
        groups.map(({ group, checks }) => (
          <HealthGroup key={group} group={group} checks={checks} />
        ))
      )}
    </div>
  );
}

function SummaryTile({ status, count }: { status: HealthStatus; count: number }) {
  const { tr } = useI18n();
  const meta = STATUS_META[status];
  const Icon = meta.icon;
  return (
    <Card compact>
      <div className="flex items-center justify-between gap-2">
        <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-xs text-zinc-400">
          <span className={cn('h-1.5 w-1.5 rounded-full', meta.dot)} />
          {tr(meta.labelZh, meta.labelEn)}
        </span>
        <Icon size={14} className="text-zinc-600" />
      </div>
      <div className="mt-2 text-xl font-semibold text-zinc-100">{count}</div>
    </Card>
  );
}

function HealthGroup({ group, checks }: { group: string; checks: HealthCheck[] }) {
  const { tr } = useI18n();
  const meta = GROUP_META[group] ?? { labelZh: group, labelEn: group, icon: Wifi };
  const Icon = meta.icon;
  return (
    <section className="space-y-1.5">
      <div className="flex items-center gap-2 px-1 text-xs font-medium text-zinc-400">
        <Icon size={13} />
        <span>{tr(meta.labelZh, meta.labelEn)}</span>
      </div>
      <Card compact className="overflow-hidden p-0">
        <div className="divide-y divide-zinc-800/60">
          {checks.map((check) => (
            <HealthRow key={check.id} check={check} />
          ))}
        </div>
      </Card>
    </section>
  );
}

function HealthRow({ check }: { check: HealthCheck }) {
  const { tr } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const meta = STATUS_META[check.status] ?? STATUS_META.unknown;
  const Icon = meta.icon;
  const label = CHECK_LABELS[check.id];
  const title = label ? tr(label.zh, label.en) : check.label;
  const abnormal = check.status !== 'ok';
  const hasDetails = Boolean(check.message || (check.details && Object.keys(check.details).length > 0));
  const expandable = abnormal && hasDetails;
  const detailId = `health-detail-${check.id}`;
  const rowContent = (
    <>
      <div className="flex min-w-0 items-center gap-2">
        {expandable ? (
          expanded ? (
            <ChevronDown size={14} className="shrink-0 text-zinc-500" />
          ) : (
            <ChevronRight size={14} className="shrink-0 text-zinc-500" />
          )
        ) : (
          <span className="h-3.5 w-3.5 shrink-0" aria-hidden />
        )}
        <Icon size={14} className={cn('shrink-0', statusIconClass(check.status))} />
        <span className="truncate text-sm font-medium text-zinc-100">{title}</span>
      </div>
      <StatusChip status={check.status} />
    </>
  );

  return (
    <div>
      {expandable ? (
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={detailId}
          onClick={() => setExpanded((v) => !v)}
          className="flex w-full items-center justify-between gap-3 px-3.5 py-2.5 text-left hover:bg-zinc-800/30 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-zinc-700"
        >
          {rowContent}
        </button>
      ) : (
        <div className="flex items-center justify-between gap-3 px-3.5 py-2.5">
          {rowContent}
        </div>
      )}

      {expandable && expanded && (
        <div id={detailId} className="px-8 pb-3 text-xs">
          {check.message && (
            <div className="rounded-md border border-zinc-800/70 bg-zinc-950/40 px-3 py-2 text-zinc-300">
              <div className="mb-1 text-[10px] font-medium uppercase text-zinc-600">
                {check.status === 'failed' ? tr('异常详情', 'Exception details') : tr('状态说明', 'Status detail')}
              </div>
              <div className="break-words leading-relaxed">{check.message}</div>
            </div>
          )}

          <div className="mt-2 text-[11px] text-zinc-600">
            {tr('耗时', 'Duration')}: {check.duration_ms} ms
          </div>

          {check.details && Object.keys(check.details).length > 0 && (
            <div className="mt-2 grid grid-cols-1 gap-2 sm:grid-cols-2">
              {Object.entries(check.details).map(([key, value]) => (
                <div key={key} className="min-w-0 rounded-md bg-zinc-950/40 px-2.5 py-2">
                  <div className="truncate text-[10px] text-zinc-600">{detailLabel(key, tr)}</div>
                  <div className="mt-0.5 break-words text-xs text-zinc-300">{formatDetailValue(value, tr)}</div>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function StatusChip({ status }: { status: HealthStatus }) {
  const { tr } = useI18n();
  const meta = STATUS_META[status] ?? STATUS_META.unknown;
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-xs text-zinc-400">
      <span className={cn('h-1.5 w-1.5 rounded-full', meta.dot)} />
      {tr(meta.labelZh, meta.labelEn)}
    </span>
  );
}

function groupChecks(checks: HealthCheck[]) {
  const map = new Map<string, HealthCheck[]>();
  for (const check of checks) {
    const group = check.group || 'core';
    map.set(group, [...(map.get(group) ?? []), check]);
  }
  const ordered = GROUP_ORDER.filter((g) => map.has(g)).map((g) => ({ group: g, checks: map.get(g) ?? [] }));
  const rest = [...map.keys()]
    .filter((g) => !GROUP_ORDER.includes(g))
    .sort()
    .map((g) => ({ group: g, checks: map.get(g) ?? [] }));
  return [...ordered, ...rest];
}

function statusIconClass(status: HealthStatus) {
  switch (status) {
    case 'ok':
      return 'text-emerald-300';
    case 'degraded':
      return 'text-amber-300';
    case 'failed':
      return 'text-red-300';
    default:
      return 'text-zinc-500';
  }
}

function detailLabel(key: string, tr: (zh: string, en: string) => string) {
  const label = DETAIL_LABELS[key];
  return label ? tr(label.zh, label.en) : key;
}

function formatDetailValue(value: unknown, tr: (zh: string, en: string) => string) {
  if (typeof value === 'boolean') {
    return value ? tr('是', 'Yes') : tr('否', 'No');
  }
  if (typeof value === 'number') {
    return Number.isInteger(value) ? String(value) : value.toFixed(2);
  }
  if (typeof value === 'string') {
    return value === '' ? tr('空', 'Empty') : value;
  }
  if (value === null || value === undefined) {
    return '-';
  }
  return JSON.stringify(value);
}

function wait(ms: number) {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}
