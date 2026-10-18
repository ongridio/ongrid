import type { Edge } from '@/api/edges';
import type { Device } from '@/api/devices';

// Only the server-owned binding selects the host execution installation.
export function selectHostEdgesByDevice(edges: Edge[], devices: Pick<Device, 'id' | 'current_edge_id'>[]): Map<number, Edge> {
  const byID = new Map(edges.map(edge => [edge.id, edge]));
  const out = new Map<number, Edge>();
  for (const device of devices) {
    const edge = device.current_edge_id ? byID.get(device.current_edge_id) : undefined;
    if (edge?.device_id === device.id) out.set(device.id, edge);
  }
  return out;
}
