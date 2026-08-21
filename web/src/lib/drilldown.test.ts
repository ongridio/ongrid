import { describe, expect, it } from 'vitest';

import { buildExploreUrl } from './drilldown';

describe('buildExploreUrl', () => {
  it('builds a Grafana Elasticsearch logs query', () => {
    const target = buildExploreUrl({
      base: 'https://grafana.example.com/',
      dsType: 'elasticsearch',
      dsUid: 'ongrid-elasticsearch',
      query: { query: '*', metrics: [{ id: '1', type: 'logs' }] },
      fromMs: 'now-1h',
      toMs: 'now',
      orgId: '1',
    });

    const url = new URL(target);
    expect(`${url.origin}${url.pathname}`).toBe('https://grafana.example.com/explore');
    expect(url.searchParams.get('schemaVersion')).toBe('1');
    expect(url.searchParams.get('orgId')).toBe('1');
    const panes = JSON.parse(url.searchParams.get('panes') ?? '{}');
    expect(panes.og).toEqual({
      datasource: 'ongrid-elasticsearch',
      queries: [{
        refId: 'A',
        datasource: { type: 'elasticsearch', uid: 'ongrid-elasticsearch' },
        query: '*',
        metrics: [{ id: '1', type: 'logs' }],
      }],
      range: { from: 'now-1h', to: 'now' },
    });
  });
});
