import { useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { RefreshCw } from 'lucide-react';
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
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
const number = (value: number | null | undefined, unit = '') =>
  value == null ? '—' : `${value.toLocaleString(undefined, { maximumFractionDigits: 2 })}${unit}`;

export default function ApmPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const [refresh, setRefresh] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [list, setList] = useState<ApmList>();
  const [overview, setOverview] = useState<ApmOverview>();
  const [dependencies, setDependencies] = useState<ApmDependencies>();
  const [diagnostics, setDiagnostics] = useState<ApmDiagnostics>();
  const [runtime, setRuntime] = useState<ApmRuntime>();
  const [metric, setMetric] = useState('error_rate');
  const [threshold, setThreshold] = useState('5');
  const [minimum, setMinimum] = useState('100');
  const [dwell, setDwell] = useState('120');
  const [creating, setCreating] = useState(false);
  const detail = params.has('service_name');
  const tab = params.get('tab') || (detail ? 'overview' : 'services');
  const query = params.toString();
  const set = (key: string, value: string | null) => {
    const next = new URLSearchParams(params);
    if (value === null) next.delete(key);
    else next.set(key, value);
    if (key !== 'page') next.delete('page');
    setParams(next);
  };
  useEffect(() => {
    if (params.has('start') && params.has('end')) return;
    const next = new URLSearchParams(params);
    const now = Date.now();
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
    setDependencies(undefined);
    setDiagnostics(undefined);
    setRuntime(undefined);
    if (!p.has('start') || !p.has('end') || tab === 'onboarding' || tab === 'alerts') return;
    const controller = new AbortController();
    setLoading(true);
    const task = !detail
      ? queryApm('services', p, controller.signal).then((data) => {
          if (!controller.signal.aborted) setList(data);
        })
      : tab === 'overview'
        ? queryApm('overview', p, controller.signal).then((data) => {
            if (!controller.signal.aborted) setOverview(data);
          })
        : tab === 'operations'
          ? queryApm('operations', p, controller.signal).then((data) => {
              if (!controller.signal.aborted) setList(data);
            })
          : tab === 'dependencies'
            ? queryApm('dependencies', p, controller.signal).then((data) => {
                if (!controller.signal.aborted) setDependencies(data);
              })
            : tab === 'runtime'
              ? queryApm('runtime', p, controller.signal).then((data) => {
                  if (!controller.signal.aborted) setRuntime(data);
                })
              : queryApm('diagnostics', p, controller.signal).then((data) => {
                  if (!controller.signal.aborted) setDiagnostics(data);
                });
    task
      .catch((e) => {
        if (!controller.signal.aborted) setError((e as Error).message);
      })
      .finally(() => {
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
  const tabs = detail
    ? [
        ['overview', tr('概览', 'Overview')],
        ['operations', tr('接口排行', 'Operations')],
        ['dependencies', tr('服务依赖', 'Dependencies')],
        ['runtime', tr('运行时指标', 'Runtime')],
        ['diagnostics', tr('接入诊断与实例', 'Diagnostics & instances')],
        ['alerts', tr('请求告警', 'Request alerts')],
        ['onboarding', tr('接入说明', 'Instrumentation')],
      ]
    : [
        ['services', tr('服务列表', 'Services')],
        ['onboarding', tr('接入说明', 'Instrumentation')],
      ];
  const back = new URLSearchParams(params);
  for (const key of ['service_name', 'operation', 'tab', 'page']) back.delete(key);
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
  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title={detail ? params.get('service_name') : tr('应用性能', 'Application performance')}
        leading={detail && <Link to={`/apm?${back}`}>← {tr('服务列表', 'Services')}</Link>}
        subtitle={
          detail
            ? `${params.get('environment') || unset} / ${params.get('service_namespace') || unset}`
            : tr('从服务请求到链路、日志和实例排查', 'Explore service requests, traces, logs and instances')
        }
        actions={
          <>
            <Button onClick={() => setRefresh((v) => v + 1)} disabled={loading}>
              <RefreshCw size={13} />
              {tr('刷新', 'Refresh')}
            </Button>
            {detail && (
              <Link className="text-xs underline" to={traceLink(params)}>
                {tr('查看链路', 'View traces')}
              </Link>
            )}
          </>
        }
        extra={
          <div className="flex flex-wrap items-end gap-3">
            {!detail && (
              <>
                <label className="grid gap-1 text-xs text-zinc-500">
                  {tr('服务搜索', 'Search services')}
                  <input
                    className={input}
                    value={params.get('search') || ''}
                    onChange={(e) => set('search', e.target.value)}
                  />
                </label>
                {[
                  ['environment', tr('环境', 'Environment')],
                  ['service_namespace', tr('业务命名空间', 'Service namespace')],
                ].map(([key, label]) => (
                  <label key={key} className="grid gap-1 text-xs text-zinc-500">
                    {label}
                    <input
                      className={input}
                      placeholder={tr('全部；∅ 表示未设置', 'All; ∅ for unset')}
                      value={params.has(key) ? params.get(key) || '∅' : ''}
                      onChange={(e) => set(key, e.target.value === '∅' ? '' : e.target.value || null)}
                    />
                  </label>
                ))}
              </>
            )}
            <label className="grid gap-1 text-xs text-zinc-500">
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
            {['start', 'end'].map((key) => (
              <label key={key} className="grid gap-1 text-xs text-zinc-500">
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
            <Button
              onClick={() => {
                const next = new URLSearchParams(params);
                const now = Date.now();
                next.set('start', new Date(now - 3600000).toISOString());
                next.set('end', new Date(now).toISOString());
                setParams(next);
              }}
            >
              {tr('最近 1 小时', 'Last hour')}
            </Button>
          </div>
        }
      />
      <nav
        aria-label={tr('应用性能视图', 'APM views')}
        className="flex flex-wrap gap-4 border-b border-zinc-800 px-6"
      >
        {tabs.map(([key, label]) => (
          <button
            key={key}
            onClick={() => set('tab', key)}
            className={`border-b-2 py-3 text-xs ${tab === key ? 'border-indigo-500 text-zinc-100' : 'border-transparent text-zinc-500'}`}
            aria-current={tab === key ? 'page' : undefined}
          >
            {label}
          </button>
        ))}
      </nav>
      <main className="flex-1 space-y-4 overflow-auto p-6">
        <p className="text-xs text-zinc-500">
          {tr(
            '指标来源：Tempo 接收到的入口 Span。上游采样覆盖率未知，数值不代表已确认的全量业务请求。无数据不等于服务宕机。',
            'Source: entry spans received by Tempo. Upstream sampling coverage is unknown; values are not confirmed total business traffic. No data does not imply downtime.',
          )}
        </p>
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
              <span>{tr(`共 ${list.total} 项`, `${list.total} items`)}</span>
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
                title={tr('当前范围未观测到服务请求指标', 'No request metrics observed in this scope')}
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
                        tr('估算样本请求数', 'Estimated sample requests'),
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
                          <td>{number(row.error_rate, '%')}</td>
                          <td>{number(row.p95_ms)}</td>
                          <td>{number(row.requests)}</td>
                          <td>{status(row.data_status)}</td>
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
            <Card>
              <div className="grid grid-cols-2 gap-5 md:grid-cols-5">
                {[
                  ['RPS', number(overview.summary.rps)],
                  [tr('技术错误率', 'Technical error rate'), number(overview.summary.error_rate, '%')],
                  ['P50', number(overview.summary.p50_ms, ' ms')],
                  ['P95', number(overview.summary.p95_ms, ' ms')],
                  ['P99', number(overview.summary.p99_ms, ' ms')],
                ].map(([k, v]) => (
                  <div key={k}>
                    <div className="text-xs text-zinc-500">{k}</div>
                    <div className="mt-2 text-xl tabular-nums">{v}</div>
                  </div>
                ))}
              </div>
              <p className="mt-4 text-xs text-zinc-500">
                {status(overview.summary.data_status)} ·{' '}
                {tr(
                  '趋势使用至少 5 分钟滚动窗口；摘要使用所选时间范围。',
                  'Trends use a rolling window of at least 5 minutes; summaries use the selected range.',
                )}
              </p>
            </Card>
            <div className="grid gap-4 lg:grid-cols-3">
              {[
                ['rps', 'RPS'],
                ['error_rate', tr('错误率 (%)', 'Error rate (%)')],
                ['p95_ms', tr('延迟 (ms)', 'Latency (ms)')],
              ].map(([key, title]) => (
                <Card key={key}>
                  <h2 className="mb-4 text-xs font-medium">{title}</h2>
                  <div className="h-52">
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
                        {(key === 'p95_ms' ? ['p50_ms', 'p95_ms', 'p99_ms'] : [key]).map((field, i) => (
                          <Line
                            key={field}
                            name={field}
                            dataKey={field}
                            stroke={['#6366f1', '#0ea5e9', '#f59e0b'][i]}
                            dot={false}
                            isAnimationActive={false}
                            connectNulls={false}
                          />
                        ))}
                      </LineChart>
                    </ResponsiveContainer>
                  </div>
                </Card>
              ))}
            </div>
            <div className="flex gap-4 text-xs underline">
              <Link to={traceLink(params, 'duration > 1s')}>
                {tr('慢请求链路 (>1s)', 'Slow request traces (>1s)')}
              </Link>
              <Link to={traceLink(params, 'status = error')}>
                {tr('错误请求链路', 'Errored request traces')}
              </Link>
            </div>
          </>
        )}
        {dependencies && (
          <Card>
            <Dependencies data={dependencies} params={params} />
          </Card>
        )}
        {runtime && (
          <Card>
            <p className="mb-3 text-xs text-zinc-500">
              {tr(
                '应用已导出的运行时指标，取结束时间前 5 分钟内的最近值；需配置服务身份标签。',
                'Runtime gauges exported by the application, using the latest value within 5 minutes of the end time; service identity labels are required.',
              )}
            </p>
            {runtime.items.length === 0 ? (
              <EmptyState title={tr('未观测到支持的运行时指标', 'No supported runtime metrics observed')} />
            ) : (
              <table className="w-full text-left text-xs">
                <thead>
                  <tr>
                    {[tr('指标', 'Metric'), tr('实例', 'Instance'), tr('数值 / 单位', 'Value / unit')].map(
                      (v) => (
                        <th className="p-3" key={v}>
                          {v}
                        </th>
                      ),
                    )}
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
        {diagnostics && (
          <>
            <Card>
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
            <Card>
              <h2 className="text-sm font-medium">{tr('样本关联实例', 'Instances in sampled traces')}</h2>
              {diagnostics.instances.length === 0 ? (
                <EmptyState title={tr('样本缺少实例关联字段', 'Samples have no instance identity')} />
              ) : (
                <div className="divide-y divide-zinc-800">
                  {diagnostics.instances.map((instance, i) => (
                    <div key={i} className="flex flex-wrap items-center justify-between gap-3 py-3 text-xs">
                      <span>
                        {instance.instance_id || instance.pod || unset} · {instance.version || unset} ·{' '}
                        {tr('设备', 'Device')} {instance.device_id || unset}
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
          </>
        )}
        {tab === 'alerts' && (
          <Card className="space-y-4">
            <h2 className="text-sm font-medium">{tr('请求级告警模板', 'Request-level alert template')}</h2>
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
