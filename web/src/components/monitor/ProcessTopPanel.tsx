// ProcessTopPanel — Monitor 页 "top-N 进程" 时间线面板。
//
// 数据源：manager-side Prometheus，PromQL 直接打 process-exporter 系列
// （`namedprocess_namegroup_*{device_id="X"}`）。procmetrics plugin 在
// edge 跑 ncabatoff/process-exporter（subprocess，受 manager Plugins UI
// 控制 enable/disable），按 comm 分组。所以"按时间线展示 top-N 进程"
// 跟上方 4 块 PromQL 图同一套数据面：刷新 / 切 range / 切 device 用
// 同一个 fromMs / toMs / tick。
//
// 没有前端 buffer，没有按需 RPC，刷新页面不丢历史。

import { useEffect, useMemo, useState } from 'react';
import {
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { Cpu, Loader2, MemoryStick } from 'lucide-react';
import { queryRange, type PromMatrixSeries } from '@/api/prom';
import { ApiError } from '@/api/client';
import { cn } from '@/lib/cn';
import { chartTooltipStyle } from '@/lib/chartTheme';
import { useI18n } from '@/i18n/locale';

const TOP_N = 8; // 同屏可读上限；再多线就糊

// 8 色调色板，浅 / 深底都能读。
const PALETTE = [
  '#8c6df0', '#30a6d0', '#10b981', '#f59e0b',
  '#ef4444', '#ec4899', '#06b6d4', '#a855f7',
];

type SortBy = 'cpu' | 'mem';

export function ProcessTopPanel({
  edgeID,
  deviceID,
  edgeName,
  tick,
  fromMs,
  toMs,
}: {
  edgeID: number;
  // device_id 是 PromQL label 的连接键（procmetrics 推上来的样本都打
  // 这个 label）；edgeID 留着仅作内部 key 用。
  deviceID: number | string;
  edgeName: string;
  tick: number;
  fromMs: number;
  toMs: number;
}) {
  const { tr } = useI18n();
  const [sortBy, setSortBy] = useState<SortBy>('mem');
  const [series, setSeries] = useState<PromMatrixSeries[]>([]);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const expr = useMemo(() => {
    // namedprocess_namegroup_memory_bytes 默认带 memtype label —— 选
    // resident 拿 RSS。CPU 走 rate(cpu_seconds_total[5m])，缩放 100
    // 让 axis 单位跟内存图风格一致（%）。
    const devSel = `device_id="${String(deviceID)}"`;
    if (sortBy === 'cpu') {
      return `topk(${TOP_N}, sum by (groupname) (rate(namedprocess_namegroup_cpu_seconds_total{${devSel}}[5m])) * 100)`;
    }
    return `topk(${TOP_N}, sum by (groupname) (namedprocess_namegroup_memory_bytes{${devSel},memtype="resident"}) / 1024 / 1024)`;
  }, [deviceID, sortBy]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setErr(null);
    const span = Math.max(1, toMs - fromMs);
    // step ≈ span / 240，匹配 Monitor 主面板的 grafanaVars.ts 节奏。
    const stepSec = Math.max(15, Math.round(span / 240 / 1000));
    queryRange({
      expr,
      start: new Date(fromMs).toISOString(),
      end: new Date(toMs).toISOString(),
      step: `${stepSec}s`,
    })
      .then((r) => {
        if (cancelled) return;
        setSeries(r.result ?? []);
      })
      .catch((e) => {
        if (cancelled) return;
        setErr(e instanceof ApiError ? e.message : (e as Error).message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [expr, fromMs, toMs, tick]);

  // Pivot matrix → recharts rows. 用 series 的 groupname 当 dataKey；
  // 每个 timestamp 一行；多 series 没值的位补 NaN（recharts 自动断线）。
  const { chartRows, seriesNames } = useMemo(() => {
    const names = series.map((s) => s.metric.groupname ?? '?');
    const tsSet = new Set<number>();
    for (const s of series) {
      for (const [t] of s.values) tsSet.add(t);
    }
    const allTs = Array.from(tsSet).sort((a, b) => a - b);
    const rows = allTs.map((t) => {
      const row: Record<string, number | string> = {
        t: fmtTime(t),
      };
      for (const s of series) {
        const name = s.metric.groupname ?? '?';
        const sample = s.values.find(([sampleT]) => sampleT === t);
        if (sample) {
          const v = parseFloat(sample[1]);
          if (Number.isFinite(v)) row[name] = v;
        }
      }
      return row;
    });
    return { chartRows: rows, seriesNames: names };
  }, [series]);

  // 给"最新快照"小表 —— 取每条 series 最末样本，按值降序展示。
  const latestSnapshot = useMemo(() => {
    return series
      .map((s) => {
        const last = s.values[s.values.length - 1];
        const v = last ? parseFloat(last[1]) : NaN;
        return { name: s.metric.groupname ?? '?', value: v };
      })
      .filter((x) => Number.isFinite(x.value))
      .sort((a, b) => b.value - a.value);
  }, [series]);

  const unit = sortBy === 'cpu' ? '%' : 'MiB';

  return (
    <section className="rounded-lg border border-zinc-800 bg-zinc-900/40">
      <header className="flex items-center justify-between gap-3 border-b border-zinc-800/60 px-4 py-2.5">
        <div className="min-w-0">
          <h3 className="truncate text-xs font-medium text-zinc-200">
            {tr(`进程 Top ${TOP_N} · 时间线`, `Top ${TOP_N} processes · timeline`)} · {edgeName}
          </h3>
          <p className="truncate text-[10px] text-zinc-500">
            {tr('数据源: process-exporter (procmetrics plugin) → Prometheus', 'Source: process-exporter (procmetrics plugin) → Prometheus')}
          </p>
        </div>
        <div className="flex items-center gap-1">
          <SortBtn icon={<MemoryStick size={11} />} label={tr('内存', 'Memory')} active={sortBy === 'mem'} onClick={() => setSortBy('mem')} />
          <SortBtn icon={<Cpu size={11} />} label="CPU" active={sortBy === 'cpu'} onClick={() => setSortBy('cpu')} />
        </div>
      </header>
      {err ? (
        <div className="px-4 py-3 text-xs text-red-300">{err}</div>
      ) : loading && chartRows.length === 0 ? (
        <div className="flex items-center gap-2 px-4 py-6 text-xs text-zinc-500">
          <Loader2 size={12} className="animate-spin" /> {tr('加载中…', 'Loading…')}
        </div>
      ) : chartRows.length === 0 ? (
        <div className="space-y-1 px-4 py-6 text-center text-xs text-zinc-500">
          <div>{tr('该设备没有进程数据', 'No process data for this device')}</div>
          <div className="text-[11px] text-zinc-600">
            {tr(
              '可能原因：edge 上 process_exporter 二进制缺失（早期安装的 edge 未 bundle）。请在该设备上重跑最新发布包里的 install-edge.sh，或在「设备 → Plugins」检查 procmetrics 状态。',
              'Likely cause: process_exporter binary is missing on the edge (older installs did not bundle it). Re-run install-edge.sh from the latest release tarball on this device, or check procmetrics status in Device → Plugins.',
            )}
          </div>
        </div>
      ) : (
        <>
          <div className="px-2 py-3" style={{ width: '100%', height: 260 }}>
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={chartRows} margin={{ top: 4, right: 12, bottom: 0, left: -10 }}>
                <CartesianGrid stroke="rgba(120,120,140,0.15)" strokeDasharray="3 3" />
                <XAxis
                  dataKey="t"
                  stroke="rgb(113 113 122)"
                  tick={{ fontSize: 10 }}
                  interval="preserveStartEnd"
                  minTickGap={40}
                />
                <YAxis
                  stroke="rgb(113 113 122)"
                  tick={{ fontSize: 10 }}
                  unit={unit}
                  width={56}
                />
                <Tooltip
                  contentStyle={chartTooltipStyle}
                  formatter={(v: number) => `${v.toFixed(1)} ${unit}`}
                />
                <Legend wrapperStyle={{ fontSize: 10 }} />
                {seriesNames.map((name, i) => (
                  <Line
                    key={name}
                    type="monotone"
                    dataKey={name}
                    stroke={PALETTE[i % PALETTE.length]}
                    dot={chartRows.length < 2 ? { r: 3 } : false}
                    isAnimationActive={false}
                    strokeWidth={1.6}
                  />
                ))}
              </LineChart>
            </ResponsiveContainer>
          </div>
          <details className="border-t border-zinc-800/60">
            <summary className="cursor-pointer list-none px-4 py-2 text-[11px] text-zinc-500 hover:text-zinc-300">
              {tr(`最新快照（${latestSnapshot.length} 组）`, `Latest snapshot (${latestSnapshot.length} groups)`)}
            </summary>
            <div className="overflow-x-auto">
              <table className="w-full text-xs">
                <thead className="text-[10px] uppercase tracking-wider text-zinc-500">
                  <tr className="border-b border-zinc-800/60">
                    <th className="px-4 py-1.5 text-left font-medium">{tr('进程组', 'Process group')}</th>
                    <th className="px-4 py-1.5 text-right font-medium">
                      {sortBy === 'cpu' ? 'CPU' : tr('内存', 'Memory')} ({unit})
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {latestSnapshot.map((row) => (
                    <tr key={row.name} className="border-b border-zinc-900/40 hover:bg-zinc-900/30">
                      <td className="px-4 py-1.5 font-medium text-zinc-200">{row.name}</td>
                      <td className="px-4 py-1.5 text-right font-mono text-zinc-300">
                        {row.value.toFixed(1)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </details>
        </>
      )}
    </section>
  );
}

function fmtTime(unixSec: number): string {
  const d = new Date(unixSec * 1000);
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`;
}

function SortBtn({ icon, label, active, onClick }: {
  icon: React.ReactNode;
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1 rounded-md border px-2 py-1 text-[11px]',
        active
          ? 'border-zinc-600 bg-zinc-800 text-zinc-100'
          : 'border-zinc-800 text-zinc-400 hover:border-zinc-700 hover:bg-zinc-800/40',
      )}
    >
      {icon}
      {label}
    </button>
  );
}
