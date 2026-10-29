import { describe, expect, it } from 'vitest';
import {
  executeFlow,
  load,
  save,
  seed,
  STORAGE_KEY,
  updateIncident,
} from './data';

describe('enterprise browser sandbox', () => {
  it('persists the snapshot, incident lifecycle, documents and simulated execution together', () => {
    const values = new Map<string, string>();
    const storage = {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => {
        values.set(key, value);
      },
    };
    const initial = seed(new Date('2026-10-27T02:00:00Z'));
    let next = updateIncident(initial, 'INC-1201', 'acknowledged');
    expect(next.incidents[0].status).toBe('acknowledged');
    next = updateIncident(next, 'INC-1201', 'closed');
    next.articles[0] = {
      ...next.articles[0],
      title: { zh: '修改后的指南', en: 'Edited guide' },
    };
    next = executeFlow(next, 'cpu');
    expect(save(storage, next)).toBe(true);
    expect(load(storage)).toEqual({ data: next, error: false });
    expect(next.runs).toHaveLength(7);
    expect(next.runs[0].flowId).toBe('cpu');
    expect(
      updateIncident(next, 'INC-1201', 'acknowledged').incidents[0].status,
    ).toBe('closed');
    expect(initial.incidents[0].status).toBe('open');
  });
  it('surfaces corrupt data, incompatible versions and storage failures', () => {
    for (const value of [
      'invalid',
      JSON.stringify({ ...seed(), version: 2 }),
      JSON.stringify({ ...seed(), articles: [{}] }),
    ]) {
      const result = load({ getItem: () => value });
      expect(result.error).toBe(true);
      expect(result.data.resources).toHaveLength(24);
    }
    expect(
      save(
        {
          setItem: () => {
            throw new Error('Quota exceeded');
          },
        },
        seed(),
      ),
    ).toBe(false);
    expect(
      load({
        getItem: () => {
          throw new Error('Access denied');
        },
      }).error,
    ).toBe(true);
  });
  it('creates isolated fresh samples and bounds execution history', () => {
    const old = seed();
    old.incidents[0].status = 'closed';
    expect(seed().incidents[0].status).toBe('open');
    let data = seed();
    for (let i = 0; i < 110; i++) data = executeFlow(data, 'daily');
    expect(data.runs).toHaveLength(100);
    expect(new Set(data.runs.map((r) => r.id)).size).toBe(100);
    expect(executeFlow(data, 'unknown')).toBe(data);
    expect(STORAGE_KEY).toMatch(/^enterprise-ai/);
  });
});
