import { expect, it } from 'vitest';
import { alignResourceColumns } from './layout';

it('keeps service/device/cluster columns with missing services and prevents snapped overlaps', () => {
  const positions = alignResourceColumns([
    { id: 'service', type: 'service', x: 40, y: 164 },
    { id: 'device', type: 'device', x: 310, y: 164 },
    { id: 'cluster', type: 'cluster', x: 580, y: 164 },
    { id: 'device-only', type: 'device', x: 40, y: 40 },
    { id: 'cluster-only', type: 'cluster', x: 310, y: 40 },
    { id: 'same-type', type: 'device', x: 580, y: 164 },
  ], 160, 44);
  expect(positions.get('service')).toEqual({ x: 40, y: 164 });
  expect(positions.get('device')).toEqual({ x: 310, y: 164 });
  expect(positions.get('cluster')).toEqual({ x: 580, y: 164 });
  expect(positions.get('device-only')).toEqual({ x: 310, y: 40 });
  expect(positions.get('cluster-only')).toEqual({ x: 580, y: 40 });
  expect(positions.get('same-type')).toEqual({ x: 310, y: 288 });
});
