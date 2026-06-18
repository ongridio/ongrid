import { Fragment, useCallback, useEffect, useMemo, useState } from 'react';
import { useParams, useNavigate, Link } from 'react-router-dom';
import { ArrowLeft, Database, Clock, Activity, Server, BarChart3, MessageSquare, AlertTriangle, Search, ChevronDown, ChevronUp, Eye, Terminal, AlertCircle } from 'lucide-react';
import { StatusPill } from '@/components/StatusPill';
import { Sparkline } from '@/components/Sparkline';
import { Card, EmptyState } from '@/components/ui';
import { relativeTime, formatNumber } from '@/lib/format';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';
import { getDatabase, fetchSlowQueries, DB_TYPE_LABELS, type DatabaseInstance, type DBType, type SlowQueryRow, type SlowQueryResponse } from '@/api/databases';
import { promQueryRange, type PromRangeResp } from '@/api/edges';
import { usePoll } from '@/lib/usePoll';

const DB_ICONS: Record<string, string> = {
  mysql: '🐬', postgresql: '🐘', redis: '🔴', mongodb: '🍃', oracle: '🟢', selectdb: '📊',
};

interface MetricDef {
  label: string;
  expr: string;
  unit: string;
  color: string;
}

const DB_METRICS: Record<string, MetricDef[]> = {
  mysql: [
    { label: '连接数', expr: 'sum(mysql_global_status_threads_connected{db_type="{{db_type}}"})', unit: 'conns', color: '#60a5fa' },
    { label: 'QPS', expr: 'rate(mysql_global_status_questions{db_type="{{db_type}}"}[5m])', unit: 'q/s', color: '#34d399' },
    { label: '慢查询', expr: 'rate(mysql_global_status_slow_queries{db_type="{{db_type}}"}[5m])', unit: 'q/s', color: '#f59e0b' },
    { label: 'InnoDB 缓冲命中率', expr: '(1 - (mysql_global_status_innodb_buffer_pool_reads{db_type="{{db_type}}"}) / (mysql_global_status_innodb_buffer_pool_read_requests{db_type="{{db_type}}"}) ) * 100', unit: '%', color: '#a78bfa' },
    { label: '复制延迟', expr: 'mysql_slave_status_seconds_behind_master{db_type="{{db_type}}"}>0', unit: 's', color: '#f87171' },
  ],
  postgresql: [
    { label: '活跃连接', expr: 'sum(pg_stat_activity_count{db_type="{{db_type}}",state="active"})', unit: 'conns', color: '#60a5fa' },
    { label: 'QPS', expr: 'rate(pg_stat_database_xact_commit{db_type="{{db_type}}"}[5m])', unit: 'tps', color: '#34d399' },
    { label: '复制延迟', expr: 'pg_replication_lag{db_type="{{db_type}}"}>0', unit: 'bytes', color: '#f87171' },
    { label: '长事务', expr: 'pg_stat_activity_max_tx_duration{db_type="{{db_type}}"}>0', unit: 's', color: '#f59e0b' },
  ],
  redis: [
    { label: '内存使用', expr: 'redis_memory_used_bytes{db_type="{{db_type}}"}/1048576', unit: 'MB', color: '#60a5fa' },
    { label: '连接数', expr: 'redis_connected_clients{db_type="{{db_type}}"}}', unit: 'conns', color: '#34d399' },
    { label: 'QPS', expr: 'rate(redis_commands_processed_total{db_type="{{db_type}}"}[5m])', unit: 'ops/s', color: '#f59e0b' },
    { label: '键总数', expr: 'redis_db_keys{db_type="{{db_type}}"}}', unit: 'keys', color: '#a78bfa' },
  ],
  mongodb: [
    { label: '连接数', expr: 'mongodb_connections{db_type="{{db_type}}"}}', unit: 'conns', color: '#60a5fa' },
    { label: 'QPS', expr: 'rate(mongodb_database_operations_total{db_type="{{db_type}}"}[5m])', unit: 'ops/s', color: '#34d399' },
    { label: '复制延迟', expr: 'mongodb_mongod_repl_set_member_optime_date_lag{db_type="{{db_type}}"}>0', unit: 's', color: '#f87171' },
  ],
  oracle: [
    { label: '活跃会话', expr: 'oracle_session_active_count{db_type="{{db_type}}"}}', unit: 'sessions', color: '#60a5fa' },
    { label: '表空间使用率', expr: 'oracle_tablespace_used_pct{db_type="{{db_type}}"}}', unit: '%', color: '#f59e0b' },
    { label: 'QPS', expr: 'rate(oracle_requests_total{db_type="{{db_type}}"}[5m])', unit: 'q/s', color: '#34d399' },
  ],
  selectdb: [
    { label: 'QPS', expr: 'rate(doris_fe_query_total{db_type="{{db_type}}"}[5m])', unit: 'q/s', color: '#34d399' },
    { label: '查询延迟', expr: 'doris_fe_query_latency_ms{db_type="{{db_type}}"}}', unit: 'ms', color: '#f59e0b' },
    { label: 'BE 节点数', expr: 'doris_be_online{db_type="{{db_type}}"}}', unit: 'nodes', color: '#60a5fa' },
    { label: '存储使用', expr: 'doris_be_disk_used_pct{db_type="{{db_type}}"}}', unit: '%', color: '#f87171' },
  ],
};

