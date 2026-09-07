import { useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { ArrowRight, Clock, RefreshCw, Search, SlidersHorizontal } from 'lucide-react';
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

export default function ApmPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const [refresh, setRefresh] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [list, setList] = useState<ApmList>();
  const [operations, setOperations] = useState<ApmList>();
  const [advanced, setAdvanced] = useState(false);
  const [overview, setOverview] = useState<ApmOverview>();
  const [dependencies, setDependencies] = useState<ApmDependencies>();
  const [diagnostics, setDiagnostics] = useState<ApmDiagnostics>();
  const [runtime, setRuntime] = useState<ApmRuntime>();
  const [latency, setLatency] = useState<'p50_ms' | 'p95_ms' | 'p99_ms'>('p95_ms');
  const [metric, setMetric] = useState('error_rate');
  const [threshold, setThreshold] = useState('5');
  const [minimum, setMinimum] = useState('100');
  const [dwell, setDwell] = useState('120');
  const [creating, setCreating] = useState(false);
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
  const set = (key: string, value: string | null) => {
    const next = new URLSearchParams(params);
    if (value === null) next.delete(key);
    else next.set(key, value);
    if (key !== 'page') next.delete('page');
    setParams(next);
  };
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
    setList(undefined);
    setOverview(undefined);
    setOperations(undefined);
    setDependencies(undefined);
    setDiagnostics(undefined);
    setRuntime(undefined);
    if (!p.has('start') || !p.has('end') || tab === 'alerts' || (!detail && tab === 'onboarding'))
      return;
    const controller = new AbortController();
    setLoading(true);
    // Each panel keeps its successful result if another source is unavailable.
    const tasks: Promise<void>[] = [];
    const fetchPanel = <T,>(task: Promise<T>, save: (data: T) => void) => {
      tasks.push(
        task
          .then((data) => {
            if (!controller.signal.aborted) save(data);
          })
          .catch((e: Error) => {
            if (!controller.signal.aborted)
              setError((previous) => [previous, e.message].filter(Boolean).join(' · '));
          }),
      );
    };
    if (!detail) fetchPanel(queryApm('services', p, controller.signal), setList);
    else if (tab === 'overview') {
      fetchPanel(queryApm('overview', p, controller.signal), setOverview);
      const top = new URLSearchParams(p);
      top.set('sort', 'p95_ms');
      top.set('page', '1');
      top.set('page_size', '5');
      fetchPanel(queryApm('operations', top, controller.signal), setOperations);
      fetchPanel(queryApm('dependencies', p, controller.signal), setDependencies);
    } else if (tab === 'operations')
      fetchPanel(queryApm('operations', p, controller.signal), setList);
    else if (tab === 'dependencies')
      fetchPanel(queryApm('dependencies', p, controller.signal), setDependencies);
    else if (tab === 'instances') {
      fetchPanel(queryApm('diagnostics', p, controller.signal), setDiagnostics);
      fetchPanel(queryApm('runtime', p, controller.signal), setRuntime);
    } else if (tab === 'onboarding')
      fetchPanel(queryApm('diagnostics', p, controller.signal), setDiagnostics);
    Promise.all(tasks).finally(() => {
      if (!controller.signal.aborted) setLoading(false);
    });
    return () => controller.abort();
  }, [query, tab, detail, refresh]);
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
    ['traces', tr('调用链', 'Traces')],
    ['dependencies', tr('依赖', 'Dependencies')],
    ['instances', tr('实例', 'Instances')],
  ];
  const viewLink = (view: string) => {
    const next = new URLSearchParams(params);
    next.set('tab', view);
    next.delete('page');
    return `/apm/service?${next}`;
  };
  const back = new URLSearchParams(params);
  for (const key of ['service_name', 'operation', 'tab', 'page', 'sort']) back.delete(key);
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
  const filtersButton = (
    <Button className="h-9" aria-expanded={advanced} onClick={() => setAdvanced(!advanced)}>
      <SlidersHorizontal size={13} />
      {tr('高级筛选', 'Advanced filters')}
      {params.get('span_kind') === 'consumer' && <span>· {tr('消息消费', 'Consumer')}</span>}
    </Button>
  );
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
      <PageHeader
        title={detail ? params.get('service_name') : tr('应用性能', 'Application performance')}
        leading={detail && <Link to={`/apm?${back}`}>← {tr('服务列表', 'Services')}</Link>}
        subtitle={
          detail
            ? `${params.get('environment') || unset} / ${params.get('service_namespace') || unset}`
            : tr(
                '从服务请求到链路、日志和实例排查',
                'Explore service requests, traces, logs and instances',
              )
        }
        className="[&>div:first-child]:flex-wrap [&>div:first-child>div:last-child]:shrink [&_h1]:break-all"
        actions={
          <>
            {detail && filtersButton}
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
            {detail && (
              <Button
                className="h-9"
                aria-pressed={tab === 'alerts'}
                onClick={() => set('tab', tab === 'alerts' ? 'overview' : 'alerts')}
              >
                {tr('创建告警', 'Create alert')}
              </Button>
            )}
            <Button
              className="h-9"
              aria-pressed={tab === 'onboarding'}
              onClick={() =>
                set('tab', tab === 'onboarding' ? (detail ? 'overview' : 'services') : 'onboarding')
              }
            >
              {tr('接入管理', 'Instrumentation')}
            </Button>
          </>
        }
        extra={
          (!detail || advanced || period === 'custom') && (
            <div className="flex flex-wrap items-center gap-3">
              {!detail && tab === 'services' && (
                <>
                  <label className="relative min-w-48 flex-1">
                    <Search size={14} className="absolute left-3 top-2.5 text-zinc-500" />
                    <input
                      aria-label={tr('服务搜索', 'Search services')}
                      className={`${input} w-full pl-9`}
                      placeholder={tr('搜索服务名称…', 'Search services…')}
                      value={params.get('search') || ''}
                      onChange={(e) => set('search', e.target.value)}
                    />
                  </label>
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
                </>
              )}
              {!detail && filtersButton}
              {advanced && (
                <label className="flex items-center gap-2 text-xs text-zinc-500">
                  {tr('入口类型', 'Entry type')}
                  <select
                    className={input}
                    value={params.get('span_kind') || 'server'}
                    onChange={(e) => set('span_kind', e.target.value)}
                  >
                    <option value="server">{tr('HTTP / RPC 服务', 'HTTP / RPC server')}</option>
                    <option value="consumer">{tr('消息消费', 'Message consumer')}</option>
                  </select>
                </label>
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
          className="flex shrink-0 flex-wrap gap-5 border-b border-zinc-800 px-6"
        >
          {tabs.map(([key, label]) => (
            <Link
              key={key}
              to={key === 'traces' ? traceLink(params) : viewLink(key)}
              className={`border-b-2 py-3 text-xs ${tab === key ? 'border-indigo-500 font-medium text-zinc-100' : 'border-transparent text-zinc-500 hover:text-zinc-300'}`}
              aria-current={tab === key ? 'page' : undefined}
            >
              {label}
            </Link>
          ))}
        </nav>
      )}
      <main className="flex-1 space-y-4 overflow-auto p-6">
        <details className="text-xs text-zinc-500">
          <summary className="w-fit cursor-pointer">
            {tr(
              '基于已接收的入口请求 · 采样覆盖率未知',
              'Based on received entry requests · Sampling coverage unknown',
            )}
          </summary>
          <p className="mt-2 max-w-3xl leading-relaxed">
            {tr(
              '指标来自 Tempo 接收的入口 Span，不代表已确认的全量业务请求；无数据不等于服务宕机。趋势使用至少 5 分钟滚动窗口，摘要使用所选时间范围。',
              'Metrics reflect entry spans received by Tempo, not confirmed total business traffic. No data does not imply downtime. Trends use a rolling window of at least 5 minutes; summaries use the selected range.',
            )}
          </p>
        </details>
        {error && (
          <Card role="alert" className="text-sm text-red-500">
            {tr('查询失败：', 'Query failed: ')}
            {error}
          </Card>
        )}
        {loading && (
          <p role="status" className="text-xs text-zinc-500">
            {tr('查询中…', 'Loading…')}
          </p>
        )}
        {tab === 'onboarding' && <Onboarding />}
        {list && (
          <Card>
            <div className="mb-3 flex justify-between text-xs">
              <h2 className="self-center text-sm font-medium">
                {detail
                  ? tr(`接口 · ${list.total}`, `Operations · ${list.total}`)
                  : tr(`服务 · ${list.total}`, `Services · ${list.total}`)}
              </h2>
              <label>
                {tr('排序 ', 'Sort ')}
                <select
                  className={input}
                  value={params.get('sort') || 'rps'}
                  onChange={(e) => set('sort', e.target.value)}
                >
                  {[
                    ['rps', 'RPS'],
                    ['error_rate', tr('错误率', 'Error rate')],
                    ['p95_ms', 'P95'],
                    ['name', tr('名称', 'Name')],
                  ].map(([k, v]) => (
                    <option key={k} value={k}>
                      {v}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            {list.items.length === 0 ? (
              <EmptyState
                title={tr(
                  '当前范围未观测到服务请求指标',
                  'No request metrics observed in this scope',
                )}
                hint={tr(
                  '检查接入、时间范围和指标生成状态。',
                  'Check instrumentation, time range and metric generation.',
                )}
                action={
                  <Button onClick={() => set('tab', 'onboarding')}>
                    {tr('接入应用', 'Instrument application')}
                  </Button>
                }
              />
            ) : (
              <div className="overflow-auto">
                <table className="w-full whitespace-nowrap text-left text-xs">
                  <thead className="text-zinc-500">
                    <tr>
                      {[
                        detail
                          ? tr('接口', 'Operation')
                          : tr('服务 / 环境 / 命名空间', 'Service / environment / namespace'),
                        'RPS',
                        tr('错误率', 'Error rate'),
                        'P95 (ms)',
                        tr('数据状态', 'Data status'),
                      ].map((v) => (
                        <th className="p-3" key={v}>
                          {v}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-zinc-800">
                    {list.items.map((row) => {
                      const p = serviceParams(params, row.identity);
                      if (row.operation) p.set('operation', row.operation);
                      return (
                        <tr key={JSON.stringify([row.identity, row.operation])}>
                          <td className="p-3">
                            <Link
                              className="font-medium underline"
                              to={detail ? traceLink(p) : `/apm/service?${p}`}
                            >
                              {detail ? row.operation : row.identity.service_name}
                            </Link>
                            {!detail && (
                              <div className="mt-1 text-zinc-500">
                                {row.identity.environment || unset} /{' '}
                                {row.identity.service_namespace || unset}
                              </div>
                            )}
                          </td>
                          <td>{number(row.rps)}</td>
                          <td
                            className={
                              row.error_rate != null && row.error_rate > 0 ? 'text-red-500' : ''
                            }
                          >
                            {number(row.error_rate, '%')}
                          </td>
                          <td>{number(row.p95_ms)}</td>
                          <td className="text-zinc-500">
                            <span
                              className={`mr-1.5 inline-block h-1.5 w-1.5 rounded-full ${row.data_status === 'observed' ? 'bg-zinc-500' : 'bg-amber-500'}`}
                            />
                            {status(row.data_status)}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
            <PaginationFooter
              page={list.page - 1}
              pageSize={list.page_size}
              shown={list.items.length}
              total={list.total}
              onPageChange={(p) => set('page', String(p + 1))}
            />
          </Card>
        )}
        {overview && (
          <>
            <div className="grid gap-4 lg:grid-cols-3">
              {[
                ['rps', tr('请求速率', 'Request rate')],
                ['error_rate', tr('错误率', 'Error rate')],
                [latency, tr('延迟', 'Latency')],
              ].map(([key, title]) => (
                <Card key={key}>
                  <h2 className="text-xs text-zinc-500">
                    {key === latency ? (
                      <select
                        aria-label={tr('延迟分位数', 'Latency percentile')}
                        className="max-w-full bg-transparent text-xs text-zinc-500"
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
                  <div className="mb-5 mt-2 text-2xl font-semibold tabular-nums">
                    {number(overview.summary[key as 'rps' | 'error_rate' | typeof latency])}
                    <span className="ml-1 text-xs font-normal text-zinc-500">
                      {key === 'rps' ? 'req/s' : key === 'error_rate' ? '%' : 'ms'}
                    </span>
                  </div>
                  <div className="h-40">
                    <ResponsiveContainer width="100%" height="100%">
                      <LineChart data={overview.points}>
                        <CartesianGrid stroke="rgb(var(--border))" strokeDasharray="3 3" />
                        <XAxis
                          dataKey="timestamp"
                          tickFormatter={(v) => new Date(v * 1000).toLocaleTimeString()}
                          tick={{ fontSize: 10 }}
                          minTickGap={45}
                        />
                        <YAxis width={45} tick={{ fontSize: 10 }} />
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
                </Card>
              ))}
            </div>
          </>
        )}
        {detail && tab === 'overview' && (
          <>
            <div className="grid gap-4 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
              <Card className="min-w-0">
                <div className="mb-4 flex items-center justify-between gap-3">
                  <h2 className="text-sm font-medium">{tr('重点接口', 'Key operations')}</h2>
                  <Link
                    className="inline-flex items-center gap-1 text-xs text-zinc-500"
                    to={viewLink('operations')}
                  >
                    {tr('全部接口', 'All operations')} <ArrowRight size={12} />
                  </Link>
                </div>
                <p className="mb-3 text-xs text-zinc-500">
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
                      <table className="w-full whitespace-nowrap text-left text-xs">
                        <thead className="text-zinc-500">
                          <tr>
                            <th className="py-2 font-normal">{tr('接口', 'Operation')}</th>
                            <th className="px-3 font-normal">RPS</th>
                            <th className="px-3 font-normal">{tr('错误率', 'Error rate')}</th>
                            <th className="pl-3 font-normal">P95 (ms)</th>
                          </tr>
                        </thead>
                        <tbody className="divide-y divide-zinc-800">
                          {operations.items.map((row) => {
                            const scope = serviceParams(params, row.identity);
                            if (row.operation) scope.set('operation', row.operation);
                            return (
                              <tr key={row.operation}>
                                <td className="max-w-64 truncate py-3">
                                  <Link
                                    title={row.operation}
                                    className="font-medium hover:underline"
                                    to={traceLink(scope)}
                                  >
                                    {row.operation}
                                  </Link>
                                </td>
                                <td className="px-3">{number(row.rps)}</td>
                                <td className={`px-3 ${row.error_rate ? 'text-red-500' : ''}`}>
                                  {number(row.error_rate, '%')}
                                </td>
                                <td className="pl-3">{number(row.p95_ms)}</td>
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
              <Card className="min-w-0">
                <div className="mb-4 flex items-center justify-between gap-3">
                  <h2 className="text-sm font-medium">
                    {tr('上下游依赖', 'Service dependencies')}
                  </h2>
                  <Link
                    className="inline-flex items-center gap-1 text-xs text-zinc-500"
                    to={viewLink('dependencies')}
                  >
                    {tr('查看拓扑', 'View map')} <ArrowRight size={12} />
                  </Link>
                </div>
                <p className="mb-3 text-xs text-zinc-500">
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
                    <div className="divide-y divide-zinc-800">
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
                          {tr('仅展示流量最高的 5 条关系', 'Showing up to 5 busiest dependencies')}
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
            </div>
            <Card className="flex flex-wrap items-center justify-between gap-4">
              <div>
                <h2 className="text-sm font-medium">{tr('排查请求', 'Investigate requests')}</h2>
                <p className="mt-1 text-xs text-zinc-500">
                  {tr(
                    '沿用当前服务和时间范围，查看链路并关联同请求日志。',
                    'Keep this service and time window when opening traces and correlated logs.',
                  )}
                </p>
              </div>
              <div className="flex flex-wrap gap-4 text-xs">
                <Link
                  className="inline-flex items-center gap-1 hover:underline"
                  to={traceLink(params, 'duration > 1s')}
                >
                  {tr('慢请求 (>1s)', 'Slow requests (>1s)')} <ArrowRight size={12} />
                </Link>
                <Link
                  className="inline-flex items-center gap-1 hover:underline"
                  to={traceLink(params, 'status = error')}
                >
                  {tr('错误请求', 'Errored requests')} <ArrowRight size={12} />
                </Link>
              </div>
            </Card>
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
                <div className="divide-y divide-zinc-800">
                  {diagnostics.checks.map((check) => (
                    <div key={check.key} className="flex justify-between gap-4 py-3 text-xs">
                      <span>
                        {{
                          metrics: tr('请求指标', 'Request metrics'),
                          sampling: tr('采样覆盖率', 'Sampling coverage'),
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
                  <div className="divide-y divide-zinc-800">
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
            <p className="mb-3 text-xs text-zinc-500">
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
                      <th className="p-3" key={v}>
                        {v}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody className="divide-y divide-zinc-800">
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
        {tab === 'alerts' && (
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
                [tr('最少样本请求数', 'Minimum sample requests'), minimum, setMinimum],
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
