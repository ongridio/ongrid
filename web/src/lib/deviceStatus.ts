import type { Incident } from '@/api/alerts';
import type { Edge } from '@/api/edges';

export const DEFAULT_HEARTBEAT_STALE_MS = 90_000;

export type DevicePresenceState =
  | 'offline-alert'
  | 'heartbeat-stale'
  | 'online'
  | 'offline'
  | 'unknown';

type PresenceEdge = Pick<Edge, 'device_id' | 'last_seen_at' | 'status'>;

export function deviceIDFromIncident(incident: Incident): string | null {
  const targetID = typeof incident.target_id === 'string' ? incident.target_id.trim() : '';
  if (targetID) return targetID;
  const labelID = incident.labels?.device_id?.trim() ?? '';
  return labelID || null;
}

export function buildOfflineAlertDeviceIDs(incidents: Incident[]): Set<string> {
  const ids = new Set<string>();
  for (const incident of incidents) {
    if (incident.status !== 'open') continue;
    if (incident.rule_key !== 'device_offline') continue;
    const deviceID = deviceIDFromIncident(incident);
    if (deviceID) ids.add(deviceID);
  }
  return ids;
}

export function isHeartbeatStale(
  lastSeenAt: string | null | undefined,
  nowMs = Date.now(),
  staleMs = DEFAULT_HEARTBEAT_STALE_MS,
): boolean {
  if (!lastSeenAt) return false;
  const lastSeenMs = new Date(lastSeenAt).getTime();
  if (!Number.isFinite(lastSeenMs)) return false;
  return nowMs - lastSeenMs > staleMs;
}

export function resolveDevicePresence(
  edge: PresenceEdge,
  offlineAlertDeviceIDs: Set<string>,
  nowMs = Date.now(),
): DevicePresenceState {
  if (edge.device_id != null && offlineAlertDeviceIDs.has(String(edge.device_id))) {
    return 'offline-alert';
  }
  if (edge.status === 'online' && isHeartbeatStale(edge.last_seen_at, nowMs)) {
    return 'heartbeat-stale';
  }
  if (edge.status === 'online' || edge.status === 'offline') {
    return edge.status;
  }
  return 'unknown';
}
