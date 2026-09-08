import { Button, Label, Input, Textarea } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import {
  AlertTriangle,
  Bell,
  BellOff,
  Bot,
  CheckCircle2,
  ChevronLeft,
  ExternalLink,
  FileText,
  GitBranch,
  Loader2,
  Pause,
  RefreshCw,
  Siren,
  Sparkles,
  User as UserIcon,
  X,
} from 'lucide-react';
import { Modal } from '@/components/Modal';
import { cn } from '@/lib/cn';
import { openObservabilityUrl, buildExploreUrl } from '@/lib/drilldown';
import { relativeTime } from '@/lib/format';
import { usePoll } from '@/lib/usePoll';
import { useObservability } from '@/store/observability';
import {
  ackIncident,
  getIncident,
  getIncidentInvestigation,
  triggerIncidentInvestigation,
  listIncidentEvents,
  localizedRuleName,
  resolveIncident,
  silenceIncident,
  type Incident,
  type IncidentEvent,
  type IncidentSeverity,
  type IncidentStatus,
  type InvestigationReport,
  type InvestigationStatus,
} from '@/api/alerts';
import {
  createSession,
  getMessages,
  listSessions,
  type ChatMessage,
  type ChatSession,
  type ToolCallSummary,
} from '@/api/chat';
import { ApiError } from '@/api/client';
import { usePermissions } from '@/store/me';
import { tr as trInline, useI18n } from '@/i18n/locale';

type ActionKind = 'ack' | 'resolve' | 'silence';

const POLL_MS = 30_000;
// While we're waiting for the proactive AI investigation to land
// (P2 — typically 5-15s after firing), poll the events endpoint at a
// tighter cadence so the panel pops in without manual refresh. After
// AI_FAST_POLL_WINDOW_MS we fall back to the standard cadence.
const AI_FAST_POLL_MS = 5_000;
const AI_FAST_POLL_WINDOW_MS = 90_000;

