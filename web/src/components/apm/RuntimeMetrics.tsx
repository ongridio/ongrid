import { useState } from 'react';
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
import type { ApmRuntime } from '@/api/apm';
import { Button, Card, EmptyState } from '@/components/ui';
import { chartTooltipStyle, chartTooltipLabelStyle } from '@/lib/chartTheme';
import { useI18n } from '@/i18n/locale';

const colors = ['#6366f1', '#0ea5e9', '#d97706', '#10b981', '#a855f7', '#e11d48'];
const seriesKey = (row: ApmRuntime['items'][number]) => JSON.stringify([row.instance_id, row.version ?? '']);

export function RuntimeMetrics({ data }: { data: ApmRuntime }) {
  const { tr } = useI18n();
  const [selectedSeries, setSelectedSeries] = useState<Record<string, string | null>>({});
  const labels: Record<string, string> = {
    process_cpu_cores: tr('CPU 使用量', 'CPU usage'),
    process_resident_memory_bytes: tr('进程常驻内存（RSS）', 'Resident memory (RSS)'),
    process_memory_usage_bytes: tr('进程常驻内存（RSS）', 'Resident memory (RSS)'),
    process_virtual_memory_bytes: tr('虚拟内存', 'Virtual memory'),
    process_threads: tr('线程数', 'Threads'),
    process_open_fds: tr('文件描述符', 'Open file descriptors'),
    process_io_read_bytes_per_second: tr('磁盘读取速率', 'Disk read rate'),
    process_io_write_bytes_per_second: tr('磁盘写入速率', 'Disk write rate'),
    jvm_memory_used_bytes: tr('JVM 已用内存（未分类型）', 'JVM used memory (untyped)'),
    jvm_heap_memory_used_bytes: tr('JVM 堆内存', 'JVM heap memory'),
    jvm_non_heap_memory_used_bytes: tr('JVM 非堆内存', 'JVM non-heap memory'),
    go_memstats_heap_alloc_bytes: tr('Go 堆内存', 'Go heap memory'),
    go_goroutines: 'Goroutines',
    go_runtime_cpu_cores: tr('Go 运行时 CPU（估算）', 'Go runtime CPU (estimated)'),
    go_stack_memory_used_bytes: tr('Go 栈内存', 'Go stack memory'),
    go_other_memory_used_bytes: tr('Go 其他运行时内存', 'Go other runtime memory'),
    go_memory_limit_bytes: tr('Go 内存软限制', 'Go memory soft limit'),
    go_memory_gc_goal_bytes: tr('Go GC 堆目标', 'Go GC heap goal'),
    go_gc_cycles_per_second: tr('Go GC 频率', 'Go GC frequency'),
    go_allocations_per_second: tr('Go 内存分配次数', 'Go allocation rate'),
    go_processor_limit: 'GOMAXPROCS',
    go_config_gogc_percent: 'GOGC',
    go_gc_pause_mean_duration_seconds: tr('Go GC 平均暂停时间（估算）', 'Go GC mean pause (estimated)'),
    go_schedule_mean_duration_seconds: tr('Go 调度平均等待时间（估算）', 'Go mean scheduling delay (estimated)'),
    jvm_heap_memory_committed_bytes: tr('JVM 堆已提交内存', 'JVM committed heap memory'),
    jvm_non_heap_memory_committed_bytes: tr('JVM 非堆已提交内存', 'JVM committed non-heap memory'),
    jvm_heap_memory_limit_bytes: tr('JVM 堆内存池上限', 'JVM heap pool limit'),
    jvm_non_heap_memory_limit_bytes: tr('JVM 非堆内存池上限', 'JVM non-heap pool limit'),
    jvm_heap_memory_used_after_last_gc_bytes: tr('JVM GC 后堆内存', 'JVM heap memory after GC'),
    jvm_non_heap_memory_used_after_last_gc_bytes: tr('JVM GC 后非堆内存', 'JVM non-heap memory after GC'),
    nodejs_eventloop_utilization_ratio: tr('事件循环利用率', 'Event loop utilization'),
    nodejs_eventloop_active_ratio: tr('事件循环活跃时间占比', 'Event loop active time ratio'),
    nodejs_eventloop_idle_ratio: tr('事件循环空闲时间占比', 'Event loop idle time ratio'),
    nodejs_eventloop_delay_min_seconds: tr('事件循环最短延时', 'Event loop minimum delay'),
    nodejs_eventloop_delay_max_seconds: tr('事件循环最长延时', 'Event loop maximum delay'),
    nodejs_eventloop_delay_mean_seconds: tr('事件循环平均延时', 'Event loop mean delay'),
    nodejs_eventloop_delay_stddev_seconds: tr('事件循环延时标准差', 'Event loop delay standard deviation'),
    nodejs_eventloop_delay_p50_seconds: tr('事件循环延时 P50', 'Event loop delay P50'),
    nodejs_eventloop_delay_p90_seconds: tr('事件循环延时 P90', 'Event loop delay P90'),
    nodejs_eventloop_delay_p99_seconds: tr('事件循环延时 P99', 'Event loop delay P99'),
    go_memory_allocation_bytes_per_second: tr('Go 内存分配速率', 'Go memory allocation rate'),
    go_gc_mean_duration_seconds: tr('Go GC 平均暂停时间', 'Go GC mean pause duration'),
    jvm_gc_mean_duration_seconds: tr('JVM GC 平均耗时', 'JVM GC mean duration'),
    jvm_thread_count: tr('JVM 线程数', 'JVM threads'),
    jvm_threads_live_threads: tr('JVM 线程数', 'JVM threads'),
    nodejs_eventloop_lag_seconds: tr('事件循环延时', 'Event loop lag'),
  };
  const metrics = Object.keys(labels)
    .filter((name) => data.items.some((row) => row.name === name));
  const sections = [
    {
      key: 'resources',
      title: tr('基础资源', 'Basic resources'),
      hint: tr('按服务实例展示 CPU、内存与进程资源。CPU 以占用核数表示，1 核相当于一个逻辑 CPU 的 100%。', 'CPU, memory and process resources by service instance. One core equals 100% of one logical CPU.'),
      empty: tr('暂无基础资源数据', 'No basic resource data'),
      emptyHint: tr('请确认 Edge 已更新且目标正在运行；CPU 和 I/O 速率需要至少两次采集。', 'Check that Edge is up to date and the target is running. CPU and I/O rates need at least two samples.'),
      metrics: metrics.filter((name) => name.startsWith('process_')),
    },
    {
      key: 'runtime',
      title: tr('运行时监控', 'Runtime monitoring'),
      hint: tr('语言运行时的堆内存、GC 与并发指标。每条曲线对应一个实例及版本。', 'Language runtime heap, GC and concurrency metrics. Each line represents an instance and version.'),
      empty: tr('暂无运行时指标', 'No runtime metrics'),
      emptyHint: tr('运行时指标取决于语言与采集方式；未上报运行时指标不影响基础资源监控。', 'Runtime metrics depend on the language and instrumentation. Basic resource monitoring remains available without them.'),
      metrics: metrics.filter((name) => !name.startsWith('process_')),
    },
  ];
  return <div className="space-y-4">
    {sections.map((section) => <Card key={section.key}>
      <section aria-label={section.title}>
        <h2 className="mb-2 text-sm font-medium">{section.title}</h2>
        <p className="mb-4 text-xs text-text-muted">{section.hint}</p>
        {section.metrics.length === 0 ? <EmptyState title={section.empty} hint={section.emptyHint} /> : <>
      <div className="grid grid-cols-1 gap-x-8 gap-y-8 lg:grid-cols-2">
        {section.metrics.map((name) => {
          const rows = data.items.filter((row) => row.name === name);
          const selectedKey = rows.some((row) => seriesKey(row) === selectedSeries[name]) ? selectedSeries[name] : null;
          const sourceUnit = rows[0].unit;
          const factor = sourceUnit.startsWith('bytes') ? 1 / 1048576 : sourceUnit === 'seconds' ? 1000 : sourceUnit === 'ratio' ? 100 : 1;
          const unit = sourceUnit === 'bytes' ? 'MiB' : sourceUnit === 'bytes_per_second' ? 'MiB/s' : sourceUnit === 'cores' ? tr('核', 'cores') : sourceUnit === 'seconds' ? 'ms' : ['ratio', 'percent'].includes(sourceUnit) ? '%' : sourceUnit === 'per_second' ? '/s' : '';
          const display = (value: number | null) => value == null ? '—' : `${(value * factor).toLocaleString(undefined, { maximumSignificantDigits: 3 })}${unit ? ` ${unit}` : ''}`;
          const points = new Map<number, Record<string, number | null>>();
          rows.forEach((row, index) => row.points?.forEach((point) => {
            if (!points.has(point.timestamp)) points.set(point.timestamp, { timestamp: point.timestamp });
            points.get(point.timestamp)![`v${index}`] = point.value == null ? null : point.value * factor;
          }));
          return <section key={name} aria-label={labels[name]} className="min-w-0">
            <h3 className="mb-2 text-xs font-medium text-text-muted">{labels[name]}{unit && ` · ${unit}`}</h3>
            {['go_gc_pause_mean_duration_seconds', 'go_schedule_mean_duration_seconds'].includes(name) && <p className="mb-2 text-xs text-text-muted">{tr('按直方图桶下界估算均值；没有观测时留空。', 'Mean estimated from histogram bucket lower bounds; blank without observations.')}</p>}
            {name === 'process_resident_memory_bytes' && <p className="mb-2 text-xs text-text-muted">{tr('Kubernetes 实例合计对应容器内的进程 RSS，不等同于容器工作集。', 'Kubernetes instances sum process RSS in the corresponding container; this differs from the container working set.')}</p>}
            {name === 'process_virtual_memory_bytes' && <p className="mb-2 text-xs text-text-muted">{tr('进程地址空间大小，不代表实际占用的物理内存。', 'Process address space size, not physical memory consumption.')}</p>}
            {name === 'go_runtime_cpu_cores' && <p className="mb-2 text-xs text-text-muted">{tr('Go 运行时估算，排除空闲时间；不等同于操作系统进程 CPU。', 'Go runtime estimate excluding idle time; differs from OS process CPU.')}</p>}
            {name.endsWith('_memory_limit_bytes') && name.startsWith('jvm_') && <p className="mb-2 text-xs text-text-muted">{tr('仅合计已上报有限上限的内存池。', 'Includes only pools reporting a finite limit.')}</p>}
            {name.startsWith('jvm_') && name.endsWith('_memory_used_bytes') && <p className="mb-2 text-xs text-text-muted">{tr('JVM 已用内存不等于进程 RSS。', 'JVM used memory is not process RSS.')}</p>}
            {name.endsWith('_gc_mean_duration_seconds') && <p className="mb-2 text-xs text-text-muted">{name.startsWith('go_')
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
                  {rows.map((row, index) => selectedKey && seriesKey(row) !== selectedKey ? null : <Line key={seriesKey(row)} type="monotone" dataKey={`v${index}`} name={`${row.instance_id || tr('未设置实例', 'Unset instance')} · ${row.version || tr('未设置版本', 'Unset version')}`} stroke={colors[index % colors.length]} strokeWidth={1.5} dot={false} connectNulls={false} isAnimationActive={false} />)}
                </LineChart>
              </ResponsiveContainer>
            </div>
            <div className="mt-2 space-y-1 text-xs">
              {rows.map((row, index) => {
                const key = seriesKey(row);
                const active = selectedKey === key;
                return <Button key={key} variant="plain" size="sm" aria-pressed={active}
                  aria-label={`${row.instance_id || tr('未设置实例', 'Unset instance')} · ${row.version || tr('未设置版本', 'Unset version')} · ${active ? tr('显示全部曲线', 'Show all lines') : tr('只显示此曲线', 'Show only this line')}`}
                  onClick={() => setSelectedSeries((current) => ({ ...current, [name]: active ? null : key }))}
                  className={`h-auto min-h-7 w-full min-w-0 shrink justify-between whitespace-normal px-1.5 py-1 text-left focus-visible:ring-2 focus-visible:ring-indigo-500 ${active ? 'border-indigo-500/40 bg-indigo-500/10' : 'hover:bg-bg'}`}>
                  <span className={`inline-flex min-w-0 items-center gap-2 ${active ? 'text-text' : 'text-text-muted'}`}><span aria-hidden="true" className="h-0.5 w-3 shrink-0" style={{ backgroundColor: colors[index % colors.length] }} /><span className="break-all">{row.instance_id || '—'} · {row.version || '—'}</span></span>
                  <span className="shrink-0 tabular-nums">{display(row.value)}</span>
                </Button>;
              })}
            </div>
          </section>;
        })}
      </div>
    </>}
      </section>
    </Card>)}
  </div>;
}