interface MetricState {
  label: string;
  unit: string;
  color: string;
  loading: boolean;
  value: string | null;
  error: string | null;
}

export default function DatabaseDetailPage() {
  const { tr } = useI18n();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();

  const [inst, setInst] = useState<DatabaseInstance | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [metrics, setMetrics] = useState<MetricState[]>([]);
  const [promData, setPromData] = useState<Record<string, PromRangeResp | null>>({});

  const loadInst = useCallback(async () => {
    if (!id) return;
    try {
      const r = await getDatabase(id);
      setInst(r);
      setError(null);
    } catch (err: any) {
      setError(err?.message ?? tr('加载失败', 'Failed to load'));
    } finally {
      setLoading(false);
    }
  }, [id, tr]);

  useEffect(() => { void loadInst(); }, [loadInst]);

  const now = useMemo(() => Date.now(), []);
  const from = new Date(now - 3600000).toISOString();
  const to = new Date(now).toISOString();

  const refreshMetrics = useCallback(async () => {
    if (!inst) return;
    const defs = DB_METRICS[inst.db_type] ?? [];
    const results: MetricState[] = [];
    const promResults: Record<string, PromRangeResp | null> = {};

    for (const def of defs) {
      const expr = def.expr.replace('{{db_type}}', inst.db_type);
      try {
        const resp = await promQueryRange({ expr, from, to, step: '60s' });
        promResults[def.label] = resp;
        let value: string | null = null;
        if (resp.matrix?.length > 0) {
          const series = resp.matrix[0];
          if (series.values?.length > 0) {
            const last = series.values[series.values.length - 1];
            value = formatNumber(parseFloat(last[1]));
          }
        }
        results.push({ label: def.label, unit: def.unit, color: def.color, loading: false, value, error: null });
      } catch {
        results.push({ label: def.label, unit: def.unit, color: def.color, loading: false, value: null, error: null });
      }
    }
    setMetrics(results);
    setPromData(promResults);
  }, [inst, from, to]);

  useEffect(() => { void refreshMetrics(); }, [refreshMetrics]);
  usePoll(refreshMetrics, 30000, !!inst?.id);

  if (loading) {
    return (
      <main className="anim-fade flex flex-1 flex-col overflow-hidden">
        <div className="flex h-40 items-center justify-center text-sm text-zinc-500">
          {tr('加载中...', 'Loading...')}
        </div>
      </main>
    );
  }

  if (error || !inst) {
    return (
      <main className="anim-fade flex flex-1 flex-col overflow-hidden">
        <div className="flex h-full flex-col items-center justify-center gap-3">
          <EmptyState
            icon={Database}
            title={error ?? tr('实例未找到', 'Instance not found')}
            action={
              <button
                onClick={() => navigate('/databases')}
                className="rounded-md bg-accent px-2.5 py-1.5 text-xs font-medium text-accent-fg hover:bg-accent/90"
              >
                {tr('返回列表', 'Back to list')}
              </button>
            }
          />
        </div>
      </main>
    );
  }

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      {/* Header */}
      <header className="app-header flex items-center gap-3 border-b border-zinc-800/60 px-6 py-4">
        <button
          onClick={() => navigate('/databases')}
          className="rounded-md p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200"
        >
          <ArrowLeft size={18} />
        </button>
        <span className="text-xl">{DB_ICONS[inst.db_type] ?? '🗄️'}</span>
        <div className="min-w-0 flex-1">
          <h1 className="text-base font-semibold text-zinc-100">{inst.name}</h1>
          <p className="mt-0.5 text-xs text-zinc-500">
            {DB_TYPE_LABELS[inst.db_type as DBType] ?? inst.db_type} · {inst.host}:{inst.port}
            {inst.version ? ` · v${inst.version}` : ''}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-3">
          <StatusPill status={inst.status} />
          <span className="text-xs text-zinc-500">Edge #{inst.edge_id}</span>
        </div>
      </header>

      {/* Content */}
      <div className="flex-1 overflow-y-auto px-6 py-6">
        {/* Overview cards */}
        <div className="mb-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <Card compact as="div">
            <div className="flex items-center gap-2 text-xs text-zinc-500">
              <Activity size={14} />
              {tr('状态', 'Status')}
            </div>
            <p className="mt-1 text-sm font-medium text-zinc-200">
              {inst.status === 'online' ? tr('在线', 'Online') :
               inst.status === 'offline' ? tr('离线', 'Offline') :
               tr('未知', 'Unknown')}
            </p>
          </Card>
          <Card compact as="div">
            <div className="flex items-center gap-2 text-xs text-zinc-500">
              <Clock size={14} />
              {tr('创建时间', 'Created')}
            </div>
            <p className="mt-1 text-sm font-medium text-zinc-200">
              {inst.created_at ? relativeTime(inst.created_at) : '-'}
            </p>
          </Card>
          <Card compact as="div">
            <div className="flex items-center gap-2 text-xs text-zinc-500">
              <Server size={14} />
              {tr('类型', 'Type')}
            </div>
            <p className="mt-1 text-sm font-medium text-zinc-200">
              {DB_TYPE_LABELS[inst.db_type as DBType] ?? inst.db_type}
            </p>
          </Card>
          <Card compact as="div">
            <div className="flex items-center gap-2 text-xs text-zinc-500">
              <Database size={14} />
              {tr('版本', 'Version')}
            </div>
            <p className="mt-1 text-sm font-medium text-zinc-200">
              {inst.version || '-'}
            </p>
          </Card>
        </div>

        {/* Monitoring charts */}
        <h2 className="mb-3 flex items-center gap-2 text-sm font-medium text-zinc-300">
          <BarChart3 size={16} />
          {tr('监控指标', 'Monitoring Metrics')}
        </h2>
        <div className="mb-6 grid gap-4 sm:grid-cols-2">
          {(DB_METRICS[inst.db_type] ?? []).map((def) => {
            const data = promData[def.label];
            const chartData = data?.matrix?.[0]?.values?.map(([ts, v]) => ({
              ts: new Date(ts * 1000).toISOString(),
              value: parseFloat(v),
            })) ?? [];

            return (
              <Card key={def.label} compact as="div">
                <div className="mb-2 flex items-center justify-between">
                  <span className="text-xs font-medium text-zinc-400">{def.label}</span>
                  <span className="text-xs text-zinc-500">{def.unit}</span>
                </div>
                {chartData.length > 0 ? (
                  <Sparkline
                    data={chartData.map((d) => d.value)}
                    variant="plain"
                    height={48}
                  />
                ) : (
                  <div className="flex h-12 items-center justify-center text-xs text-zinc-600">
                    {tr('暂无数据', 'No data')}
                  </div>
                )}
                {metrics.find((m) => m.label === def.label)?.value && (
                  <p className="mt-1 text-right text-xs text-zinc-500">
                    {tr('当前', 'Current')}: {metrics.find((m) => m.label === def.label)?.value} {def.unit}
                  </p>
                )}
              </Card>
            );
          })}
        </div>

        {/* No metrics placeholder */}
        {(DB_METRICS[inst.db_type] ?? []).length === 0 && (
          <div className="mb-6 flex items-center justify-center py-12 text-xs text-zinc-500">
            {tr('该数据库类型暂未定义监控指标', 'No monitoring metrics defined for this database type')}
          </div>
        )}

        {/* Slow Query Analysis */}
        <h2 className="mb-3 flex items-center gap-2 text-sm font-medium text-zinc-300">
          <AlertTriangle size={16} className="text-amber-400" />
          {tr('慢查询分析', 'Slow Query Analysis')}
        </h2>

        <SlowQueryPanel inst={inst} />
      </div>
    </main>
  );
}

