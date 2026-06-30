import { describe, expect, it } from 'vitest';
import type { PromMatrixSeries } from '@/api/edges';
import {
  buildGpuColorMap,
  buildGpuExprs,
  extractUuidsFromMatrix,
  gpuSeriesLabel,
  isGpuAvailable,
  isGpuPanelEmpty,
  matrixToGpuPanel,
  EMPTY_PANEL,
  SERIES_COLORS,
} from './edgeGpuMetrics';

describe('buildGpuExprs', () => {
  it('includes device_id filter and no aggregation', () => {
    const exprs = buildGpuExprs(25);
    for (const expr of Object.values(exprs)) {
      expect(expr).toContain('device_id="25"');
      expect(expr).not.toMatch(/\bavg\s*\(/);
      expect(expr).not.toMatch(/sum by \(device_id\)/);
    }
    expect(exprs.gpuUtil).toBe(
      '100 * nvidia_smi_utilization_gpu_ratio{device_id="25"}',
    );
    expect(exprs.gpuMem).toContain('nvidia_smi_memory_used_bytes');
    expect(exprs.gpuTemp).toBe('nvidia_smi_temperature_gpu{device_id="25"}');
    expect(exprs.gpuPower).toBe('nvidia_smi_power_draw_watts{device_id="25"}');
  });
});

describe('buildGpuColorMap', () => {
  it('sorts uuids lexicographically for stable color indices', () => {
    const map = buildGpuColorMap(['uuid-b', 'uuid-a', 'uuid-b']);
    expect(map.get('uuid-a')).toBe(0);
    expect(map.get('uuid-b')).toBe(1);
    expect(map.size).toBe(2);
  });
});

describe('gpuSeriesLabel', () => {
  it('returns GPU N labels', () => {
    expect(gpuSeriesLabel(0)).toBe('GPU 0');
    expect(gpuSeriesLabel(1)).toBe('GPU 1');
  });
});

describe('isGpuAvailable', () => {
  it('returns true only when gpu_available is true', () => {
    expect(isGpuAvailable({ gpu_available: true })).toBe(true);
    expect(isGpuAvailable({ gpu_available: false })).toBe(false);
    expect(isGpuAvailable({})).toBe(false);
    expect(isGpuAvailable(null)).toBe(false);
    expect(isGpuAvailable(JSON.stringify({ gpu_available: true }))).toBe(true);
    expect(isGpuAvailable(JSON.stringify({ gpu_available: false }))).toBe(false);
  });
});

function dualGpuMatrix(): PromMatrixSeries[] {
  return [
    {
      metric: { uuid: 'GPU-aaa', device_id: '25' },
      values: [
        [1_700_000_000, '10'],
        [1_700_000_060, '20'],
        [1_700_000_120, '30'],
      ],
    },
    {
      metric: { uuid: 'GPU-bbb', device_id: '25' },
      values: [
        [1_700_000_000, '40'],
        [1_700_000_060, '50'],
        [1_700_000_120, '60'],
      ],
    },
  ];
}

describe('matrixToGpuPanel', () => {
  it('splits one line per uuid with GPU 0/1 legend labels', () => {
    const colorByUuid = buildGpuColorMap(extractUuidsFromMatrix(dualGpuMatrix()));
    const panel = matrixToGpuPanel(dualGpuMatrix(), 'gpuUtil', colorByUuid);
    expect(panel.series).toHaveLength(2);
    expect(panel.series.map((s) => s.label)).toEqual(['GPU 0', 'GPU 1']);
    expect(new Set(panel.series.map((s) => s.key)).size).toBe(2);
    expect(panel.rows).toHaveLength(3);
  });

  it('shares colors across panels for the same uuid', () => {
    const matrix = dualGpuMatrix();
    const colorByUuid = buildGpuColorMap(extractUuidsFromMatrix(matrix));
    const util = matrixToGpuPanel(matrix, 'gpuUtil', colorByUuid);
    const temp = matrixToGpuPanel(matrix, 'gpuTemp', colorByUuid);
    expect(util.series[0].color).toBe(temp.series[0].color);
    expect(util.series[1].color).toBe(temp.series[1].color);
    expect(util.series[0].color).toBe(SERIES_COLORS[0]);
    expect(util.series[1].color).toBe(SERIES_COLORS[1]);
  });

  it('returns EMPTY_PANEL for empty matrix', () => {
    const panel = matrixToGpuPanel([], 'gpuUtil', new Map());
    expect(panel).toEqual(EMPTY_PANEL);
  });
});

describe('isGpuPanelEmpty', () => {
  it('is true when all panels have no series', () => {
    expect(
      isGpuPanelEmpty({
        gpuUtil: EMPTY_PANEL,
        gpuMem: EMPTY_PANEL,
        gpuTemp: EMPTY_PANEL,
        gpuPower: EMPTY_PANEL,
      }),
    ).toBe(true);
  });
});
