import { useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  ArrowUpRight,
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  ListTree,
  ChartNoAxesGantt,
  Search,
  ChevronsDownUp,
  ChevronsUpDown,
} from 'lucide-react';
import type { AgentTrace } from '@/api/agent-observability';
import { Button, Card, Chip, EmptyState, Input, Select } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';
import {
  CodeValue,
  ContentView,
  CopyButton,
  KindIcon,
  RunStatus,
  InspectorSection,
  AttributeTable,
} from './AgentParts';
import {
  duration,
  inspectionSteps,
  runTitle,
  sdkLabels,
  stepRows,
  tokens,
} from './model';
import { issueLabel } from './diagnostics';

type Props = {
  trace: AgentTrace;
  entrySpanID?: string;
  selectedSpanID?: string;
  onSelectSpan(id: string): void;
  onSelectRun(id: string): void;
};

export function AgentTraceDetail({
  trace,
  entrySpanID,
  selectedSpanID,
  onSelectSpan,
  onSelectRun,
}: Props) {
  const { tr } = useI18n();
  const [collapsed, setCollapsed] = useState(new Set<string>());
  const [search, setSearch] = useState('');
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [includeInternal, setIncludeInternal] = useState(false);
  const [visibleCount, setVisibleCount] = useState(300);
  const [closedSections, setClosedSections] = useState(new Set<string>());
  const panelRef = useRef<HTMLElement>(null);
  const sectionRefs = useRef<Record<string, HTMLDivElement | null>>({});
  const [view, setView] = useState<'tree' | 'waterfall'>('tree');
  const run =
    trace.runs.find((item) => item.entry_span_id === entrySpanID) ??
    trace.runs[0];
  const selected =
    trace.steps.find((step) => step.span_id === selectedSpanID) ??
    trace.steps.find((step) => step.span_id === run?.entry_span_id) ??
    trace.steps[0];
  useEffect(() => {
    setClosedSections(new Set());
    panelRef.current?.scrollTo?.({ top: 0 });
  }, [selected?.span_id]);
  const inspectedSteps = useMemo(
    () =>
      inspectionSteps(
        trace.steps,
        includeInternal || !!search || errorsOnly,
        new Set([
          ...trace.runs.map((item) => item.entry_span_id),
          ...(selectedSpanID ? [selectedSpanID] : []),
        ]),
      ),
    [
      trace.steps,
      trace.runs,
      includeInternal,
      search,
      errorsOnly,
      selectedSpanID,
    ],
  );
  const rows = useMemo(
    () => stepRows(inspectedSteps, collapsed, search, errorsOnly),
    [inspectedSteps, collapsed, search, errorsOnly],
  );
  const times = trace.steps
    .map((step) => Date.parse(step.start))
    .filter(Number.isFinite);
  const start = times.length ? Math.min(...times) : Date.now();
  const end = trace.steps.reduce(
    (latest, step) =>
      Math.max(latest, Date.parse(step.start) + step.duration_ms),
    start,
  );
  const total = Math.max(1, end - start);
  const selectedIndex = rows.findIndex(
    ({ step }) => step.span_id === selected?.span_id,
  );
  const path = useMemo(() => {
    const byID = new Map(trace.steps.map((step) => [step.span_id, step]));
    const parents = [];
    const seen = new Set<string>();
    let parent = selected && byID.get(selected.parent_span_id);
    while (parent && !seen.has(parent.span_id)) {
      seen.add(parent.span_id);
      parents.unshift(parent);
      parent = byID.get(parent.parent_span_id);
    }
    return parents;
  }, [trace.steps, selected]);
  if (!selected)
    return (
      <Card>
        <EmptyState
          icon={ListTree}
          title={tr('链路尚无步骤', 'No steps in this trace')}
          hint={tr(
            '数据可能仍在写入，请稍后刷新。',
            'Telemetry may still be arriving. Refresh shortly.',
          )}
        />
      </Card>
    );
  const window = {
    start: new Date(Math.max(0, start - 300000)).toISOString(),
    end: new Date(Math.max(end + 300000, start + 600000)).toISOString(),
  };
  const correlation = new URLSearchParams({
    ...window,
    trace_id: trace.trace_id,
  });
  const service = new URLSearchParams({ ...window, ...selected.identity });
  const device = String(selected.resource.device_id ?? '');
  const usage = selected.usage;
  const chatSessionID = selected.chat_session_id || run?.chat_session_id;
  const sections = [
    { id: 'input', label: tr('输入', 'Input') },
    { id: 'output', label: tr('输出', 'Output') },
    { id: 'metadata', label: tr('属性', 'Attributes') },
    { id: 'events', label: tr('事件', 'Events') },
    { id: 'links', label: tr('关联', 'Links') },
  ];
  const toggleSection = (id: string) =>
    setClosedSections((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const jumpToSection = (id: string) => {
    setClosedSections((current) => {
      const next = new Set(current);
      next.delete(id);
      return next;
    });
    sectionRefs.current[id]?.scrollIntoView({ block: 'start' });
    sectionRefs.current[id]
      ?.querySelector<HTMLButtonElement>('button[aria-expanded]')
      ?.focus({ preventScroll: true });
  };
  return (
    <div className="agent-detail-layout" data-testid="agent-trace-detail">
      <div className="agent-run-heading">
        <div className="flex min-w-0 items-start gap-3">
          <span className="agent-run-mark">
            <KindIcon
              kind={
                trace.steps.find((step) => step.span_id === run?.entry_span_id)
                  ?.kind || 'workflow'
              }
              size={16}
            />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-3">
              <h2 className="break-words text-base font-semibold text-text">
                {(run && runTitle(run)) || selected.name}
              </h2>
              <RunStatus
                status={run?.status || selected.status}
                errors={run?.error_count}
              />
            </div>
            <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-text-muted">
              {run && runTitle(run) !== run.name && (
                <>
                  <span>{run.name}</span>
                  <span>·</span>
                </>
              )}
              <span>
                {run?.identity.service_name || selected.identity.service_name}
              </span>
              <span>·</span>
              <span>
                {run?.identity.environment ||
                  tr('环境未设置', 'Environment unset')}
              </span>
              <span>·</span>
              <span className="font-mono" title={trace.trace_id}>
                {trace.trace_id.slice(0, 12)}…
              </span>
              <CopyButton
                value={trace.trace_id}
                label={tr('复制 Trace ID', 'Copy Trace ID')}
              />
            </div>
          </div>
        </div>
        <div className="agent-run-facts">
          <span>
            {tr('耗时', 'Latency')}{' '}
            <strong className="ml-1 font-mono font-medium text-text">
              {duration(run?.duration_ms ?? total)}
            </strong>
          </span>
          <span>
            {tr('步骤', 'Steps')}{' '}
            <strong className="ml-1 font-mono font-medium text-text">
              {run?.step_count ?? trace.steps.length}
            </strong>
          </span>
          {([
            [tr('输入 Token', 'Input tokens'), run?.usage.input_tokens],
            [tr('输出 Token', 'Output tokens'), run?.usage.output_tokens],
            [tr('总 Token', 'Total tokens'), run?.usage.total_tokens],
          ] as const).map(([label, value]) => (
            <span key={label}>
              {label}{' '}
              <strong className="ml-1 font-mono font-medium text-text">
                {tokens(value)}
              </strong>
            </span>
          ))}
          {run?.usage.conflict && (
            <Chip tone="warning">{tr('用量待核对', 'Usage conflict')}</Chip>
          )}
          <span>
            {run?.sdks.map((sdk) => sdkLabels[sdk] || sdk).join(' · ')}
          </span>
          {chatSessionID && (
            <Link
              className="ml-auto inline-flex items-center gap-1 text-text-muted hover:text-text"
              to={`/chat/${encodeURIComponent(chatSessionID)}`}
            >
              {tr('打开原对话', 'Open conversation')}
              <ArrowUpRight size={13} />
            </Link>
          )}
          <Link
            className={cn(
              !chatSessionID && 'ml-auto',
              'inline-flex items-center gap-1 text-text-muted hover:text-text',
            )}
            to={`/traces/${trace.trace_id}?${new URLSearchParams(window)}`}
          >
            {tr('原始链路', 'Original trace')}
            <ArrowUpRight size={13} />
          </Link>
        </div>
        {trace.runs.length > 1 && (
          <div className="mt-3">
            <Select
              label={tr('运行入口', 'Run entry')}
              value={run?.entry_span_id || ''}
              onValueChange={onSelectRun}
              options={trace.runs.map((item) => ({
                value: item.entry_span_id,
                label: `${item.identity.service_name} / ${runTitle(item)}`,
              }))}
            />
          </div>
        )}
      </div>
      <Card className="agent-trace-workspace !p-0" data-view={view}>
        <aside className="agent-tree-panel">
          <div className="flex items-center justify-between gap-2 border-b border-border px-4 py-3">
            <h3 className="flex items-center gap-2 text-sm font-medium text-text">
              <ListTree size={15} />
              {tr('执行树', 'Execution tree')}
            </h3>
            <div
              className="agent-view-switch"
              aria-label={tr('执行视图', 'Execution view')}
            >
              <Button
                size="sm"
                variant={view === 'tree' ? 'outline' : 'subtle'}
                aria-pressed={view === 'tree'}
                aria-label={tr('树状视图', 'Tree view')}
                title={tr('树状视图', 'Tree view')}
                onClick={() => setView('tree')}
              >
                <ListTree size={14} />
              </Button>
              <Button
                size="sm"
                variant={view === 'waterfall' ? 'outline' : 'subtle'}
                aria-pressed={view === 'waterfall'}
                aria-label={tr('瀑布视图', 'Waterfall view')}
                title={tr('瀑布视图', 'Waterfall view')}
                onClick={() => setView('waterfall')}
              >
                <ChartNoAxesGantt size={14} />
              </Button>
            </div>
          </div>
          <div className="space-y-2 px-3 py-3">
            <div className="relative">
              <Search
                size={13}
                className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-faint"
              />
              <Input
                type="search"
                aria-label={tr('搜索步骤', 'Search steps')}
                placeholder={tr('搜索步骤、模型…', 'Search steps, models…')}
                value={search}
                onChange={(event) => {
                  setSearch(event.target.value);
                  setVisibleCount(300);
                }}
                className="w-full pl-8"
              />
            </div>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <Button
                size="sm"
                variant={errorsOnly ? 'outline' : 'subtle'}
                aria-pressed={errorsOnly}
                onClick={() => setErrorsOnly(!errorsOnly)}
              >
                {tr('仅失败步骤', 'Failed steps')}
              </Button>
              {trace.steps.some((step) => step.kind === 'span') && (
                <Button
                  size="sm"
                  variant={includeInternal ? 'outline' : 'subtle'}
                  aria-pressed={includeInternal}
                  onClick={() => {
                    setIncludeInternal(!includeInternal);
                    setVisibleCount(300);
                  }}
                >
                  {tr('内部步骤', 'Internal steps')}
                </Button>
              )}
              <Button
                size="sm"
                variant="subtle"
                onClick={() =>
                  setCollapsed(
                    collapsed.size
                      ? new Set()
                      : new Set(
                          inspectedSteps
                            .map((step) => step.parent_span_id)
                            .filter(Boolean),
                        ),
                  )
                }
              >
                {collapsed.size
                  ? tr('全部展开', 'Expand all')
                  : tr('全部收起', 'Collapse all')}
              </Button>
            </div>
          </div>
          {view === 'waterfall' && (
            <div className="agent-waterfall-ruler">
              <span>{tr('步骤', 'Step')}</span>
              <span className="flex justify-between">
                <span>0</span>
                <span>{duration(total)}</span>
              </span>
            </div>
          )}
          <div
            className="agent-tree-scroll"
            aria-label={tr('链路步骤', 'Trace steps')}
          >
            {rows.slice(0, visibleCount).map(({ step, depth, hasChildren }) => {
              const left = Math.max(
                0,
                Math.min(100, ((Date.parse(step.start) - start) / total) * 100),
              );
              const width = Math.max(
                0.8,
                Math.min(100 - left, (step.duration_ms / total) * 100),
              );
              return (
                <div
                  className={cn(
                    'agent-tree-row',
                    selected.span_id === step.span_id && 'is-selected',
                  )}
                  key={step.span_id}
                  style={{
                    paddingLeft: `${view === 'tree' ? 10 + Math.min(depth, 8) * 14 : 10}px`,
                  }}
                >
                  {hasChildren ? (
                    <Button
                      size="sm"
                      variant="subtle"
                      className="agent-tree-toggle"
                      aria-label={
                        collapsed.has(step.span_id)
                          ? tr(`展开 ${step.name}`, `Expand ${step.name}`)
                          : tr(`收起 ${step.name}`, `Collapse ${step.name}`)
                      }
                      aria-expanded={!collapsed.has(step.span_id)}
                      onClick={() =>
                        setCollapsed((current) => {
                          const next = new Set(current);
                          if (next.has(step.span_id)) next.delete(step.span_id);
                          else next.add(step.span_id);
                          return next;
                        })
                      }
                    >
                      {collapsed.has(step.span_id) ? (
                        <ChevronRight size={12} />
                      ) : (
                        <ChevronDown size={12} />
                      )}
                    </Button>
                  ) : (
                    <span className="agent-tree-toggle" />
                  )}
                  <button
                    type="button"
                    className="agent-step-button"
                    aria-pressed={selected.span_id === step.span_id}
                    onClick={() => onSelectSpan(step.span_id)}
                    title={step.name}
                  >
                    <span
                      className="agent-step-label"
                      style={
                        view === 'waterfall'
                          ? { paddingLeft: Math.min(depth, 8) * 10 }
                          : undefined
                      }
                    >
                      <KindIcon kind={step.kind} />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate">{step.name}</span>
                        {view === 'tree' &&
                          (step.model || step.usage.total_tokens != null) && (
                            <span className="agent-step-caption">
                              {step.model}
                              {step.model && step.usage.total_tokens != null
                                ? ' · '
                                : ''}
                              {step.usage.total_tokens != null
                                ? `${tokens(step.usage.total_tokens)} tokens`
                                : ''}
                            </span>
                          )}
                      </span>
                      {step.status === 'error' && (
                        <span
                          className="h-1.5 w-1.5 shrink-0 rounded-full bg-red-500"
                          aria-label={tr('失败', 'Error')}
                        />
                      )}
                    </span>
                    <span className="agent-step-timing">
                      {view === 'waterfall' && (
                        <span className="agent-timing-track">
                          <span
                            style={{ left: `${left}%`, width: `${width}%` }}
                            data-error={step.status === 'error'}
                            data-kind={step.kind}
                          />
                        </span>
                      )}
                      <span className="font-mono text-xs text-text-muted">
                        {duration(step.duration_ms)}
                      </span>
                    </span>
                  </button>
                </div>
              );
            })}
            {rows.length === 0 && (
              <p className="px-4 py-8 text-center text-xs text-text-muted">
                {tr('没有匹配的步骤', 'No matching steps')}
              </p>
            )}
            {rows.length > visibleCount && (
              <Button
                size="sm"
                className="m-3"
                onClick={() => setVisibleCount(visibleCount + 300)}
              >
                {tr('显示更多步骤', 'Show more steps')} (
                {rows.length - visibleCount})
              </Button>
            )}
          </div>
          <div className="flex justify-between border-t border-border px-4 py-3 font-mono text-xs text-text-faint">
            <span>
              {inspectedSteps.length < trace.steps.length
                ? tr(
                    `${rows.length} / ${trace.steps.length} 个步骤`,
                    `${rows.length} / ${trace.steps.length} steps`,
                  )
                : tr(`${rows.length} 个步骤`, `${rows.length} steps`)}
            </span>
            <span>{duration(total)}</span>
          </div>
        </aside>
        <section
          className="agent-step-panel"
          aria-label={tr('步骤详情', 'Step details')}
          ref={panelRef}
        >
          <div className="agent-step-header">
            <div className="agent-step-navigation">
              <nav
                aria-label={tr('父级步骤', 'Parent steps')}
                className="agent-step-path"
              >
                {path.length ? (
                  path.map((parent) => (
                    <span key={parent.span_id}>
                      <button
                        type="button"
                        onClick={() => onSelectSpan(parent.span_id)}
                        title={parent.name}
                      >
                        {parent.name}
                      </button>
                      <ChevronRight size={11} />
                    </span>
                  ))
                ) : (
                  <span>{tr('运行入口', 'Run entry')}</span>
                )}
              </nav>
              <div className="flex shrink-0 items-center gap-1">
                <span className="mr-1 text-xs tabular-nums text-text-faint">
                  {selectedIndex >= 0
                    ? `${selectedIndex + 1} / ${rows.length}`
                    : '—'}
                </span>
                <Button
                  size="sm"
                  variant="subtle"
                  aria-label={tr('上一步', 'Previous step')}
                  title={tr('上一步', 'Previous step')}
                  disabled={selectedIndex <= 0}
                  onClick={() =>
                    onSelectSpan(rows[selectedIndex - 1].step.span_id)
                  }
                >
                  <ArrowUp size={13} />
                </Button>
                <Button
                  size="sm"
                  variant="subtle"
                  aria-label={tr('下一步', 'Next step')}
                  title={tr('下一步', 'Next step')}
                  disabled={
                    selectedIndex < 0 || selectedIndex >= rows.length - 1
                  }
                  onClick={() =>
                    onSelectSpan(rows[selectedIndex + 1].step.span_id)
                  }
                >
                  <ArrowDown size={13} />
                </Button>
              </div>
            </div>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="flex min-w-0 items-center gap-2 text-sm font-semibold text-text">
                <KindIcon kind={selected.kind} />
                <span className="break-all">{selected.name}</span>
              </h3>
              <RunStatus status={selected.status} />
            </div>
            <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-text-muted">
              <span>{selected.kind}</span>
              {selected.model && (
                <span className="break-all text-text">{selected.model}</span>
              )}
              <span className="font-mono">
                {duration(selected.duration_ms)}
              </span>
              <span>{sdkLabels[selected.sdk] || selected.sdk}</span>
            </div>
            {(selected.kind === 'llm' || selected.kind === 'embedding') && (
              <dl className="agent-step-usage">
                <div>
                  <dt>{tr('输入 Token', 'Input tokens')}</dt>
                  <dd>{tokens(usage.input_tokens)}</dd>
                </div>
                <div>
                  <dt>{tr('输出 Token', 'Output tokens')}</dt>
                  <dd>{tokens(usage.output_tokens)}</dd>
                </div>
                <div>
                  <dt>{tr('总 Token', 'Total tokens')}</dt>
                  <dd>{tokens(usage.total_tokens)}</dd>
                </div>
                <div>
                  <dt>{tr('缓存命中（计入输入）', 'Cache read (part of input)')}</dt>
                  <dd>{tokens(usage.cached_tokens)}</dd>
                </div>
                <div>
                  <dt>{tr('SDK 上报成本', 'SDK reported cost')}</dt>
                  <dd>
                    {usage.reported_cost == null
                      ? '—'
                      : `${usage.reported_cost.toLocaleString(undefined, { maximumFractionDigits: 8 })} ${usage.currency || ''}`}
                  </dd>
                </div>
              </dl>
            )}
            {selected.issues.length > 0 && (
              <div className="flex flex-wrap gap-1.5">
                {selected.issues.map((issue) => (
                  <Chip key={issue} tone="warning">
                    {issueLabel(issue, tr)}
                  </Chip>
                ))}
              </div>
            )}
          </div>
          <nav
            className="agent-inspector-nav"
            aria-label={tr('详情导航', 'Detail navigation')}
          >
            <div className="agent-inspector-nav-items">
              {sections.map(({ id, label }) => (
                <Button
                  key={id}
                  size="sm"
                  variant="subtle"
                  aria-label={tr(`定位到${label}`, `Go to ${label}`)}
                  onClick={() => jumpToSection(id)}
                >
                  {label}
                  {id === 'events' && selected.events.length > 0 && (
                    <span className="agent-tab-count">
                      {selected.events.length}
                    </span>
                  )}
                  {id === 'links' && selected.links.length > 0 && (
                    <span className="agent-tab-count">
                      {selected.links.length}
                    </span>
                  )}
                </Button>
              ))}
            </div>
            <Button
              size="sm"
              variant="subtle"
              aria-label={
                closedSections.size === sections.length
                  ? tr('展开所有详情', 'Expand all sections')
                  : tr('收起所有详情', 'Collapse all sections')
              }
              title={
                closedSections.size === sections.length
                  ? tr('展开所有详情', 'Expand all sections')
                  : tr('收起所有详情', 'Collapse all sections')
              }
              onClick={() =>
                setClosedSections(
                  closedSections.size === sections.length
                    ? new Set()
                    : new Set(sections.map(({ id }) => id)),
                )
              }
            >
              {closedSections.size === sections.length ? (
                <ChevronsUpDown size={14} />
              ) : (
                <ChevronsDownUp size={14} />
              )}
            </Button>
          </nav>
          <div className="agent-inspector-sections" key={selected.span_id}>
            <div
              className="agent-inspector-section"
              data-section="input"
              ref={(node) => {
                sectionRefs.current.input = node;
              }}
            >
              <ContentView
                title={tr('输入', 'Input')}
                content={selected.input}
                expanded={!closedSections.has('input')}
                onExpandedChange={() => toggleSection('input')}
              />
            </div>
            <div
              className="agent-inspector-section"
              data-section="output"
              ref={(node) => {
                sectionRefs.current.output = node;
              }}
            >
              <ContentView
                title={tr('输出', 'Output')}
                content={selected.output}
                expanded={!closedSections.has('output')}
                onExpandedChange={() => toggleSection('output')}
              />
            </div>
            <div
              className="agent-inspector-section"
              data-section="metadata"
              ref={(node) => {
                sectionRefs.current.metadata = node;
              }}
            >
              <InspectorSection
                title={tr('属性', 'Attributes')}
                open={!closedSections.has('metadata')}
                onToggle={() => toggleSection('metadata')}
              >
                <div className="mb-4 flex flex-wrap gap-3 text-xs">
                  <Link
                    className="inline-flex items-center gap-1 text-indigo-500 hover:underline"
                    to={`/logs?${correlation}`}
                  >
                    {tr('关联日志', 'Related logs')}
                    <ArrowUpRight size={13} />
                  </Link>
                  {selected.identity.service_name && (
                    <Link
                      className="inline-flex items-center gap-1 text-indigo-500 hover:underline"
                      to={`/apm/service?${service}`}
                    >
                      {tr('服务详情', 'Service details')}
                      <ArrowUpRight size={13} />
                    </Link>
                  )}
                  {/^\d+$/.test(device) && (
                    <Link
                      className="inline-flex items-center gap-1 text-indigo-500 hover:underline"
                      to={`/devices/${device}`}
                    >
                      {tr('设备', 'Device')} #{device}
                      <ArrowUpRight size={13} />
                    </Link>
                  )}
                </div>
                <details className="agent-attribute-group" open>
                  <summary>{tr('采集来源', 'Instrumentation scope')}</summary>
                  <AttributeTable values={{ ...selected.scope }} />
                </details>
                <details className="agent-attribute-group" open>
                  <summary>{tr('步骤属性', 'Span attributes')}</summary>
                  <AttributeTable values={selected.attributes} />
                </details>
                <details className="agent-attribute-group" open>
                  <summary>{tr('资源属性', 'Resource attributes')}</summary>
                  <AttributeTable values={selected.resource} />
                </details>
                <details className="agent-attribute-group">
                  <summary>{tr('原始 Span', 'Raw span')}</summary>
                  <div className="mt-3">
                    <CodeValue value={selected.raw} />
                  </div>
                </details>
              </InspectorSection>
            </div>
            <div
              className="agent-inspector-section"
              data-section="events"
              ref={(node) => {
                sectionRefs.current.events = node;
              }}
            >
              <InspectorSection
                title={tr('事件', 'Events')}
                count={selected.events.length}
                open={!closedSections.has('events')}
                onToggle={() => toggleSection('events')}
              >
                {selected.events.length ? (
                  selected.events.map((event, index) => (
                    <div key={index}>
                      <h4 className="mb-2 text-sm font-medium text-text">
                        {event.name}
                      </h4>
                      <AttributeTable values={event.attributes} />
                    </div>
                  ))
                ) : (
                  <p className="py-2 text-xs text-text-muted">
                    {tr('没有上报事件', 'No events reported')}
                  </p>
                )}
              </InspectorSection>
            </div>
            <div
              className="agent-inspector-section"
              data-section="links"
              ref={(node) => {
                sectionRefs.current.links = node;
              }}
            >
              <InspectorSection
                title={tr('关联', 'Links')}
                count={selected.links.length}
                open={!closedSections.has('links')}
                onToggle={() => toggleSection('links')}
              >
                {selected.links.length ? (
                  selected.links.map((link, index) => (
                    <div key={index} className="space-y-2">
                      <Link
                        className="break-all font-mono text-xs text-indigo-500 hover:underline"
                        to={`/agent-observability?${new URLSearchParams({ trace_id: link.trace_id, step_id: link.span_id })}`}
                      >
                        {link.trace_id} / {link.span_id}
                        <ArrowUpRight size={13} className="ml-1 inline" />
                      </Link>
                      <AttributeTable values={link.attributes} />
                    </div>
                  ))
                ) : (
                  <p className="py-2 text-xs text-text-muted">
                    {tr('没有上报 Span Links', 'No span links reported')}
                  </p>
                )}
              </InspectorSection>
            </div>
          </div>
        </section>
      </Card>
    </div>
  );
}
