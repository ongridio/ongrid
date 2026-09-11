import { FilterField } from '@/components/ui/FilterField';
import { Label, Input } from '@/components/ui';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/Tabs';
import { Hint } from '@/components/ui/Tooltip';
import { Select } from '@/components/ui/Select';
import { lazy, Suspense, useCallback, useEffect, useRef, useState, type MouseEvent } from 'react';
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { ArrowDown, ArrowRight, ArrowUp, ArrowUpRight, RefreshCw } from 'lucide-react';
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { usePoll } from '@/lib/usePoll';
import { listDevices } from '@/api/devices';
import { listAllNodes } from '@/api/topology';
import { TimeRangePicker } from '@/components/ui/TimeRangePicker';
import {
  queryApm,
  serviceParams,
  traceLink,
  type ApmList,
  type ApmOverview,
  type ApmDependencies,
  type ApmRuntime,
} from '@/api/apm';
import { Button, Card, Chip, EmptyState, PageHeader, PaginationFooter } from '@/components/ui';
import { ServiceMap } from '@/components/apm/ServiceMap';
import { Dependencies } from '@/components/apm/Dependencies';
import { ServiceTraces } from '@/components/apm/ErrorTraces';
import { ErrorGroups } from '@/components/apm/ErrorGroups';
import { VersionComparison } from '@/components/apm/VersionComparison';
import { ServiceLogs } from '@/components/apm/ServiceLogs';
import { RepositoryBindingButton } from '@/components/apm/RepositoryBinding';
import { SearchInput } from '@/components/apm/SearchInput';
import { ServiceSwitcher } from '@/components/apm/ServiceSwitcher';
import { RuntimeMetrics } from '@/components/apm/RuntimeMetrics';
import { InstrumentationDiagnostics } from '@/components/apm/InstrumentationDiagnostics';
import { Onboarding } from '@/components/apm/Onboarding';
import { chartTooltipStyle, chartTooltipLabelStyle } from '@/lib/chartTheme';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';
import './Apm.css';

const ServiceProfiles = lazy(() => import('@/components/apm/ServiceProfiles').then((module) => ({ default: module.ServiceProfiles })));

const input = "";
const languageLabels: Record<string, string> = {
  go: 'Go', java: 'Java', nodejs: 'Node.js', webjs: 'JavaScript', javascript: 'JavaScript', python: 'Python',
  dotnet: '.NET', cpp: 'C++', ruby: 'Ruby', php: 'PHP', rust: 'Rust', swift: 'Swift',
};
const languageIcons: Record<string, string> = {
  go: 'go', java: 'java', nodejs: 'nodejs', webjs: 'javascript', javascript: 'javascript',
  python: 'python', dotnet: 'dotnet', cpp: 'cpp', ruby: 'ruby', php: 'php', rust: 'rust', swift: 'swift',
};
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
  rpcOverview?: ApmOverview;
  rpcOperations?: ApmList;
  rpcList?: ApmList;
  dependencies?: ApmDependencies;
  runtime?: ApmRuntime;
};

