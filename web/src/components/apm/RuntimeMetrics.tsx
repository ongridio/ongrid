import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
import type { ApmRuntime } from '@/api/apm';
import { Card, EmptyState } from '@/components/ui';
import { chartTooltipStyle, chartTooltipLabelStyle } from '@/lib/chartTheme';
import { useI18n } from '@/i18n/locale';

const colors = ['#6366f1', '#0ea5e9', '#d97706', '#10b981', '#a855f7', '#e11d48'];

export function RuntimeMetrics({ data }: { data: ApmRuntime }) {
  const { tr } = useI18n();
  const labels: Record<string, string> = {
    process_cpu_cores: tr('CPU 使用量（核）', 'CPU usage (cores)'),
    process_resident_memory_bytes: tr('进程常驻内存（RSS）', 'Resident memory (RSS)'),
    process_memory_usage_bytes: tr('进程常驻内存（RSS）', 'Resident memory (RSS)'),
    jvm_memory_used_bytes: tr('JVM 已用内存（未分类型）', 'JVM used memory (untyped)'),
    jvm_heap_memory_used_bytes: tr('JVM 堆内存', 'JVM heap memory'),
    jvm_non_heap_memory_used_bytes: tr('JVM 非堆内存', 'JVM non-heap memory'),
    go_memstats_heap_alloc_bytes: tr('Go 堆内存', 'Go heap memory'),
    go_goroutines: 'Goroutines',
    go_memory_allocation_bytes_per_second: tr('Go 内存分配速率', 'Go memory allocation rate'),
    go_gc_mean_duration_seconds: tr('Go GC 平均暂停时间', 'Go GC mean pause duration'),
    jvm_gc_mean_duration_seconds: tr('JVM GC 平均耗时', 'JVM GC mean duration'),
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
    {metrics.length === 0 ? <EmptyState title={tr('未观测到支持的运行时指标', 'No supported runtime metrics observed')} hint={tr('请确认已启用对应语言的运行时指标采集，并携带与请求指标一致的服务和实例标识。', 'Enable runtime metric collection for the language and use the same service and instance identity as request metrics.')} /> : <>
      <div className="grid grid-cols-1 gap-x-8 gap-y-8 lg:grid-cols-2">
        {metrics.map((name) => {
          const rows = data.items.filter((row) => row.name === name);
          const sourceUnit = rows[0].unit;
          const factor = sourceUnit.startsWith('bytes') ? 1 / 1048576 : sourceUnit === 'seconds' ? 1000 : 1;
          const unit = sourceUnit === 'bytes' ? 'MiB' : sourceUnit === 'bytes_per_second' ? 'MiB/s' : sourceUnit === 'cores' ? tr('核', 'cores') : sourceUnit === 'seconds' ? 'ms' : '';
          const display = (value: number | null) => value == null ? '—' : `${(value * factor).toLocaleString(undefined, { maximumSignificantDigits: 3 })}${unit ? ` ${unit}` : ''}`;
          const points = new Map<number, Record<string, number | null>>();
          rows.forEach((row, index) => row.points?.forEach((point) => {
            if (!points.has(point.timestamp)) points.set(point.timestamp, { timestamp: point.timestamp });
            points.get(point.timestamp)![`v${index}`] = point.value == null ? null : point.value * factor;
          }));
          return <section key={name} aria-label={labels[name]} className="min-w-0">
            <h3 className="mb-2 text-xs font-medium text-zinc-400">{labels[name]}{unit && ` · ${unit}`}</h3>
            {name.startsWith('jvm_') && name.endsWith('_memory_used_bytes') && <p className="mb-2 text-xs text-zinc-500">{tr('JVM 已用内存不等于进程 RSS。', 'JVM used memory is not process RSS.')}</p>}
            {name.endsWith('_gc_mean_duration_seconds') && <p className="mb-2 text-xs text-zinc-500">{name.startsWith('go_')
              ? tr('窗口内 GC 暂停总时长 / GC 次数；没有 GC 时留空。', 'GC pause time divided by collection count in the window; blank when no GC occurs.')
              : tr('窗口内 GC 动作总时长 / 次数；不等同于应用暂停时间，没有 GC 时留空。', 'GC action time divided by collection count in the window; not application pause time, and blank when no GC occurs.')}</p>}
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
                <span className="tabular-nums">{display(row.value)}</span>
              </div>)}
            </div>
          </section>;
        })}
      </div>
    </>}
  </Card>;
}
