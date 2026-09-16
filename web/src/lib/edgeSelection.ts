import type { Edge } from '@/api/edges';

// One row per device: prefer an online collector, then the latest heartbeat.
export function selectHostEdgesByDevice(edges: Edge[]): Map<number, Edge> {
  const out = new Map<number, Edge>();
  for (const edge of edges) {
    const deviceID = edge.device_id;
    if (!deviceID) continue;
    const current = out.get(deviceID);
    if (!current || isBetterHostEdge(edge, current)) {
      out.set(deviceID, edge);
    }
  }
  return out;
}

function isBetterHostEdge(candidate: Edge, current: Edge): boolean {
  if (candidate.status !== current.status) {
    return candidate.status === "online";
  }
  return edgeSeenAt(candidate) > edgeSeenAt(current);
}

function edgeSeenAt(edge: Edge): number {
  if (!edge.last_seen_at) return 0;
  const ts = Date.parse(edge.last_seen_at);
  return Number.isFinite(ts) ? ts : 0;
}
