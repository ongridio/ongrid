import { request } from './client';

export type DeviceRole = 'host' | 'discovered';

export type Device = {
  id: number;
  name: string;
  hostname?: string;
  description?: string;
  ip_address?: string;
  os?: string;
  os_version?: string;
  arch?: string;
  kernel_version?: string;
  cpu_count?: number;
  mem_total_bytes?: number;
  disk_total_bytes?: number;
  cpu_usage_pct?: number;
  mem_usage_pct?: number;
  disk_usage_pct?: number;
  roles?: string[];
  scope?: DeviceRole;
  online?: boolean;
  last_seen_at?: string | null;
  created_at?: string;
  updated_at?: string;
  // — points at the row in topology.nodes that fronts this
  // device. Null until topology.Migrate's backfill has run.
  node_id?: number | null;
};

export type DeviceEdgeLink = {
  edge_id: number;
  device_id: number;
  type: DeviceRole | 'unknown';
  created_at: string;
};

export function listDevices(params?: { roles?: string }) {
  const qs = params?.roles
    ? `?${new URLSearchParams({ roles: params.roles }).toString()}`
    : '';
  return request<{ items: Device[]; total: number }>('GET', `/devices${qs}`);
}

export function getDevice(id: string | number) {
  return request<Device>('GET', `/devices/${encodeURIComponent(String(id))}`);
}

export function deleteDevice(id: string | number) {
  return request<void>('DELETE', `/devices/${encodeURIComponent(String(id))}`);
}

export function listDeviceEdges(id: string | number) {
  return request<{ items: DeviceEdgeLink[] }>(
    'GET',
    `/devices/${encodeURIComponent(String(id))}/edges`,
  );
}