// ─── Slow Query Panel ─────────────────────────────────────────────────────

function SlowQueryPanel({ inst }: { inst: DatabaseInstance }) {
  const { tr } = useI18n();
  const [phase, setPhase] = useState<'connect' | 'loading' | 'results' | 'error'>('connect');
  const [sqUser, setSqUser] = useState('');
  const [sqPass, setSqPass] = useState('');
  const [sqDb, setSqDb] = useState('');
  const [sqMinDuration, setSqMinDuration] = useState(100);
  const [sqData, setSqData] = useState<SlowQueryResponse | null>(null);
  const [sqError, setSqError] = useState<string | null>(null);
  const [expandedRow, setExpandedRow] = useState<number | null>(null);
  const [sortField, setSortField] = useState<'avg_latency_ms' | 'max_latency_ms' | 'exec_count'>('avg_latency_ms');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');

  const runAnalysis = async () => {
    if (!sqUser) return;
    setPhase('loading');
    setSqError(null);
    try {
      const resp = await fetchSlowQueries(inst.id, {
        user: sqUser,
        password: sqPass,
        database: sqDb || undefined,
        limit: 50,
        min_duration_ms: sqMinDuration,
      });
      setSqData(resp);
      setPhase(resp.error && !resp.queries?.length ? 'error' : 'results');
      if (resp.error && !resp.queries?.length) {
        setSqError(resp.error);
      }
    } catch (err: any) {
      setSqError(err?.message ?? tr('查询失败', 'Query failed'));
      setPhase('error');
    }
  };

  const sortedQueries = useMemo(() => {
    if (!sqData?.queries) return [];
    const qs = [...sqData.queries];
    qs.sort((a, b) => {
      const va = a[sortField] ?? 0;
      const vb = b[sortField] ?? 0;
      return sortDir === 'desc' ? vb - va : va - vb;
    });
    return qs;
  }, [sqData, sortField, sortDir]);

  const toggleSort = (field: typeof sortField) => {
    if (sortField === field) {
      setSortDir((d) => (d === 'desc' ? 'asc' : 'desc'));
    } else {
      setSortField(field);
      setSortDir('desc');
    }
  };

  const SortIcon = ({ field }: { field: typeof sortField }) => {
    if (sortField !== field) return null;
    return sortDir === 'desc' ? <ChevronDown size={12} /> : <ChevronUp size={12} />;
  };

  const formatLatency = (ms?: number): string => {
    if (ms == null) return '-';
    if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`;
    return `${ms.toFixed(1)}ms`;
  };

  const supportPerfSchema = inst.db_type === 'mysql' || inst.db_type === 'postgresql';

  return (
    <Card as="div" className="not-prose">
      {/* Connection form */}
      {phase === 'connect' && (
        <div>
          <div className="mb-3 flex items-start justify-between">
            <div>
              <p className="mb-1 text-sm text-zinc-300">
                {tr('需要连接数据库查看慢查询 Top SQL', 'Connect to the database to view top slow queries')}
              </p>
              <p className="text-xs text-zinc-500">
                {supportPerfSchema
                  ? tr('需要数据库账号(只读即可)，将查询 performance_schema / pg_stat_statements', 'A read-only DB account is required to query performance_schema / pg_stat_statements')
                  : tr('需要数据库账号以查询系统视图', 'A DB account is required to query system views')}
              </p>
            </div>
            <Link
              to={`/chat/new?prompt=${encodeURIComponent(
                `分析数据库实例 ${inst.name} (${inst.host}:${inst.port}) 的慢查询情况，` +
                `数据库类型 ${DB_TYPE_LABELS[inst.db_type as DBType] ?? inst.db_type}。` +
                `请先查看慢查询指标趋势，然后连接数据库获取 TOP SQL 并分析根因。`
              )}`}
              className="inline-flex shrink-0 items-center gap-1.5 rounded-md bg-accent px-2.5 py-1.5 text-xs font-medium text-accent-fg hover:bg-accent/90"
            >
              <MessageSquare size={12} />
              {tr('AI 分析', 'AI Analysis')}
            </Link>
          </div>
          <div className="flex flex-wrap items-end gap-3">
            <div className="min-w-[140px] flex-1">
              <label className="mb-1 block text-xs text-zinc-500">{tr('用户名', 'Username')}</label>
              <input
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
                value={sqUser}
                onChange={(e) => setSqUser(e.target.value)}
                placeholder="monitor"
              />
            </div>
            <div className="min-w-[140px] flex-1">
              <label className="mb-1 block text-xs text-zinc-500">{tr('密码', 'Password')}</label>
              <input
                type="password"
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
                value={sqPass}
                onChange={(e) => setSqPass(e.target.value)}
                placeholder="••••••••"
              />
            </div>
            <div className="w-36">
              <label className="mb-1 block text-xs text-zinc-500">{tr('数据库(可选)', 'Database (opt)')}</label>
              <input
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
                value={sqDb}
                onChange={(e) => setSqDb(e.target.value)}
                placeholder={tr('留空自动', 'Auto')}
              />
            </div>
            <div className="w-28">
              <label className="mb-1 block text-xs text-zinc-500">{tr('最慢阈值', 'Min duration')}</label>
              <select
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
                value={sqMinDuration}
                onChange={(e) => setSqMinDuration(Number(e.target.value))}
              >
                <option value={50}>50ms</option>
                <option value={100}>100ms</option>
                <option value={200}>200ms</option>
                <option value={500}>500ms</option>
                <option value={1000}>1s</option>
                <option value={5000}>5s</option>
              </select>
            </div>
            <button
              onClick={runAnalysis}
              disabled={!sqUser || !sqPass}
              className="inline-flex items-center gap-1.5 rounded-md bg-amber-600 px-2.5 py-1.5 text-xs font-medium text-white hover:bg-amber-500 disabled:opacity-50"
            >
              <Search size={12} />
              {tr('分析慢查询', 'Analyze')}
            </button>
          </div>
        </div>
      )}

      {/* Loading */}
      {phase === 'loading' && (
        <div className="flex items-center justify-center gap-2 py-8 text-zinc-500">
          <div className="h-4 w-4 animate-spin rounded-full border-2 border-zinc-500 border-t-transparent" />
          <span className="text-sm">
            {tr('正在查询 performance_schema ...', 'Querying performance_schema ...')}
          </span>
        </div>
      )}

      {/* Error */}
      {phase === 'error' && (
        <div>
          <div className="mb-3 flex items-center gap-2 text-amber-400">
            <AlertCircle size={14} />
            <span className="text-sm font-medium">{tr('慢查询分析失败', 'Slow query analysis failed')}</span>
          </div>
          <div className="mb-3 rounded-lg bg-zinc-800/60 p-3 text-xs text-zinc-400 font-mono">
            {sqError}
          </div>
          <div className="flex gap-2">
            <button
              onClick={() => setPhase('connect')}
              className="rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
            >
              {tr('返回重试', 'Back & Retry')}
            </button>
            <Link
              to={`/chat/new?prompt=${encodeURIComponent(
                `分析数据库实例 ${inst.name} (${inst.host}:${inst.port}) 的慢查询情况，` +
                `数据库类型 ${DB_TYPE_LABELS[inst.db_type as DBType] ?? inst.db_type}。` +
                `失败原因：${sqError}。请尝试连接数据库获取 TOP SQL 并分析根因。`
              )}`}
              className="inline-flex items-center gap-1.5 rounded-md bg-accent px-2.5 py-1.5 text-xs font-medium text-accent-fg hover:bg-accent/90"
            >
              <MessageSquare size={12} />
              {tr('AI 分析', 'AI Analysis')}
            </Link>
          </div>
        </div>
      )}

      {/* Results table */}
      {phase === 'results' && (
        <div>
          {/* Toolbar */}
          <div className="-mx-4 -mt-4 mb-2 flex items-center justify-between border-b border-zinc-800/60 px-4 py-2">
            <div className="flex items-center gap-2 text-xs text-zinc-500">
              <Terminal size={12} />
              <span>
                {sqData?.truncated
                  ? tr('显示前 {n} 条（结果已截断）', `Showing top ${sortedQueries.length} (truncated)`)
                  : tr('共 {n} 条慢查询', `${sortedQueries.length} slow queries`)}
                {inst.db_type === 'mysql' && (
                  <span className="ml-2 text-zinc-600">
                    {tr('来源: performance_schema', 'Source: performance_schema')}
                  </span>
                )}
                {inst.db_type === 'postgresql' && (
                  <span className="ml-2 text-zinc-600">
                    {tr('来源: pg_stat_statements', 'Source: pg_stat_statements')}
                  </span>
                )}
              </span>
            </div>
            <div className="flex gap-2">
              <button
                onClick={() => setPhase('connect')}
                className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-400 hover:bg-zinc-800"
              >
                {tr('更换账号', 'Change Account')}
              </button>
              <button
                onClick={runAnalysis}
                className="inline-flex items-center gap-1 rounded-md bg-amber-600/20 px-2 py-1 text-xs text-amber-300 hover:bg-amber-600/30"
              >
                <Search size={12} />
                {tr('刷新', 'Refresh')}
              </button>
            </div>
          </div>

          {/* Empty state */}
          {sortedQueries.length === 0 && (
            <div className="flex flex-col items-center justify-center py-10 text-zinc-600">
              <Database size={32} />
              <p className="mt-2 text-sm">
                {tr('没有找到慢查询（可能 performance_schema 未启用）', 'No slow queries found (performance_schema may not be enabled)')}
              </p>
            </div>
          )}

          {/* Table */}
          {sortedQueries.length > 0 && (
            <div className="-mx-4 overflow-x-auto px-4">
              <table className="w-full text-left text-xs">
                <thead>
                  <tr className="border-b border-zinc-800/60 text-zinc-500">
                    <th className="px-4 py-2 font-medium">{tr('SQL', 'SQL')}</th>
                    <th
                      className="cursor-pointer px-4 py-2 font-medium hover:text-zinc-300"
                      onClick={() => toggleSort('exec_count')}
                    >
                      <span className="flex items-center gap-1">
                        {tr('执行次数', 'Calls')} <SortIcon field="exec_count" />
                      </span>
                    </th>
                    <th
                      className="cursor-pointer px-4 py-2 font-medium hover:text-zinc-300"
                      onClick={() => toggleSort('avg_latency_ms')}
                    >
                      <span className="flex items-center gap-1">
                        {tr('平均延迟', 'Avg')} <SortIcon field="avg_latency_ms" />
                      </span>
                    </th>
                    <th
                      className="cursor-pointer px-4 py-2 font-medium hover:text-zinc-300"
                      onClick={() => toggleSort('max_latency_ms')}
                    >
                      <span className="flex items-center gap-1">
                        {tr('最大延迟', 'Max')} <SortIcon field="max_latency_ms" />
                      </span>
                    </th>
                    <th className="px-4 py-2 font-medium">
                      {inst.db_type === 'mysql' ? tr('索引', 'Index') : tr('缓存命中', 'Cache')}
                    </th>
                    <th className="px-4 py-2 font-medium">{tr('操作', 'Action')}</th>
                  </tr>
                </thead>
                <tbody>
                  {sortedQueries.map((q, i) => {
                    const isExpanded = expandedRow === i;
                    const sqlPreview = q.sql_text?.length > 120
                      ? q.sql_text.slice(0, 120) + '...'
                      : q.sql_text || tr('(空)', '(empty)');

                    return (
                      <Fragment key={i}>
                        <tr
                          className="cursor-pointer border-b border-zinc-800/60 text-zinc-300 hover:bg-zinc-800/40"
                          onClick={() => setExpandedRow(isExpanded ? null : i)}
                        >
                          <td className="max-w-[400px] truncate px-4 py-2.5 font-mono text-[11px] text-zinc-400">
                            {sqlPreview}
                          </td>
                          <td className="px-4 py-2.5">{q.exec_count ?? '-'}</td>
                          <td className="px-4 py-2.5">{formatLatency(q.avg_latency_ms)}</td>
                          <td className="px-4 py-2.5">{formatLatency(q.max_latency_ms)}</td>
                          <td className="px-4 py-2.5">
                            {inst.db_type === 'mysql' && q.has_no_index_used && (
                              <span className="text-amber-400" title={tr('存在全表扫描', 'Has full table scan')}>
                                {tr('无索引', 'No idx')}
                              </span>
                            )}
                            {inst.db_type === 'postgresql' && q.cache_hit_pct != null && (
                              <span className={q.cache_hit_pct < 95 ? 'text-amber-400' : 'text-zinc-500'}>
                                {q.cache_hit_pct.toFixed(1)}%
                              </span>
                            )}
                            {inst.db_type === 'postgresql' && q.cache_hit_pct == null && '-'}
                            {!['mysql', 'postgresql'].includes(inst.db_type) && '-'}
                          </td>
                          <td className="px-4 py-2.5">
                            <button
                              onClick={(e) => { e.stopPropagation(); setExpandedRow(isExpanded ? null : i); }}
                              className="inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-xs text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200"
                            >
                              <Eye size={12} />
                              {isExpanded ? tr('收起', 'Hide') : tr('详情', 'Detail')}
                            </button>
                          </td>
                        </tr>
                        {isExpanded && (
                          <tr className="border-b border-zinc-800/60">
                            <td colSpan={6} className="px-4 py-3">
                              <div className="space-y-3">
                                {/* Full SQL */}
                                <div>
                                  <span className="mb-1 block text-[10px] uppercase tracking-wide text-zinc-500">
                                    {tr('完整 SQL', 'Full SQL')}
                                  </span>
                                  <pre className="max-h-[200px] overflow-auto whitespace-pre-wrap break-all rounded-lg bg-zinc-950 p-3 font-mono text-[11px] leading-relaxed text-zinc-300">
                                    {q.sql_text || tr('(空)', '(empty)')}
                                  </pre>
                                </div>
                                {/* Details grid */}
                                <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                                  <div className="rounded-lg bg-zinc-800/40 p-2">
                                    <span className="block text-[10px] text-zinc-500">{tr('执行次数', 'Exec Count')}</span>
                                    <span className="text-sm font-medium text-zinc-200">{q.exec_count ?? '-'}</span>
                                  </div>
                                  <div className="rounded-lg bg-zinc-800/40 p-2">
                                    <span className="block text-[10px] text-zinc-500">{tr('平均延迟', 'Avg Latency')}</span>
                                    <span className="text-sm font-medium text-zinc-200">{formatLatency(q.avg_latency_ms)}</span>
                                  </div>
                                  <div className="rounded-lg bg-zinc-800/40 p-2">
                                    <span className="block text-[10px] text-zinc-500">{tr('最大延迟', 'Max Latency')}</span>
                                    <span className="text-sm font-medium text-zinc-200">{formatLatency(q.max_latency_ms)}</span>
                                  </div>
                                  <div className="rounded-lg bg-zinc-800/40 p-2">
                                    <span className="block text-[10px] text-zinc-500">{tr('总延迟', 'Total Latency')}</span>
                                    <span className="text-sm font-medium text-zinc-200">{formatLatency(q.total_latency_ms)}</span>
                                  </div>
                                  {q.avg_rows_examined != null && (
                                    <div className="rounded-lg bg-zinc-800/40 p-2">
                                      <span className="block text-[10px] text-zinc-500">{tr('平均扫描行', 'Rows Examined')}</span>
                                      <span className="text-sm font-medium text-zinc-200">{formatNumber(q.avg_rows_examined)}</span>
                                    </div>
                                  )}
                                  {q.avg_rows_sent != null && (
                                    <div className="rounded-lg bg-zinc-800/40 p-2">
                                      <span className="block text-[10px] text-zinc-500">{tr('平均返回行', 'Rows Sent')}</span>
                                      <span className="text-sm font-medium text-zinc-200">{formatNumber(q.avg_rows_sent)}</span>
                                    </div>
                                  )}
                                  {q.tmp_disk_tables != null && q.tmp_disk_tables > 0 && (
                                    <div className="rounded-lg bg-amber-900/30 p-2">
                                      <span className="block text-[10px] text-zinc-500">{tr('磁盘临时表', 'Tmp Disk')}</span>
                                      <span className="text-sm font-medium text-amber-400">{q.tmp_disk_tables}</span>
                                    </div>
                                  )}
                                  {q.has_no_index_used && (
                                    <div className="rounded-lg bg-red-900/30 p-2">
                                      <span className="block text-[10px] text-zinc-500">{tr('索引问题', 'Index Issue')}</span>
                                      <span className="text-sm font-medium text-red-400">
                                        {q.has_no_good_index ? tr('无合适索引', 'No good idx') : tr('全表扫描', 'Full scan')}
                                      </span>
                                    </div>
                                  )}
                                  {q.cache_hit_pct != null && (
                                    <div className="rounded-lg bg-zinc-800/40 p-2">
                                      <span className="block text-[10px] text-zinc-500">{tr('缓存命中', 'Cache Hit')}</span>
                                      <span className={cn(
                                        'text-sm font-medium',
                                        q.cache_hit_pct < 95 ? 'text-amber-400' : 'text-green-400'
                                      )}>
                                        {q.cache_hit_pct.toFixed(1)}%
                                      </span>
                                    </div>
                                  )}
                                </div>
                              </div>
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}

