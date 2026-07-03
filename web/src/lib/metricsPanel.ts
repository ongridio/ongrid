/** Grafana-leaning palette shared by EdgeDetail metrics panels and GPU helpers. */
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

/** One time bucket: {ts, tsLabel, <seriesKey>: number | null, ...}. */
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