export default function IncidentDetailPage() {
  const { tr } = useI18n();
  // viewer-related gating lives inside Header() where the
  // action buttons render — see the usePermissions() call there.
  const { id: rawId = '' } = useParams<{ id: string }>();
  const incidentId = Number(rawId);

  const [incident, setIncident] = useState<Incident | null>(null);
  const [events, setEvents] = useState<IncidentEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [action, setAction] = useState<ActionKind | null>(null);

  const fetchAll = useCallback(
    async (silent = false) => {
      if (!incidentId || Number.isNaN(incidentId)) {
        setErr(tr('无效的 incident id', 'Invalid incident id'));
        setLoading(false);
        return;
      }
      if (silent) setRefreshing(true);
      else setLoading(true);
      try {
        const [inc, ev] = await Promise.all([
          getIncident(incidentId),
          listIncidentEvents(incidentId, 200),
        ]);
        setIncident(inc);
        setEvents(ev.items ?? []);
        setErr(null);
      } catch (e) {
        setErr(e instanceof ApiError ? e.message : (e as Error).message);
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [incidentId]
  );

  useEffect(() => {
    void fetchAll();
  }, [fetchAll]);
  // Steady-state 30s poll for incident + events + report.
  usePoll(() => fetchAll(true), POLL_MS);

  // Tight 5s poll for the AI initial diagnosis event during the first
  // 90s after firing. Stops as soon as the event arrives or the
  // window expires; the slower 30s poll above handles the steady
  // state. Gated via usePoll's enabled flag so we don't keep a
  // ticking interval around past the window.
  const fastPollEnabled = useMemo(() => {
    if (!incident) return false;
    const hasAI = events.some((e) => e.event_type === 'ai_initial_diagnosis');
    if (hasAI) return false;
    const fired = incident.fired_at ? new Date(incident.fired_at).getTime() : NaN;
    if (Number.isNaN(fired)) return false;
    return Date.now() - fired <= AI_FAST_POLL_WINDOW_MS;
  }, [incident, events]);
  usePoll(() => fetchAll(true), AI_FAST_POLL_MS, fastPollEnabled);

  return (
    <>
      <main className="anim-fade flex flex-1 flex-col overflow-hidden">
        <Header
          incident={incident}
          loading={loading}
          refreshing={refreshing}
          onRefresh={() => fetchAll(true)}
          onAct={(k) => setAction(k)}
        />

        <div className="flex-1 overflow-y-auto">
          {err && (
            <div className="m-6 rounded-lg border border-red-500/40 bg-red-500/5 px-4 py-3 text-sm text-red-300">
              {tr('加载失败：', 'Load failed: ')}{err}
            </div>
          )}

          {loading && !incident ? (
            <div className="flex h-60 items-center justify-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
          ) : incident ? (
            <div className="space-y-6 px-6 py-6">
              <InvestigationReportPanel incidentId={incident.id} />
              <AIInitialDiagnosisPanel incident={incident} events={events} />
              <AgentTimelinePanel incidentId={incident.id} />
              <div className="grid grid-cols-1 gap-6 lg:grid-cols-[1fr_320px]">
                <Timeline events={events} />
                <Sidebar2 incident={incident} />
              </div>
            </div>
          ) : (
            !err && (
              <div className="flex h-60 items-center justify-center text-sm text-zinc-500">{tr('未找到 incident', 'Incident not found')}</div>
            )
          )}
        </div>
      </main>

      {action && incident && (
        <ActionDialog
          kind={action}
          incident={incident}
          onClose={() => setAction(null)}
          onDone={() => {
            setAction(null);
            void fetchAll(true);
          }}
        />
      )}
    </>
  );
}

function Header({
  incident,
  loading,
  refreshing,
  onRefresh,
  onAct,
}: {
  incident: Incident | null;
  loading: boolean;
  refreshing: boolean;
  onRefresh(): void;
  onAct(kind: ActionKind): void;
}) {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const { canMutate } = usePermissions();
  const mutateBlocked = !canMutate;
  const viewerTip = mutateBlocked
    ? tr('只读账号不能操作告警', 'Viewer accounts cannot act on alerts')
    : undefined;
  const [deepDiveBusy, setDeepDiveBusy] = useState(false);
  const grafanaUrl = useGrafanaDrilldownUrl(incident);
  const lokiUrl = useLokiExploreUrl(incident);
  const tempoUrl = useTempoExploreUrl(incident);
  const canAck = !mutateBlocked && incident?.status === 'open';
  const canResolve = !mutateBlocked && incident && incident.status !== 'resolved';
  const canSilence = !mutateBlocked && incident && incident.status !== 'resolved' && incident.status !== 'silenced';

  // Deep-dive: spin a fresh chat seeded to make the agent fan out
  // get_incident_detail / correlate_incident on this incident_id and
  // continue conversationally. The one-shot AI 初查 panel above only
  // runs once at fire time; this is the "ask follow-up" affordance.
  const onDeepDive = useCallback(async () => {
    if (!incident || deepDiveBusy) return;
    setDeepDiveBusy(true);
    try {
      const titleSrc = incident.summary || localizedRuleName(incident.rule_key, incident.rule_name || incident.rule_key) || `incident #${incident.id}`;
      const title = tr(`诊断 #${incident.id} · ${titleSrc}`, `Diagnose #${incident.id} · ${titleSrc}`).slice(0, 60);
      const prompt = tr(
        `请深入诊断告警 incident_id=${incident.id}（${titleSrc}）。先用 get_incident_detail 拉详情，再用 correlate_incident 做 metric/log/trace/edge 关联，给出根因判断和处置建议。`,
        `Please diagnose alert incident_id=${incident.id} (${titleSrc}). First fetch details with get_incident_detail, then correlate metric/log/trace/edge via correlate_incident, and give a root-cause assessment and remediation suggestions.`,
      );
      const session = await createSession({
        title,
        related_incident_id: incident.id,
      });
      navigate(`/chat/${session.id}`, { state: { initialPrompt: prompt } });
    } catch (e) {
      // Surface inline; the page error banner handles fetch errors but
      // this is a user-initiated action so a toast-shaped fallback is
      // overkill — we just leave the button enabled for retry.
      console.error('deep dive: createSession failed', e);
      setDeepDiveBusy(false);
    }
  }, [incident, deepDiveBusy, navigate]);

  return (
    <header className="app-header border-b border-zinc-800/60 px-6 py-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 text-xs text-zinc-500">
            <Link to="/alerts" className="inline-flex items-center gap-1 text-zinc-400 hover:text-zinc-200">
              <ChevronLeft size={12} /> {tr('返回告警', 'Back to alerts')}
            </Link>
          </div>
          <h1 className="mt-1 truncate text-base font-semibold text-zinc-100">
            {incident?.summary || (incident && localizedRuleName(incident.rule_key, incident.rule_name || incident.rule_key)) || `incident #${incident?.id ?? '—'}`}
          </h1>
          <div className="mt-1.5 flex flex-wrap items-center gap-2 text-[11px] text-zinc-500">
            {incident && <SeverityBadge severity={incident.severity} />}
            {incident && <StatusBadge status={incident.status} />}
            {incident && (
              <span className="text-zinc-500">
                #{incident.id} · {localizedRuleName(incident.rule_key, incident.rule_name || incident.rule_key)}
                {incident.rule_key && incident.rule_name && incident.rule_name !== incident.rule_key
                  ? ` (${incident.rule_key})`
                  : ''}
              </span>
            )}
          </div>
          {incident && (
            <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-zinc-500">
              {incident.target_type === 'edge' && incident.target_id && (
                <span>
                  {tr('设备 ', 'Device ')}
                  <Link to={`/edges/${incident.target_id}`} className="text-zinc-300 hover:underline">
                    {incident.target_id}
                  </Link>
                </span>
              )}
              <span>
                {tr('触发：', 'Fired: ')}{relativeTime(incident.fired_at)}
              </span>
              <span>{tr('最近：', 'Last: ')}{relativeTime(incident.last_fired_at)}</span>
              <span>{tr('次数：', 'Count: ')}{incident.event_count}</span>
              {incident.acknowledged_at && (
                <span>{tr('已确认：', 'Acked: ')}{relativeTime(incident.acknowledged_at)}</span>
              )}
              {incident.resolved_at && (
                <span>{tr('已解决：', 'Resolved: ')}{relativeTime(incident.resolved_at)}</span>
              )}
            </div>
          )}
        </div>

        <div className="flex shrink-0 flex-wrap items-center gap-2">
          <Button variant="outline" size="sm"
            type="button"
            onClick={onRefresh}
            disabled={loading || refreshing}
            className="inline-flex items-center gap-1.5 px-2.5 py-1.5"
          >
            <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} />
            {tr('刷新', 'Refresh')}
          </Button>
          <Button variant="plain" size="sm"
            type="button"
            disabled={!grafanaUrl}
            aria-disabled={!grafanaUrl}
            onClick={() => {
              if (grafanaUrl) void openObservabilityUrl(grafanaUrl);
            }}
            className={cn(
              'inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-xs',
              grafanaUrl
                ? 'border-indigo-500/40 bg-indigo-500/10 text-indigo-200 hover:bg-indigo-500/20'
                : 'cursor-not-allowed border-zinc-800 bg-zinc-900/50 text-zinc-600'
            )}
          >
            <ExternalLink size={12} /> {tr('在 Grafana 查看相关指标', 'View related metrics in Grafana')}
          </Button>
          {lokiUrl && (
            <Button variant="outline" size="sm"
              type="button"
              onClick={() => void openObservabilityUrl(lokiUrl)}
              className="inline-flex items-center gap-1.5 px-2.5 py-1.5 text-emerald-200"
            >
              <FileText size={12} /> {tr('跳查相关日志', 'View related logs')}
            </Button>
          )}
          {tempoUrl && (
            <Button variant="outline" size="sm"
              type="button"
              onClick={() => void openObservabilityUrl(tempoUrl)}
              className="inline-flex items-center gap-1.5 px-2.5 py-1.5 text-violet-200"
            >
              <GitBranch size={12} /> {tr('跳查相关链路', 'View related traces')}
            </Button>
          )}
          <Button variant="plain" size="sm"
            type="button"
            onClick={() => void onDeepDive()}
            disabled={!incident || deepDiveBusy}
            className="inline-flex items-center gap-1.5 px-2.5 py-1.5 border border-indigo-500/40 bg-indigo-500/10 text-indigo-200 hover:bg-indigo-500/20"
          >
            <Bot size={12} /> {deepDiveBusy ? tr('创建会话…', 'Creating…') : tr('深入诊断', 'Deep diagnose')}
          </Button>
          <Hint content={viewerTip}><Button variant="outline" size="sm"
            type="button"
            onClick={() => onAct('ack')}
            disabled={!canAck}

            className="inline-flex items-center gap-1.5 px-2.5 py-1.5"
          >
            <Bell size={12} /> Ack
          </Button></Hint>
          <Hint content={viewerTip}><Button variant="outline" size="sm"
            type="button"
            onClick={() => onAct('resolve')}
            disabled={!canResolve}

            className="inline-flex items-center gap-1.5 px-2.5 py-1.5 text-emerald-300"
          >
            <CheckCircle2 size={12} /> Resolve
          </Button></Hint>
          <Hint content={viewerTip}><Button variant="outline" size="sm"
            type="button"
            onClick={() => onAct('silence')}
            disabled={!canSilence}

            className="inline-flex items-center gap-1.5 px-2.5 py-1.5"
          >
            <BellOff size={12} /> Silence
          </Button></Hint>
        </div>
      </div>
    </header>
  );
}

// InvestigationReportPanel renders the structured root-cause report
// produced by the per-alert investigator (PR-2/3). Status drives the
// UI:
//   pending / running    → spinner + "investigating…"
//   ready                → full report (summary + pinpoint + evidence
//                          + suggested actions + collapsible findings)
//   failed               → red badge + reason
//   skipped              → grey badge + reason (severity-floor / dedup /
//                          budget — non-actionable for operator)
//   not_started          → silent (alert is too fresh; we poll)
//   feature_disabled     → grey badge once + stop polling
// Polls every 5 s while pending/running; stops on terminal status.
function InvestigationReportPanel({ incidentId }: { incidentId: number }) {
  const { tr, locale } = useI18n();
  const [report, setReport] = useState<InvestigationReport | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [expandFindings, setExpandFindings] = useState(false);
  const [triggering, setTriggering] = useState(false);
  // Counts how many consecutive not_started polls we've done. Caps at
  // NOT_STARTED_MAX_POLLS (≈ 30 s) before we stop polling and show the
  // manual "run now" CTA — historical bug (v0.7.49) was: incidents
  // pre-investigator-feature have no report ever, so the SPA spun a
  // "正在派出根因分析 worker…" spinner forever.
  const [notStartedTicks, setNotStartedTicks] = useState(0);
  const NOT_STARTED_MAX_POLLS = 6; // 6 × 5s = 30s grace for async enqueue

  // Fetch + auto-poll while running.
  useEffect(() => {
    let cancelled = false;
    let timer: number | null = null;
    let localNotStartedTicks = 0;

    const fetchOnce = async () => {
      try {
        const r = await getIncidentInvestigation(incidentId);
        if (cancelled) return;
        setReport(r);
        setErr(null);
        // pending / running → always poll. not_started → poll a few
        // times (grace for async enqueue from alert fire) then stop
        // and surface a manual trigger button.
        if (r.status === 'pending' || r.status === 'running') {
          localNotStartedTicks = 0;
          setNotStartedTicks(0);
          timer = window.setTimeout(fetchOnce, 5000);
        } else if (r.status === 'not_started') {
          localNotStartedTicks++;
          setNotStartedTicks(localNotStartedTicks);
          if (localNotStartedTicks < NOT_STARTED_MAX_POLLS) {
            timer = window.setTimeout(fetchOnce, 5000);
          }
          // else: stop polling; UI will render the "trigger manually" CTA.
        }
      } catch (e) {
        if (!cancelled) setErr((e as Error).message || 'load failed');
      }
    };
    void fetchOnce();
    return () => {
      cancelled = true;
      if (timer) window.clearTimeout(timer);
    };
  }, [incidentId]);

  // Manual enqueue — POST /v1/alerts/incidents/{id}/investigation. On
  // success the polling effect's next tick (re-armed by setReport
  // resetting notStartedTicks) picks up the new pending row.
  const triggerNow = async () => {
    setTriggering(true);
    setErr(null);
    try {
      const r = await triggerIncidentInvestigation(incidentId);
      setReport(r);
      setNotStartedTicks(0);
      // Re-arm the poller by mounting effect: just call a manual refresh
      // loop here (effect already cleaned up; spin a fresh one in-place).
      const poll = async () => {
        const next = await getIncidentInvestigation(incidentId);
        setReport(next);
        if (next.status === 'pending' || next.status === 'running') {
          window.setTimeout(poll, 5000);
        }
      };
      window.setTimeout(poll, 1500);
    } catch (e) {
      setErr((e as Error).message || 'trigger failed');
    } finally {
      setTriggering(false);
    }
  };

  // Hide silently when nothing to show yet AND no error (alert may be
  // too fresh / investigator disabled — don't add noise).
  if (!report && !err) return null;
  if (err) {
    return (
      <section className="rounded-lg border border-red-500/30 bg-red-500/5 px-4 py-3 text-xs text-red-300">
        {tr('加载根因报告失败：', 'Failed to load investigation report: ')}
        {err}
      </section>
    );
  }
  if (!report) return null;

  if (report.status === 'feature_disabled') {
    return (
      <section className="rounded-lg border border-zinc-700/40 bg-zinc-900/30 px-4 py-2.5 text-[12px] text-zinc-500">
        {tr('自动根因分析未启用 — 设置 ONGRID_INVESTIGATOR_ENABLED=true 后重启 manager 可开启。',
            'Auto root-cause analysis is not enabled — set ONGRID_INVESTIGATOR_ENABLED=true and restart the manager to enable.')}
      </section>
    );
  }
  if (report.status === 'pending') {
    return (
      <section className="rounded-lg border border-indigo-500/20 bg-indigo-500/5 px-4 py-3">
        <div className="flex items-center gap-2 text-sm text-indigo-200/80">
          <Loader2 size={14} className="animate-spin text-indigo-300" />
          <span>{tr('正在派出根因分析 worker…', 'Spawning root-cause analysis worker…')}</span>
        </div>
      </section>
    );
  }
  if (report.status === 'not_started') {
    // Two sub-states: still within polling grace, or exhausted (no
    // auto-spawn — likely a pre-feature incident or one the
    // investigator dedup-suppressed).
    if (notStartedTicks < NOT_STARTED_MAX_POLLS) {
      return (
        <section className="rounded-lg border border-indigo-500/20 bg-indigo-500/5 px-4 py-3">
          <div className="flex items-center gap-2 text-sm text-indigo-200/80">
            <Loader2 size={14} className="animate-spin text-indigo-300" />
            <span>{tr('等待自动派出根因分析 worker…', 'Waiting for auto-spawned root-cause analysis worker…')}</span>
          </div>
        </section>
      );
    }
    return (
      <section className="flex items-center justify-between gap-3 rounded-lg border border-zinc-700/40 bg-zinc-900/30 px-4 py-3">
        <div className="text-sm text-zinc-300">
          {tr('该告警暂无自动根因分析（功能启用前发生的告警，或严重度低于阈值）。点右侧按钮手动派出。',
              'No automatic investigation for this alert (fired before the feature was enabled, or below severity floor). Trigger manually with the button.')}
        </div>
        <Button type="submit" variant="plain" size="sm"
          onClick={triggerNow}
          disabled={triggering}
          className="px-3 py-1.5 border border-indigo-500/40 bg-indigo-500/10 text-indigo-200 hover:bg-indigo-500/20"
        >
          {triggering
            ? tr('派出中…', 'Spawning…')
            : tr('手动派出根因分析', 'Run root-cause analysis now')}
        </Button>
      </section>
    );
  }
  if (report.status === 'running') {
    return (
      <section className="rounded-lg border border-indigo-500/20 bg-indigo-500/5 px-4 py-3">
        <div className="flex items-center gap-2 text-sm text-indigo-200/80">
          <Loader2 size={14} className="animate-spin text-indigo-300" />
          <span>{tr('Worker 正在调用诊断工具，30 - 90 秒…', 'Worker is calling diagnostic tools, 30 - 90 s…')}</span>
        </div>
      </section>
    );
  }
  if (report.status === 'failed') {
    return (
      <section className="flex items-start justify-between gap-3 rounded-lg border border-red-500/30 bg-red-500/5 px-4 py-3 text-xs text-red-300">
        <div>
          <div className="font-medium">{tr('根因分析失败', 'Investigation failed')}</div>
          {report.status_reason && <div className="mt-1 text-red-300/80">{report.status_reason}</div>}
        </div>
        <Button type="submit" variant="dangerGhost" size="sm"
          onClick={triggerNow}
          disabled={triggering}
          className="shrink-0 px-2.5 py-1 text-red-200"
        >
          {triggering ? tr('派出中…', 'Spawning…') : tr('重新分析', 'Re-analyze')}
        </Button>
      </section>
    );
  }
  if (report.status === 'skipped') {
    return (
      <section className="flex items-center justify-between gap-3 rounded-lg border border-zinc-700/40 bg-zinc-900/30 px-4 py-2.5 text-[12px] text-zinc-500">
        <span>
          {tr('根因分析已跳过：', 'Investigation skipped: ')}
          {report.status_reason || tr('未知原因', 'no reason provided')}
        </span>
        <Button type="submit" variant="outline" size="sm"
          onClick={triggerNow}
          disabled={triggering}
          className="shrink-0 px-2.5 py-1"
        >
          {triggering ? tr('派出中…', 'Spawning…') : tr('重新分析', 'Re-analyze')}
        </Button>
      </section>
    );
  }

  // Ready — full report.
  const elapsedMs =
    report.created_at && report.ready_at
      ? new Date(report.ready_at).getTime() - new Date(report.created_at).getTime()
      : null;
  const elapsedText = elapsedMs != null ? formatElapsed(elapsedMs) : null;
  const conf = report.confidence ?? null;
  const target = report.pinpointed_target ?? {};
  const targetStr = formatTarget(target);
  const evidence = report.evidence ?? [];
  const actions = report.suggested_actions ?? [];

  return (
    <section className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 shadow-sm">
      <header className="flex items-center justify-between gap-3 border-b border-emerald-500/20 px-4 py-2.5">
        <div className="flex items-center gap-2">
          <span className="inline-flex h-6 w-6 items-center justify-center rounded-md bg-emerald-500/20 text-emerald-300 ring-1 ring-inset ring-emerald-500/40">
            <Sparkles size={13} />
          </span>
          <span className="text-sm font-medium text-emerald-100">
            {tr('根因分析报告', 'Root cause report')}
          </span>
          <StatusPill status="ready" />
          {conf != null && (
            <span className="text-[11px] text-emerald-200/70">
              {tr('置信度', 'confidence')} {Math.round(conf * 100)}%
            </span>
          )}
        </div>
        <div className="flex items-center gap-3 text-[11px] text-zinc-500">
          {elapsedText && <span>{tr('耗时', 'elapsed')} {elapsedText}</span>}
          {(report.tool_call_count ?? 0) > 0 && (
            <span>{report.tool_call_count} {tr('个 tool call', 'tool calls')}</span>
          )}
          {report.ready_at && <span>{relativeTime(report.ready_at)}</span>}
          <Button type="submit" variant="outline" size="sm"
            onClick={triggerNow}
            disabled={triggering}
            className="px-2 py-0.5 text-emerald-200"
          >
            {triggering ? tr('派出中…', 'Spawning…') : tr('重新分析', 'Re-analyze')}
          </Button>
        </div>
      </header>

      <div className="space-y-3 px-4 py-3 text-[13px] text-zinc-100">
        {report.root_cause && (
          <div>
            <div className="mb-1 text-[11px] uppercase tracking-wider text-emerald-300/70">
              {tr('根因', 'Root cause')}
            </div>
            <div className="text-[14px] leading-relaxed">{report.root_cause}</div>
          </div>
        )}

        {(report.affected_window || targetStr || (report.related_alerts?.length ?? 0) > 0) && (
          <div className="grid grid-cols-1 gap-2 text-[12px] sm:grid-cols-2">
            {report.affected_window && (
              <div>
                <span className="text-zinc-500">{tr('影响窗口', 'Window')}: </span>
                <span className="font-mono text-zinc-300">{formatWindow(report.affected_window, locale)}</span>
              </div>
            )}
            {targetStr && (
              <div>
                <span className="text-zinc-500">{tr('定位对象', 'Target')}: </span>
                <span className="font-mono text-zinc-300">{targetStr}</span>
              </div>
            )}
          </div>
        )}

        {evidence.length > 0 && (
          <div>
            <div className="mb-1 text-[11px] uppercase tracking-wider text-emerald-300/70">
              {tr('证据链', 'Evidence')}
            </div>
            <ol className="space-y-1 text-[12px]">
              {evidence.map((e, i) => (
                <li key={i} className="flex gap-2">
                  <span className="text-zinc-500">{e.step ?? i + 1}.</span>
                  {e.tool && (
                    <span className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-[11px] text-zinc-300">
                      {e.tool}
                    </span>
                  )}
                  <span className="text-zinc-300">{e.summary}</span>
                </li>
              ))}
            </ol>
          </div>
        )}

        {(report.related_alerts?.length ?? 0) > 0 && (
          <div>
            <div className="mb-1 text-[11px] uppercase tracking-wider text-emerald-300/70">
              {tr('关联告警', 'Related alerts')}
            </div>
            <ul className="space-y-1 text-[12px]">
              {report.related_alerts!.map((ra) => (
                <li key={ra.incident_id} className="flex flex-wrap items-center gap-2">
                  <Link
                    to={`/alerts/incidents/${ra.incident_id}`}
                    className="font-mono text-[11px] text-emerald-300 hover:text-emerald-200"
                  >
                    #{ra.incident_id}
                  </Link>
                  <span className="text-zinc-200">{ra.rule_name || ra.rule}</span>
                  <span className={cn(
                    'rounded px-1.5 py-0.5 text-[10px]',
                    ra.severity === 'critical' ? 'bg-red-500/15 text-red-300'
                      : ra.severity === 'warning' ? 'bg-amber-500/15 text-amber-300'
                      : 'bg-zinc-500/15 text-zinc-300',
                  )}>
                    {ra.severity}
                  </span>
                  <span className="text-[11px] text-zinc-500">
                    {tr('最近触发', 'last fired')} {relativeTime(ra.last_fired_at)}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {actions.length > 0 && (
          <div>
            <div className="mb-1 text-[11px] uppercase tracking-wider text-emerald-300/70">
              {tr('建议动作', 'Suggested actions')}
            </div>
            <ul className="space-y-1.5 text-[12px]">
              {actions.map((a, i) => (
                <li key={i} className="flex flex-wrap items-center gap-2">
                  <DangerBadge danger={a.danger} category={a.category} />
                  <span className="text-zinc-200">{a.label}</span>
                  {a.command && (
                    <code className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-[11px] text-zinc-300">
                      {a.command}
                    </code>
                  )}
                  {a.deeplink && (
                    <a href={a.deeplink} className="text-[11px] text-emerald-300 hover:text-emerald-200" target="_blank" rel="noreferrer">
                      {tr('跳转', 'open')} →
                    </a>
                  )}
                </li>
              ))}
            </ul>
          </div>
        )}

        {report.findings_md && (
          <div>
            <Button variant="subtle" size="sm"
              type="button"
              onClick={() => setExpandFindings((v) => !v)}
              className="inline-flex items-center gap-1"
            >
              {expandFindings ? '▾' : '▸'}{' '}
              {expandFindings
                ? tr('收起完整 markdown', 'Hide full markdown')
                : tr('展开完整 markdown', 'Show full markdown')}
            </Button>
            {expandFindings && (
              <div className="md-body mt-2 rounded-md border border-zinc-800 bg-zinc-950/40 px-3 py-2 text-[13px] leading-relaxed text-zinc-100">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{report.findings_md}</ReactMarkdown>
              </div>
            )}
          </div>
        )}

        <div className="border-t border-emerald-500/10 pt-2 text-[11px] text-zinc-500">
          {tr('⚠️ 本报告由 LLM 综合生成，关键决策请二次核验。', '⚠️ Generated by LLM — verify before acting on critical decisions.')}
        </div>
      </div>
    </section>
  );
}

function StatusPill({ status }: { status: InvestigationStatus }) {
  const { tr } = useI18n();
  const labels: Record<InvestigationStatus, [string, string, string]> = {
    pending:          ['排队中', 'Queued', 'bg-zinc-700/50 text-zinc-300'],
    running:          ['进行中', 'Running', 'bg-indigo-500/20 text-indigo-200'],
    ready:            ['已完成', 'Ready', 'bg-emerald-500/20 text-emerald-200'],
    failed:           ['失败', 'Failed', 'bg-red-500/20 text-red-200'],
    skipped:          ['跳过', 'Skipped', 'bg-zinc-700/40 text-zinc-400'],
    not_started:      ['未开始', 'Not started', 'bg-zinc-700/40 text-zinc-400'],
    feature_disabled: ['未启用', 'Disabled', 'bg-zinc-700/40 text-zinc-400'],
  };
  const [zh, en, cls] = labels[status];
  return (
    <span className={`inline-flex items-center rounded-md px-1.5 py-0.5 text-[10px] font-medium ${cls}`}>
      ● {tr(zh, en)}
    </span>
  );
}

function DangerBadge({ danger, category }: { danger?: string; category?: string }) {
  const { tr } = useI18n();
  const dCls: Record<string, string> = {
    high:   'text-red-300',
    medium: 'text-amber-300',
    low:    'text-emerald-300',
    none:   'text-zinc-400',
  };
  const cIcon: Record<string, string> = {
    mutate:   '⚠️',
    capacity: '↻',
    observe:  '📈',
  };
  return (
    <span className={`inline-flex items-center gap-1 text-[11px] ${dCls[danger ?? 'none'] ?? 'text-zinc-400'}`}>
      <span>{cIcon[category ?? ''] ?? '•'}</span>
      <span className="font-mono">{category ? tr(category, category) : '—'}</span>
      <span className="text-zinc-600">/</span>
      <span>{danger ?? 'none'}</span>
    </span>
  );
}

function formatTarget(t: Record<string, unknown>): string {
  if (!t || Object.keys(t).length === 0) return '';
  const parts: string[] = [];
  if (t.device_id != null) parts.push(`device=${t.device_id}`);
  if (t.pid != null) parts.push(`pid=${t.pid}`);
  if (t.service) parts.push(`service=${t.service}`);
  if (t.cmd) parts.push(`cmd=${String(t.cmd).slice(0, 60)}`);
  return parts.join(' ');
}

function formatWindow(w: string, _locale: string): string {
  // Backend stores "start/end" ISO range; render compactly as start - end.
  const slash = w.indexOf('/');
  if (slash <= 0) return w;
  const start = w.slice(0, slash);
  const end = w.slice(slash + 1);
  return `${start} → ${end}`;
}

function formatElapsed(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(0)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.floor((ms % 60_000) / 1000);
  return `${m}m${s}s`;
}

// AIInitialDiagnosisPanel renders the proactive AI investigation result
// (P2 — backend writes one alert_events row with
// event_type=ai_initial_diagnosis on every fresh firing). When the
// event isn't there yet AND the incident is younger than the LLM
// budget window (60s + grace), we show a "AI 正在分析..." placeholder.
// Beyond that we render nothing — operators can still scroll the
// timeline if they want raw context, and we don't want to imply the
// LLM is "stuck" when actually it's just disabled or failed silently.
function AIInitialDiagnosisPanel({
  incident,
  events,
}: {
  incident: Incident;
  events: IncidentEvent[];
}) {
  const { tr } = useI18n();
  const aiEvent = useMemo(
    () => events.find((e) => e.event_type === 'ai_initial_diagnosis'),
    [events]
  );
  const ageMs = useMemo(() => {
    if (!incident.fired_at) return Infinity;
    const t = new Date(incident.fired_at).getTime();
    if (Number.isNaN(t)) return Infinity;
    return Date.now() - t;
  }, [incident.fired_at]);

  if (aiEvent) {
    const text = (aiEvent.message || aiEvent.reason || '').trim();
    if (!text) return null;
    return (
      <section className="rounded-lg border border-indigo-500/30 bg-indigo-500/5 shadow-sm">
        <header className="flex items-center justify-between border-b border-indigo-500/20 px-4 py-2.5">
          <div className="flex items-center gap-2">
            <span className="inline-flex h-6 w-6 items-center justify-center rounded-md bg-indigo-500/20 text-indigo-300 ring-1 ring-inset ring-indigo-500/40">
              <Sparkles size={13} />
            </span>
            <span className="text-sm font-medium text-indigo-200">{tr('AI 初查', 'AI initial diagnosis')}</span>
            <span className="text-[11px] text-indigo-300/70">{tr('系统自动 · 单次诊断', 'Automatic · one-shot')}</span>
          </div>
          <span className="text-[11px] text-zinc-500">
            {relativeTime(aiEvent.occurred_at || aiEvent.created_at)}
          </span>
        </header>
        <div className="md-body px-4 py-3 text-[14px] leading-relaxed text-zinc-100">
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
        </div>
      </section>
    );
  }

  // No AI event yet — show a thin placeholder while the incident is
  // young (LLM round-trip typically lands within 5-15s). Beyond 90s,
  // assume LLM disabled / failed silently and render nothing.
  if (ageMs < 90_000) {
    return (
      <section className="rounded-lg border border-indigo-500/20 bg-indigo-500/5 px-4 py-3">
        <div className="flex items-center gap-2 text-sm text-indigo-200/80">
          <Loader2 size={14} className="animate-spin text-indigo-300" />
          <span>{tr('AI 正在分析这条告警，预计 5-15 秒…', 'AI is analyzing this alert, expected in 5-15 s…')}</span>
        </div>
      </section>
    );
  }

  return null;
}

function Timeline({ events }: { events: IncidentEvent[] }) {
  const { tr } = useI18n();
  if (events.length === 0) {
    return (
      <div className="rounded-xl border border-zinc-800/60 bg-zinc-900/40 p-6 text-sm text-zinc-500">
        {tr('暂无事件记录', 'No event records')}
      </div>
    );
  }
  return (
    <div className="rounded-xl border border-zinc-800/60 bg-zinc-900/40">
      <div className="border-b border-zinc-800/60 px-4 py-2.5 text-[11px] uppercase tracking-wider text-zinc-500">
        {tr(`时间线 · ${events.length}`, `Timeline · ${events.length}`)}
      </div>
      <ol className="divide-y divide-zinc-800/40">
        {events.map((ev) => (
          <TimelineItem key={ev.id} event={ev} />
        ))}
      </ol>
    </div>
  );
}

function TimelineItem({ event }: { event: IncidentEvent }) {
  const text = event.message || event.reason || '';
  return (
    <li className="flex gap-3 px-4 py-3">
      <div className="flex shrink-0 flex-col items-center">
        <div
          className={cn(
            'flex h-7 w-7 items-center justify-center rounded-full ring-1 ring-inset',
            event.actor_type === 'user'
              ? 'bg-blue-500/10 text-blue-300 ring-blue-500/30'
              : 'bg-zinc-800 text-zinc-400 ring-zinc-700'
          )}
        >
          {event.actor_type === 'user' ? <UserIcon size={13} /> : <Bot size={13} />}
        </div>
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <EventTypeBadge type={event.event_type} />
          {event.severity && (
            <span className="text-[11px] text-zinc-500">{event.severity}</span>
          )}
          <span className="ml-auto text-[11px] text-zinc-500">
            {relativeTime(event.occurred_at || event.created_at)}
          </span>
        </div>
        {text && (
          <div className="mt-1 whitespace-pre-wrap break-words text-sm text-zinc-300">{text}</div>
        )}
        {!text && event.title && (
          <div className="mt-1 text-sm text-zinc-400">{event.title}</div>
        )}
      </div>
    </li>
  );
}

function Sidebar2({ incident }: { incident: Incident }) {
  const { tr } = useI18n();
  const labels = incident.labels || {};
  const labelKeys = Object.keys(labels).sort();
  return (
    <aside className="space-y-4">
      <section className="rounded-xl border border-zinc-800/60 bg-zinc-900/40">
        <div className="border-b border-zinc-800/60 px-4 py-2.5 text-[11px] uppercase tracking-wider text-zinc-500">
          Labels
        </div>
        {labelKeys.length === 0 ? (
          <div className="px-4 py-3 text-xs text-zinc-500">—</div>
        ) : (
          <table className="w-full text-xs">
            <tbody>
              {labelKeys.map((k) => (
                <tr key={k} className="border-t border-zinc-800/40 first:border-t-0">
                  <td className="px-4 py-1.5 align-top text-zinc-500">{k}</td>
                  <td className="px-4 py-1.5 break-all text-zinc-200">{labels[k]}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      {incident.runbook_url && (
        <section className="rounded-xl border border-zinc-800/60 bg-zinc-900/40 px-4 py-3">
          <div className="text-[11px] uppercase tracking-wider text-zinc-500">{tr('说明 / Runbook', 'Notes / Runbook')}</div>
          {/^https?:\/\//i.test(incident.runbook_url.trim()) ? (
            <a
              href={incident.runbook_url}
              target="_blank"
              rel="noopener noreferrer"
              className="mt-1 inline-flex items-center gap-1 break-all text-xs text-indigo-300 hover:text-indigo-200 hover:underline"
            >
              <ExternalLink size={11} /> {incident.runbook_url}
            </a>
          ) : (
            <p className="mt-1 whitespace-pre-wrap break-words text-xs text-zinc-300">
              {incident.runbook_url}
            </p>
          )}
        </section>
      )}

      {incident.dedupe_key && (
        <section className="rounded-xl border border-zinc-800/60 bg-zinc-900/40 px-4 py-3">
          <div className="text-[11px] uppercase tracking-wider text-zinc-500">Dedupe key</div>
          <div className="mt-1 break-all font-mono text-[11px] text-zinc-400">{incident.dedupe_key}</div>
        </section>
      )}

      {/* 影响面 — topology neighbours of the alerting device.
          Helps the operator see "this service depends on X, Y, Z" at
          a glance before diving into the chat panel. Only renders for
          edge-targeted incidents where we can resolve a device row. */}
      {(incident.target_type === 'edge' || incident.target_type === 'device') &&
        incident.target_id && <ImpactPanel deviceID={Number(incident.target_id)} />}
    </aside>
  );
}

function ImpactPanel({ deviceID }: { deviceID: number }) {
  const { tr } = useI18n();
  const [nodeID, setNodeID] = useState<number | null | undefined>(undefined);
  useEffect(() => {
    if (!deviceID) return;
    let cancelled = false;
    setNodeID(undefined);
    import('@/api/devices').then(({ getDevice }) =>
      getDevice(deviceID)
        .then((d) => {
          if (!cancelled) setNodeID(d.node_id ?? null);
        })
        .catch(() => {
          if (!cancelled) setNodeID(null);
        }),
    );
    return () => {
      cancelled = true;
    };
  }, [deviceID]);
  if (nodeID === undefined) {
    return (
      <section className="rounded-xl border border-zinc-800/60 bg-zinc-900/40 px-4 py-3">
        <div className="text-[11px] uppercase tracking-wider text-zinc-500">{tr('影响面', 'Impact')}</div>
        <div className="mt-2 text-xs text-zinc-500">{tr('解析中…', 'Resolving…')}</div>
      </section>
    );
  }
  if (nodeID === null) return null;
  return (
    <section className="rounded-xl border border-zinc-800/60 bg-zinc-900/40 px-4 py-3">
      <div className="mb-2 text-[11px] uppercase tracking-wider text-zinc-500">
        {tr('影响面（拓扑邻居）', 'Impact (Topology neighbours)')}
      </div>
      <ImpactNeighbors nodeID={nodeID} />
    </section>
  );
}

// Inline lazy-load of NodeNeighbors so the IncidentDetail page doesn't
// pay the topology chunk import cost when the alert target isn't
// device-shaped (the common case for cluster-wide alerts).
function ImpactNeighbors({ nodeID }: { nodeID: number }) {
  const [Comp, setComp] = useState<React.ComponentType<{ nodeID: number }> | null>(null);
  useEffect(() => {
    import('@/components/topology/NodeNeighbors').then((m) => setComp(() => m.NodeNeighbors));
  }, []);
  if (!Comp) return <div className="text-xs text-zinc-500">…</div>;
  return <Comp nodeID={nodeID} />;
}

function ActionDialog({
  kind,
  incident,
  onClose,
  onDone,
}: {
  kind: ActionKind;
  incident: Incident;
  onClose(): void;
  onDone(): void;
}) {
  const { tr } = useI18n();
  const [note, setNote] = useState('');
  const [until, setUntil] = useState('30m');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const title = kind === 'ack' ? tr('确认告警', 'Acknowledge alert') : kind === 'resolve' ? tr('解决告警', 'Resolve alert') : tr('静默告警', 'Silence alert');
  const cta = kind === 'ack' ? tr('确认', 'Acknowledge') : kind === 'resolve' ? tr('解决', 'Resolve') : tr('静默', 'Silence');

  const submit = async () => {
    if (kind === 'silence' && !note.trim()) {
      setErr(tr('请填写静默原因', 'Please add a silence reason'));
      return;
    }
    if (kind === 'silence' && !until.trim()) {
      setErr(tr('请填写静默时长（例如 30m / 2h / RFC3339 时间戳）', 'Please enter a silence duration (e.g. 30m / 2h / RFC3339 timestamp)'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      if (kind === 'ack') await ackIncident(incident.id, note.trim());
      else if (kind === 'resolve') await resolveIncident(incident.id, note.trim());
      else await silenceIncident(incident.id, until.trim(), note.trim());
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
      title={title}
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
            {submitting ? tr('提交中…', 'Submitting…') : cta}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="rounded-md border border-zinc-800/60 bg-zinc-950/40 px-3 py-2 text-xs text-zinc-400">
          <div className="text-zinc-200">{incident.summary || incident.rule_key}</div>
          <div className="mt-1 text-[11px] text-zinc-500">incident #{incident.id}</div>
        </div>
        {kind === 'silence' && (
          <Label className="block text-xs text-zinc-400">
            <span className="mb-1 block">{tr('静默时长（必填）', 'Silence duration (required)')}</span>
            <Input
              type="text"
              value={until}
              onChange={(e) => setUntil(e.target.value)}
              placeholder={tr('例：30m / 2h / 2026-05-03T12:00:00Z', 'e.g. 30m / 2h / 2026-05-03T12:00:00Z')}
              className="w-full"
            />
            <span className="mt-1 block text-[11px] text-zinc-600">
              {tr('支持持续时间（30m / 2h）、RFC3339 时间戳或 unix 秒。', 'Supports a duration (30m / 2h), RFC3339 timestamp, or unix seconds.')}
            </span>
          </Label>
        )}
        <Label className="block text-xs text-zinc-400">
          <span className="mb-1 block">
            {kind === 'silence'
              ? tr('原因（必填）', 'Reason (required)')
              : tr('备注（可选，进入 incident 时间线）', 'Note (optional; recorded in the incident timeline)')}
          </span>
          <Textarea
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={3}
            placeholder={
              kind === 'ack'
                ? tr('例：oncall 已接单，正在排查', 'e.g. oncall has picked it up, investigating')
                : kind === 'resolve'
                ? tr('例：服务已重启，指标恢复', 'e.g. service restarted, metrics back to normal')
                : tr('例：上线变更窗口，先静默 30 分钟', 'e.g. deploy window, silencing for 30 min')
            }
            className="w-full"
          />
        </Label>
        {err && <div className="text-xs text-red-400">{err}</div>}
      </div>
    </Modal>
  );
}

function useGrafanaDrilldownUrl(incident: Incident | null): string | null {
  const grafanaBaseUrl = useObservability((s) => s.grafanaBaseUrl);
  const grafanaDashboardUid = useObservability((s) => s.grafanaDashboardUid);
  const grafanaOrgId = useObservability((s) => s.grafanaOrgId);
  return useMemo(() => {
    if (!incident) return null;
    if (incident.target_type !== 'edge' || !incident.target_id) return null;
    const dashboardUid = (grafanaDashboardUid || 'ongrid-server-detail').trim();
    if (!dashboardUid) return null;
    const base = (grafanaBaseUrl || '').replace(/\/+$/, '') || `${window.location.origin}/grafana`;
    const params = new URLSearchParams();
    // 30 minutes window centred on fired_at, defaulting to "now-30m..now" when fired_at is missing.
    const firedAt = incident.fired_at ? new Date(incident.fired_at) : null;
    if (firedAt && !Number.isNaN(firedAt.getTime())) {
      params.set('from', String(firedAt.getTime() - 30 * 60 * 1000));
      params.set('to', 'now');
    } else {
      params.set('from', 'now-30m');
      params.set('to', 'now');
    }
    if (grafanaOrgId.trim()) params.set('orgId', grafanaOrgId.trim());
    params.set('var-device_id', String(incident.target_id));
    params.set('kiosk', '');
    return `${base}/d/${dashboardUid}/server-detail?${params.toString()}`;
  }, [incident, grafanaBaseUrl, grafanaDashboardUid, grafanaOrgId]);
}

// useGrafanaExploreUrl is shared by both Loki + Tempo deep-link hooks.
// Delegates to the shared buildExploreUrl (Grafana 11 panes schema —
// the old left= format opened Explore empty under Grafana 11). Time
// range mirrors useGrafanaDrilldownUrl (30 min window centred on
// fired_at).
function buildGrafanaExploreUrl(opts: {
  base: string;
  orgId: string;
  datasource: string;
  expr: string;
  firedAt: string;
  queryShape?: 'logql' | 'traceql';
}): string {
  const fired = opts.firedAt ? new Date(opts.firedAt) : null;
  const valid = fired && !Number.isNaN(fired.getTime());
  const from = valid ? fired!.getTime() - 30 * 60 * 1000 : Date.now() - 30 * 60 * 1000;
  const to = valid ? fired!.getTime() + 30 * 60 * 1000 : Date.now();
  const isTrace = opts.queryShape === 'traceql';
  return buildExploreUrl({
    base: opts.base,
    dsType: isTrace ? 'tempo' : 'loki',
    dsUid: opts.datasource,
    query: isTrace
      ? { query: opts.expr, queryType: 'traceql' }
      : { expr: opts.expr, queryType: 'range' },
    fromMs: from,
    toMs: to,
    orgId: opts.orgId,
  });
}

// useLokiExploreUrl returns a Loki Explore deep-link for log_match /
// log_volume incidents. Returns null when:
//   - incident kind is not a log_*
//   - no device_id label is present (the deep-link would be too broad to
//     be useful, and we don't want a "look at every log line ever" button)
function useLokiExploreUrl(incident: Incident | null): string | null {
  const grafanaBaseUrl = useObservability((s) => s.grafanaBaseUrl);
  const grafanaOrgId = useObservability((s) => s.grafanaOrgId);
  return useMemo(() => {
    if (!incident) return null;
    if (incident.rule_key && !isLogIncident(incident)) return null;
    const labels = incident.labels ?? {};
    const edgeId = labels.device_id ?? labels.edgeId;
    if (!edgeId) return null;
    const base =
      (grafanaBaseUrl || '').replace(/\/+$/, '') || `${window.location.origin}/grafana`;
    // Build a minimal LogQL expression. Prefer the rule's stream_selector
    // if it leaks through labels; otherwise scope to device_id only.
    let expr = `{device_id="${edgeId}"}`;
    const lineFilter = labels.line_filter || labels.regex;
    if (lineFilter) expr += ` |~ "${lineFilter.replace(/"/g, '\\"')}"`;
    return buildGrafanaExploreUrl({
      base,
      orgId: grafanaOrgId,
      datasource: 'ongrid-loki',
      expr,
      firedAt: incident.fired_at,
      queryShape: 'logql',
    });
  }, [incident, grafanaBaseUrl, grafanaOrgId]);
}

function useTempoExploreUrl(incident: Incident | null): string | null {
  const grafanaBaseUrl = useObservability((s) => s.grafanaBaseUrl);
  const grafanaOrgId = useObservability((s) => s.grafanaOrgId);
  return useMemo(() => {
    if (!incident) return null;
    if (incident.rule_key && !isTraceIncident(incident)) return null;
    const labels = incident.labels ?? {};
    const service = labels.service || labels['service.name'];
    // Need at least device_id or service to build a useful TraceQL query;
    // otherwise we'd dump every span which is meaningless in a tempo UI.
    if (!service && !labels.device_id) return null;
    const base =
      (grafanaBaseUrl || '').replace(/\/+$/, '') || `${window.location.origin}/grafana`;
    const parts: string[] = [];
    if (service) parts.push(`resource.service.name="${service}"`);
    if (labels.operation) parts.push(`name="${labels.operation}"`);
    if (labels.device_id) parts.push(`resource.device_id="${labels.device_id}"`);
    const expr = `{${parts.join(' && ') || 'true'}}`;
    return buildGrafanaExploreUrl({
      base,
      orgId: grafanaOrgId,
      datasource: 'ongrid-tempo',
      expr,
      firedAt: incident.fired_at,
      queryShape: 'traceql',
    });
  }, [incident, grafanaBaseUrl, grafanaOrgId]);
}

// isLogIncident / isTraceIncident — used to decide whether to render the
// jump-to-{logs,traces} button. We key off rule_key suffix because the
// kind isn't on the wire shape (Incident only carries rule_key /
// rule_name / labels). Backend tagging via labels.kind would be cleaner
// but isn't available yet — kind may land later.
function isLogIncident(incident: Incident): boolean {
  const k = (incident.labels?.kind ?? '').toLowerCase();
  if (k === 'log_match' || k === 'log_volume') return true;
  return /^log_(match|volume)/.test(incident.rule_key ?? '');
}

function isTraceIncident(incident: Incident): boolean {
  const k = (incident.labels?.kind ?? '').toLowerCase();
  if (k === 'trace_latency' || k === 'trace_error_rate') return true;
  return /^trace_(latency|error_rate)/.test(incident.rule_key ?? '');
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
      {status === 'resolved' ? (
        <CheckCircle2 size={11} />
      ) : status === 'silenced' ? (
        <Pause size={11} />
      ) : (
        <span className="h-1.5 w-1.5 rounded-full bg-current" />
      )}
      {status}
    </span>
  );
}

function EventTypeBadge({ type }: { type: string }) {
  const styles = eventTypeStyles(type);
  const label = humanizeEventType(type);
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
        styles
      )}
    >
      {type === 'resolved' ? (
        <CheckCircle2 size={10} />
      ) : type === 'acknowledged' ? (
        <Bell size={10} />
      ) : type === 'silenced' || type === 'inhibited' || type === 'repeat_suppressed' ? (
        <Pause size={10} />
      ) : type === 'notification_sent' ? (
        <Bell size={10} />
      ) : type === 'notification_failed' ? (
        <X size={10} />
      ) : type === 'ai_initial_diagnosis' ? (
        <Sparkles size={10} />
      ) : (
        <Siren size={10} />
      )}
      {label}
    </span>
  );
}

function eventTypeStyles(type: string): string {
  switch (type) {
    case 'firing':
    case 'reopened':
    case 'notification_failed':
      return 'bg-red-500/10 text-red-300 ring-red-500/30';
    case 'acknowledged':
      return 'bg-blue-500/10 text-blue-300 ring-blue-500/30';
    case 'resolved':
      return 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30';
    case 'notification_sent':
      return 'bg-indigo-500/10 text-indigo-300 ring-indigo-500/30';
    case 'ai_initial_diagnosis':
      return 'bg-indigo-500/15 text-indigo-200 ring-indigo-500/40';
    case 'silenced':
    case 'inhibited':
    case 'repeat_suppressed':
    case 'note':
    default:
      return 'bg-zinc-800 text-zinc-300 ring-zinc-700';
  }
}

function humanizeEventType(type: string): string {
  switch (type) {
    case 'firing':
      return 'firing';
    case 'reopened':
      return 'reopened';
    case 'acknowledged':
      return 'acknowledged';
    case 'resolved':
      return 'resolved';
    case 'silenced':
      return 'silenced';
    case 'inhibited':
      return 'inhibited';
    case 'repeat_suppressed':
      return 'repeat suppressed';
    case 'notification_sent':
      return 'notification sent';
    case 'notification_failed':
      return 'notification failed';
    case 'note':
      return 'note';
    case 'ai_initial_diagnosis':
      return trInline('AI 初查', 'AI initial diagnosis');
    default:
      return type;
  }
}

// AgentTimelinePanel surfaces every chat session linked to this
// incident — i.e. each "深入诊断" the operator opened. For each session
// we render the tool-call sequence and a preview of the final assistant
// message so the operator can scan multiple investigation threads
// without opening each chat.
//
// Implementation: one /v1/chat/sessions?related_incident_id={id} query
// returns the session list; per-session messages are fetched on first
// expand (lazy) to keep the initial paint cheap when there are 3+
// sessions.
function AgentTimelinePanel({ incidentId }: { incidentId: number }) {
  const { tr } = useI18n();
  const [sessions, setSessions] = useState<ChatSession[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    listSessions({ related_incident_id: incidentId })
      .then((r) => {
        if (cancelled) return;
        setSessions(r.items ?? []);
        setErr(null);
      })
      .catch((e) => {
        if (cancelled) return;
        setErr(e instanceof ApiError ? e.message : (e as Error).message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [incidentId]);

  if (loading) {
    return (
      <section className="rounded-lg border border-zinc-800 bg-zinc-900/40 px-4 py-3 text-xs text-zinc-500">
        {tr('AI 诊断时间线加载中…', 'Loading AI diagnosis timeline…')}
      </section>
    );
  }
  if (err) {
    return (
      <section className="rounded-lg border border-red-500/30 bg-red-500/5 px-4 py-3 text-xs text-red-300">
        {tr('AI 诊断时间线加载失败：', 'AI diagnosis timeline failed: ')}{err}
      </section>
    );
  }
  if (sessions.length === 0) {
    // Quiet empty state — the AI 初查 panel above already covers the "no
    // human deep-dive yet" case; we don't want to clutter the page when
    // nobody clicked 深入诊断.
    return null;
  }
  return (
    <section className="rounded-lg border border-indigo-500/30 bg-indigo-500/5">
      <header className="flex items-center justify-between border-b border-indigo-500/20 px-4 py-2.5">
        <div className="flex items-center gap-2">
          <span className="inline-flex h-6 w-6 items-center justify-center rounded-md bg-indigo-500/20 text-indigo-300 ring-1 ring-inset ring-indigo-500/40">
            <Bot size={13} />
          </span>
          <span className="text-sm font-medium text-indigo-200">{tr('AI 诊断时间线', 'AI diagnosis timeline')}</span>
          <span className="text-[11px] text-indigo-300/70">
            {tr(`${sessions.length} 次深入诊断会话`, `${sessions.length} deep-dive session(s)`)}
          </span>
        </div>
      </header>
      <div className="space-y-3 p-4">
        {sessions.map((s) => (
          <AgentSessionCard key={s.id} session={s} />
        ))}
      </div>
    </section>
  );
}

// AgentSessionCard — one row per chat session. Lazy-loads messages on
// mount (cheap; sessions per incident usually ≤ 3) and renders the
// tool_call sequence + last assistant content preview.
function AgentSessionCard({ session }: { session: ChatSession }) {
  const { tr } = useI18n();
  const [messages, setMessages] = useState<ChatMessage[] | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    getMessages(session.id)
      .then((r) => {
        if (!cancelled) setMessages(r.items ?? []);
      })
      .catch(() => {
        if (!cancelled) setMessages([]);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [session.id]);

  // Flatten tool_calls across all assistant messages in the session in
  // chronological order. Each ToolCallSummary already has name/status/
  // duration/error.
  const toolCalls = useMemo(() => {
    if (!messages) return [];
    const out: ToolCallSummary[] = [];
    for (const m of messages) {
      if (m.tool_calls && m.tool_calls.length > 0) {
        out.push(...m.tool_calls);
      }
    }
    return out;
  }, [messages]);

  // Last non-empty assistant message text — what the agent's "answer"
  // looks like at the end of this thread.
  const lastAssistantText = useMemo(() => {
    if (!messages) return '';
    for (let i = messages.length - 1; i >= 0; i--) {
      const m = messages[i];
      if (m.role === 'assistant' && m.content && m.content.trim() !== '') {
        return m.content.trim();
      }
    }
    return '';
  }, [messages]);

  return (
    <article className="rounded-md border border-zinc-800/60 bg-zinc-950/40 p-3.5">
      <header className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="truncate text-[13px] font-medium text-zinc-100">{session.title}</div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            {session.created_at && relativeTime(session.created_at)}
            {' · '}
            <span className="font-mono text-zinc-600">{session.id.slice(0, 8)}</span>
          </div>
        </div>
        <Link
          to={`/chat/${encodeURIComponent(session.id)}`}
          className="inline-flex shrink-0 items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-200 hover:bg-zinc-800"
        >
          {tr('继续会话', 'Continue session')}
        </Link>
      </header>

      {loading ? (
        <div className="mt-3 text-[11px] text-zinc-500">{tr('载入消息…', 'Loading messages…')}</div>
      ) : (
        <>
          {toolCalls.length > 0 && (
            <div className="mt-3">
              <div className="mb-1.5 text-[10px] font-medium uppercase tracking-wider text-zinc-500">
                Tool calls ({toolCalls.length})
              </div>
              <ul className="flex flex-wrap gap-1.5">
                {toolCalls.map((tc, i) => (
                  <li key={tc.id ?? `${tc.name}-${i}`}>
                    <ToolCallChip tc={tc} />
                  </li>
                ))}
              </ul>
            </div>
          )}
          {lastAssistantText !== '' && (
            <div className="mt-3 rounded border border-zinc-800/60 bg-zinc-900/40 px-2.5 py-2 text-[12px] leading-relaxed text-zinc-200">
              <div className="line-clamp-3 whitespace-pre-wrap break-words">
                {lastAssistantText}
              </div>
            </div>
          )}
        </>
      )}
    </article>
  );
}

function ToolCallChip({ tc }: { tc: ToolCallSummary }) {
  const color =
    tc.status === 'success'
      ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-200'
      : tc.status === 'error' || tc.status === 'timeout'
      ? 'border-red-500/30 bg-red-500/10 text-red-200'
      : 'border-zinc-700 bg-zinc-800 text-zinc-300';
  return (
    <Hint content={tc.error || `${tc.status}${tc.duration_ms != null ? ` · ${tc.duration_ms}ms` : ''}`}><span

      className={cn(
        'inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 font-mono text-[10px]',
        color,
      )}
    >
      {tc.name}
      {tc.duration_ms != null && (
        <span className="text-[9px] text-zinc-500">{tc.duration_ms}ms</span>
      )}
    </span></Hint>
  );
}
