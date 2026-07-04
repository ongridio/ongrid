import type { PromMatrixSeries } from '@/api/edges';
import {
  EMPTY_PANEL,
  SERIES_COLORS,
  type ChartRow,
  type PanelData,
} from '@/lib/metricsPanel';

export { EMPTY_PANEL, SERIES_COLORS, type ChartRow, type PanelData };

export type GpuPanelKey = 'gpuUtil' | 'gpuMem' | 'gpuTemp' | 'gpuPower';

/**
 * Stable Grafana panel ids on `ongrid-server-detail`.
 * Keep in sync with internal/manager/biz/grafana/dashboards/server-detail.json
 * (and the deploy provisioning copies). Changing these breaks Edge drilldown
 * deep-links (`?viewPanel=`).
 */
export const GPU_GRAFANA_PANEL_IDS: Record<GpuPanelKey, number> = {
  gpuUtil: 10,
  gpuMem: 11,
  gpuTemp: 12,
  gpuPower: 13,
};

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

/**
 * Legend text: physical index from uuid lexicographic order, plus a short
 * uuid tail when multiple GPUs are present.
 */
export function gpuLegendLabel(uuid: string, colorByUuid: Map<string, number>): string {
  const physical = colorByUuid.get(uuid) ?? 0;
  if (colorByUuid.size <= 1) {
    return `GPU ${physical}`;
  }
  return `GPU ${physical} (${uuidLegendTail(uuid)})`;
}

function uuidLegendTail(uuid: string): string {
  const parts = uuid.split(/[-_]/);
  const last = parts[parts.length - 1]?.trim();
  if (last) {
    return last.length > 4 ? last.slice(-4) : last;
  }
  return uuid.length > 4 ? uuid.slice(-4) : uuid;
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

/** Union uuids across all GPU panel matrices (util may be empty for a card). */
export function collectGpuUuidsFromMatrices(matrices: PromMatrixSeries[][]): string[] {
  const uuids: string[] = [];
  for (const matrix of matrices) {
    uuids.push(...extractUuidsFromMatrix(matrix));
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
        label: gpuLegendLabel(uuid, colorByUuid),
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
