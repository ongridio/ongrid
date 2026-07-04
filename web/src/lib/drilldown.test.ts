import { describe, expect, it, vi } from 'vitest';
import { buildGrafanaDashboardQuery } from '@/lib/drilldown';
import { GPU_GRAFANA_PANEL_IDS } from '@/lib/edgeGpuMetrics';

vi.mock('@/i18n/locale', () => ({
  getLocale: () => 'zh-CN',
}));

describe('buildGrafanaDashboardQuery', () => {
  it('includes device_id, time range, and lang', () => {
    const qs = buildGrafanaDashboardQuery({
      rangeInput: '6h',
      deviceId: 25,
      orgId: '1',
    });
    const params = new URLSearchParams(qs);
    expect(params.get('from')).toBe('now-6h');
    expect(params.get('to')).toBe('now');
    expect(params.get('var-device_id')).toBe('25');
    expect(params.get('orgId')).toBe('1');
    expect(params.get('lang')).toBe('zh-Hans');
    expect(params.get('viewPanel')).toBeNull();
  });

  it('adds viewPanel when provided', () => {
    const qs = buildGrafanaDashboardQuery({
      rangeInput: '6h',
      deviceId: 25,
      viewPanel: GPU_GRAFANA_PANEL_IDS.gpuUtil,
      orgId: '1',
    });
    const params = new URLSearchParams(qs);
    expect(params.get('viewPanel')).toBe('10');
    expect(params.get('var-device_id')).toBe('25');
  });

  it('maps each GPU panel id for drilldown deep-links', () => {
    expect(GPU_GRAFANA_PANEL_IDS).toEqual({
      gpuUtil: 10,
      gpuMem: 11,
      gpuTemp: 12,
      gpuPower: 13,
    });
    for (const id of Object.values(GPU_GRAFANA_PANEL_IDS)) {
      const qs = buildGrafanaDashboardQuery({ deviceId: 1, viewPanel: id });
      expect(new URLSearchParams(qs).get('viewPanel')).toBe(String(id));
    }
  });

  it('omits viewPanel for non-finite values', () => {
    const qs = buildGrafanaDashboardQuery({
      deviceId: 1,
      viewPanel: Number.NaN,
    });
    expect(new URLSearchParams(qs).get('viewPanel')).toBeNull();
  });
});
