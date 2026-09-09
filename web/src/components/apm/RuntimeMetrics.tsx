import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
import type { ApmRuntime } from '@/api/apm';
import { Card, EmptyState } from '@/components/ui';
import { chartTooltipStyle, chartTooltipLabelStyle } from '@/lib/chartTheme';
import { useI18n } from '@/i18n/locale';

const colors = ['#6366f1', '#0ea5e9', '#d97706', '#10b981', '#a855f7', '#e11d48'];
const display = (value: number | null, unit: string) => value == null ? '—' :
  unit === 'bytes' ? `${(value / 1048576).toLocaleString(undefined, { maximumFractionDigits: 2 })} MiB` :
    `${value.toLocaleString(undefined, { maximumSignificantDigits: 3 })}${unit === 'cores' ? ' CPU' : unit === 'seconds' ? ' s' : ''}`;

export function RuntimeMetrics({ data }: { data: ApmRuntime }) {
  const { tr } = useI18n();
  const labels: Record<string, string> = {
    process_cpu_cores: tr('CPU 使用量（核）', 'CPU usage (cores)'),
    process_resident_memory_bytes: tr('进程常驻内存（RSS）', 'Resident memory (RSS)'),
    process_memory_usage_bytes: tr('进程常驻内存（RSS）', 'Resident memory (RSS)'),
    jvm_memory_used_bytes: tr('JVM 已用内存（堆 + 非堆）', 'JVM used memory (heap + non-heap)'),
    go_memstats_heap_alloc_bytes: tr('Go 堆内存', 'Go heap memory'),
    go_goroutines: 'Goroutines',
    jvm_thread_count: tr('JVM 线程数', 'JVM threads'),
    jvm_threads_live_threads: tr('JVM 线程数', 'JVM threads'),
    nodejs_eventloop_lag_seconds: tr('事件循环延时', 'Event loop lag'),
  };
  const metrics = Object.keys(labels)
    .filter((name) => data.items.some((row) => row.name === name));
  return <Card>
    <h2 className="mb-2 text-sm font-medium">{tr('实例资源与运行时', 'Instance resources and runtime')}</h2>
    <p className="mb-4 text-xs text-zinc-500">{tr(
      '每条曲线对应一个实例及版本，图例显示最近值。CPU 以占用核数表示。',
      'Each line represents an instance and version; legends show latest values. CPU is measured in cores.',
    )}</p>
    {data.items.length === 0 ? <EmptyState title={tr('未观测到支持的运行时指标', 'No supported runtime metrics observed')} /> : <>
      <div className="grid grid-cols-1 gap-x-8 gap-y-8 lg:grid-cols-2">
        {metrics.map((name) => {
          const rows = data.items.filter((row) => row.name === name);
          const bytes = rows[0].unit === 'bytes';
          const unit = bytes ? 'MiB' : rows[0].unit === 'cores' ? tr('核', 'cores') : rows[0].unit === 'seconds' ? 's' : '';
          const points = new Map<number, Record<string, number | null>>();
          rows.forEach((row, index) => row.points?.forEach((point) => {
            if (!points.has(point.timestamp)) points.set(point.timestamp, { timestamp: point.timestamp });
            points.get(point.timestamp)![`v${index}`] = point.value == null ? null : bytes ? point.value / 1048576 : point.value;
          }));
          return <section key={name} aria-label={labels[name]} className="min-w-0">
            <h3 className="mb-2 text-xs font-medium text-zinc-400">{labels[name]}{bytes && ' · MiB'}</h3>
            {name === 'jvm_memory_used_bytes' && <p className="mb-2 text-xs text-zinc-500">{tr('JVM 已用内存不等于进程 RSS。', 'JVM used memory is not process RSS.')}</p>}
            <div className="h-48">
              <ResponsiveContainer width="100%" height="100%">
                <LineChart data={[...points.values()].sort((a, b) => a.timestamp! - b.timestamp!)} margin={{ left: 0, right: 12, top: 5, bottom: 0 }}>
                  <CartesianGrid stroke="rgb(var(--border))" strokeDasharray="3 3" />
                  <XAxis dataKey="timestamp" tickFormatter={(value) => new Date(value * 1000).toLocaleTimeString()} tick={{ fontSize: 10, fill: 'rgb(var(--text-muted))' }} minTickGap={45} />
                  <YAxis width={48} tick={{ fontSize: 10, fill: 'rgb(var(--text-muted))' }} tickFormatter={(value: number) => value.toLocaleString(undefined, { maximumSignificantDigits: 3 })} />
                  <Tooltip contentStyle={chartTooltipStyle} labelStyle={chartTooltipLabelStyle}
                    labelFormatter={(value) => new Date(Number(value) * 1000).toLocaleString()}
                    formatter={(value: number) => `${value.toLocaleString(undefined, { maximumSignificantDigits: 4 })} ${unit}`} />
                  {rows.map((row, index) => <Line key={`${row.instance_id}-${row.version}`} type="monotone" dataKey={`v${index}`} name={`${row.instance_id || tr('未设置实例', 'Unset instance')} · ${row.version || tr('未设置版本', 'Unset version')}`} stroke={colors[index % colors.length]} strokeWidth={1.5} dot={false} connectNulls={false} isAnimationActive={false} />)}
                </LineChart>
              </ResponsiveContainer>
            </div>
            <div className="mt-2 space-y-1 text-xs">
              {rows.map((row, index) => <div key={`${row.instance_id}-${row.version}`} className="flex flex-wrap items-center justify-between gap-2">
                <span className="inline-flex min-w-0 items-center gap-2 text-zinc-500"><span aria-hidden="true" className="h-0.5 w-3 shrink-0" style={{ backgroundColor: colors[index % colors.length] }} /><span className="break-all">{row.instance_id || '—'} · {row.version || '—'}</span></span>
                <span className="tabular-nums">{display(row.value, row.unit)}</span>
              </div>)}
            </div>
          </section>;
        })}
      </div>
    </>}
  </Card>;
}
