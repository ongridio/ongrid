import { Button, Label, Textarea } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { AlertTriangle, CheckCircle2, ListTodo, RefreshCw, Siren, X } from 'lucide-react';
import { Modal } from '@/components/Modal';
import { cn } from '@/lib/cn';
import { relativeTime } from '@/lib/format';
import { usePoll } from '@/lib/usePoll';
import {
  ackIncident,
  listIncidents,
  localizedRuleName,
  resolveIncident,
  type Incident,
  type IncidentSeverity,
  type IncidentStatus,
} from '@/api/alerts';
import { listEdges } from '@/api/edges';
import { ApiError } from '@/api/client';
import { useIncidentBadge } from '@/store/incidentBadge';
import { usePermissions } from '@/store/me';
import { useI18n } from '@/i18n/locale';
import { PaginationFooter } from '@/components/ui';

const STATUS_FILTERS: { key: string; labelZh: string; labelEn: string }[] = [
  { key: '', labelZh: '全部', labelEn: 'All' },
  { key: 'open', labelZh: '未确认', labelEn: 'Open' },
  { key: 'acknowledged', labelZh: '已确认', labelEn: 'Acknowledged' },
  { key: 'silenced', labelZh: '静默中', labelEn: 'Silenced' },
  { key: 'resolved', labelZh: '已解决', labelEn: 'Resolved' },
];

const SEVERITY_FILTERS: { key: string; labelZh: string; labelEn: string }[] = [
  { key: '', labelZh: '全部', labelEn: 'All' },
  { key: 'critical', labelZh: 'Critical', labelEn: 'Critical' },
  { key: 'warning', labelZh: 'Warning', labelEn: 'Warning' },
  { key: 'info', labelZh: 'Info', labelEn: 'Info' },
];

const POLL_INTERVAL_MS = 30_000;
const PAGE_SIZE = 50;

