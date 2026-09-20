import { expect, it } from 'vitest';
import { groupDiscoveredProcesses } from './hostProcesses';

it('groups and sorts ports only for the same executable and known PID', () => {
  expect(groupDiscoveredProcesses()).toEqual([]);
  expect(groupDiscoveredProcesses([
    { executable: '/app', pid: 12, port: 9090 },
    { executable: '/app', pid: 12, port: 8080 },
    { executable: '/app', pid: 12, port: 8080 },
    { executable: '/app', pid: 13, port: 8081 },
    { executable: '/other', pid: 12, port: 8082 },
    { executable: '/app', pid: 0, port: 8000 },
    { executable: '/app', pid: 0, port: 8001 },
  ])).toEqual([
    { executable: '/app', pid: 12, ports: [8080, 9090] },
    { executable: '/app', pid: 13, ports: [8081] },
    { executable: '/other', pid: 12, ports: [8082] },
    { executable: '/app', pid: 0, ports: [8000] },
    { executable: '/app', pid: 0, ports: [8001] },
  ]);
});
