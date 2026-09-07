import { useCallback, useEffect, useRef, useState, type MouseEvent } from 'react';
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { ArrowDown, ArrowRight, ArrowUp, ArrowUpRight, Clock, RefreshCw } from 'lucide-react';
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { localDateTime } from '@/lib/telemetryContext';
import {
  queryApm,
  serviceParams,
  traceLink,
  type ApmList,
  type ApmOverview,
  type ApmDependencies,
  type ApmDiagnostics,
  type ApmRuntime,
} from '@/api/apm';
import { Button, Card, EmptyState, PageHeader, PaginationFooter } from '@/components/ui';
import { Dependencies } from '@/components/apm/Dependencies';
import { SearchInput } from '@/components/apm/SearchInput';
import { ServiceSwitcher } from '@/components/apm/ServiceSwitcher';
import { Onboarding } from '@/components/apm/Onboarding';
import { chartTooltipStyle, chartTooltipLabelStyle } from '@/lib/chartTheme';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';

const input = 'h-9 rounded-md border border-zinc-800 bg-zinc-950 px-2 text-xs text-zinc-100';
const periods = [
  ['15m', 900000, '最近 15 分钟', 'Last 15 minutes'],
  ['1h', 3600000, '最近 1 小时', 'Last hour'],
  ['6h', 21600000, '最近 6 小时', 'Last 6 hours'],
  ['24h', 86400000, '最近 24 小时', 'Last 24 hours'],
] as const;
const number = (value: number | null | undefined, unit = '') =>
  value == null
    ? '—'
    : `${value.toLocaleString(undefined, value !== 0 && Math.abs(value) < 0.01 ? { maximumSignificantDigits: 2 } : { maximumFractionDigits: 2 })}${unit}`;

type Panels = {
  list?: ApmList;
  operations?: ApmList;
  overview?: ApmOverview;
  dependencies?: ApmDependencies;
  diagnostics?: ApmDiagnostics;
  runtime?: ApmRuntime;
};