export default function AlertsPage() {
  const { tr } = useI18n();
  const { canMutate } = usePermissions();
  const [items, setItems] = useState<Incident[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState<string>('open');
  const [severityFilter, setSeverityFilter] = useState<string>('');
  const [page, setPage] = useState(0);
  const [resolving, setResolving] = useState<{ incident: Incident } | null>(null);
  const [ackBusyId, setAckBusyId] = useState<number | null>(null);
  // device_id → name for the Target column. incident.target_id is the
  // device id (renamed from edge id May 2026, see alert model.go:212).
  // Best-effort: missing name falls back to "Device <id>".
  //
  // IMPORTANT: only key by edge.device_id. The old code also wrote
  // m[String(e.id)] = e.name which mixed edge.id space with device.id
  // space — when two edges had id↔device_id swapped (e.g. edge 3 →
  // device 4 + edge 4 → device 3), the later-iterated edge would
  // clobber both entries with its own name, so device #3 and #4
  // rendered with the same name. See 2026-06-02 fix.
  const [deviceNames, setDeviceNames] = useState<Record<string, string>>({});
  // Source of truth for the global "未确认" count — same store the
  // sidebar badge polls. Reading from here (instead of a page-local
  // computation over filtered items) guarantees the page header
  // matches the sidebar pill no matter what status/severity the user
  // narrowed to. The store polls every 30s; we also call refresh()
  // after ack/resolve to keep both in sync without waiting for the tick.
  const globalOpen = useIncidentBadge((s) => s.openCount);
  const refreshBadge = useIncidentBadge((s) => s.refresh);

  const fetchIncidents = useCallback(
    async (opts?: { silent?: boolean }) => {
      if (!opts?.silent) setLoading(true);
      else setRefreshing(true);
      try {
        const r = await listIncidents({
          status: statusFilter || undefined,
          severity: severityFilter || undefined,
          page: page + 1,
          pageSize: PAGE_SIZE,
        });
        setItems(r.items ?? []);
        setTotal(r.total ?? 0);
        setErr(null);
        // Piggy-back: every fetch (including the 15s silent poll) is
        // also a chance to sync the global badge. Cheap because the
        // store de-dupes back-to-back refreshes implicitly via the
        // single in-flight request.
        void refreshBadge();
      } catch (e) {
        if ((e as Error).name !== 'AbortError') {
          setErr(e instanceof ApiError ? e.message : (e as Error).message);
        }
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [statusFilter, severityFilter, page, refreshBadge]
  );

  // Load the edge inventory once for Target-column name resolution.
  useEffect(() => {
    let cancelled = false;
    listEdges()
      .then((r) => {
        if (cancelled) return;
        const m: Record<string, string> = {};
        for (const e of r.items ?? []) {
          // Only key by device_id. Mixing edge.id keys caused
          // cross-contamination across rows when ids were swapped.
          if (e.device_id != null) m[String(e.device_id)] = e.name;
        }
        setDeviceNames(m);
      })
      .catch(() => {
        /* best-effort: Target falls back to "Device <id>" */
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    fetchIncidents();
  }, [fetchIncidents]);
  usePoll(() => fetchIncidents({ silent: true }), POLL_INTERVAL_MS);

  const counts = useMemo(() => {
    const pageTotal = items.length;
    let open = 0;
    let critical = 0;
    for (const i of items) {
      if (i.status === 'open') open++;
      if (i.severity === 'critical') critical++;
    }
    return { total: pageTotal, open, critical };
  }, [items]);

  return (
    <>
      <main className="anim-fade flex flex-1 flex-col overflow-hidden">
        <header className="app-header border-b border-zinc-800 px-6 py-4">
          <div className="flex items-center justify-between gap-4">
            <div>
              <h1 className="flex items-center gap-2 text-base font-semibold text-zinc-100">
                {tr('告警', 'Alerts')}
                {globalOpen > 0 && (
                  <Hint content={tr('全局未确认告警数 — 跟侧边栏红点同源', 'Global unacknowledged count — same source as the sidebar badge')}><span
                    className="inline-flex items-center rounded-full bg-red-500/90 px-2 py-0.5 text-[11px] font-medium text-white"

                  >
                    {globalOpen} {tr('未确认', 'open')}
                  </span></Hint>
                )}
              </h1>
              <p className="mt-0.5 text-xs text-zinc-500">
                {tr(
                  `全局 ${globalOpen} 未确认 · 当前筛选 ${total} 条 · 本页 Critical ${counts.critical}`,
                  `${globalOpen} open globally · ${total} in current filter · ${counts.critical} critical on this page`,
                )}
              </p>
            </div>
            <div className="flex gap-2">
              <Link
                to="/alerts/rules"
                className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
              >
                <ListTodo size={12} /> {tr('规则配置', 'Rule config')}
              </Link>
              <Button variant="outline" size="sm"
                type="button"
                onClick={() => fetchIncidents()}
                disabled={loading || refreshing}
                className="inline-flex items-center gap-1.5 px-2.5 py-1.5"
              >
                <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} />
                {tr('刷新', 'Refresh')}
              </Button>
            </div>
          </div>
        </header>

        <div className="flex flex-wrap items-center gap-3 border-b border-zinc-800 px-6 py-3 text-xs text-zinc-400">
          <FilterGroup
            label={tr('状态', 'Status')}
            options={STATUS_FILTERS.map((o) => ({ key: o.key, label: tr(o.labelZh, o.labelEn) }))}
            value={statusFilter}
            onChange={(next) => {
              setPage(0);
              setStatusFilter(next);
            }}
          />
          <FilterGroup
            label={tr('级别', 'Severity')}
            options={SEVERITY_FILTERS.map((o) => ({ key: o.key, label: tr(o.labelZh, o.labelEn) }))}
            value={severityFilter}
            onChange={(next) => {
              setPage(0);
              setSeverityFilter(next);
            }}
          />
        </div>

        <div className="flex-1 overflow-y-auto">
          {err && (
            <div className="m-6 rounded-lg border border-red-500/40 bg-red-500/5 px-4 py-3 text-sm text-red-300">
              {tr('加载失败：', 'Load failed: ')}{err}
            </div>
          )}
          {loading ? (
            <div className="flex h-40 items-center justify-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
          ) : items.length === 0 ? (
            <EmptyState />
          ) : (
            <table className="w-full text-sm">
              <thead className="sticky top-0 bg-zinc-950 text-left text-[11px] uppercase tracking-wider text-zinc-500">
                <tr className="border-b border-zinc-800">
                  <th className="px-4 py-2 font-medium">{tr('级别', 'Severity')}</th>
                  <th className="px-4 py-2 font-medium">{tr('规则', 'Rule')}</th>
                  <th className="px-4 py-2 font-medium">{tr('摘要', 'Summary')}</th>
                  <th className="px-4 py-2 font-medium">{tr('目标', 'Target')}</th>
                  <th className="px-4 py-2 font-medium">{tr('状态', 'Status')}</th>
                  <th className="px-4 py-2 font-medium">{tr('触发', 'Fired')}</th>
                  <th className="px-4 py-2 font-medium">{tr('最近', 'Last')}</th>
                  <th className="px-4 py-2 font-medium">{tr('次数', 'Count')}</th>
                  <th className="px-4 py-2 text-right font-medium">{tr('操作', 'Actions')}</th>
                </tr>
              </thead>
              <tbody>
                {items.map((inc) => (
                  <IncidentRow
                    key={inc.id}
                    incident={inc}
                    deviceNames={deviceNames}
                    ackBusy={ackBusyId === inc.id}
                    canMutate={canMutate}
                    onAck={async () => {
                      setAckBusyId(inc.id);
                      try {
                        await ackIncident(inc.id, '');
                        await Promise.all([
                          fetchIncidents({ silent: true }),
                          refreshBadge(),
                        ]);
                      } catch (e) {
                        setErr(e instanceof ApiError ? e.message : (e as Error).message);
                      } finally {
                        setAckBusyId(null);
                      }
                    }}
                    onResolve={() => setResolving({ incident: inc })}
                  />
                ))}
              </tbody>
            </table>
          )}
          <PaginationFooter
            page={page}
            pageSize={PAGE_SIZE}
            shown={items.length}
            total={total}
            loading={loading || refreshing}
            matchLabel={Boolean(statusFilter || severityFilter)}
            className="px-6"
            onPageChange={setPage}
          />
        </div>
      </main>

      {resolving && (
        <ResolveDialog
          incident={resolving.incident}
          onClose={() => setResolving(null)}
          onDone={() => {
            setResolving(null);
            void fetchIncidents({ silent: true });
            void refreshBadge();
          }}
        />
      )}
    </>
  );
}

function IncidentRow({
  incident,
  deviceNames,
  onAck,
  onResolve,
  ackBusy,
  canMutate,
}: {
  incident: Incident;
  deviceNames: Record<string, string>;
  onAck(): void;
  onResolve(): void;
  ackBusy: boolean;
  canMutate: boolean;
}) {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const viewerTip = canMutate ? undefined : tr('只读账号不能操作告警', 'Viewer accounts cannot act on alerts');
  const canAck = canMutate && incident.status === 'open' && !ackBusy;
  const canResolve = canMutate && incident.status !== 'resolved';
  const detailHref = `/alerts/incidents/${incident.id}`;
  // Row-level click → detail page. Skip when the click target is inside
  // an interactive element (buttons / nested Link) so action buttons
  // and the original rule-name Link keep working as expected.
  const onRowClick = (e: React.MouseEvent<HTMLTableRowElement>) => {
    if ((e.target as HTMLElement).closest('button, a, [data-stop-row-nav]')) return;
    navigate(detailHref);
  };
  return (
    <tr
      className={cn(
        'cursor-pointer border-b border-zinc-900 hover:bg-zinc-900/30',
        // Open rows get a red left bar so the unack'd ones are visible
        // at a glance even when the status column is off-screen.
        incident.status === 'open' && 'bg-red-500/[0.04]',
      )}
      onClick={onRowClick}
    >
      <td
        className={cn(
          'whitespace-nowrap px-4 py-2.5',
          incident.status === 'open' && 'border-l-2 border-l-red-500/70',
        )}
      >
        <SeverityBadge severity={incident.severity} />
      </td>
      <td className="whitespace-nowrap px-4 py-2.5">
        <Link
          to={`/alerts/incidents/${incident.id}`}
          className="block hover:underline"
        >
          <div className="font-medium text-zinc-100">{localizedRuleName(incident.rule_key, incident.rule_name || incident.rule_key)}</div>
          <div className="text-[11px] text-zinc-500">
            #{incident.id} · {incident.rule_key}
          </div>
        </Link>
      </td>
      {/* Summary is the truncate-absorber. title= gives the full text on
          hover — summaries can be long (full PromQL + label set) and the
          truncate cuts mid-expression. */}
      <td className="w-full max-w-0 px-4 py-2.5 text-zinc-300">
        <Hint content={incident.summary}><div className="truncate" >{incident.summary}</div></Hint>
      </td>
      <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">
        {/* Target stays simple: the device (name + id). Detail lives in the
            wide Summary column — never dump the internal dedupe_key here. */}
        {incident.target_type === 'edge' && incident.target_id
          ? (deviceNames[incident.target_id]
              ? `${deviceNames[incident.target_id]} · #${incident.target_id}`
              : tr(`设备 ${incident.target_id}`, `Device ${incident.target_id}`))
          : '—'}
      </td>
      <td className="whitespace-nowrap px-4 py-2.5">
        <StatusBadge status={incident.status} />
      </td>
      <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">{relativeTime(incident.fired_at)}</td>
      <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">{relativeTime(incident.last_fired_at)}</td>
      <td className="px-4 py-2.5 text-zinc-400">{incident.event_count}</td>
      <td className="px-4 py-2.5 text-right">
        <div className="inline-flex gap-1.5">
          <Hint content={viewerTip}><Button variant="outline" size="sm"
            type="button"
            onClick={onAck}
            disabled={!canAck}

            className="px-2 py-1"
          >
            {ackBusy ? tr('处理中…', 'Working…') : 'Ack'}
          </Button></Hint>
          <Hint content={viewerTip}><Button variant="outline" size="sm"
            type="button"
            onClick={onResolve}
            disabled={!canResolve}

            className="px-2 py-1 text-emerald-300"
          >
            Resolve
          </Button></Hint>
        </div>
      </td>
    </tr>
  );
}

function ResolveDialog({
  incident,
  onClose,
  onDone,
}: {
  incident: Incident;
  onClose(): void;
  onDone(): void;
}) {
  const { tr } = useI18n();
  const [note, setNote] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      await resolveIncident(incident.id, note.trim());
      onDone();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('解决告警', 'Resolve alert')}
      footer={
        <>
          <Button variant="outline" size="sm"
            type="button"
            onClick={onClose}
            className="px-3 py-1.5"
          >
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" size="sm"
            type="button"
            onClick={submit}
            disabled={submitting}
            className="px-3 py-1.5 font-medium text-accent-fg"
          >
            {submitting ? tr('提交中…', 'Submitting…') : tr('解决', 'Resolve')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="rounded-md border border-zinc-800 bg-zinc-950/40 px-3 py-2 text-xs text-zinc-400">
          <div className="text-zinc-200">{incident.summary || incident.rule_key}</div>
          <div className="mt-1 text-[11px] text-zinc-500">incident #{incident.id}</div>
        </div>
        <Label className="block text-xs text-zinc-400">
          <span className="mb-1 block">{tr('备注（可选，进入 incident 时间线）', 'Note (optional; recorded in the incident timeline)')}</span>
          <Textarea
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={3}
            placeholder={tr('例：服务已重启，指标恢复', 'e.g. service restarted, metrics back to normal')}
            className="w-full"
          />
        </Label>
        {err && <div className="text-xs text-red-400">{err}</div>}
      </div>
    </Modal>
  );
}

function FilterGroup({
  label,
  options,
  value,
  onChange,
}: {
  label: string;
  options: { key: string; label: string }[];
  value: string;
  onChange(v: string): void;
}) {
  return (
    <div className="inline-flex items-center gap-1.5">
      <span className="text-zinc-500">{label}</span>
      <div className="flex gap-1">
        {options.map((opt) => (
          <Button variant="outline" size="sm"
            key={opt.key || '_all'}
            type="button"
            onClick={() => onChange(opt.key)}
            className={cn(
              'rounded-md border px-2 py-0.5 text-[11px] transition-colors',
              value === opt.key
                ? 'border-zinc-600 bg-zinc-800 text-zinc-100'
                : 'border-zinc-800 bg-zinc-900/50 text-zinc-400 hover:bg-zinc-800/60 hover:text-zinc-200'
            )}
          >
            {opt.label}
          </Button>
        ))}
      </div>
    </div>
  );
}

function SeverityBadge({ severity }: { severity: IncidentSeverity }) {
  const styles =
    severity === 'critical'
      ? 'bg-red-500/15 text-red-300 ring-red-500/40'
      : severity === 'warning'
      ? 'bg-amber-500/10 text-amber-300 ring-amber-500/30'
      : 'bg-zinc-800 text-zinc-300 ring-zinc-700';
  return (
    <span className={cn('inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset', styles)}>
      {severity === 'critical' ? <Siren size={11} /> : <AlertTriangle size={11} />}
      {severity}
    </span>
  );
}

function StatusBadge({ status }: { status: IncidentStatus }) {
  const styles =
    status === 'open'
      ? 'bg-red-500/10 text-red-300 ring-red-500/30'
      : status === 'acknowledged'
      ? 'bg-blue-500/10 text-blue-300 ring-blue-500/30'
      : status === 'silenced'
      ? 'bg-zinc-700 text-zinc-300 ring-zinc-600'
      : 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30';
  return (
    <span className={cn('inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset', styles)}>
      {status === 'resolved' ? <CheckCircle2 size={11} /> : status === 'silenced' ? <X size={11} /> : <span className="h-1.5 w-1.5 rounded-full bg-current" />}
      {status}
    </span>
  );
}

function EmptyState() {
  const { tr } = useI18n();
  return (
    <div className="flex h-60 flex-col items-center justify-center gap-2 text-zinc-500">
      <CheckCircle2 size={28} className="text-emerald-500/60" />
      <div className="text-sm">{tr('当前没有匹配的告警', 'No matching alerts right now')}</div>
      <div className="text-[11px] text-zinc-600">{tr('所有规则未触发，或当前筛选下没有 incident', 'No rules have fired, or no incidents match the current filter')}</div>
    </div>
  );
}