export default function ApmPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const navigate = useNavigate();
  const location = useLocation();
  const main = useRef<HTMLElement>(null);
  const restoredScroll = useRef('');
  const initialWindow = useRef(true);
  const [params, setParams] = useSearchParams();
  const [refresh, setRefresh] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [resourceOptions, setResourceOptions] = useState<Record<string, { value: string; label: string }[]>>({});
  const [resourceError, setResourceError] = useState(false);
  useEffect(() => {
    let cancelled = false;
    void Promise.allSettled([listDevices(), listAllNodes('cluster').then((items) => ({ items }))]).then((results) => {
      if (cancelled) return;
      setResourceError(results.some((result) => result.status === 'rejected'));
      setResourceOptions(Object.fromEntries(results.map((result, index) => [index === 0 ? 'device_id' : 'cluster_node_id',
        result.status === 'fulfilled' ? (result.value.items || []).map((item) => ({ value: String(item.id), label: `${item.name || item.id} (#${item.id})` })) : [],
      ])));
    });
    return () => { cancelled = true; };
  }, [refresh]);
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
  const combined = detail && !traceMetrics && !operation;
  const scopedReplica = !!(params.get('service_version') || params.get('instance_id') || params.get('device_id') || params.get('cluster_id') || params.get('cluster_node_id'));
  const activeProtocol = params.get('protocol') === 'rpc' ? 'rpc' : 'http';
  const context = new URLSearchParams(params);
  context.delete('list_query');
  // Relative refresh advances time within the same view; a changed identity,
  // protocol, filter or absolute window must never display the previous result.
  if (period !== 'custom') {
    context.delete('start');
    context.delete('end');
  }
  const scope = context.toString();
  // Discovery options survive result refreshes, but never cross service/device scope.
  const optionScope = JSON.stringify([
    detail ? ['service_name', 'service_namespace', 'environment'].map((key) => params.get(key)) : null,
    ...['device_id', 'cluster_id', 'cluster_node_id', 'metric_source'].map((key) => params.get(key)),
  ]);
  const [filterOptions, setFilterOptions] = useState<{
    scope: string;
    instances?: ApmRuntime['instances'];
    environments?: string[];
    service_namespaces?: string[];
  }>({ scope: '' });
  const available = filterOptions.scope === optionScope ? filterOptions : undefined;
  const [results, setResults] = useState<Panels & { scope: string; updated?: number }>({
    scope: '',
  });
  const current = results.scope === scope ? results : undefined;
  const { list, overview, dependencies, runtime } = current || {};
  const instances = [...new Map(
    (runtime?.instances || [])
      .map((instance) => [JSON.stringify([instance.instance_id, instance.version]), instance]),
  ).values()];
  const visibleInstances = instances.filter((item) =>
    (!params.get('service_version') || item.version === params.get('service_version')) &&
    (!params.get('instance_id') || item.instance_id === params.get('instance_id')));
  const versions = [...new Set([...(available?.instances || []).map((item) => item.version), params.get('service_version') || ''].filter(Boolean))].sort();
  const instanceOptions = [...new Set([
    ...(available?.instances || []).filter((item) => !params.get('service_version') || item.version === params.get('service_version')).map((item) => item.instance_id),
    params.get('instance_id') || '',
  ].filter(Boolean))].sort();


  const set = useCallback(
    (key: string, value: string | null) => {
      const next = new URLSearchParams(params);
      if (value === null) next.delete(key);
      else next.set(key, value);
      if (['metric_source', 'protocol', 'metric_format'].includes(key)) {
        next.delete('operation');
        next.delete('span_kind');
      }
      if (key === 'service_version') next.delete('instance_id');
      if (key === 'cluster_node_id') next.delete('cluster_id');
      if (key === 'device_id' || key === 'cluster_node_id') {
        next.delete('instance_id');
        next.delete('service_version');
      }
      if (key.endsWith('_sort')) next.delete(key.replace('_sort', '_page'));
      else if (!key.endsWith('page')) {
        for (const page of ['page', 'http_page', 'rpc_page']) next.delete(page);
      }
      setParams(next, {
        replace: key === 'search',
        state:
          detail && /^(http|rpc)_(page|sort)$/.test(key)
            ? {
                ...location.state,
                apmOperations: { query: next.toString(), scroll: main.current?.scrollTop || 0 },
              }
            : location.state,
      });
    },
    [params, setParams, detail, location.state],
  );
  const changeSearch = useCallback((value: string) => set('search', value), [set]);
  const pickPeriod = (value: string, automatic = false) => {
    const next = new URLSearchParams(params);
    next.set('range', value);
    const duration = periods.find(([key]) => key === value)?.[1];
    if (duration) {
      const now = Date.now();
      next.set('start', new Date(now - duration).toISOString());
      next.set('end', new Date(now).toISOString());
    }
    if (!automatic)
      for (const page of ['page', 'http_page', 'rpc_page']) next.delete(page);
    setParams(next, { replace: automatic, state: location.state });
  };
  usePoll(() => {
    if (!loading) pickPeriod(period, true);
  }, 30_000, periods.some(([key]) => key === period) && !['onboarding', 'alerts', 'errors'].includes(tab));
  useEffect(() => {
    const next = new URLSearchParams(params);
    const duration = periods.find(([key]) => key === params.get('range'))?.[1];
    if (!params.has('start') || !params.has('end') || (initialWindow.current && duration)) {
      const now = Date.now();
      next.set('range', duration ? params.get('range')! : '1h');
      next.set('start', new Date(now - (duration || 3600000)).toISOString());
      next.set('end', new Date(now).toISOString());
    }
    initialWindow.current = false;
    if (!detail && tab === 'services') {
      for (const key of ['environment', 'service_namespace'])
        if (next.get(key) === '') next.delete(key);
    }
    if (next.toString() !== params.toString()) setParams(next, { replace: true });
  }, [params, setParams, detail, tab]);
  useEffect(() => {
    const p = new URLSearchParams(query);
    if (combined || p.get('protocol') === 'all') p.set('protocol', 'http');
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
      protocol?: string,
    ) => {
      tasks.push(
        task
          .then((data) => {
            if (!controller.signal.aborted) {
              if (panel === 'runtime') setFilterOptions({ scope: optionScope, instances: (data as ApmRuntime).instances });
              else if (panel === 'list' && !detail) {
                const facets = data as ApmList;
                setFilterOptions({ scope: optionScope, environments: facets.environments, service_namespaces: facets.service_namespaces });
              }
              setResults((previous) => ({
                ...(previous.scope === scope ? previous : { scope }),
                [panel]: data,
              }));
            }
          })
          .catch((e: Error) => {
            failed = true;
            if (!controller.signal.aborted)
              setError((previous) =>
                [previous, protocol ? `${protocol.toUpperCase()}: ${e.message}` : e.message]
                  .filter(Boolean)
                  .join(' · '),
              );
          }),
      );
    };
    if (!detail && tab === 'map') {
      if (!scopedReplica) {
        p.set('page', '1'); p.set('page_size', '100'); p.set('sort', 'rps');
        p.delete('search');
        fetchPanel(queryApm('services', p, controller.signal), 'list');
        fetchPanel(queryApm('dependencies', p, controller.signal), 'dependencies');
      }
    } else if (!detail) fetchPanel(queryApm('services', p, controller.signal), 'list');
    else if (tab === 'overview' || tab === 'operations') {
      for (const protocol of combined ? ['http', 'rpc'] : [activeProtocol]) {
        const scoped = new URLSearchParams(p);
        scoped.set('protocol', protocol);
        const rpc = combined && protocol === 'rpc';
        if (tab === 'overview') {
          fetchPanel(
            queryApm('overview', scoped, controller.signal),
            rpc ? 'rpcOverview' : 'overview',
            protocol,
          );
          if (!operation) {
            scoped.set('sort', 'p95_ms');
            scoped.set('page', '1');
            scoped.set('page_size', '5');
            fetchPanel(
              queryApm('operations', scoped, controller.signal),
              rpc ? 'rpcOperations' : 'operations',
              protocol,
            );
          }
        } else {
          scoped.set('page', p.get(`${protocol}_page`) || p.get('page') || '1');
          scoped.set('sort', p.get(`${protocol}_sort`) || p.get('sort') || 'rps');
          fetchPanel(
            queryApm('operations', scoped, controller.signal),
            rpc ? 'rpcList' : 'list',
            protocol,
          );
        }
      }
      if (tab === 'overview' && !operation && !scopedReplica)
        fetchPanel(queryApm('dependencies', p, controller.signal), 'dependencies');
    } else if (tab === 'dependencies' && !scopedReplica)
      fetchPanel(queryApm('dependencies', p, controller.signal), 'dependencies');
    if (detail && tab !== 'alerts' && tab !== 'onboarding')
      fetchPanel(queryApm(tab === 'instances' ? 'runtime' : 'instances', p, controller.signal), 'runtime');
    Promise.all(tasks).finally(() => {
      if (!controller.signal.aborted) {
        setLoading(false);
        if (!failed && tasks.length)
          setResults((previous) => ({ ...previous, updated: Date.now() }));
      }
    });
    return () => controller.abort();
  }, [query, tab, detail, refresh, scope, optionScope, combined, activeProtocol, operation, scopedReplica]);
  useEffect(() => {
    if (detail && tab !== 'operations') {
      restoredScroll.current = '';
      return;
    }
    if (!(list || current?.rpcList) || !main.current || restoredScroll.current === scope) return;
    restoredScroll.current = scope;
    const saved = location.state?.[detail ? 'apmOperations' : 'apmList'];
    main.current.scrollTop = saved?.query === query ? saved.scroll : 0;
  }, [list, current?.rpcList, detail, tab, query, scope, location.state]);
  const unset = tr('未设置', 'Unset');
  const status = (s: string) =>
    ({
      observed: tr('已观测', 'Observed'),
      traces_only: tr('仅 Trace · 无 HTTP 维度', 'Traces only · No HTTP dimensions'),
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
    ['instances', tr('实例', 'Instances')],
    ['dependencies', tr('依赖', 'Dependencies')],
    ['traces', tr('链路', 'Traces')],
    ['errors', tr('错误', 'Errors')],
    ['compare', tr('版本对比', 'Compare versions')],
    ['logs', tr('日志', 'Logs')],
    ['profiles', tr('性能剖析', 'Profiling')],
  ];
  const viewLink = (view: string) => {
    if (view === 'operations' && operation && location.state?.apmOperations)
      return `/apm/service?${location.state.apmOperations.query}`;
    const next = new URLSearchParams(params);
    next.set('tab', view);
    next.delete('page');
    if (view !== 'compare') next.delete('operation');
    next.delete('search');
    return `/apm/service?${next}`;
  };
  const traceParams = new URLSearchParams(params);
  const resolvedScope = runtime?.metadata?.resource_scope;
  const traceScopeReady = !params.has('cluster_node_id') || !!resolvedScope;
  if (resolvedScope) {
    traceParams.set('telemetry_cluster_id', resolvedScope.cluster_id);
    traceParams.set('cluster_device_ids', (resolvedScope.device_ids || []).join(','));
  }
  if (combined) traceParams.set('protocol', 'all');
  const logParams = new URLSearchParams(params);
  if (params.has('cluster_node_id')) logParams.set('cluster_id', params.get('cluster_node_id')!);
  const back = new URLSearchParams(params.get('list_query') || params);
  if (!params.has('list_query'))
    for (const key of ['service_name', 'operation', 'tab', 'page', 'sort', 'search'])
      back.delete(key);
  back.delete('list_query');
  async function createAlert(protocol: string) {
    setCreating(true);
    setError('');
    try {
      const p = new URLSearchParams(params);
      p.set('protocol', protocol);
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
  const sortHeading = (key: string, label: string, numeric = false, protocol = '') => {
    const sortKey = detail && protocol ? `${protocol}_sort` : 'sort';
    const active = (params.get(sortKey) || params.get('sort') || 'rps') === key;
    const hint =
      !detail && !traceMetrics
        ? (
            {
              rps: tr('按服务总请求速率排序', 'Sort by total service request rate'),
              error_rate: tr('按服务整体错误率排序', 'Sort by overall service error rate'),
              p95_ms: tr('取 HTTP、RPC 各自 P95 中的较大值，并按此排序；Trace 样本服务使用 HTTP P95。', 'Uses and sorts by the higher HTTP or RPC P95; Trace-sampled services use HTTP P95.'),
            } as Record<string, string>
          )[key]
        : undefined;
    return (
      <th
        className={`px-3 py-2.5 font-normal ${numeric ? 'text-right' : 'pl-4'}`}
        aria-sort={active ? (key === 'name' ? 'ascending' : 'descending') : 'none'}
      >
        <Hint content={hint}><Button variant="subtle" size="sm"
          type="button"

          onClick={() => set(sortKey, key)}
          aria-label={tr(`按${label}排序`, `Sort by ${label}`)}
          className={`inline-flex items-center gap-1 rounded py-0.5 hover:text-zinc-100 ${active ? 'font-medium text-zinc-100' : ''}`}
        >
          {label}
          {active && (key === 'name' ? <ArrowUp size={12} /> : <ArrowDown size={12} />)}
        </Button></Hint>
      </th>
    );
  };
  const resourceFilters = <>
    {([
      ['device_id', tr('设备', 'Device'), tr('全部设备', 'All devices')],
      ['cluster_node_id', tr('集群', 'Cluster'), tr('全部集群', 'All clusters')],
    ] as const).map(([key, label, all]) => {
      const options = resourceOptions[key] || [];
      const selected = params.get(key) || '';
      return <FilterField key={key} label={label} className="max-w-sm">
        <Select aria-label={label} className="max-w-52" value={selected}
          onValueChange={(value) => set(key, value || null)}
          options={[{ value: '', label: all }, ...options,
            ...(selected && !options.some((item) => item.value === selected) ? [{ value: selected, label: `#${selected}` }] : [])]} />
      </FilterField>;
    })}
    {resourceError && <span role="status" className="text-xs text-amber-500">{tr('部分筛选选项加载失败，请刷新重试', 'Some filter options failed to load; refresh to retry')}</span>}
  </>;

  return (
    <Tabs value={operation && ['overview', 'operations'].includes(tab) ? 'operations' : tab} className="contents"><div className="apm-page flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
      <PageHeader
        title={
          detail ? (
            <ServiceSwitcher params={params} navigationState={location.state} />
          ) : (
            tr('服务', 'Services')
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
            <span role="status" className="text-xs text-zinc-500">
              {loading
                ? tr('正在更新…', 'Updating…')
                : current?.updated
                  ? tr(
                      `更新于 ${new Date(current.updated).toLocaleTimeString()}`,
                      `Updated ${new Date(current.updated).toLocaleTimeString()}`,
                    )
                  : ''}
            </span>
            <TimeRangePicker
              value={{ range: period, start: params.get('start') || undefined, end: params.get('end') || undefined }}
              presets={periods.map(([value, durationMs, zh, en]) => ({ value, durationMs, label: tr(zh, en) }))}
              minDurationMs={60000}
              onChange={(selection) => {
                const next = new URLSearchParams(params);
                for (const [key, value] of Object.entries(selection)) next.set(key, value);
                for (const page of ['page', 'http_page', 'rpc_page']) next.delete(page);
                setParams(next, { state: location.state });
              }}
            />
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
          detail ? (
            <div className="flex flex-wrap items-center gap-3">
              {resourceFilters}
              {tab !== 'compare' && <>
              <FilterField label={tr('版本', 'Version')}>
                <Select aria-label={tr('版本', 'Version')} className="w-44" value={params.get('service_version') || ''}
                  onValueChange={(value) => set('service_version', value || null)}
                  options={[{value: '', label: tr('全部版本', 'All versions')}, ...versions.map((value) => ({value, label: value}))]} />
              </FilterField>
              <FilterField label={tr('实例', 'Instance')}>
                <Select aria-label={tr('实例', 'Instance')} className="w-56" value={params.get('instance_id') || ''}
                  onValueChange={(value) => set('instance_id', value || null)}
                  options={[{value: '', label: tr('全部实例', 'All instances')}, ...instanceOptions.map((value) => ({value, label: value}))]} />
              </FilterField>
              <span className="text-xs text-zinc-500">{tr('请求指标与资源指标使用相同筛选', 'Requests and resources share these filters')}</span>
              </>}
            </div>
          ) :
          !detail && ['services', 'map'].includes(tab) && (
            <div className="flex flex-wrap items-center gap-3">
              {tab !== 'map' && resourceFilters}
              {!detail && ['services', 'map'].includes(tab) && (
                <>
                  {(
                    [
                      ['environment', tr('全部环境', 'All environments'), available?.environments],
                      [
                        'service_namespace',
                        tr('全部命名空间', 'All namespaces'),
                        available?.service_namespaces,
                      ],
                    ] as const
                  ).map(([key, label, options]) => (
                    <FilterField key={key} label={key === 'environment' ? tr('环境', 'Environment') : tr('业务命名空间', 'Service namespace')} className="max-w-sm">
                      <Select
                        key={key}
                        aria-label={
                          key === 'environment'
                            ? tr('环境', 'Environment')
                            : tr('业务命名空间', 'Service namespace')
                        }
                        className="h-9 w-auto max-w-52"
                        value={params.get(key) || ''}
                        onValueChange={(selectedValue) => set(key, selectedValue || null)}
                      >
                        <option value="">{label}</option>
                        {[
                          ...new Set([
                            ...(options || []),
                            ...(params.has(key) ? [params.get(key)!] : []),
                          ]),
                        ]
                          .filter(Boolean)
                          .map((value) => (
                            <option key={value} value={value}>
                              {value}
                            </option>
                          ))}
                      </Select>
                    </FilterField>
                  ))}

                  <div className="w-full sm:max-w-sm sm:flex-1" hidden={tab === 'map'}>
                    <SearchInput
                      value={params.get('search') || ''}
                      onChange={changeSearch}
                      label={tr('搜索服务名称…', 'Search services…')}
                    />
                  </div>
                </>
              )}

            </div>
          )
        }
      />
      {!detail && <TabsList activateOnFocus={false} aria-label={tr('服务视图', 'Service views')} className="shrink-0 border-b border-zinc-800 px-6">
        {[['services', tr('服务列表', 'Service list')], ['map', tr('服务地图', 'Service map')]].map(([value, label]) => <TabsTrigger key={value} value={value} onClick={() => set('tab', value)}>{label}</TabsTrigger>)}
      </TabsList>}
      {detail && (
        <TabsList activateOnFocus={false}
          aria-label={tr('服务视图', 'Service views')}
          className="flex shrink-0 flex-wrap items-center gap-x-5 border-b border-zinc-800 px-6"
        >
          {tabs.map(([key, label]) => (
            <TabsTrigger key={key} value={key} nativeButton={false} render={<Link state={location.state} to={viewLink(key)} />} >
              {label}
            </TabsTrigger>
          ))}
          <div className="ml-auto flex items-center gap-4 py-1.5">
            <RepositoryBindingButton key={JSON.stringify([params.get('environment'), params.get('service_namespace'), params.get('service_name')])} identity={{ service_name: params.get('service_name') || '', service_namespace: params.get('service_namespace') || '', environment: params.get('environment') || '' }} canEdit={isAdmin} />
            <Button variant="subtle" size="sm"
              type="button"
              className=""
              onClick={() => set('tab', 'onboarding')}
            >
              {tr('接入管理', 'Instrumentation')}
            </Button>
          </div>
        </TabsList>
      )}
      <TabsContent value={operation && ['overview', 'operations'].includes(tab) ? 'operations' : tab} className="contents"><main ref={main} className="flex-1 space-y-3 overflow-auto px-6 py-4">
        {!detail && tab === 'map' && (scopedReplica ? <EmptyState title={tr('服务地图按服务汇总', 'The service map is service-wide')}
          hint={tr('请清除设备、集群、版本和实例筛选后查看。', 'Clear device, cluster, version and instance filters to view the map.')}
          action={<Button onClick={() => { const next = new URLSearchParams(params); for (const key of ['device_id', 'cluster_id', 'cluster_node_id', 'service_version', 'instance_id']) next.delete(key); setParams(next); }}>{tr('清除不支持的筛选', 'Clear unsupported filters')}</Button>} />
          : <ServiceMap list={list} dependencies={dependencies} params={params} loading={loading} />)}
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
            <div className="flex flex-wrap items-center gap-2">
              {isAdmin && !traceMetrics && overview?.metadata?.metric_source !== 'tempo_spanmetrics' && (
                <Button variant="ghost" size="sm"
                  type="button"
                  onClick={() => set('tab', 'alerts')}
                >
                  {tr('创建告警', 'Create alert')}
                </Button>
              )}
              {traceScopeReady && <>
              <Link
                className="og-button" data-slot="button" data-variant="ghost" data-size="sm"
                to={traceLink(traceParams)}
              >
                {tr('查看链路', 'View traces')}
                <ArrowUpRight size={13} />
              </Link>
              <Link
                className="og-button" data-slot="button" data-variant="ghost" data-size="sm"
                to={traceLink(traceParams, 'duration > 1s')}
              >
                {tr('慢链路 (>1s)', 'Slow traces (>1s)')}
                <ArrowUpRight size={13} aria-hidden="true" />
              </Link>
              <Link
                className="og-button" data-slot="button" data-variant="ghost" data-size="sm"
                to={traceLink(traceParams, 'status = error')}
              >
                {tr('错误链路', 'Error traces')}
                <ArrowUpRight size={13} aria-hidden="true" />
              </Link>
              </>}
              <Link
                className="og-button" data-slot="button" data-variant="ghost" data-size="sm"
                to={`/logs?${logParams}`}
              >
                {scopedReplica ? tr('服务日志（全部实例）', 'Service logs (all instances)') : tr('服务日志', 'Service logs')}
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
        {loading && tab !== 'map' &&
          !list &&
          !overview &&
          !current?.rpcList &&
          !current?.rpcOverview &&
          !dependencies &&
          !runtime && (
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
            </div>
            {detail ? (
              <Tabs defaultValue="diagnostics" className="space-y-3">
                <TabsList aria-label={tr('接入管理', 'Instrumentation management')}>
                  <TabsTrigger value="diagnostics">{tr('接入诊断', 'Diagnostics')}</TabsTrigger>
                  <TabsTrigger value="guide">{tr('接入指南', 'Setup guide')}</TabsTrigger>
                </TabsList>
                <TabsContent value="diagnostics">
                  <InstrumentationDiagnostics params={params} refresh={refresh} />
                </TabsContent>
                <TabsContent value="guide"><Onboarding /></TabsContent>
              </Tabs>
            ) : <Onboarding />}
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
        {(['services', 'overview', 'operations'].includes(tab)
          ? combined
            ? ['http', 'rpc']
            : [detail ? activeProtocol : '']
          : []
        ).map((protocol) => {
          const rpc = combined && protocol === 'rpc';
          const overview = rpc ? current?.rpcOverview : current?.overview;
          const operations = rpc ? current?.rpcOperations : current?.operations;
          const list = rpc ? current?.rpcList : current?.list;
          if (detail && ['no_data', 'traces_only'].includes(overview?.summary.data_status || '')) return null;
          return (
            <section
              key={protocol}
              aria-label={detail && !traceMetrics ? protocol.toUpperCase() : undefined}
              className="space-y-3"
            >
              {detail && !traceMetrics && (tab === 'overview' || tab === 'operations') && (
                <h2 className="text-sm font-semibold">{protocol.toUpperCase()}</h2>
              )}
              {(overview?.metadata?.metric_source === 'tempo_spanmetrics' || list?.metadata?.metric_source === 'tempo_spanmetrics') && (
                <p className="text-xs text-zinc-500">{tr(
                  '以下指标来自 Trace 样本，受采样影响；RPS 为样本速率，错误率和延迟仅代表已采样请求。',
                  'These metrics use Trace samples. RPS is the sampled rate; errors and latency describe sampled requests only.',
                )}</p>
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
                      <Button variant="subtle" size="sm"
                        type="button"
                        className=""
                        onClick={() => set('tab', 'onboarding')}
                      >
                        {tr('接入管理', 'Instrumentation')}
                      </Button>
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
                      <table className="w-full min-w-[960px] table-fixed text-left text-sm">
                        <colgroup>
                          {(!detail
                            ? ['22%', '12%', '12%', '8%', '9%', '12%', '12%', '13%']
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
                              false,
                              protocol,
                            )}
                            {!detail && (
                              <>
                                <th className="px-3 py-3 font-normal">
                                  {tr('环境', 'Environment')}
                                </th>
                                <th className="px-3 py-3 font-normal">
                                  {tr('命名空间', 'Namespace')}
                                </th>
                              </>
                            )}
                            {sortHeading('rps', 'RPS', true, protocol)}
                            {sortHeading('error_rate', tr('错误率', 'Error rate'), true, protocol)}
                            {sortHeading(
                              'p95_ms',
                              'P95 (ms)',
                              true,
                              protocol,
                            )}
                            <th className="px-4 py-3 font-normal">
                              {tr('数据状态', 'Data status')}
                            </th>
                            {!detail && <th className="px-4 py-3 font-normal">{tr('代码仓库', 'Repository')}</th>}
                          </tr>
                        </thead>
                        {list.items.map((row) => {
                          const target = serviceParams(
                            params,
                            row.identity,
                            detail ? protocol : undefined,
                          );
                          if (row.operation) {
                            target.set('operation', row.operation);
                            if (row.metric_source === 'tempo_spanmetrics') target.set('metric_source', row.metric_source);
                          }
                          const to = `/apm/service?${target}`;
                          return (
                            <tbody
                              key={JSON.stringify([row.identity, row.operation])}
                              className="border-b border-[rgb(var(--border))] last:border-0 hover:bg-zinc-900/40"
                            >
                              <tr>
                                <td className="px-4 py-4 align-top">
                                  <div className="flex items-center gap-2">
                                    <Link
                                      className="min-w-0 break-words font-medium text-zinc-100 hover:text-indigo-500 hover:underline"
                                      state={location.state}
                                      to={to}
                                      onClick={(event) => openService(event, to)}
                                    >
                                      {detail ? row.operation : row.identity.service_name}
                                    </Link>
                                    {!detail && (
                                      <Chip className="shrink-0" title={tr('编程语言', 'Programming language')}>
                                        {row.languages?.length
                                          ? row.languages.map((language, index) => (
                                            <span key={language} className="inline-flex items-center gap-1">
                                              {index > 0 && <span className="mx-1">/</span>}
                                              {languageIcons[language] && <img src={`/icons/languages/${languageIcons[language]}.svg`} alt="" aria-hidden="true" width={14} height={14} className={`h-3.5 w-3.5 shrink-0${language === 'rust' ? ' rounded-full bg-white' : ''}`} />}
                                              {languageLabels[language] || language}
                                            </span>
                                          ))
                                          : tr('未知', 'Unknown')}
                                      </Chip>
                                    )}
                                  </div>
                                </td>
                                {!detail && (
                                  <>
                                    <Hint content={row.identity.environment || unset}><td
                                      className="truncate px-3 py-4 align-top text-zinc-400"

                                    >
                                      {row.identity.environment || unset}
                                    </td></Hint>
                                    <Hint content={row.identity.service_namespace || unset}><td
                                      className="truncate px-3 py-4 align-top text-zinc-400"

                                    >
                                      {row.identity.service_namespace || unset}
                                    </td></Hint>
                                  </>
                                )}
                                <td className="px-3 py-3 text-right tabular-nums">
                                  {number(row.rps)}
                                </td>
                                <td
                                  className={`px-3 py-3 text-right tabular-nums ${row.error_rate != null && row.error_rate > 0 ? 'text-red-500' : 'text-zinc-400'}`}
                                >
                                  {number(row.error_rate, '%')}
                                </td>
                                <td className="px-3 py-3 text-right tabular-nums">
                                  {detail ? number(row.p95_ms) : (
                                    <div className="inline-grid grid-cols-[auto_auto] gap-x-3 gap-y-1 text-xs">
                                      {(['http', 'rpc'] as const).filter((protocol) =>
                                        row.protocols?.some((item) => item.protocol === protocol)
                                        || (protocol === 'http' && row.metric_source === 'tempo_spanmetrics' && row.data_status !== 'traces_only' && row.data_status !== 'no_data'),
                                      ).map((protocol) => (
                                        <div key={protocol} className="contents">
                                          <span className="text-left text-zinc-500">{protocol.toUpperCase()}</span>
                                          <span>{number(row.protocols?.find((item) => item.protocol === protocol)?.p95_ms
                                            ?? (protocol === 'http' && row.metric_source === 'tempo_spanmetrics' ? row.p95_ms : null))}</span>
                                        </div>
                                      ))}
                                    </div>
                                  )}
                                </td>
                                <td className="px-4 py-3 text-xs text-zinc-500">
                                  <span
                                    className={`mr-1.5 inline-block h-1.5 w-1.5 rounded-full ${row.data_status === 'observed' ? 'bg-zinc-500' : 'bg-amber-500'}`}
                                  />
                                  {row.metric_source === 'tempo_spanmetrics' && row.data_status !== 'traces_only' && `${tr('Trace 样本', 'Trace samples')} · `}
                                  {status(row.data_status)}
                                </td>
                                {!detail && <td className="px-3 py-3"><RepositoryBindingButton identity={row.identity} canEdit={isAdmin} /></td>}
                              </tr>
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
                      onPageChange={(p) => set(detail ? `${protocol}_page` : 'page', String(p + 1))}
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
                            <Select
                              aria-label={tr('延迟分位数', 'Latency percentile')}
                              className="max-w-full"
                              value={latency}
                              onValueChange={(selectedValue) => setLatency(selectedValue as typeof latency)}
                            >
                              <option value="p95_ms">{tr('P95 延迟', 'P95 latency')}</option>
                              <option value="p50_ms">{tr('P50 延迟', 'P50 latency')}</option>
                              <option value="p99_ms">{tr('P99 延迟', 'P99 latency')}</option>
                            </Select>
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
                                  const scope = serviceParams(params, row.identity, protocol);
                                  if (row.operation) scope.set('operation', row.operation);
                                  if (row.metric_source === 'tempo_spanmetrics') scope.set('metric_source', row.metric_source);
                                  return (
                                    <tr key={row.operation}>
                                      <td className="max-w-64 break-words py-2.5">
                                        <Hint content={row.operation}><Link

                                          className="font-medium hover:text-indigo-500 hover:underline"
                                          state={{
                                            ...location.state,
                                            apmOperations: undefined,
                                          }}
                                          to={`/apm/service?${scope}`}
                                        >
                                          {row.operation}
                                        </Link></Hint>
                                      </td>
                                      <td className="px-3 text-right tabular-nums">
                                        {number(row.rps)}
                                      </td>
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
                            title={tr(
                              '当前范围未观测到接口',
                              'No operations observed in this window',
                            )}
                          />
                        ))}
                    </Card>
                  </div>
                </>
              )}
            </section>
          );
        })}
        {detail && tab === 'traces' && traceScopeReady && <ServiceTraces params={traceParams} refresh={refresh} />}
        {detail && tab === 'errors' && traceScopeReady && <ErrorGroups params={traceParams} refresh={refresh} />}
        {detail && tab === 'compare' && <VersionComparison params={params} versions={versions} refresh={refresh} onChange={(next) => setParams(next, { state: location.state })} />}
        {detail && ['traces', 'errors'].includes(tab) && !traceScopeReady && !loading && (
          <EmptyState title={tr('集群范围尚未解析', 'Cluster scope is not resolved')} hint={tr('请刷新后重试，避免查询到其他集群的链路。', 'Refresh to retry resolving the cluster scope.')} />
        )}
        {detail && tab === 'logs' && <ServiceLogs params={logParams} refresh={refresh} />}
        {detail && tab === 'profiles' && <Suspense fallback={<p role="status" className="text-sm text-zinc-500">{tr('正在加载性能剖析…', 'Loading profiling…')}</p>}><ServiceProfiles params={params} instances={visibleInstances} loading={loading} refresh={refresh} /></Suspense>}
        {detail && tab === 'overview' && !operation && (
          <>
            <div className="space-y-3">
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
                                    <Hint content={peer.service_name}><Link

                                      className="truncate font-medium hover:underline"
                                      state={location.state}
                                      to={`/apm/service?${serviceParams(params, peer)}`}
                                    >
                                      {peer.service_name}
                                    </Link></Hint>
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
                          '暂未观测到可识别的服务依赖',
                          'No identifiable service dependencies observed',
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
        {tab === 'instances' && (
              <Card>
                <h2 className="text-sm font-medium">
                  {tr('观测到的实例', 'Observed instances')}
                </h2>
                {visibleInstances.length === 0 ? (
                  <EmptyState
                    title={tr('未观测到实例关联字段', 'No instance identity observed')}
                  />
                ) : (
                  <div className="divide-y divide-[rgb(var(--border))]">
                    {visibleInstances.map((instance, i) => (
                      <div
                        key={i}
                        className="flex flex-wrap items-center justify-between gap-3 py-3 text-xs"
                      >
                        <span>
                          <Button variant="link" size="sm" onClick={() => { const next = new URLSearchParams(params); next.set('instance_id', instance.instance_id); if (instance.version) next.set('service_version', instance.version); next.set('tab', 'instances'); setParams(next); }}>{instance.instance_id || instance.pod || unset}</Button> ·{' '}
                          {instance.version || unset} · {tr('设备', 'Device')}{' '}
                          {instance.device_id || unset}
                        </span>
                        {instance.device_id && (
                          <Link
                            className="underline"
                            to={`/apm/service?${new URLSearchParams({ ...Object.fromEntries(params), tab: 'profiles', device_id: instance.device_id, instance_id: instance.instance_id })}`}
                          >
                            {tr('性能剖析', 'Profiling')}
                          </Link>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </Card>
        )}
        {runtime && tab === 'instances' && <RuntimeMetrics data={runtime} />}
        {tab === 'dependencies' && scopedReplica && (
          <EmptyState title={tr('依赖图按服务汇总', 'Dependency graphs are service-wide')}
            hint={tr('请选择全部设备、集群、版本和实例查看服务依赖。', 'Select all devices, clusters, versions and instances to view dependencies.')} />
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
              <Label className="grid gap-1 text-xs">
                {tr('指标', 'Metric')}
                <Select
                  className="h-9 w-auto"
                  value={metric}
                  onValueChange={(selectedValue) => {
                    setMetric(selectedValue);
                    setThreshold(selectedValue === 'p95_ms' ? '500' : '5');
                  }}
                >
                  <option value="error_rate">{tr('错误率 (%)', 'Error rate (%)')}</option>
                  <option value="p95_ms">P95 (ms)</option>
                </Select>
              </Label>
              {[
                [tr('阈值', 'Threshold'), threshold, setThreshold],
                [tr('最少请求数', 'Minimum requests'), minimum, setMinimum],
                [tr('持续秒数（30 的倍数）', 'Duration (multiples of 30s)'), dwell, setDwell],
              ].map(([label, value, setter]) => (
                <Label key={String(label)} className="grid gap-1 text-xs">
                  {String(label)}
                  <Input
                    type="number"
                    min="0"
                    className={input}
                    value={String(value)}
                    onChange={(e) => (setter as (s: string) => void)(e.target.value)}
                  />
                </Label>
              ))}
            </div>
            <div className="flex gap-3">
              {(combined ? ['http', 'rpc'] : [activeProtocol]).map((protocol) => (
                <Button
                  key={protocol}
                  disabled={creating || !isAdmin}
                  onClick={() => createAlert(protocol)}
                >
                  {tr(
                    `预览 ${protocol.toUpperCase()} 规则`,
                    `Preview ${protocol.toUpperCase()} rule`,
                  )}
                </Button>
              ))}
            </div>
            {!isAdmin && (
              <p className="text-xs text-zinc-500">
                {tr('只有管理员可创建规则。', 'Only admins can create rules.')}
              </p>
            )}
          </Card>
        )}
      </main></TabsContent>
    </div></Tabs>
  );
}