export default function ApmPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const navigate = useNavigate();
  const location = useLocation();
  const main = useRef<HTMLElement>(null);
  const restoredScroll = useRef('');
  const [params, setParams] = useSearchParams();
  const [refresh, setRefresh] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [latency, setLatency] = useState<'p50_ms' | 'p95_ms' | 'p99_ms'>('p95_ms');
  const [metric, setMetric] = useState('error_rate');
  const [threshold, setThreshold] = useState('5');
  const [minimum, setMinimum] = useState('100');
  const [dwell, setDwell] = useState('120');
  const [creating, setCreating] = useState(false);
  const traceMetrics = params.get('metric_source') === 'tempo_spanmetrics';
  const detail = params.has('service_name');
  const requestedTab = params.get('tab') || (detail ? 'overview' : 'services');
  const tab =
    requestedTab === 'runtime'
      ? 'instances'
      : requestedTab === 'diagnostics'
        ? 'onboarding'
        : requestedTab;
  const period = params.get('range') || 'custom';
  const query = params.toString();
  const operation = params.get('operation');
  const context = new URLSearchParams(params);
  context.delete('list_query');
  // Relative refresh advances time within the same view; a changed identity,
  // protocol, filter or absolute window must never display the previous result.
  if (period !== 'custom') {
    context.delete('start');
    context.delete('end');
  }
  const scope = context.toString();
  const [results, setResults] = useState<Panels & { scope: string; updated?: number }>({
    scope: '',
  });
  const current = results.scope === scope ? results : undefined;
  const { list, operations, overview, dependencies, diagnostics, runtime } = current || {};

  const set = useCallback(
    (key: string, value: string | null) => {
      const next = new URLSearchParams(params);
      if (value === null) next.delete(key);
      else next.set(key, value);
      if (['metric_source', 'protocol', 'metric_format'].includes(key)) {
        next.delete('operation');
        next.delete('span_kind');
      }
      if (key !== 'page') next.delete('page');
      setParams(next, { replace: key === 'search' });
    },
    [params, setParams],
  );
  const changeSearch = useCallback((value: string) => set('search', value), [set]);
  const pickPeriod = (value: string) => {
    const next = new URLSearchParams(params);
    next.set('range', value);
    const duration = periods.find(([key]) => key === value)?.[1];
    if (duration) {
      const now = Date.now();
      next.set('start', new Date(now - duration).toISOString());
      next.set('end', new Date(now).toISOString());
    }
    next.delete('page');
    setParams(next);
  };
  useEffect(() => {
    if (params.has('start') && params.has('end')) return;
    const next = new URLSearchParams(params);
    const now = Date.now();
    next.set('range', '1h');
    next.set('start', new Date(now - 3600000).toISOString());
    next.set('end', new Date(now).toISOString());
    setParams(next, { replace: true });
  }, [params, setParams]);
  useEffect(() => {
    const p = new URLSearchParams(query);
    setLoading(false);
    setError('');
    setResults((previous) => (previous.scope === scope ? previous : { scope }));
    if (!p.has('start') || !p.has('end') || tab === 'alerts' || (!detail && tab === 'onboarding'))
      return;
    const controller = new AbortController();
    setLoading(true);
    // Each panel keeps its successful result if another source is unavailable.
    const tasks: Promise<void>[] = [];
    let failed = false;
    const fetchPanel = <K extends keyof Panels>(
      task: Promise<NonNullable<Panels[K]>>,
      panel: K,
    ) => {
      tasks.push(
        task
          .then((data) => {
            if (!controller.signal.aborted)
              setResults((previous) => ({
                ...(previous.scope === scope ? previous : { scope }),
                [panel]: data,
              }));
          })
          .catch((e: Error) => {
            failed = true;
            if (!controller.signal.aborted)
              setError((previous) => [previous, e.message].filter(Boolean).join(' · '));
          }),
      );
    };
    if (!detail) fetchPanel(queryApm('services', p, controller.signal), 'list');
    else if (tab === 'overview') {
      fetchPanel(queryApm('overview', p, controller.signal), 'overview');
      const top = new URLSearchParams(p);
      top.set('sort', 'p95_ms');
      top.set('page', '1');
      top.set('page_size', '5');
      if (!p.has('operation')) {
        fetchPanel(queryApm('operations', top, controller.signal), 'operations');
        fetchPanel(queryApm('dependencies', p, controller.signal), 'dependencies');
      }
    } else if (tab === 'operations')
      fetchPanel(queryApm('operations', p, controller.signal), 'list');
    else if (tab === 'dependencies')
      fetchPanel(queryApm('dependencies', p, controller.signal), 'dependencies');
    else if (tab === 'instances') {
      fetchPanel(queryApm('diagnostics', p, controller.signal), 'diagnostics');
      fetchPanel(queryApm('runtime', p, controller.signal), 'runtime');
    } else if (tab === 'onboarding')
      fetchPanel(queryApm('diagnostics', p, controller.signal), 'diagnostics');
    Promise.all(tasks).finally(() => {
      if (!controller.signal.aborted) {
        setLoading(false);
        if (!failed && tasks.length)
          setResults((previous) => ({ ...previous, updated: Date.now() }));
      }
    });
    return () => controller.abort();
  }, [query, tab, detail, refresh, scope]);
  useEffect(() => {
    if (detail && tab !== 'operations') {
      restoredScroll.current = '';
      return;
    }
    if (!list || !main.current || restoredScroll.current === query) return;
    restoredScroll.current = query;
    const saved = location.state?.[detail ? 'apmOperations' : 'apmList'];
    main.current.scrollTop = saved?.query === query ? saved.scroll : 0;
  }, [list, detail, tab, query, location.state]);
  const unset = tr('未设置', 'Unset');
  const status = (s: string) =>
    ({
      observed: tr('已观测', 'Observed'),
      no_data: tr('无数据', 'No data'),
      no_requests: tr('无请求', 'No requests'),
      insufficient_samples: tr('样本不足', 'Insufficient samples'),
      unavailable: tr('查询不可用', 'Unavailable'),
      not_observed: tr('未观测到', 'Not observed'),
      unknown: tr('未知', 'Unknown'),
      incomplete: tr('字段或链路不完整', 'Incomplete'),
    })[s] || s;
  const tabs = [
    ['overview', tr('概览', 'Overview')],
    ['operations', tr('接口', 'Operations')],
    ['dependencies', tr('依赖', 'Dependencies')],
    ['instances', tr('实例', 'Instances')],
  ];
  const viewLink = (view: string) => {
    if (view === 'operations' && operation && location.state?.apmOperations)
      return `/apm/service?${location.state.apmOperations.query}`;
    const next = new URLSearchParams(params);
    next.set('tab', view);
    next.delete('page');
    next.delete('operation');
    next.delete('search');
    return `/apm/service?${next}`;
  };
  const back = new URLSearchParams(params.get('list_query') || params);
  if (!params.has('list_query'))
    for (const key of ['service_name', 'operation', 'tab', 'page', 'sort', 'search'])
      back.delete(key);
  back.delete('list_query');
  async function createAlert() {
    setCreating(true);
    setError('');
    try {
      const p = new URLSearchParams(params);
      p.set('metric', metric);
      p.set('threshold', threshold);
      p.set('min_requests', minimum);
      p.set('for_seconds', dwell);
      const template = await queryApm('alert-template', p);
      navigate('/alerts/rules', {
        state: {
          apmDraft: {
            kind: 'metric_raw',
            scope_type: 'global',
            conditions: [],
            name: `APM ${params.get('service_name')} ${metric}`,
            spec: { expr: template.expr },
            runbook_url: 'https://github.com/ongridio/ongrid/blob/main/' + template.runbook_path,
          },
        },
      });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setCreating(false);
    }
  }
  const protocolSwitch = !traceMetrics && (
    <div
      role="group"
      aria-label={tr('请求协议', 'Request protocol')}
      className="inline-flex shrink-0 rounded-md border border-zinc-800 bg-zinc-950 p-0.5"
    >
      {['http', 'rpc'].map((value) => (
        <button
          key={value}
          type="button"
          aria-pressed={(params.get('protocol') || 'http') === value}
          onClick={() => set('protocol', value)}
          className={`rounded px-3 py-1 text-xs font-medium ${(params.get('protocol') || 'http') === value ? 'bg-indigo-600 text-white' : 'text-zinc-500 hover:text-zinc-100'}`}
        >
          {value.toUpperCase()}
        </button>
      ))}
    </div>
  );
  const openService = (event: MouseEvent<HTMLAnchorElement>, to: string) => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey)
      return;
    event.preventDefault();
    const state = {
      ...location.state,
      [detail ? 'apmOperations' : 'apmList']: {
        query,
        scroll: main.current?.scrollTop || 0,
      },
    };
    navigate(`${location.pathname}?${query}`, { replace: true, state });
    navigate(to, { state });
  };
  const sortHeading = (key: string, label: string, numeric = false) => {
    const active = (params.get('sort') || 'rps') === key;
    const hint =
      !detail && !traceMetrics
        ? (
            {
              rps: tr('按服务总请求速率排序', 'Sort by total service request rate'),
              error_rate: tr('按服务整体错误率排序', 'Sort by overall service error rate'),
              p95_ms: tr('按各协议中最高 P95 排序', 'Sort by the highest protocol P95'),
            } as Record<string, string>
          )[key]
        : undefined;
    return (
      <th
        className={`px-3 py-2.5 font-normal ${numeric ? 'text-right' : 'pl-4'}`}
        aria-sort={active ? (key === 'name' ? 'ascending' : 'descending') : 'none'}
      >
        <button
          type="button"
          title={hint}
          onClick={() => set('sort', key)}
          aria-label={tr(`按${label}排序`, `Sort by ${label}`)}
          className={`inline-flex items-center gap-1 rounded py-0.5 hover:text-zinc-100 ${active ? 'font-medium text-zinc-100' : ''}`}
        >
          {label}
          {active && (key === 'name' ? <ArrowUp size={12} /> : <ArrowDown size={12} />)}
        </button>
      </th>
    );
  };
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
      <PageHeader
        title={
          detail ? (
            <ServiceSwitcher params={params} navigationState={location.state} />
          ) : (
            tr('应用性能', 'Application performance')
          )
        }
        leading={
          detail && (
            <Link state={location.state} to={`/apm?${back}`}>
              ← {tr('服务列表', 'Services')}
            </Link>
          )
        }
        subtitle={
          detail
            ? `${params.get('environment') || unset} / ${params.get('service_namespace') || unset}`
            : tr(
                '从服务请求到链路、日志和实例排查',
                'Explore service requests, traces, logs and instances',
              )
        }
        className="!py-3 [&>div:first-child]:flex-wrap [&>div:first-child>div:last-child]:shrink [&_h1]:break-all"
        actions={
          <>
            <div className="flex items-center gap-2 rounded-md border border-zinc-800 bg-zinc-950 px-2">
              <Clock size={13} className="text-zinc-500" />
              <select
                aria-label={tr('时间范围', 'Time range')}
                className={`${input} border-0 px-0`}
                value={period}
                onChange={(e) => pickPeriod(e.target.value)}
              >
                {periods.map(([key, , zh, en]) => (
                  <option key={key} value={key}>
                    {tr(zh, en)}
                  </option>
                ))}
                <option value="custom">{tr('自定义时间', 'Custom range')}</option>
              </select>
            </div>
            <Button
              className="h-9"
              onClick={() => (period === 'custom' ? setRefresh((v) => v + 1) : pickPeriod(period))}
              disabled={loading}
            >
              <RefreshCw size={13} />
              {tr('刷新', 'Refresh')}
            </Button>
          </>
        }
        extra={
          ((!detail && tab === 'services') || period === 'custom') && (
            <div className="flex flex-wrap items-center gap-3">
              {!detail && tab === 'services' && (
                <>
                  {(
                    [
                      ['environment', tr('全部环境', 'All environments'), list?.environments],
                      [
                        'service_namespace',
                        tr('全部命名空间', 'All namespaces'),
                        list?.service_namespaces,
                      ],
                    ] as const
                  ).map(([key, label, options]) => (
                    <select
                      key={key}
                      aria-label={
                        key === 'environment'
                          ? tr('环境', 'Environment')
                          : tr('业务命名空间', 'Service namespace')
                      }
                      className={`${input} max-w-52`}
                      value={params.has(key) ? JSON.stringify(params.get(key)) : 'all'}
                      onChange={(e) =>
                        set(
                          key,
                          e.target.value === 'all' ? null : (JSON.parse(e.target.value) as string),
                        )
                      }
                    >
                      <option value="all">{label}</option>
                      {[
                        ...new Set([
                          '',
                          ...(options || []),
                          ...(params.has(key) ? [params.get(key)!] : []),
                        ]),
                      ].map((value) => (
                        <option key={value} value={JSON.stringify(value)}>
                          {value || unset}
                        </option>
                      ))}
                    </select>
                  ))}

                  <div className="w-full sm:max-w-sm sm:flex-1">
                    <SearchInput
                      value={params.get('search') || ''}
                      onChange={changeSearch}
                      label={tr('搜索服务名称…', 'Search services…')}
                    />
                  </div>
                </>
              )}
              {period === 'custom' &&
                ['start', 'end'].map((key) => (
                  <label
                    key={key}
                    className="flex flex-wrap items-center gap-2 text-xs text-zinc-500"
                  >
                    {key === 'start' ? tr('开始时间', 'Start time') : tr('结束时间', 'End time')}
                    <input
                      type="datetime-local"
                      step="1"
                      className={input}
                      value={localDateTime(params.get(key) || '')}
                      onChange={(e) => {
                        if (e.target.value) set(key, new Date(e.target.value).toISOString());
                      }}
                    />
                  </label>
                ))}
            </div>
          )
        }
      />
      {detail && (
        <nav
          aria-label={tr('应用性能视图', 'APM views')}
          className="flex shrink-0 flex-wrap items-center gap-x-5 border-b border-zinc-800 px-6"
        >
          {tabs.map(([key, label]) => (
            <Link
              key={key}
              state={location.state}
              to={viewLink(key)}
              className={`border-b-2 py-3 text-xs ${(operation ? 'operations' : tab) === key ? 'border-indigo-500 font-medium text-zinc-100' : 'border-transparent text-zinc-500 hover:text-zinc-300'}`}
              aria-current={(operation ? 'operations' : tab) === key ? 'page' : undefined}
            >
              {label}
            </Link>
          ))}
          <div className="ml-auto flex items-center gap-4 py-1.5">
            <button
              type="button"
              className="text-xs text-zinc-500 hover:text-zinc-100"
              onClick={() => set('tab', 'onboarding')}
            >
              {tr('接入管理', 'Instrumentation')}
            </button>
            {protocolSwitch}
          </div>
        </nav>
      )}
      <main ref={main} className="flex-1 space-y-3 overflow-auto px-6 py-4">
        <div className="flex min-h-6 flex-wrap items-center justify-between gap-2 text-xs text-zinc-500">
          <details>
            <summary className="w-fit cursor-pointer">
              {traceMetrics
                ? tr('Trace 样本 · 采样覆盖率未知', 'Trace samples · Sampling coverage unknown')
                : tr(
                    '应用指标 · 独立于 Trace 采样',
                    'Application metrics · Independent of trace sampling',
                  )}
              {params.get('metric_format') === 'legacy' && !traceMetrics
                ? tr(' · 旧版格式', ' · Legacy format')
                : ''}
            </summary>
            <p className="mt-2 max-w-3xl leading-relaxed">
              {tr(
                '数值按所选时段汇总，趋势使用至少 5 分钟滚动窗口。链路和依赖依靠 Trace 样本；未观测到样本不等于应用没有请求。',
                'Values summarize the selected range; trends use rolling windows of at least 5 minutes. Traces and dependencies rely on trace samples; no samples does not mean no application requests.',
              )}
            </p>
          </details>
          <span role="status">
            {loading
              ? tr('正在更新…', 'Updating…')
              : current?.updated
                ? tr(
                    `更新于 ${new Date(current.updated).toLocaleTimeString()}`,
                    `Updated ${new Date(current.updated).toLocaleTimeString()}`,
                  )
                : ''}
          </span>
        </div>
        {detail && tab === 'overview' && (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              {operation ? (
                <>
                  <Link
                    state={location.state}
                    className="text-xs text-zinc-500 hover:underline"
                    to={viewLink('operations')}
                  >
                    ← {tr('全部接口', 'All operations')}
                  </Link>
                  <h2 className="mt-1 break-all text-sm font-semibold">{operation}</h2>
                </>
              ) : (
                <h2 className="text-sm font-medium">{tr('服务指标', 'Service metrics')}</h2>
              )}
              <p className="mt-1 text-xs text-zinc-500">
                {tr(
                  '趋势使用至少 5 分钟滚动窗口',
                  'Trends use rolling windows of at least 5 minutes',
                )}
              </p>
            </div>
            <div className="flex flex-wrap gap-4 text-sm">
              {isAdmin && !traceMetrics && (
                <button
                  type="button"
                  className="text-zinc-500 hover:text-indigo-500"
                  onClick={() => set('tab', 'alerts')}
                >
                  {tr('创建告警', 'Create alert')}
                </button>
              )}
              <Link
                className="inline-flex items-center gap-1 text-zinc-500 hover:text-indigo-500"
                to={traceLink(params)}
              >
                {tr('查看链路', 'View traces')}
                <ArrowUpRight size={13} />
              </Link>
              <Link
                className="text-zinc-500 hover:text-indigo-500"
                to={traceLink(params, 'duration > 1s')}
              >
                {tr('慢链路 (>1s)', 'Slow traces (>1s)')}
              </Link>
              <Link
                className="text-zinc-500 hover:text-indigo-500"
                to={traceLink(params, 'status = error')}
              >
                {tr('错误链路', 'Error traces')}
              </Link>
              <Link
                className="inline-flex items-center gap-1 text-zinc-500 hover:text-indigo-500"
                to={`/logs?${params}`}
              >
                {tr('服务日志', 'Service logs')}
                <ArrowUpRight size={13} />
              </Link>
            </div>
          </div>
        )}
        {error && (
          <Card role="alert" className="text-sm text-red-500">
            {current?.updated
              ? tr('更新失败，保留上次结果：', 'Update failed; retaining previous results: ')
              : tr('查询失败：', 'Query failed: ')}
            {error}
          </Card>
        )}
        {loading && !list && !overview && !dependencies && !diagnostics && !runtime && (
          <Card className="flex min-h-64 items-center justify-center text-sm text-zinc-500">
            {tr('正在加载当前范围的数据…', 'Loading data for the current scope…')}
          </Card>
        )}
        {tab === 'onboarding' && (
          <>
            <div className="flex flex-wrap items-center gap-3">
              <Link
                className="text-sm text-zinc-400 hover:underline"
                state={location.state}
                to={detail ? viewLink('overview') : `/apm?${back}`}
              >
                ← {tr('返回指标', 'Back to metrics')}
              </Link>
              <label className="flex items-center gap-2 text-xs text-zinc-500">
                {tr('指标来源', 'Metric source')}
                <select
                  className={input}
                  value={params.get('metric_source') || 'application_metrics'}
                  onChange={(e) => set('metric_source', e.target.value)}
                >
                  <option value="application_metrics">
                    {tr('应用指标', 'Application metrics')}
                  </option>
                  <option value="tempo_spanmetrics">{tr('Trace 样本', 'Trace samples')}</option>
                </select>
              </label>
              {!traceMetrics ? (
                <label className="flex items-center gap-2 text-xs text-zinc-500">
                  {tr('指标格式', 'Metric format')}
                  <select
                    className={input}
                    value={params.get('metric_format') || 'otel'}
                    onChange={(e) => set('metric_format', e.target.value)}
                  >
                    <option value="otel">{tr('当前 OTel 约定', 'Current OTel conventions')}</option>
                    <option value="legacy">{tr('旧版 HTTP / gRPC', 'Legacy HTTP / gRPC')}</option>
                  </select>
                </label>
              ) : (
                <label className="flex items-center gap-2 text-xs text-zinc-500">
                  {tr('入口类型', 'Entry type')}
                  <select
                    className={input}
                    value={params.get('span_kind') || 'server'}
                    onChange={(e) => set('span_kind', e.target.value)}
                  >
                    <option value="server">{tr('服务端请求', 'Server requests')}</option>
                    <option value="consumer">{tr('消息消费', 'Message consumer')}</option>
                  </select>
                </label>
              )}
            </div>
            <Onboarding />
          </>
        )}
        {detail && tab === 'operations' && (
          <div className="max-w-sm">
            <SearchInput
              value={params.get('search') || ''}
              onChange={changeSearch}
              label={tr('搜索接口…', 'Search operations…')}
            />
          </div>
        )}
        {list && (
          <Card className="!p-0 overflow-hidden">
            <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
              <h2 className="text-sm font-medium">
                {detail
                  ? tr(`接口 · ${list.total}`, `Operations · ${list.total}`)
                  : tr(`服务 · ${list.total}`, `Services · ${list.total}`)}
              </h2>
              {!detail && (
                <button
                  type="button"
                  className="text-xs text-zinc-500 hover:text-zinc-100"
                  onClick={() => set('tab', 'onboarding')}
                >
                  {tr('接入管理', 'Instrumentation')}
                </button>
              )}
            </div>
            {list.items.length === 0 ? (
              <EmptyState
                title={tr(
                  '当前范围未观测到服务请求指标',
                  'No request metrics observed in this scope',
                )}
                hint={tr(
                  '检查 Metrics 导出、指标格式和时间范围。',
                  'Check Metrics export, metric format and time range.',
                )}
                action={
                  <Button onClick={() => set('tab', 'onboarding')}>
                    {tr('接入应用', 'Instrument application')}
                  </Button>
                }
              />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full min-w-[760px] table-fixed text-left text-sm">
                  <colgroup>
                    {(!detail
                      ? ['26%', '12%', '12%', '8%', '10%', '10%', '11%', '11%']
                      : ['45%', '14%', '14%', '14%', '13%']
                    ).map((width, i) => (
                      <col key={i} style={{ width }} />
                    ))}
                  </colgroup>
                  <thead className="border-y border-[rgb(var(--border))] bg-zinc-900/40 text-xs text-zinc-500">
                    <tr>
                      {sortHeading(
                        'name',
                        detail ? tr('接口', 'Operation') : tr('服务', 'Service'),
                      )}
                      {!detail && (
                        <>
                          <th className="px-3 py-3 font-normal">{tr('环境', 'Environment')}</th>
                          <th className="px-3 py-3 font-normal">{tr('命名空间', 'Namespace')}</th>
                          <th className="px-3 py-3 font-normal">{tr('协议', 'Protocol')}</th>
                        </>
                      )}
                      {sortHeading('rps', 'RPS', true)}
                      {sortHeading('error_rate', tr('错误率', 'Error rate'), true)}
                      {sortHeading('p95_ms', 'P95 (ms)', true)}
                      <th className="px-4 py-3 font-normal">{tr('数据状态', 'Data status')}</th>
                    </tr>
                  </thead>
                  {list.items.map((row) => {
                    const protocols =
                      !detail && row.protocols?.length
                        ? row.protocols
                        : [
                            {
                              ...row,
                              protocol: traceMetrics ? '' : params.get('protocol') || 'http',
                            },
                          ];
                    const target = serviceParams(
                      params,
                      row.identity,
                      !detail ? protocols[0].protocol : undefined,
                    );
                    if (row.operation) target.set('operation', row.operation);
                    const to = `/apm/service?${target}`;
                    return (
                      <tbody
                        key={JSON.stringify([row.identity, row.operation])}
                        className="border-b border-[rgb(var(--border))] last:border-0 hover:bg-zinc-900/40"
                      >
                        {protocols.map((metrics, i) => (
                          <tr key={metrics.protocol}>
                            {i === 0 && (
                              <>
                                <td rowSpan={protocols.length} className="px-4 py-4 align-top">
                                  <Link
                                    className="break-words font-medium text-zinc-100 hover:text-indigo-500 hover:underline"
                                    state={location.state}
                                    to={to}
                                    onClick={(event) => openService(event, to)}
                                  >
                                    {detail ? row.operation : row.identity.service_name}
                                  </Link>
                                </td>
                                {!detail && (
                                  <>
                                    <td
                                      rowSpan={protocols.length}
                                      className="truncate px-3 py-4 align-top text-zinc-400"
                                      title={row.identity.environment || unset}
                                    >
                                      {row.identity.environment || unset}
                                    </td>
                                    <td
                                      rowSpan={protocols.length}
                                      className="truncate px-3 py-4 align-top text-zinc-400"
                                      title={row.identity.service_namespace || unset}
                                    >
                                      {row.identity.service_namespace || unset}
                                    </td>
                                  </>
                                )}
                              </>
                            )}
                            {!detail && (
                              <td className="px-3 py-3 text-xs text-zinc-400">
                                {metrics.protocol ? (
                                  <Link
                                    state={location.state}
                                    className="rounded border border-[rgb(var(--border))] px-1.5 py-0.5 hover:text-indigo-500"
                                    to={`/apm/service?${serviceParams(params, row.identity, metrics.protocol)}`}
                                    onClick={(event) =>
                                      openService(
                                        event,
                                        `/apm/service?${serviceParams(params, row.identity, metrics.protocol)}`,
                                      )
                                    }
                                  >
                                    {metrics.protocol.toUpperCase()}
                                  </Link>
                                ) : (
                                  '—'
                                )}
                              </td>
                            )}
                            <td className="px-3 py-3 text-right tabular-nums">
                              {number(metrics.rps)}
                            </td>
                            <td
                              className={`px-3 py-3 text-right tabular-nums ${metrics.error_rate != null && metrics.error_rate > 0 ? 'text-red-500' : 'text-zinc-400'}`}
                            >
                              {number(metrics.error_rate, '%')}
                            </td>
                            <td className="px-3 py-3 text-right tabular-nums">
                              {number(metrics.p95_ms)}
                            </td>
                            <td className="px-4 py-3 text-xs text-zinc-500">
                              <span
                                className={`mr-1.5 inline-block h-1.5 w-1.5 rounded-full ${metrics.data_status === 'observed' ? 'bg-zinc-500' : 'bg-amber-500'}`}
                              />
                              {status(metrics.data_status)}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    );
                  })}
                </table>
              </div>
            )}
            <div className="px-4">
              <PaginationFooter
                page={list.page - 1}
                pageSize={list.page_size}
                shown={list.items.length}
                total={list.total}
                onPageChange={(p) => set('page', String(p + 1))}
              />
            </div>
          </Card>
        )}
        {overview && (
          <>
            <Card className="grid gap-4 lg:grid-cols-3 lg:divide-x lg:divide-[rgb(var(--border))]">
              {[
                ['rps', tr('请求速率', 'Request rate')],
                ['error_rate', tr('错误率', 'Error rate')],
                [latency, tr('延迟', 'Latency')],
              ].map(([key, title]) => (
                <section key={key} className="min-w-0 lg:pl-3 first:lg:pl-0">
                  <h2 className="text-sm text-zinc-400">
                    {key === latency ? (
                      <select
                        aria-label={tr('延迟分位数', 'Latency percentile')}
                        className="max-w-full bg-transparent text-sm text-zinc-400"
                        value={latency}
                        onChange={(e) => setLatency(e.target.value as typeof latency)}
                      >
                        <option value="p95_ms">{tr('P95 延迟', 'P95 latency')}</option>
                        <option value="p50_ms">{tr('P50 延迟', 'P50 latency')}</option>
                        <option value="p99_ms">{tr('P99 延迟', 'P99 latency')}</option>
                      </select>
                    ) : (
                      title
                    )}
                  </h2>
                  <div className="mb-1 mt-1 text-2xl font-semibold tabular-nums">
                    {number(overview.summary[key as 'rps' | 'error_rate' | typeof latency])}
                    <span className="ml-1 text-xs font-normal text-zinc-500">
                      {key === 'rps' ? 'req/s' : key === 'error_rate' ? '%' : 'ms'}
                    </span>
                  </div>
                  <p className="mb-2 text-xs text-zinc-500">
                    {key === 'rps'
                      ? tr('所选时段平均速率', 'Average rate in selected range')
                      : tr('所选时段汇总', 'Selected range summary')}
                  </p>
                  <div className="h-28">
                    <ResponsiveContainer width="100%" height="100%">
                      <LineChart data={overview.points} syncId="apm-red">
                        <CartesianGrid stroke="rgb(var(--border))" strokeDasharray="3 3" />
                        <XAxis
                          dataKey="timestamp"
                          tickFormatter={(v) => new Date(v * 1000).toLocaleTimeString()}
                          tick={{ fontSize: 11 }}
                          minTickGap={45}
                        />
                        <YAxis width={45} tick={{ fontSize: 11 }} />
                        <Tooltip
                          contentStyle={chartTooltipStyle}
                          labelStyle={chartTooltipLabelStyle}
                          labelFormatter={(v) => new Date(Number(v) * 1000).toLocaleString()}
                        />
                        {
                          <Line
                            name={title}
                            dataKey={key}
                            stroke={key === 'error_rate' ? '#ef4444' : '#6366f1'}
                            dot={false}
                            isAnimationActive={false}
                            connectNulls={false}
                          />
                        }
                      </LineChart>
                    </ResponsiveContainer>
                  </div>
                </section>
              ))}
            </Card>
          </>
        )}
        {detail && tab === 'overview' && !operation && (
          <>
            <div className="space-y-3">
              <Card className="min-w-0">
                <div className="mb-2 flex items-center justify-between gap-3">
                  <h2 className="text-sm font-medium">{tr('重点接口', 'Key operations')}</h2>
                  <Link
                    className="inline-flex items-center gap-1 text-xs text-zinc-500"
                    state={location.state}
                    to={viewLink('operations')}
                  >
                    {tr('全部接口', 'All operations')} <ArrowRight size={12} />
                  </Link>
                </div>
                <p className="mb-2 text-xs text-zinc-500">
                  {tr('按 P95 延迟排序，最多展示 5 项', 'Top 5 operations by P95 latency')}
                </p>
                {!operations && !loading && (
                  <p className="py-6 text-xs text-zinc-500">
                    {tr('接口查询不可用，请重试。', 'Operations unavailable. Please retry.')}
                  </p>
                )}
                {operations &&
                  (operations.items.length ? (
                    <div className="overflow-auto">
                      <table className="w-full text-left text-sm">
                        <thead className="text-zinc-500">
                          <tr>
                            <th className="py-2 font-normal">{tr('接口', 'Operation')}</th>
                            <th className="px-3 text-right font-normal">RPS</th>
                            <th className="px-3 text-right font-normal">
                              {tr('错误率', 'Error rate')}
                            </th>
                            <th className="pl-3 text-right font-normal">P95 (ms)</th>
                          </tr>
                        </thead>
                        <tbody className="divide-y divide-[rgb(var(--border))]">
                          {operations.items.map((row) => {
                            const scope = serviceParams(params, row.identity);
                            if (row.operation) scope.set('operation', row.operation);
                            return (
                              <tr key={row.operation}>
                                <td className="max-w-64 break-words py-2.5">
                                  <Link
                                    title={row.operation}
                                    className="font-medium hover:text-indigo-500 hover:underline"
                                    state={{
                                      ...location.state,
                                      apmOperations: undefined,
                                    }}
                                    to={`/apm/service?${scope}`}
                                  >
                                    {row.operation}
                                  </Link>
                                </td>
                                <td className="px-3 text-right tabular-nums">{number(row.rps)}</td>
                                <td
                                  className={`px-3 text-right tabular-nums ${row.error_rate ? 'text-red-500' : ''}`}
                                >
                                  {number(row.error_rate, '%')}
                                </td>
                                <td className="pl-3 text-right tabular-nums">
                                  {number(row.p95_ms)}
                                </td>
                              </tr>
                            );
                          })}
                        </tbody>
                      </table>
                    </div>
                  ) : (
                    <EmptyState
                      title={tr('当前范围未观测到接口', 'No operations observed in this window')}
                    />
                  ))}
              </Card>
              {dependencies?.items.length ? (
                <Card className="min-w-0">
                  <div className="mb-2 flex items-center justify-between gap-3">
                    <h2 className="text-sm font-medium">
                      {tr('上下游依赖', 'Service dependencies')}
                    </h2>
                    <Link
                      className="inline-flex items-center gap-1 text-xs text-zinc-500"
                      state={location.state}
                      to={viewLink('dependencies')}
                    >
                      {tr('查看拓扑', 'View map')} <ArrowRight size={12} />
                    </Link>
                  </div>
                  <p className="mb-2 text-xs text-zinc-500">
                    {tr(
                      '所有入口类型 · 按观测流量排列',
                      'All entry types · Ordered by observed traffic',
                    )}
                  </p>
                  {!dependencies && !loading && (
                    <p className="py-6 text-xs text-zinc-500">
                      {tr('依赖查询不可用，请重试。', 'Dependencies unavailable. Please retry.')}
                    </p>
                  )}
                  {dependencies &&
                    (dependencies.items.length ? (
                      <div className="divide-y divide-[rgb(var(--border))]">
                        {dependencies.items.slice(0, 5).map((edge, i) => {
                          const outgoing = Object.entries(edge.client).every(
                            ([key, value]) => value === (params.get(key) || ''),
                          );
                          const peer = outgoing ? edge.server : edge.client;
                          const external = outgoing
                            ? edge.connection_type === 'database'
                            : edge.connection_type === 'virtual_node';
                          return (
                            <div
                              key={i}
                              className="flex items-center justify-between gap-3 py-3 text-xs"
                            >
                              <div className="min-w-0">
                                <div className="flex items-center gap-2">
                                  <span className="shrink-0 text-zinc-500">
                                    {outgoing ? tr('下游', 'Downstream') : tr('上游', 'Upstream')}
                                  </span>
                                  {external ? (
                                    <span className="truncate">{peer.service_name}</span>
                                  ) : (
                                    <Link
                                      title={peer.service_name}
                                      className="truncate font-medium hover:underline"
                                      state={location.state}
                                      to={`/apm/service?${serviceParams(params, peer)}`}
                                    >
                                      {peer.service_name}
                                    </Link>
                                  )}
                                </div>
                                <p className="mt-1 truncate text-zinc-500">
                                  {external
                                    ? tr('推断的外部依赖', 'Inferred external dependency')
                                    : `${peer.environment || unset} / ${peer.service_namespace || unset}`}
                                </p>
                              </div>
                              <span className="shrink-0 tabular-nums text-zinc-500">
                                {number(edge.rps)} req/s
                              </span>
                            </div>
                          );
                        })}
                        {(dependencies.items.length > 5 || dependencies.truncated) && (
                          <p className="pt-3 text-xs text-zinc-500">
                            {tr(
                              '仅展示流量最高的 5 条关系',
                              'Showing up to 5 busiest dependencies',
                            )}
                          </p>
                        )}
                      </div>
                    ) : (
                      <EmptyState
                        title={tr(
                          '当前时间段未观测到依赖',
                          'No dependencies observed in this window',
                        )}
                      />
                    ))}
                </Card>
              ) : (
                <div className="flex flex-wrap items-center justify-between gap-2 px-1 py-2 text-sm text-zinc-500">
                  <span>
                    {!dependencies && !loading
                      ? tr('依赖查询不可用', 'Dependency query unavailable')
                      : tr(
                          '当前范围未观测到 Trace 依赖',
                          'No trace dependencies observed in this scope',
                        )}
                  </span>
                  <Link
                    state={location.state}
                    className="hover:underline"
                    to={viewLink('dependencies')}
                  >
                    {tr('查看依赖', 'View dependencies')} →
                  </Link>
                </div>
              )}
            </div>
          </>
        )}
        {dependencies && tab === 'dependencies' && (
          <Card>
            <Dependencies data={dependencies} params={params} />
          </Card>
        )}
        {diagnostics && (
          <>
            {tab === 'onboarding' && (
              <Card>
                <h2 className="mb-3 text-sm font-medium">
                  {tr('接入诊断', 'Instrumentation diagnostics')}
                </h2>
                <p className="mb-4 text-xs text-zinc-500">
                  {tr(
                    `检查 ${diagnostics.sampled_traces} 条代表性链路。仅反映样本，不自动认定根因或采样比例。`,
                    `Inspected ${diagnostics.sampled_traces} representative traces. These are sample observations, not a root-cause or sampling-coverage determination.`,
                  )}
                </p>
                <div className="divide-y divide-[rgb(var(--border))]">
                  {diagnostics.checks.map((check) => (
                    <div key={check.key} className="flex justify-between gap-4 py-3 text-xs">
                      <span>
                        {{
                          metrics: tr('请求指标', 'Request metrics'),
                          sampling: tr('Trace 采样覆盖率', 'Trace sampling coverage'),
                          traces: tr('链路接收', 'Trace ingestion'),
                          resource_identity: tr('服务身份', 'Service identity'),
                          downstream: tr('下游埋点', 'Downstream instrumentation'),
                          context: tr('父子上下文', 'Parent context'),
                          logs: tr('样本请求日志', 'Sample request logs'),
                        }[check.key] || check.key}
                      </span>
                      <span
                        className={
                          check.status === 'unavailable' || check.status === 'incomplete'
                            ? 'text-amber-500'
                            : 'text-zinc-500'
                        }
                      >
                        {status(check.status)}
                      </span>
                    </div>
                  ))}
                </div>
                <div className="mt-4 flex flex-wrap gap-4 text-xs underline">
                  {diagnostics.trace_ids.map((id) => (
                    <span key={id}>
                      <Link to={traceLink(params, undefined, id)}>{id.slice(0, 12)}…</Link> ·{' '}
                      <Link
                        to={`/logs?${new URLSearchParams({ start: params.get('start')!, end: params.get('end')!, trace_id: id, service_name: params.get('service_name')!, environment: params.get('environment') || '', service_namespace: params.get('service_namespace') || '' })}`}
                      >
                        {tr('同请求日志', 'Request logs')}
                      </Link>
                    </span>
                  ))}
                </div>
              </Card>
            )}
            {tab === 'instances' && (
              <Card>
                <h2 className="text-sm font-medium">
                  {tr('样本关联实例', 'Instances in sampled traces')}
                </h2>
                {diagnostics.instances.length === 0 ? (
                  <EmptyState
                    title={tr('样本缺少实例关联字段', 'Samples have no instance identity')}
                  />
                ) : (
                  <div className="divide-y divide-[rgb(var(--border))]">
                    {diagnostics.instances.map((instance, i) => (
                      <div
                        key={i}
                        className="flex flex-wrap items-center justify-between gap-3 py-3 text-xs"
                      >
                        <span>
                          {instance.instance_id || instance.pod || unset} ·{' '}
                          {instance.version || unset} · {tr('设备', 'Device')}{' '}
                          {instance.device_id || unset}
                        </span>
                        {instance.device_id && (
                          <Link
                            className="underline"
                            to={`/tools?${new URLSearchParams({ tool: 'profile', device_id: instance.device_id, service_name: params.get('service_name')!, environment: params.get('environment') || '', service_namespace: params.get('service_namespace') || '', instance_id: instance.instance_id, start: params.get('start')!, end: params.get('end')! })}`}
                          >
                            {tr('按需 pprof 采集', 'On-demand pprof')}
                          </Link>
                        )}
                      </div>
                    ))}
                  </div>
                )}
                <p className="text-xs text-zinc-500">
                  {tr(
                    '需确认所选设备能访问该实例的 pprof 端点。新采集不能补回历史 Profile，也不代表某个 Span 的函数耗时。',
                    'Confirm that the device can reach this instance’s pprof endpoint. New captures cannot recover historical profiles or attribute function time to one span.',
                  )}
                </p>
              </Card>
            )}
          </>
        )}
        {runtime && (
          <Card>
            <h2 className="mb-3 text-sm font-medium">{tr('运行时指标', 'Runtime metrics')}</h2>
            <p className="mb-2 text-xs text-zinc-500">
              {tr(
                '应用已导出的运行时指标，取结束时间前 5 分钟内的最近值；需配置服务身份标签。',
                'Runtime gauges exported by the application, using the latest value within 5 minutes of the end time; service identity labels are required.',
              )}
            </p>
            {runtime.items.length === 0 ? (
              <EmptyState
                title={tr('未观测到支持的运行时指标', 'No supported runtime metrics observed')}
              />
            ) : (
              <table className="w-full text-left text-xs">
                <thead>
                  <tr>
                    {[
                      tr('指标', 'Metric'),
                      tr('实例', 'Instance'),
                      tr('数值 / 单位', 'Value / unit'),
                    ].map((v) => (
                      <th className="p-3 [&:nth-child(n+2):nth-child(-n+4)]:text-right" key={v}>
                        {v}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody className="divide-y divide-[rgb(var(--border))]">
                  {runtime.items.map((row, i) => (
                    <tr key={i}>
                      <td className="p-3">{row.name}</td>
                      <td>{row.instance_id || unset}</td>
                      <td>
                        {number(row.value)} {row.unit}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Card>
        )}
        {tab === 'alerts' && traceMetrics && (
          <Card>
            {tr(
              '切换到应用指标后创建请求级告警。',
              'Switch to application metrics to create request alerts.',
            )}
          </Card>
        )}
        {tab === 'alerts' && !traceMetrics && (
          <Card className="space-y-4">
            <h2 className="text-sm font-medium">
              {tr('请求级告警模板', 'Request-level alert template')}
            </h2>
            <p className="text-xs text-zinc-500">
              {tr(
                '使用与概览相同的入口请求口径，固定 5 分钟窗口。生成后进入现有规则编辑器预览、配置通知并保存。',
                'Uses the overview entry-request definition over 5 minutes. Continue to the existing rule editor to preview, configure notifications and save.',
              )}
            </p>
            <div className="flex flex-wrap gap-3">
              <label className="grid gap-1 text-xs">
                {tr('指标', 'Metric')}
                <select
                  className={input}
                  value={metric}
                  onChange={(e) => {
                    setMetric(e.target.value);
                    setThreshold(e.target.value === 'p95_ms' ? '500' : '5');
                  }}
                >
                  <option value="error_rate">{tr('错误率 (%)', 'Error rate (%)')}</option>
                  <option value="p95_ms">P95 (ms)</option>
                </select>
              </label>
              {[
                [tr('阈值', 'Threshold'), threshold, setThreshold],
                [tr('最少请求数', 'Minimum requests'), minimum, setMinimum],
                [tr('持续秒数（30 的倍数）', 'Duration (multiples of 30s)'), dwell, setDwell],
              ].map(([label, value, setter]) => (
                <label key={String(label)} className="grid gap-1 text-xs">
                  {String(label)}
                  <input
                    type="number"
                    min="0"
                    className={input}
                    value={String(value)}
                    onChange={(e) => (setter as (s: string) => void)(e.target.value)}
                  />
                </label>
              ))}
            </div>
            <Button disabled={creating || !isAdmin} onClick={createAlert}>
              {tr('在规则编辑器中预览', 'Preview in rule editor')}
            </Button>
            {!isAdmin && (
              <p className="text-xs text-zinc-500">
                {tr('只有管理员可创建规则。', 'Only admins can create rules.')}
              </p>
            )}
          </Card>
        )}
      </main>
    </div>
  );
}
