import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import {
  Activity,
  ArrowLeft,
  ArrowRight,
  ArrowUpRight,
  Bot,
  CheckCircle2,
  CircleHelp,
  Info,
  ListTree,
  MessageSquare,
  Plug,
  RefreshCw,
  Search,
  TriangleAlert,
} from 'lucide-react';
import {
  getAgentTrace,
  queryAgentRuns,
  type AgentList,
  type AgentRun,
  type AgentSession,
  type AgentTrace,
} from '@/api/agent-observability';
import {
  Button,
  Card,
  Chip,
  EmptyState,
  FilterField,
  Input,
  PageHeader,
  PaginationFooter,
  Select,
} from '@/components/ui';
import { TimeRangePicker } from '@/components/ui/TimeRangePicker';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/Tabs';
import { useI18n } from '@/i18n/locale';
import { absoluteWindow, localDateTime } from '@/lib/telemetryContext';
import { AgentSetup } from '@/components/agent-observability/AgentSetup';
import { AgentTraceDetail } from '@/components/agent-observability/AgentTraceDetail';
import {
  KindIcon,
  RunStatus,
} from '@/components/agent-observability/AgentParts';
import {
  duration,
  identityKey,
  runKey,
  runTitle,
  contentStateLabel,
  sdkLabels,
  tokens,
} from '@/components/agent-observability/model';
import { issueLabel } from '@/components/agent-observability/diagnostics';
import './AgentObservability.css';

const pageSize = 20;

