import { describe, expect, it } from 'vitest';
import type { Edge } from '@/api/edges';
import { selectHostEdgesByDevice } from './edgeSelection';

describe('Host execution binding', () => {
  const edges = [
    { id: 7, device_id: 41, status: 'online' },
    { id: 23, device_id: 41, status: 'offline' },
    { id: 41, device_id: 99, status: 'online' },
  ] as Edge[];
  it('uses the current installation even when an older one is online', () => {
    expect(selectHostEdgesByDevice(edges, [{ id: 41, current_edge_id: 23 }]).get(41)?.id).toBe(23);
  });
  it('never falls back to same-number IDs or arbitrary online installations', () => {
    expect(selectHostEdgesByDevice(edges, [{ id: 41 }]).size).toBe(0);
    expect(selectHostEdgesByDevice(edges, [{ id: 41, current_edge_id: 41 }]).size).toBe(0);
    expect(selectHostEdgesByDevice(edges, [{ id: 41, current_edge_id: 999 }]).size).toBe(0);
  });
});
