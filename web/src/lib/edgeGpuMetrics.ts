import type { PromMatrixSeries } from '@/api/edges';

/** Grafana-leaning palette — keep in sync with EdgeDetail / Monitor. */
export const SERIES_COLORS = [
  '#60a5fa',
  '#34d399',
  '#f59e0b',
  '#a78bfa',
  '#f87171',
  '#22d3ee',
  '#fb7185',
  '#facc15',
] as const;

export type GpuPanelKey = 'gpuUtil' | 'gpuMem' | 'gpuTemp' | 'gpuPower';

export type ChartRow = {
  ts: number;
  tsLabel: string;
} & Record<string, number | null | string>;

export type SeriesDescriptor = {
  key: string;
  label: string;
  color: string;
};

export type PanelData = {
  rows: ChartRow[];
  series: SeriesDescriptor[];
};

export const EMPTY_PANEL: PanelData = { rows: [], series: [] };

export function normalizeHostInfo(
  hostInfo: Record<string, unknown> | string | null | undefined,
): Record<string, unknown> | null {
  if (!hostInfo) return null;
  if (typeof hostInfo === 'string') {
    try {
      const parsed = JSON.parse(hostInfo);
      return typeof parsed === 'object' && parsed !== null
        ? (parsed as Record<string, unknown>)
        : null;
    } catch {
      return null;
    }
  }
  return hostInfo;
}

export function isGpuAvailable(
  hostInfo: Record<string, unknown> | string | null | undefined,
): boolean {
  const obj = normalizeHostInfo(hostInfo);
  return obj?.gpu_available === true;
}

export function gpuSeriesLabel(index: number): string {
  return `GPU ${index}`;
}

function deviceLabelSel(deviceId: number): string {
  return `device_id="${deviceId}"`;
}

export function buildGpuExprs(deviceId: number): Record<GpuPanelKey, string> {
  const sel = deviceLabelSel(deviceId);
  return {
    gpuUtil: `100 * nvidia_smi_utilization_gpu_ratio{${sel}}`,
    gpuMem: `100 * nvidia_smi_memory_used_bytes{${sel}} / nvidia_smi_memory_total_bytes{${sel}}`,
    gpuTemp: `nvidia_smi_temperature_gpu{${sel}}`,
    gpuPower: `nvidia_smi_power_draw_watts{${sel}}`,
  };
}

/** Stable uuid → color index for cross-panel consistency. */
export function buildGpuColorMap(uuids: string[]): Map<string, number> {
  const sorted = [...new Set(uuids)].sort((a, b) => a.localeCompare(b));
  const map = new Map<string, number>();
  sorted.forEach((uuid, idx) => map.set(uuid, idx));
  return map;
}

export function extractUuidsFromMatrix(matrix: PromMatrixSeries[]): string[] {
  const uuids: string[] = [];
  for (const s of matrix) {
    const uuid = s.metric.uuid;
    if (typeof uuid === 'string' && uuid) uuids.push(uuid);
  }
  return uuids;
}

function formatTimeLabel(ms: number): string {
  const date = new Date(ms);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

/** One GPU uuid → one line; colors from shared colorByUuid map. */
export function matrixToGpuPanel(
  matrix: PromMatrixSeries[],
  panelKey: GpuPanelKey,
  colorByUuid: Map<string, number>,
): PanelData {
  if (!matrix || matrix.length === 0) return EMPTY_PANEL;

  const filtered = matrix.filter((s) => {
    const uuid = s.metric.uuid;
    return typeof uuid === 'string' && uuid !== '';
  });
  if (filtered.length === 0) return EMPTY_PANEL;

  const seriesEntries = filtered
    .map((s) => {
      const uuid = s.metric.uuid as string;
      const colorIdx = colorByUuid.get(uuid) ?? 0;
      const key = `${panelKey}_${uuid}`;
      return {
        uuid,
        key,
        label: gpuSeriesLabel(colorIdx),
        color: SERIES_COLORS[colorIdx % SERIES_COLORS.length],
        raw: s,
      };
    })
    .sort((a, b) => a.uuid.localeCompare(b.uuid));

  const valuesByKey = new Map<string, Map<number, number>>();
  for (const entry of seriesEntries) {
    const m = new Map<number, number>();
    for (const [tsSec, vStr] of entry.raw.values) {
      const v = parseFloat(vStr);
      if (Number.isFinite(v)) m.set(tsSec, v);
    }
    valuesByKey.set(entry.key, m);
  }

  const tsSet = new Set<number>();
  for (const m of valuesByKey.values()) {
    for (const ts of m.keys()) tsSet.add(ts);
  }
  const tsSorted = Array.from(tsSet).sort((a, b) => a - b);

  const rows: ChartRow[] = tsSorted.map((tsSec): ChartRow => {
    const row: ChartRow = {
      ts: tsSec,
      tsLabel: formatTimeLabel(tsSec * 1000),
    };
    for (const desc of seriesEntries) {
      const m = valuesByKey.get(desc.key);
      const v = m?.get(tsSec);
      row[desc.key] = typeof v === 'number' ? v : null;
    }
    return row;
  });

  return {
    rows,
    series: seriesEntries.map(({ key, label, color }) => ({ key, label, color })),
  };
}

export function isGpuPanelEmpty(panels: Record<GpuPanelKey, PanelData>): boolean {
  return (Object.values(panels) as PanelData[]).every((p) => p.series.length === 0);
}

export function formatPercent(v: number): string {
  if (!Number.isFinite(v)) return '—';
  return `${v.toFixed(1)}%`;
}

export function formatWatts(v: number): string {
  if (!Number.isFinite(v)) return '—';
  return `${v.toFixed(1)} W`;
}

export function formatCelsius(v: number): string {
  if (!Number.isFinite(v)) return '—';
  return `${v.toFixed(1)}°C`;
}