export default function AgentObservability() {
  const { tr } = useI18n();
  const [params, setParams] = useSearchParams();
  const [initialWindow] = useState(() => ({
    start: new Date(Date.now() - 3600000).toISOString(),
    end: new Date().toISOString(),
  }));
  const [refresh, setRefresh] = useState(0);
  const [data, setData] = useState<AgentList>();
  const [trace, setTrace] = useState<AgentTrace>();
  const [loading, setLoading] = useState(false);
  const [traceLoading, setTraceLoading] = useState(false);
  const [error, setError] = useState('');
  const [traceError, setTraceError] = useState('');
  const [setup, setSetup] = useState(false);
  const [search, setSearch] = useState('');
  const [sdk, setSDK] = useState('');
  const [model, setModel] = useState('');
  const [page, setPage] = useState(0);
  const traceID = params.get('trace_id') || '';
  const tab = ['runs', 'sessions', 'diagnostics'].includes(
    params.get('tab') || '',
  )
    ? params.get('tab')!
    : 'runs';
  const window = absoluteWindow(params) || initialWindow;
  const range = params.get('range') || (params.has('start') ? 'custom' : '1h');
  const selectedIdentity = params.has('service_name')
    ? {
        service_name: params.get('service_name')!,
        service_namespace: params.get('service_namespace') || '',
        environment: params.get('environment') || '',
      }
    : null;
  const query = new URLSearchParams(window);
  for (const key of [
    'service_name',
    'service_namespace',
    'environment',
    'session_id',
  ])
    if (params.has(key)) query.set(key, params.get(key)!);
  if (params.get('session_id')) {
    for (const key of ['service_name', 'service_namespace', 'environment']) {
      const value = params.get(`session_${key}`);
      if (value != null) query.set(key, value);
    }
  }
  const queryKey = query.toString();
  const sessionID = params.get('session_id') || '';
  const changeParams = (values: Record<string, string | null>) => {
    setParams((current) => {
      const next = new URLSearchParams(current);
      for (const [key, value] of Object.entries(values)) {
        if (value == null) next.delete(key);
        else next.set(key, value);
      }
      if (values.session_id === null) {
        for (const key of ['service_name', 'service_namespace', 'environment'])
          next.delete(`session_${key}`);
      }
      return next;
    });
  };
  useEffect(() => {
    if (traceID) return;
    const controller = new AbortController();
    setLoading(true);
    setError('');
    setData(undefined);
    void queryAgentRuns(new URLSearchParams(queryKey), controller.signal)
      .then((result) => {
        if (controller.signal.aborted) return;
        setData(result);
        setPage(0);
      })
      .catch((err: Error) => {
        if (!controller.signal.aborted) {
          setError(err.message);
          setData(undefined);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [queryKey, refresh, traceID]);
  useEffect(() => {
    if (!traceID) {
      setTrace(undefined);
      setTraceError('');
      setTraceLoading(false);
      return;
    }
    const controller = new AbortController();
    setTrace(undefined);
    setTraceLoading(true);
    setTraceError('');
    void getAgentTrace(traceID, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) setTrace(result);
      })
      .catch((err: Error) => {
        if (!controller.signal.aborted) setTraceError(err.message);
      })
      .finally(() => {
        if (!controller.signal.aborted) setTraceLoading(false);
      });
    return () => controller.abort();
  }, [traceID, refresh]);
  useEffect(() => setPage(0), [search, sdk, model, tab]);
  const runs = useMemo(
    () =>
      (data?.items || []).filter(
        (run) =>
          (!sdk || run.sdks.includes(sdk)) &&
          (!model || run.models.includes(model)) &&
          (!search ||
            `${runTitle(run)} ${run.name} ${run.input_preview} ${run.output_preview} ${run.trace_id} ${run.session_id} ${run.identity.service_name}`
              .toLowerCase()
              .includes(search.toLowerCase())),
      ),
    [data, search, sdk, model],
  );
  const sessions = (data?.sessions || []).filter(
    (session) =>
      !search ||
      `${session.session_id} ${session.identity.service_name}`
        .toLowerCase()
        .includes(search.toLowerCase()),
  );
  const apps = data?.facets.applications.slice() || [];
  if (
    selectedIdentity &&
    !apps.some(
      (identity) => identityKey(identity) === identityKey(selectedIdentity),
    )
  )
    apps.push(selectedIdentity);
  const sortedDurations = (data?.items || [])
    .filter((run) => !run.issues.includes('invalid_end_time'))
    .map((run) => run.duration_ms)
    .sort((a, b) => a - b);
  const median = sortedDurations.length
    ? (sortedDurations[Math.floor((sortedDurations.length - 1) / 2)] +
        sortedDurations[Math.floor(sortedDurations.length / 2)]) /
      2
    : null;
  const issueCount =
    data?.checks.filter((check) => check.status === 'incomplete').length || 0;
  const toTrace = (run: AgentRun) => {
    const next = new URLSearchParams(params);
    next.set('trace_id', run.trace_id);
    next.set('entry_span_id', run.entry_span_id);
    next.delete('step_id');
    return `/agent-observability?${next}`;
  };
  const toSession = (session: AgentSession) => {
    const next = new URLSearchParams(params);
    next.set('tab', 'sessions');
    next.set('session_id', session.session_id);
    for (const [key, value] of Object.entries(session.identity))
      next.set(`session_${key}`, value);
    return `/agent-observability?${next}`;
  };
  const reload = () => {
    if (!traceID && range !== 'custom') {
      const ranges: Record<string, number> = {
        '15m': 900000,
        '1h': 3600000,
        '6h': 21600000,
        '24h': 86400000,
      };
      const now = Date.now();
      changeParams({
        start: new Date(now - (ranges[range] || 3600000)).toISOString(),
        end: new Date(now).toISOString(),
      });
    }
    setRefresh((value) => value + 1);
  };
  return (
    <div className="agent-page flex h-full min-h-0 min-w-0 flex-1 flex-col">
      {!traceID && (
        <PageHeader
          title={tr('Agent', 'Agent')}
          subtitle={tr(
            '追踪推理、工具调用与每一轮对话。',
            'Trace reasoning, tool calls, and every conversation turn.',
          )}
          actions={
            <>
              <Button
                size="icon"
                aria-label={tr('刷新', 'Refresh')}
                title={tr('刷新', 'Refresh')}
                disabled={traceID ? traceLoading : loading}
                onClick={reload}
              >
                <RefreshCw
                  size={15}
                  className={
                    (traceID ? traceLoading : loading) ? 'animate-spin' : ''
                  }
                />
              </Button>
              <Button
                variant="primary"
                aria-label={tr('接入应用', 'Connect application')}
                onClick={() => setSetup(true)}
              >
                <Plug size={14} />
                <span className="hidden sm:inline">
                  {tr('接入应用', 'Connect application')}
                </span>
              </Button>
            </>
          }
          extra={
            !traceID && (
              <div className="flex flex-wrap items-center gap-2">
                <FilterField
                  label={tr('应用', 'Application')}
                  className="w-full sm:w-80"
                >
                  <Select
                    label={tr('应用范围', 'Application scope')}
                    value={
                      selectedIdentity ? identityKey(selectedIdentity) : ''
                    }
                    onValueChange={(value) => {
                      const identity = apps.find(
                        (item) => identityKey(item) === value,
                      );
                      changeParams({
                        service_name: identity?.service_name ?? null,
                        service_namespace: identity?.service_namespace ?? null,
                        environment: identity?.environment ?? null,
                        session_id: null,
                      });
                    }}
                    options={[
                      { value: '', label: tr('全部应用', 'All applications') },
                      ...apps.map((identity) => ({
                        value: identityKey(identity),
                        label: `${identity.service_name || tr('未命名应用', 'Unnamed application')} · ${identity.service_namespace || '—'} · ${identity.environment || '—'}`,
                      })),
                    ]}
                  />
                </FilterField>
                <TimeRangePicker
                  value={{ ...window, range }}
                  onChange={(value) => changeParams(value)}
                  presets={[
                    {
                      value: '15m',
                      label: tr('最近 15 分钟', 'Last 15 minutes'),
                      durationMs: 900000,
                    },
                    {
                      value: '1h',
                      label: tr('最近 1 小时', 'Last hour'),
                      durationMs: 3600000,
                    },
                    {
                      value: '6h',
                      label: tr('最近 6 小时', 'Last 6 hours'),
                      durationMs: 21600000,
                    },
                    {
                      value: '24h',
                      label: tr('最近 24 小时', 'Last 24 hours'),
                      durationMs: 86400000,
                    },
                  ]}
                />
                <span className="ml-auto flex items-center gap-1.5 text-xs text-text-faint">
                  <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
                  OpenTelemetry
                </span>
              </div>
            )
          }
        />
      )}
      <main
        className={`agent-page-body min-h-0 min-w-0 flex-1 overflow-y-auto ${traceID ? 'agent-page-detail' : ''}`}
      >
        {traceID ? (
          <>
            <div className="agent-detail-toolbar">
              <Button
                variant="subtle"
                size="sm"
                onClick={() =>
                  changeParams({
                    trace_id: null,
                    entry_span_id: null,
                    step_id: null,
                  })
                }
              >
                <ArrowLeft size={14} />
                {tr('Agent', 'Agent')}
              </Button>
              <span className="text-text-faint">/</span>
              <span className="text-xs text-text-muted">
                {tr('运行详情', 'Run details')}
              </span>
              <Button
                size="sm"
                variant="subtle"
                className="ml-auto"
                disabled={traceLoading}
                onClick={reload}
                aria-label={tr('刷新', 'Refresh')}
              >
                <RefreshCw size={14} />
                {tr('刷新', 'Refresh')}
              </Button>
            </div>
            {traceLoading && (
              <Card>
                <EmptyState
                  icon={ListTree}
                  title={tr('正在读取执行链…', 'Loading execution trace…')}
                />
              </Card>
            )}
            {traceError && (
              <Card>
                <EmptyState
                  icon={TriangleAlert}
                  title={tr('无法读取链路', 'Could not load trace')}
                  hint={traceError}
                  action={
                    <Button onClick={reload}>{tr('重试', 'Retry')}</Button>
                  }
                />
              </Card>
            )}
            {trace && (
              <AgentTraceDetail
                key={trace.trace_id}
                trace={trace}
                entrySpanID={params.get('entry_span_id') || undefined}
                selectedSpanID={params.get('step_id') || undefined}
                onSelectSpan={(id) => changeParams({ step_id: id })}
                onSelectRun={(id) =>
                  changeParams({ entry_span_id: id, step_id: id })
                }
              />
            )}
          </>
        ) : (
          <Tabs
            value={tab}
            onValueChange={(value) =>
              changeParams({
                tab: String(value),
                session_id: value === 'sessions' ? sessionID || null : null,
              })
            }
          >
            <TabsList className="mb-5">
              <TabsTrigger value="runs">
                <ListTree size={14} />
                {tr('运行', 'Runs')}
                <span className="agent-tab-count">
                  {data?.items.length ?? '—'}
                </span>
              </TabsTrigger>
              <TabsTrigger value="sessions">
                <MessageSquare size={14} />
                {tr('会话', 'Sessions')}
                <span className="agent-tab-count">
                  {data?.sessions.length ?? '—'}
                </span>
              </TabsTrigger>
              <TabsTrigger value="diagnostics">
                <Activity size={14} />
                {tr('接入诊断', 'Diagnostics')}
                {issueCount > 0 && (
                  <span className="agent-tab-count">{issueCount}</span>
                )}
              </TabsTrigger>
            </TabsList>
            {loading && (
              <div
                role="status"
                className="mb-4 flex items-center gap-2 text-xs text-text-muted"
              >
                <RefreshCw size={13} className="animate-spin" />
                {tr('正在检索 Agent 链路…', 'Searching Agent traces…')}
              </div>
            )}
            {error && (
              <Card>
                <EmptyState
                  icon={TriangleAlert}
                  title={tr('查询失败', 'Query failed')}
                  hint={error}
                  action={
                    <Button onClick={reload}>{tr('重试', 'Retry')}</Button>
                  }
                />
              </Card>
            )}
            {data && (
              <>
                {data.metadata.partial && (
                  <div className="agent-notice mb-4" role="status">
                    <TriangleAlert
                      size={16}
                      className="shrink-0 text-amber-500"
                    />
                    <div>
                      <p className="text-sm font-medium text-text">
                        {tr('当前展示部分结果', 'Showing partial results')}
                      </p>
                      <p className="mt-1 text-xs text-text-muted">
                        {data.metadata.reasons
                          .map((reason) => issueLabel(reason, tr))
                          .join(' · ')}
                        。
                        {tr(
                          '缩小时间范围或选择应用后重试。',
                          'Narrow the time range or select an application and retry.',
                        )}
                      </p>
                    </div>
                  </div>
                )}
                <TabsContent value="runs">
                  <div className="agent-summary-strip">
                    <div>
                      <span>{tr('检索到的运行', 'Retrieved runs')}</span>
                      <strong>{data.items.length}</strong>
                    </div>
                    <div>
                      <span>{tr('失败运行', 'Failed runs')}</span>
                      <strong>
                        {
                          data.items.filter((run) => run.status === 'error')
                            .length
                        }
                      </strong>
                    </div>
                    <div>
                      <span>{tr('耗时中位数', 'Median latency')}</span>
                      <strong>{median == null ? '—' : duration(median)}</strong>
                    </div>
                    <div>
                      <span>{tr('当前范围的会话', 'Sessions in scope')}</span>
                      <strong>{data.sessions.length}</strong>
                    </div>
                  </div>
                  <Card className="!p-0">
                    <div className="flex flex-wrap items-center gap-2 border-b border-border p-3">
                      <div className="relative min-w-0 flex-1 basis-56">
                        <Search
                          size={14}
                          className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-faint"
                        />
                        <Input
                          type="search"
                          aria-label={tr('搜索运行', 'Search runs')}
                          value={search}
                          onChange={(event) => setSearch(event.target.value)}
                          placeholder={tr(
                            '搜索问题、回答、运行名称或 ID…',
                            'Search question, reply, name or ID…',
                          )}
                          className="w-full pl-9"
                        />
                      </div>
                      <Select
                        label="SDK"
                        value={sdk}
                        onValueChange={setSDK}
                        options={[
                          { value: '', label: tr('全部 SDK', 'All SDKs') },
                          ...data.facets.sdks.map((value) => ({
                            value,
                            label: sdkLabels[value] || value,
                          })),
                        ]}
                      />
                      <Select
                        label={tr('模型', 'Model')}
                        value={model}
                        onValueChange={setModel}
                        options={[
                          { value: '', label: tr('全部模型', 'All models') },
                          ...data.facets.models.map((value) => ({
                            value,
                            label: value,
                          })),
                        ]}
                        className="max-w-full sm:max-w-56"
                      />
                    </div>
                    {runs.length ? (
                      <div className="agent-table-scroll">
                        <table className="og-resource-table agent-runs-table">
                          <thead>
                            <tr>
                              {[
                                tr('运行', 'Run'),
                                tr('输入', 'Input'),
                                tr('输出', 'Output'),
                                tr('耗时', 'Latency'),
                                tr('输入 Token', 'Input tokens'),
                                tr('输出 Token', 'Output tokens'),
                                tr('开始时间', 'Started'),
                                'SDK',
                              ].map((label) => (
                                <th key={label}>{label}</th>
                              ))}
                            </tr>
                          </thead>
                          <tbody>
                            {runs
                              .slice(page * pageSize, (page + 1) * pageSize)
                              .map((run) => (
                                <tr key={runKey(run)}>
                                  <td>
                                    <div className="flex items-start gap-2.5">
                                      <span className="mt-1">
                                        <KindIcon
                                          kind={
                                            run.kinds.includes('agent')
                                              ? 'agent'
                                              : 'workflow'
                                          }
                                        />
                                      </span>
                                      <div className="min-w-0 flex-1">
                                        <div className="flex min-w-0 items-center gap-2">
                                          <Link
                                            className="agent-run-link min-w-0 flex-1"
                                            to={toTrace(run)}
                                            title={runTitle(run)}
                                          >
                                            {runTitle(run) ||
                                              tr('未命名运行', 'Unnamed run')}
                                          </Link>
                                          <RunStatus status={run.status} errors={run.error_count} />
                                        </div>
                                        <div
                                          className="mt-1 truncate text-xs text-text-faint"
                                          title={[
                                            runTitle(run) !== run.name &&
                                              run.name,
                                            run.identity.service_name,
                                          ]
                                            .filter(Boolean)
                                            .join(' · ')}
                                        >
                                          {runTitle(run) !== run.name && (
                                            <>{run.name} · </>
                                          )}
                                          {run.identity.service_name} ·{' '}
                                          {run.step_count} {tr('步骤', 'steps')}
                                        </div>
                                      </div>
                                    </div>
                                  </td>
                                  <td>
                                    <div
                                      className="agent-preview"
                                      title={run.input_preview}
                                    >
                                      {run.input_preview ||
                                        contentStateLabel(run.input_state, tr)}
                                    </div>
                                  </td>
                                  <td>
                                    <div
                                      className="agent-preview"
                                      title={run.output_preview}
                                    >
                                      {run.output_preview ||
                                        contentStateLabel(run.output_state, tr)}
                                    </div>
                                  </td>
                                  <td className="whitespace-nowrap font-mono text-xs">
                                    {duration(run.duration_ms)}
                                  </td>
                                  {(['input_tokens', 'output_tokens'] as const).map((field) => (
                                    <td
                                      key={field}
                                      className="whitespace-nowrap font-mono text-xs"
                                      title={run.usage.conflict
                                        ? tr('用量冲突，未汇总', 'Conflicting usage; total withheld')
                                        : tr(`总 Token：${tokens(run.usage.total_tokens)}`, `Total tokens: ${tokens(run.usage.total_tokens)}`)}
                                    >
                                      {tokens(run.usage[field])}
                                      {run.usage.conflict && field === 'input_tokens' && (
                                        <TriangleAlert
                                          size={12}
                                          aria-label={tr('用量待核对', 'Usage conflict')}
                                          className="ml-1 inline text-amber-500"
                                        />
                                      )}
                                    </td>
                                  ))}
                                  <td
                                    className="whitespace-nowrap text-xs text-text-muted"
                                    title={new Date(run.start).toLocaleString()}
                                  >
                                    {localDateTime(run.start).replace('T', ' ') || '—'}
                                  </td>
                                  <td>
                                    <span
                                      className="agent-sdk-label"
                                      title={run.sdks
                                        .map((name) => sdkLabels[name] || name)
                                        .join(', ')}
                                    >
                                      {run.sdks
                                        .map((name) => sdkLabels[name] || name)
                                        .join(' · ')}
                                    </span>
                                  </td>
                                </tr>
                              ))}
                          </tbody>
                        </table>
                      </div>
                    ) : (
                      <EmptyState
                        icon={Bot}
                        title={
                          data.items.length
                            ? tr('没有匹配的运行', 'No matching runs')
                            : tr(
                                '等待第一条 Agent 链路',
                                'Waiting for the first Agent trace',
                              )
                        }
                        hint={
                          data.items.length
                            ? tr(
                                '筛选只作用于本次检索结果。',
                                'Filters apply to this retrieved result set.',
                              )
                            : tr(
                                '接入 SDK 并运行一次任务后，执行步骤会出现在这里。',
                                'Connect an SDK and run a task to see its execution steps here.',
                              )
                        }
                        action={
                          data.items.length ? (
                            <Button
                              onClick={() => {
                                setSearch('');
                                setSDK('');
                                setModel('');
                              }}
                            >
                              {tr('清除筛选', 'Clear filters')}
                            </Button>
                          ) : (
                            <Button
                              variant="primary"
                              onClick={() => setSetup(true)}
                            >
                              <Plug size={14} />
                              {tr(
                                '接入第一个应用',
                                'Connect your first application',
                              )}
                            </Button>
                          )
                        }
                      />
                    )}
                    <div className="px-4">
                      <PaginationFooter
                        page={page}
                        pageSize={pageSize}
                        shown={
                          runs.slice(page * pageSize, (page + 1) * pageSize)
                            .length
                        }
                        total={runs.length}
                        onPageChange={setPage}
                      />
                    </div>
                  </Card>
                </TabsContent>
                <TabsContent value="sessions">
                  {sessionID ? (
                    <div className="space-y-4">
                      <div className="flex flex-wrap items-center gap-3">
                        <Button
                          size="sm"
                          variant="subtle"
                          onClick={() => changeParams({ session_id: null })}
                        >
                          <ArrowLeft size={14} />
                          {tr('全部会话', 'All sessions')}
                        </Button>
                        <span className="break-all font-mono text-sm text-text">
                          {sessionID}
                        </span>
                        <Chip>
                          {data.items.length} {tr('次运行', 'runs')}
                        </Chip>
                      </div>
                      {data.items.length ? (
                        data.items
                          .slice()
                          .reverse()
                          .map((run, index) => (
                            <Card
                              key={runKey(run)}
                              className="agent-session-turn"
                            >
                              <div className="flex flex-wrap items-center gap-3">
                                <span className="agent-turn-number">
                                  {index + 1}
                                </span>
                                <Link
                                  className="min-w-0 flex-1 break-words text-sm font-medium text-text hover:underline"
                                  to={toTrace(run)}
                                >
                                  {runTitle(run)}
                                </Link>
                                <RunStatus
                                  status={run.status}
                                  errors={run.error_count}
                                />
                                <span className="text-xs text-text-muted">
                                  {new Date(run.start).toLocaleString()}
                                </span>
                                <Link
                                  to={toTrace(run)}
                                  className="inline-flex items-center gap-1 text-xs text-indigo-500"
                                >
                                  {tr('执行详情', 'View trace')}
                                  <ArrowUpRight size={13} />
                                </Link>
                              </div>
                              <div className="mt-4 grid gap-4 border-t border-border pt-4 md:grid-cols-2">
                                <div>
                                  <p className="mb-2 text-xs text-text-faint">
                                    {tr('输入摘要', 'Input preview')}
                                  </p>
                                  <p className="break-words text-sm text-text-muted">
                                    {run.input_preview ||
                                      contentStateLabel(run.input_state, tr)}
                                  </p>
                                </div>
                                <div>
                                  <p className="mb-2 text-xs text-text-faint">
                                    {tr('输出摘要', 'Output preview')}
                                  </p>
                                  <p className="break-words text-sm text-text-muted">
                                    {run.output_preview ||
                                      contentStateLabel(run.output_state, tr)}
                                  </p>
                                </div>
                              </div>
                            </Card>
                          ))
                      ) : (
                        <Card>
                          <EmptyState
                            icon={MessageSquare}
                            title={tr(
                              '当前范围未检索到该会话的运行',
                              'No runs retrieved for this session in the selected window',
                            )}
                          />
                        </Card>
                      )}
                    </div>
                  ) : (
                    <Card className="!p-0">
                      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border p-4">
                        <h2 className="text-sm font-medium text-text">
                          {tr('会话', 'Conversations')}
                        </h2>
                        <Input
                          type="search"
                          value={search}
                          onChange={(event) => setSearch(event.target.value)}
                          aria-label={tr('搜索会话', 'Search sessions')}
                          placeholder={tr(
                            '搜索会话 ID 或应用…',
                            'Search session ID or application…',
                          )}
                          className="w-full sm:w-72"
                        />
                      </div>
                      {sessions.length ? (
                        <div className="agent-table-scroll">
                          <table className="og-resource-table agent-sessions-table">
                            <thead>
                              <tr>
                                {[
                                  tr('会话 ID', 'Session ID'),
                                  tr('应用', 'Application'),
                                  tr('运行次数', 'Runs'),
                                  tr('最近输出', 'Latest output'),
                                  tr('最近活动', 'Last active'),
                                  '',
                                ].map((label, i) => (
                                  <th key={i}>{label}</th>
                                ))}
                              </tr>
                            </thead>
                            <tbody>
                              {sessions
                                .slice(page * pageSize, (page + 1) * pageSize)
                                .map((session) => (
                                  <tr
                                    key={`${identityKey(session.identity)}:${session.session_id}`}
                                  >
                                    <td>
                                      <Link
                                        to={toSession(session)}
                                        className="agent-run-link font-mono"
                                        title={session.session_id}
                                      >
                                        {session.session_id}
                                      </Link>
                                    </td>
                                    <td>
                                      <div className="max-w-56 truncate text-sm">
                                        {session.identity.service_name}
                                      </div>
                                      <div className="mt-1 text-xs text-text-faint">
                                        {session.identity.service_namespace} ·{' '}
                                        {session.identity.environment}
                                      </div>
                                    </td>
                                    <td className="font-mono">
                                      {session.run_count}
                                    </td>
                                    <td>
                                      <div
                                        className="agent-preview"
                                        title={session.output_preview}
                                      >
                                        {session.output_preview || '—'}
                                      </div>
                                    </td>
                                    <td className="whitespace-nowrap text-xs text-text-muted">
                                      {new Date(
                                        session.last_seen,
                                      ).toLocaleString()}
                                    </td>
                                    <td>
                                      <Link
                                        to={toSession(session)}
                                        aria-label={tr(
                                          `打开会话 ${session.session_id}`,
                                          `Open session ${session.session_id}`,
                                        )}
                                      >
                                        <ArrowRight size={15} />
                                      </Link>
                                    </td>
                                  </tr>
                                ))}
                            </tbody>
                          </table>
                        </div>
                      ) : (
                        <EmptyState
                          icon={MessageSquare}
                          title={tr(
                            '当前范围内没有会话',
                            'No sessions in the selected window',
                          )}
                          hint={tr(
                            'SDK 需要上报 session.id 或 thread_id；会话按应用和环境隔离。',
                            'The SDK must report a session or thread ID. Sessions are grouped by application and environment.',
                          )}
                        />
                      )}
                      <div className="px-4">
                        <PaginationFooter
                          page={page}
                          pageSize={pageSize}
                          shown={
                            sessions.slice(
                              page * pageSize,
                              (page + 1) * pageSize,
                            ).length
                          }
                          total={sessions.length}
                          onPageChange={setPage}
                        />
                      </div>
                    </Card>
                  )}
                </TabsContent>
                <TabsContent value="diagnostics">
                  <Card className="!p-0">
                    <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border p-4">
                      <div>
                        <h2 className="text-sm font-medium text-text">
                          {tr('采集质量', 'Collection quality')}
                        </h2>
                        <p className="mt-1 text-xs text-text-muted">
                          {tr(
                            '检查当前检索到的运行，不代表应用的全部流量。',
                            'Checks cover retrieved runs, not all application traffic.',
                          )}
                        </p>
                      </div>
                      <Button size="sm" onClick={() => setSetup(true)}>
                        <Plug size={13} />
                        {tr('查看接入配置', 'View setup')}
                      </Button>
                    </div>
                    <div className="divide-y divide-border">
                      {data.checks.map((check) => (
                        <div
                          key={check.key}
                          className="flex items-center gap-3 px-4 py-3.5"
                        >
                          {check.status === 'incomplete' ? (
                            <TriangleAlert
                              size={16}
                              className="shrink-0 text-amber-500"
                            />
                          ) : check.status === 'observed' ? (
                            <CheckCircle2
                              size={16}
                              className="shrink-0 text-emerald-500"
                            />
                          ) : (
                            <CircleHelp
                              size={16}
                              className="shrink-0 text-text-faint"
                            />
                          )}
                          <span className="min-w-0 flex-1 text-sm text-text">
                            {issueLabel(check.key, tr)}
                          </span>
                          <span className="text-right text-xs text-text-muted">
                            {check.status === 'incomplete'
                              ? tr(
                                  `${check.count} 次运行受影响`,
                                  `${check.count} affected runs`,
                                )
                              : check.key === 'traces'
                                ? tr(
                                    `${check.count} 条链路`,
                                    `${check.count} traces`,
                                  )
                                : check.status === 'observed'
                                  ? tr(
                                      '样本中未发现问题',
                                      'No issue found in samples',
                                    )
                                  : tr('尚无法判断', 'Not determined')}
                          </span>
                        </div>
                      ))}
                    </div>
                  </Card>
                  {data.metadata.failures.length > 0 && (
                    <Card className="mt-4">
                      <h3 className="mb-3 text-sm font-medium">
                        {tr('读取失败的链路', 'Traces that could not be read')}
                      </h3>
                      <div className="space-y-2">
                        {data.metadata.failures.map((failure, i) => (
                          <div
                            key={i}
                            className="flex flex-wrap justify-between gap-2 text-xs"
                          >
                            <span className="break-all font-mono">
                              {failure.trace_id}
                            </span>
                            <span className="text-text-muted">
                              {issueLabel(failure.reason, tr)}
                            </span>
                          </div>
                        ))}
                      </div>
                    </Card>
                  )}
                </TabsContent>
                <div className="mt-4 flex items-start gap-2 text-xs leading-relaxed text-text-faint">
                  <Info size={13} className="mt-0.5 shrink-0" />
                  <p>
                    {tr(
                      `已读取 ${data.metadata.read_traces} 条候选链路，上限 ${data.metadata.candidate_limit} 条。统计、会话和筛选仅覆盖本次结果；上游采样比例未知。`,
                      `Read ${data.metadata.read_traces} candidate traces, up to ${data.metadata.candidate_limit}. Statistics, sessions, and filters cover this result set; upstream sampling is unknown.`,
                    )}
                  </p>
                </div>
              </>
            )}
          </Tabs>
        )}
      </main>
      {setup && (
        <AgentSetup
          onClose={() => setSetup(false)}
          onVerify={(identity) => {
            setSetup(false);
            const now = Date.now();
            changeParams({
              ...identity,
              tab: 'diagnostics',
              trace_id: null,
              entry_span_id: null,
              step_id: null,
              session_id: null,
              range: '15m',
              start: new Date(now - 900000).toISOString(),
              end: new Date(now).toISOString(),
            });
            setRefresh((value) => value + 1);
          }}
        />
      )}
    </div>
  );
}
